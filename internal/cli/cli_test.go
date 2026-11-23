package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/devvmhq/ciwatch/internal/cli"
	"github.com/devvmhq/ciwatch/internal/gh"
)

const sha = "3bb2dccd75a2754b8d45e522b3c26c22bc66a21a"

// checkoutRoot is what the fake git prints for git rev-parse --show-toplevel.
const checkoutRoot = "/repo"

var ghArgs = []string{"run", "list", "--commit", sha, "--limit", "100", "--json", "databaseId,workflowDatabaseId,workflowName,status,conclusion,url,createdAt,updatedAt,headBranch,headSha,event,attempt"}

// counts is how many times the fake ran gh run list and gh run view, and for
// which runs.
type counts struct {
	list    int
	view    int
	viewIDs []string
	branch  int      // gh run list --branch calls
	logs    int      // gh run view --log-failed calls
	logIDs  []string // the job ids of those calls, in order
	events  []string // "list" and "view", in the order gh ran them
	clock   *fakeClock

	artifactIDs []string   // the run ids of the gh api artifacts calls, in order
	downloads   [][]string // the args of each gh run download call
	opened      []string   // the roots the App opened as the checkout

	reruns          []string // the run ids of the gh run rerun calls, in order
	rerunAfterLists []int    // how many gh run list calls had been made at each of those
}

// testInterval and testGrace are what the fake helpers set on the App.
const (
	testInterval = 10 * time.Second
	testGrace    = 60 * time.Second
	testLines    = 20 // the most lines of one failing test the table shows
)

// fixtureNow is after the createdAt of every recorded run, so the grace period
// of a recorded commit is long over.
var fixtureNow = time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)

// fakeClock is the clock the tests control. After never waits: it records d,
// moves the time on by d, runs the hook, and returns a channel that is ready,
// or, with hold, one that never is, so only the context can end the wait.
type fakeClock struct {
	now   time.Time
	waits []time.Duration
	hook  func()
	hold  bool
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.waits = append(c.waits, d)
	c.now = c.now.Add(d)
	if c.hook != nil {
		c.hook()
	}
	ch := make(chan time.Time, 1)
	if !c.hold {
		ch <- c.now
	}
	return ch
}

func (c *fakeClock) waited() time.Duration {
	var total time.Duration
	for _, d := range c.waits {
		total += d
	}
	return total
}

// withClock returns app with a fake clock at now and the test interval and
// grace period.
func withClock(app cli.App, now time.Time) (cli.App, *fakeClock) {
	c := &fakeClock{now: now}
	app.Clock = c
	app.Interval = testInterval
	app.Grace = testGrace
	app.Backoff = time.Second
	app.MaxBackoff = 30 * time.Second
	app.Retries = 8
	app.MaxTestLines = testLines
	return app, c
}

// fakeOpts changes what the fake gh prints for gh run view <id> --json jobs.
type fakeOpts struct {
	viewFixture  map[string]string // run id to a fixture other than run-view-<id>.json
	viewErr      map[string]error  // run id to the error gh run view fails with
	onView       func(id string)   // runs before gh run view answers
	viewErrCalls map[string]int    // run id to how many of its first gh run view calls fail with a network error

	logFixture  map[string]string // job id to a file in testdata/logs/ that gh run view --log-failed prints; an empty log if absent
	logErr      map[string]error  // job id to the error gh run view --log-failed fails with
	logErrCalls map[string]int    // job id to how many of its first log calls fail with a network error
	onLog       func(id string)   // runs before gh run view --log-failed answers

	artifacts     map[string]string            // run id to the output of gh api .../artifacts: a fixture name, or JSON if it starts with {; none if absent
	artifactsErr  map[string]error             // run id to the error the artifacts call fails with
	checkout      fstest.MapFS                 // the files of the checkout the App opens
	downloadErr   map[string]error             // artifact name to the error gh run download fails with
	root          string                       // a real directory the fake git prints as the toplevel and the App opens with os.DirFS, instead of checkout
	downloadFiles map[string]map[string]string // artifact name to the files (name to content) gh run download writes into its -D directory
	onDownload    func()                       // runs when gh run download is called
	tempDir       string                       // App.TempDir, the parent of the staging directories
	toplevelErr   error                        // the error git rev-parse --show-toplevel fails with
	rerunErr      map[string]error             // run id to the error gh run rerun fails with

	listFixtures []string      // gh run list prints these fixtures, one per call, the last repeated
	listRaw      []string      // like listFixtures, but the output itself
	listErr      map[int]error // call number (from 1) to the error gh run list fails with
	now          time.Time     // the fake clock's time, fixtureNow if zero
	onWait       func()        // runs when the fake clock is waited on
	holdWait     bool          // the fake clock's wait never ends by itself
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "gh", name)) //nolint:gosec // reads a fixture from testdata/ by a name the test supplies
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// readLog reads a recorded CI log from testdata/logs/.
func readLog(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "logs", name)) //nolint:gosec // reads a fixture from testdata/ by a name the test supplies
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fake returns an App whose git prints sha (or fails with gitErr) and whose gh
// run list prints the fixture (or fails with ghErr) and whose gh run view
// prints run-view-<id>.json. It fails the test on any other command, and
// counts the gh calls.
func fake(t *testing.T, fixture string, ghErr, gitErr error) (cli.App, *counts) {
	t.Helper()
	return fakeWith(t, fixture, ghErr, gitErr, fakeOpts{})
}

func fakeWith(t *testing.T, fixture string, ghErr, gitErr error, opts fakeOpts) (cli.App, *counts) {
	t.Helper()
	c := new(counts)
	viewCalls := map[string]int{}
	logCalls := map[string]int{}
	var lists [][]byte
	for _, f := range opts.listFixtures {
		lists = append(lists, readFixture(t, f))
	}
	for _, raw := range opts.listRaw {
		lists = append(lists, []byte(raw))
	}
	now := opts.now
	if now.IsZero() {
		now = fixtureNow
	}
	app := cli.App{GH: gh.Runner{Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "gh" && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		switch {
		case name == "git" && slices.Equal(args, []string{"rev-parse", "--show-toplevel"}):
			if opts.toplevelErr != nil {
				return nil, opts.toplevelErr
			}
			if opts.root != "" {
				return []byte(opts.root + "\n"), nil
			}
			return []byte(checkoutRoot + "\n"), nil
		case name == "git":
			if gitErr != nil {
				return nil, gitErr
			}
			return []byte(sha + "\n"), nil
		case name == "gh" && len(args) == 4 && args[0] == "api":
			id := strings.TrimSuffix(strings.TrimPrefix(args[1], "repos/{owner}/{repo}/actions/runs/"), "/artifacts?per_page=100")
			if want := []string{"api", "repos/{owner}/{repo}/actions/runs/" + id + "/artifacts?per_page=100", "--jq", "{total_count, artifacts: [.artifacts[] | {name}]}"}; !slices.Equal(args, want) {
				t.Errorf("gh args = %q, want %q", args, want)
			}
			c.artifactIDs = append(c.artifactIDs, id)
			if err := opts.artifactsErr[id]; err != nil {
				return nil, err
			}
			switch out, ok := opts.artifacts[id]; {
			case !ok:
				return []byte(`{"total_count":0,"artifacts":[]}`), nil
			case strings.HasPrefix(out, "{"):
				return []byte(out), nil
			default:
				return readFixture(t, out), nil
			}
		case name == "gh" && len(args) == 4 && args[0] == "run" && args[1] == "rerun" && args[3] == "--failed":
			c.reruns = append(c.reruns, args[2])
			c.rerunAfterLists = append(c.rerunAfterLists, c.list)
			c.events = append(c.events, "rerun")
			return nil, opts.rerunErr[args[2]]
		case name == "gh" && len(args) == 7 && args[0] == "run" && args[1] == "download":
			c.downloads = append(c.downloads, args)
			if opts.onDownload != nil {
				opts.onDownload()
			}
			if err := opts.downloadErr[args[4]]; err != nil {
				return nil, err
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			for name, content := range opts.downloadFiles[args[4]] {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(args[6], name)), 0o700); err != nil {
					t.Error(err)
				}
				if err := os.WriteFile(filepath.Join(args[6], name), []byte(content), 0o600); err != nil {
					t.Error(err)
				}
			}
			return nil, nil
		case name == "gh" && len(args) >= 4 && args[0] == "run" && args[1] == "list" && args[2] == "--branch":
			c.branch++
			if len(args) != 12 || args[6] != "--created" || args[8] != "--limit" || args[9] != "100" {
				t.Errorf("gh args = %q, want the lookup since the cancelled run: --branch, --workflow, --created and --limit 100", args)
			}
			return []byte("[]"), nil // these tests have no newer run; newer_test.go covers one
		case name == "gh" && len(args) >= 2 && args[0] == "run" && args[1] == "list":
			c.list++
			c.events = append(c.events, "list")
			if !reflect.DeepEqual(args, ghArgs) {
				t.Errorf("gh args = %q, want %q", args, ghArgs)
			}
			if err := opts.listErr[c.list]; err != nil {
				return nil, err
			}
			if ghErr != nil {
				return nil, ghErr
			}
			if len(lists) > 0 {
				return lists[min(c.list, len(lists))-1], nil
			}
			return readFixture(t, fixture), nil
		case name == "gh" && len(args) == 6 && args[0] == "run" && args[1] == "view" && args[3] == "--log-failed" && args[4] == "--job":
			id := args[5]
			c.logs++
			c.logIDs = append(c.logIDs, id)
			c.events = append(c.events, "log")
			if opts.onLog != nil {
				opts.onLog(id)
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			logCalls[id]++
			if logCalls[id] <= opts.logErrCalls[id] {
				return nil, networkErr(t)
			}
			if err := opts.logErr[id]; err != nil {
				return nil, err
			}
			if f, ok := opts.logFixture[id]; ok {
				return readLog(t, f), nil
			}
			return nil, nil
		case name == "gh" && len(args) == 5 && args[0] == "run" && args[1] == "view":
			id := args[2]
			c.view++
			c.events = append(c.events, "view")
			c.viewIDs = append(c.viewIDs, id)
			if want := []string{"run", "view", id, "--json", "jobs"}; !reflect.DeepEqual(args, want) {
				t.Errorf("gh args = %q, want %q", args, want)
			}
			if opts.onView != nil {
				opts.onView(id)
			}
			viewCalls[id]++
			if viewCalls[id] <= opts.viewErrCalls[id] {
				return nil, networkErr(t)
			}
			if err := opts.viewErr[id]; err != nil {
				return nil, err
			}
			file := "run-view-" + id + ".json"
			if f, ok := opts.viewFixture[id]; ok {
				file = f
			}
			return readFixture(t, file), nil
		}
		t.Errorf("unexpected command %s %q", name, args)
		return nil, errors.New("unexpected command")
	}}}
	app.OpenDir = func(root string) fs.FS {
		c.opened = append(c.opened, root)
		if opts.root != "" {
			return os.DirFS(opts.root)
		}
		return opts.checkout
	}
	app.TempDir = opts.tempDir
	app, c.clock = withClock(app, now)
	c.clock.hook = opts.onWait
	c.clock.hold = opts.holdWait
	return app, c
}

// inline returns an App whose gh run list prints list and whose gh run view
// prints jobs for every run.
func inline(list, jobs string) cli.App {
	app, _ := withClock(cli.App{GH: gh.Runner{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" {
			return []byte(sha + "\n"), nil
		}
		if len(args) > 0 && args[0] == "api" {
			return []byte(`{"total_count":0,"artifacts":[]}`), nil
		}
		if len(args) >= 2 && args[1] == "view" {
			return []byte(jobs), nil
		}
		return []byte(list), nil
	}}}, fixtureNow)
	return app
}

// noJobs is what gh run view prints for a run without jobs.
const noJobs = `{"jobs":[]}`

func TestRunExitCodes(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		fixture    string
		ghErr      error
		gitErr     error
		want       int
		stderr     string
		wantGHCall bool
		wantViews  int
	}{
		{"success", []string{"HEAD"}, "run-list-success.json", nil, nil, cli.ExitOK, "", true, 3},
		{"failure", []string{"HEAD"}, "run-list-failure.json", nil, nil, cli.ExitFailed, "", true, 2},
		{"cancelled", []string{"HEAD"}, "run-list-cancelled.json", nil, nil, cli.ExitCancelled, "", true, 1},
		{"in progress", []string{"--once", "HEAD"}, "run-list-in-progress.json", nil, nil, cli.ExitUnchecked, "in progress", true, 2},
		{"empty", []string{"HEAD"}, "run-list-empty.json", nil, nil, cli.ExitUnchecked, "no runs", true, 0},
		{"gh error", []string{"HEAD"}, "", errors.New("HTTP 401"), nil, cli.ExitUnchecked, "HTTP 401", true, 0},
		{"bad commit", []string{"nope"}, "", nil, errors.New("fatal: bad revision"), cli.ExitUsage, "ciwatch:", false, 0},
		{"no args", nil, "", nil, nil, cli.ExitUsage, "usage: ciwatch", false, 0},
		{"two commits", []string{"a", "b"}, "", nil, nil, cli.ExitUsage, "usage: ciwatch", false, 0},
		{"unknown flag", []string{"--bogus", "HEAD"}, "", nil, nil, cli.ExitUsage, "bogus", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, calls := fake(t, tt.fixture, tt.ghErr, tt.gitErr)
			var stdout, stderr bytes.Buffer
			code := app.Run(t.Context(), tt.args, &stdout, &stderr)
			if code != tt.want {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.want, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.stderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.stderr)
			}
			if (calls.list > 0) != tt.wantGHCall {
				t.Errorf("gh run list calls = %d, want called = %v", calls.list, tt.wantGHCall)
			}
			if calls.view != tt.wantViews {
				t.Errorf("gh run view calls = %d, want %d", calls.view, tt.wantViews)
			}
			// Runs that haven't finished are a result worth showing, so they
			// are the one exit-4 case that prints the table.
			if code != cli.ExitOK && code != cli.ExitFailed && code != cli.ExitCancelled && tt.name != "in progress" && stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing on error", stdout.String())
			}
		})
	}
}

func TestRunWithoutArgumentsPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.App{}.Run(t.Context(), nil, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUsage)
	}
	if !strings.HasPrefix(stderr.String(), "usage: ciwatch") {
		t.Errorf("stderr = %q, want the usage", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}

// row is one expected line of the table: a run, or a job indented under it.
type row struct {
	fields []string
	job    bool
}

func runRow(workflow, status, conclusion string) row {
	return row{fields: nonEmpty(workflow, status, conclusion)}
}

// jobRowsOf is the job rows for the recorded gh run view output in fixture.
func jobRowsOf(t *testing.T, fixture string) []row {
	t.Helper()
	var view struct {
		Jobs []struct{ Name, Status, Conclusion string } `json:"jobs"`
	}
	if err := json.Unmarshal(readFixture(t, fixture), &view); err != nil {
		t.Fatal(err)
	}
	rows := make([]row, 0, len(view.Jobs))
	for _, j := range view.Jobs {
		fields := append(strings.Fields(j.Name), nonEmpty(j.Status, j.Conclusion)...)
		rows = append(rows, row{fields: fields, job: true})
	}
	return rows
}

func nonEmpty(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func checkTable(t *testing.T, stdout string, want []row) {
	t.Helper()
	table, _, _ := strings.Cut(stdout, "\n\n") // the failing-test blocks follow a blank line
	lines := strings.Split(strings.TrimRight(table, "\n"), "\n")
	if len(lines) != len(want)+1 {
		t.Fatalf("stdout = %q, want a header and %d rows, got %d lines", stdout, len(want), len(lines))
	}
	if got := strings.Fields(lines[0]); !reflect.DeepEqual(got, []string{"WORKFLOW", "STATUS", "CONCLUSION"}) {
		t.Errorf("header = %q", lines[0])
	}
	for i, w := range want {
		l := lines[i+1]
		if got := strings.Fields(l); !reflect.DeepEqual(got, w.fields) {
			t.Errorf("line %d = %q, want fields %q", i+1, l, w.fields)
		}
		if indented := strings.HasPrefix(l, " "); indented != w.job {
			t.Errorf("line %d = %q, indented = %v, want %v", i+1, l, indented, w.job)
		}
	}
}

func TestRunPrintsTable(t *testing.T) {
	app, _ := fake(t, "run-list-failure.json", nil, nil)
	var stdout, stderr bytes.Buffer
	app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr)
	want := []row{runRow("Fuzz", "completed", "failure")}
	want = append(want, jobRowsOf(t, "run-view-44729305066.json")...)
	want = append(want, runRow("CI", "completed", "success"))
	want = append(want, jobRowsOf(t, "run-view-44709305564.json")...)
	checkTable(t, stdout.String(), want)
	table, _, _ := strings.Cut(stdout.String(), "\n\n") // the failed job's block follows it
	rows := strings.Count(table, "\n")                  // every line but the last ends in a newline, and one is the header
	if !strings.Contains(stdout.String(), "FuzzPromisorHostSite") || rows != 2+43+5 {
		t.Errorf("table has %d rows and %q, want the 2 runs and their 48 jobs, with the failed job", rows, stdout.String())
	}
}

func TestRunJSON(t *testing.T) {
	for _, args := range [][]string{{"HEAD", "--json"}, {"--json", "HEAD"}} {
		app, _ := fake(t, "run-list-failure.json", nil, nil)
		var stdout, stderr bytes.Buffer
		code := app.Run(t.Context(), args, &stdout, &stderr)
		if code != cli.ExitFailed {
			t.Errorf("%q: exit code = %d, want %d", args, code, cli.ExitFailed)
		}
		var got struct {
			SHA  string `json:"sha"`
			Runs []struct {
				ID         int64  `json:"id"`
				Workflow   string `json:"workflow"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				URL        string `json:"url"`
				Jobs       []struct {
					ID         int64  `json:"id"`
					Name       string `json:"name"`
					Status     string `json:"status"`
					Conclusion string `json:"conclusion"`
					URL        string `json:"url"`
				} `json:"jobs"`
			} `json:"runs"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("%q: stdout %q is not JSON: %v", args, stdout.String(), err)
		}
		if got.SHA != sha || len(got.Runs) != 2 {
			t.Fatalf("%q: got %+v, want sha %s and 2 runs", args, got, sha)
		}
		if r := got.Runs[0]; r.ID != 44729305066 || r.Workflow != "Fuzz" || r.Status != "completed" || r.Conclusion != "failure" || r.URL == "" {
			t.Errorf("%q: first run = %+v", args, r)
		}
		if n := len(got.Runs[0].Jobs); n != 43 {
			t.Fatalf("%q: first run has %d jobs, want 43", args, n)
		}
		failed := got.Runs[0].Jobs[40]
		if failed.ID != 136264712505 || failed.Name != "FuzzPromisorHostSite" || failed.Status != "completed" || failed.Conclusion != "failure" ||
			failed.URL != "https://github.com/devvmhq/devvm/actions/runs/44729305066/job/136264712505" {
			t.Errorf("%q: failed job = %+v", args, failed)
		}
		if n := len(got.Runs[1].Jobs); n != 5 || got.Runs[1].Jobs[0].Name != "nocgo" || got.Runs[1].Jobs[4].Name != "check (ubuntu-latest, America/Los_Angeles)" {
			t.Errorf("%q: CI jobs = %+v, want its 5 jobs", args, got.Runs[1].Jobs)
		}
	}
}

func TestRunPassesResolvedSHAToGh(t *testing.T) {
	app, calls := fake(t, "run-list-success.json", nil, nil) // fake asserts gh gets sha, not HEAD
	var stdout, stderr bytes.Buffer
	app.Run(t.Context(), []string{"--once", "HEAD"}, &stdout, &stderr)
	if calls.list != 1 {
		t.Errorf("gh run list calls = %d, want 1", calls.list)
	}
}

func TestRunExitCodePrecedence(t *testing.T) {
	run := func(status, conclusion string) string {
		return `{"databaseId":1,"workflowName":"w","status":"` + status + `","conclusion":"` + conclusion + `","url":"u"}`
	}
	tests := []struct {
		name string
		runs []string
		want int
	}{
		{"skipped and neutral pass", []string{run("completed", "skipped"), run("completed", "neutral")}, cli.ExitOK},
		{"timed out fails", []string{run("completed", "timed_out")}, cli.ExitFailed},
		{"startup failure fails", []string{run("completed", "startup_failure")}, cli.ExitFailed},
		{"action required fails", []string{run("completed", "action_required")}, cli.ExitFailed},
		{"failure beats cancelled", []string{run("completed", "cancelled"), run("completed", "failure")}, cli.ExitFailed},
		{"failure beats in progress", []string{run("in_progress", ""), run("completed", "failure")}, cli.ExitFailed},
		{"cancelled beats in progress", []string{run("queued", ""), run("completed", "cancelled")}, cli.ExitCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := inline("["+strings.Join(tt.runs, ",")+"]", noJobs)
			var stdout, stderr bytes.Buffer
			if code := app.Run(t.Context(), []string{"--once", "HEAD"}, &stdout, &stderr); code != tt.want {
				t.Errorf("exit code = %d, want %d", code, tt.want)
			}
		})
	}
}

func TestRunMalformedGhOutputCouldNotCheck(t *testing.T) {
	app, _ := withClock(cli.App{GH: gh.Runner{Exec: func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "git" {
			return []byte(sha + "\n"), nil
		}
		return []byte("oops"), nil
	}}}, fixtureNow)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUnchecked)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}

func TestRunDashDashEndsFlags(t *testing.T) {
	app, calls := fake(t, "run-list-success.json", nil, nil)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--once", "--", "HEAD"}, &stdout, &stderr); code != cli.ExitOK || calls.list != 1 {
		t.Errorf("exit code = %d, gh calls = %d, want 0 and 1 (stderr %q)", code, calls.list, stderr.String())
	}
}

func TestRunGitFailuresThatAreNotBadRevisions(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	tests := []struct {
		name   string
		ctx    context.Context
		gitErr error
	}{
		{"not a git repository", t.Context(), errors.New("git: exit status 128: fatal: not a git repository (or any of the parent directories): .git")},
		{"git missing", t.Context(), exec.ErrNotFound},
		{"context cancelled", canceled, context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, calls := fake(t, "", nil, tt.gitErr)
			var stdout, stderr bytes.Buffer
			if code := app.Run(tt.ctx, []string{"HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitUnchecked, stderr.String())
			}
			if calls.list != 0 || calls.view != 0 {
				t.Errorf("gh calls = %d list and %d view, want 0", calls.list, calls.view)
			}
		})
	}
}

func TestRunFullPageOfRunsMayBeTruncated(t *testing.T) {
	run := func(conclusion string) string {
		return `{"databaseId":1,"workflowName":"w","status":"completed","conclusion":"` + conclusion + `","url":"u"}`
	}
	page := func(first string) string {
		runs := []string{first}
		for len(runs) < 100 {
			runs = append(runs, run("success"))
		}
		return "[" + strings.Join(runs, ",") + "]"
	}
	tests := []struct {
		name       string
		out        string
		want       int
		wantWarned bool
	}{
		{"all pass but maybe more", page(run("success")), cli.ExitUnchecked, true},
		{"failure is already known", page(run("failure")), cli.ExitFailed, false},
		{"cancellation is already known", page(run("cancelled")), cli.ExitCancelled, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := inline(tt.out, noJobs)
			var stdout, stderr bytes.Buffer
			if code := app.Run(t.Context(), []string{"--once", "HEAD"}, &stdout, &stderr); code != tt.want {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.want, stderr.String())
			}
			// The rerun hint of a failed run is not the warning.
			if warned := strings.Contains(stderr.String(), "can't vouch"); warned != tt.wantWarned {
				t.Errorf("stderr = %q, want a warning = %v", stderr.String(), tt.wantWarned)
			}
		})
	}
}

func TestRunOnlySuccessNeutralAndSkippedPass(t *testing.T) {
	tests := []struct {
		conclusion string
		want       int
	}{
		{"success", cli.ExitOK},
		{"neutral", cli.ExitOK},
		{"skipped", cli.ExitOK},
		{"stale", cli.ExitFailed},
		{"", cli.ExitFailed},
		{"something_github_adds_later", cli.ExitFailed},
	}
	for _, tt := range tests {
		t.Run(tt.conclusion, func(t *testing.T) {
			out := `[{"databaseId":1,"workflowName":"w","status":"completed","conclusion":"` + tt.conclusion + `","url":"u"}]`
			app := inline(out, noJobs)
			var stdout, stderr bytes.Buffer
			if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != tt.want {
				t.Errorf("exit code = %d, want %d", code, tt.want)
			}
		})
	}
}

func TestRunInProgressPrintsJobs(t *testing.T) {
	app, _ := fakeWith(t, "run-list-in-progress.json", nil, nil, fakeOpts{
		viewFixture: map[string]string{"45017063720": "run-view-45017063720-in-progress.json"},
	})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--once", "HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUnchecked)
	}
	want := []row{runRow("Fuzz", "in_progress", "")}
	want = append(want, jobRowsOf(t, "run-view-45017063720-in-progress.json")...)
	want = append(want, runRow("CI", "completed", "success"))
	want = append(want, jobRowsOf(t, "run-view-44951283922.json")...)
	checkTable(t, stdout.String(), want)
	var inProgress, queued int
	for _, l := range strings.Split(stdout.String(), "\n") {
		f := strings.Fields(l)
		if !strings.HasPrefix(l, " ") || len(f) == 0 {
			continue
		}
		switch f[len(f)-1] {
		case "in_progress":
			inProgress++
		case "queued":
			queued++
		}
	}
	if inProgress == 0 || queued == 0 {
		t.Errorf("%d in_progress and %d queued job rows, want some of each", inProgress, queued)
	}
}

func TestRunCallsRunViewOncePerRun(t *testing.T) {
	app, calls := fake(t, "run-list-success.json", nil, nil)
	var stdout, stderr bytes.Buffer
	app.Run(t.Context(), []string{"--once", "HEAD"}, &stdout, &stderr)
	if calls.list != 1 || calls.view != 3 {
		t.Errorf("calls = %d list and %d view, want 1 and 3", calls.list, calls.view)
	}
	if want := []string{"45017063720", "44951283922", "44951284032"}; !reflect.DeepEqual(calls.viewIDs, want) {
		t.Errorf("gh run view ids = %q, want %q", calls.viewIDs, want)
	}
}

func TestRunJobsDoNotChangeExitCode(t *testing.T) {
	run := func(conclusion string) string {
		return `[{"databaseId":7,"workflowName":"w","status":"completed","conclusion":"` + conclusion + `","url":"u"}]`
	}
	job := func(conclusion string) string {
		return `{"jobs":[{"databaseId":8,"name":"j","status":"completed","conclusion":"` + conclusion + `","url":"ju"}]}`
	}
	tests := []struct {
		name             string
		runConc, jobConc string
		want             int
	}{
		{"successful run with a failed job", "success", "failure", cli.ExitOK},
		{"failed run whose jobs all succeeded", "failure", "success", cli.ExitFailed},
		{"cancelled run with a failed job", "cancelled", "failure", cli.ExitCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := inline(run(tt.runConc), job(tt.jobConc)).Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != tt.want {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.want, stderr.String())
			}
			checkTable(t, stdout.String(), []row{
				runRow("w", "completed", tt.runConc),
				{fields: []string{"j", "completed", tt.jobConc}, job: true},
			})
		})
	}
}

func TestRunJobsErrorKeepsRunExitCode(t *testing.T) {
	app, calls := fakeWith(t, "run-list-failure.json", nil, nil, fakeOpts{
		viewErr: map[string]error{"44729305066": errors.New("HTTP 401")},
	})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Errorf("exit code = %d, want %d", code, cli.ExitFailed)
	}
	if want := "ciwatch: can't list jobs for run 44729305066 (Fuzz): "; !strings.Contains(stderr.String(), want) || !strings.Contains(stderr.String(), "HTTP 401") {
		t.Errorf("stderr = %q, want it to contain %q and HTTP 401", stderr.String(), want)
	}
	if calls.view != 2 {
		t.Errorf("gh run view calls = %d, want 2, one for each run", calls.view)
	}
	want := []row{
		runRow("Fuzz", "completed", "failure"),
		{fields: []string{"(jobs", "unavailable)"}, job: true},
		runRow("CI", "completed", "success"),
	}
	want = append(want, jobRowsOf(t, "run-view-44709305564.json")...)
	checkTable(t, stdout.String(), want)
}

func TestRunJSONShowsJobsError(t *testing.T) {
	app, _ := fakeWith(t, "run-list-failure.json", nil, nil, fakeOpts{
		viewErr: map[string]error{"44729305066": errors.New("HTTP 401")},
	})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD", "--json"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Errorf("exit code = %d, want %d", code, cli.ExitFailed)
	}
	var got struct {
		Runs []map[string]json.RawMessage `json:"runs"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout %q is not JSON: %v", stdout.String(), err)
	}
	if len(got.Runs) != 2 {
		t.Fatalf("stdout = %q, want 2 runs", stdout.String())
	}
	var msg string
	if err := json.Unmarshal(got.Runs[0]["jobs_error"], &msg); err != nil || !strings.Contains(msg, "HTTP 401") {
		t.Errorf("failed run's jobs_error = %s, want a string containing HTTP 401", got.Runs[0]["jobs_error"])
	}
	if _, ok := got.Runs[1]["jobs_error"]; ok {
		t.Errorf("run whose jobs were listed has jobs_error %s, want it omitted", got.Runs[1]["jobs_error"])
	}
}

func TestRunJSONJobsAreAlwaysArrays(t *testing.T) {
	tests := []struct {
		name   string
		app    func() cli.App
		stderr bool
	}{
		{"run without jobs", func() cli.App {
			return inline(`[{"databaseId":7,"workflowName":"w","status":"completed","conclusion":"success","url":"u"}]`, noJobs)
		}, false},
		{"gh run view fails", func() cli.App {
			app, _ := fakeWith(t, "run-list-failure.json", nil, nil, fakeOpts{
				viewErr: map[string]error{"44729305066": errors.New("HTTP 401"), "44709305564": errors.New("HTTP 401")},
			})
			return app
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			tt.app().Run(t.Context(), []string{"--once", "HEAD", "--json"}, &stdout, &stderr)
			var got struct {
				Runs []map[string]json.RawMessage `json:"runs"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("stdout %q is not JSON: %v", stdout.String(), err)
			}
			if len(got.Runs) == 0 {
				t.Fatalf("stdout = %q, want runs", stdout.String())
			}
			for _, r := range got.Runs {
				if jobs := string(r["jobs"]); jobs != "[]" {
					t.Errorf("jobs = %q, want []", jobs)
				}
			}
			if (stderr.Len() > 0) != tt.stderr {
				t.Errorf("stderr = %q, want a warning = %v", stderr.String(), tt.stderr)
			}
		})
	}
}

func TestRunCancelledWhileFetchingJobs(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	app, _ := withClock(cli.App{GH: gh.Runner{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "git":
			return []byte(sha + "\n"), nil
		case len(args) >= 2 && args[1] == "view":
			cancel()
			return nil, context.Canceled
		}
		return readFixture(t, "run-list-failure.json"), nil
	}}}, fixtureNow)
	var stdout, stderr bytes.Buffer
	if code := app.Run(ctx, []string{"HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
		t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitUnchecked, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
	if !strings.Contains(stderr.String(), "context canceled") || strings.Contains(stderr.String(), "can't list jobs") {
		t.Errorf("stderr = %q, want the cancellation and no per-run warning", stderr.String())
	}
}

func TestRunHelpPrintsOnlyUsage(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		t.Run(flag, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := cli.App{}.Run(t.Context(), []string{flag}, &stdout, &stderr)
			if code != cli.ExitUsage {
				t.Errorf("exit code = %d, want %d", code, cli.ExitUsage)
			}
			if !strings.HasPrefix(stderr.String(), "usage: ciwatch") {
				t.Errorf("stderr = %q, want it to start with the usage", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", stdout.String())
			}
		})
	}
}

// Runs as gh run list prints them, with the createdAt of the recorded runs.
const (
	fuzzRunning = `{"conclusion":"","createdAt":"2026-11-22T11:23:10Z","databaseId":45017063720,"status":"in_progress","url":"u","workflowName":"Fuzz"}`
	fuzzDone    = `{"conclusion":"success","createdAt":"2026-11-22T11:23:10Z","databaseId":45017063720,"status":"completed","url":"u","workflowName":"Fuzz"}`
	ciDone      = `{"conclusion":"success","createdAt":"2026-11-21T21:23:21Z","databaseId":44951283922,"status":"completed","url":"u","workflowName":"CI"}`
	docsQueued  = `{"conclusion":"","createdAt":"2026-11-21T21:23:21Z","databaseId":44951284032,"status":"queued","url":"u","workflowName":"Docs"}`
	docsDone    = `{"conclusion":"success","createdAt":"2026-11-21T21:23:21Z","databaseId":44951284032,"status":"completed","url":"u","workflowName":"Docs"}`
	docsFailed  = `{"conclusion":"failure","createdAt":"2026-11-21T21:23:21Z","databaseId":44951284032,"status":"completed","url":"u","workflowName":"Docs"}`
)

func list(runs ...string) string { return "[" + strings.Join(runs, ",") + "]" }

func TestWatchPollsUntilAllComplete(t *testing.T) {
	app, calls := fakeWith(t, "", nil, nil, fakeOpts{
		listFixtures: []string{"run-list-in-progress.json", "run-list-in-progress.json", "run-list-success.json"},
	})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitOK, stderr.String())
	}
	// The runs are old, so after they complete it still waits out the grace
	// period from the start: six waits and seven lists.
	if calls.list != 7 {
		t.Errorf("gh run list calls = %d, want 7", calls.list)
	}
	if want := slices.Repeat([]time.Duration{testInterval}, 6); !reflect.DeepEqual(calls.clock.waits, want) {
		t.Errorf("waits = %v, want %v", calls.clock.waits, want)
	}
	if want := append(slices.Repeat([]string{"list"}, 7), "view", "view", "view"); !reflect.DeepEqual(calls.events, want) {
		t.Errorf("gh calls = %q, want the jobs fetched once, after the last list: %q", calls.events, want)
	}
	var want []row
	want = append(want, runRow("Fuzz", "completed", "success"))
	want = append(want, jobRowsOf(t, "run-view-45017063720.json")...)
	want = append(want, runRow("CI", "completed", "success"))
	want = append(want, jobRowsOf(t, "run-view-44951283922.json")...)
	want = append(want, runRow("Docs", "completed", "success"))
	want = append(want, jobRowsOf(t, "run-view-44951284032.json")...)
	checkTable(t, stdout.String(), want)
}

func TestWatchGraceFindsLateRun(t *testing.T) {
	// The push, and so the run, was created just now.
	createdNow := strings.ReplaceAll(ciDone, "2026-11-21T21:23:21Z", fixtureNow.Format(time.RFC3339))
	app, calls := fakeWith(t, "", nil, nil, fakeOpts{
		listRaw: []string{
			list(createdNow),
			list(createdNow, docsQueued),
			list(createdNow, docsFailed),
		},
	})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	if calls.list < 3 {
		t.Errorf("gh run list calls = %d, want at least 3: it stopped at the first list, where every run had completed", calls.list)
	}
	want := []row{runRow("CI", "completed", "success")}
	want = append(want, jobRowsOf(t, "run-view-44951283922.json")...)
	want = append(want, runRow("Docs", "completed", "failure"))
	want = append(want, jobRowsOf(t, "run-view-44951284032.json")...)
	checkTable(t, stdout.String(), want)
}

func TestWatchStopsWhenGraceEnds(t *testing.T) {
	createdNow := strings.ReplaceAll(ciDone, "2026-11-21T21:23:21Z", fixtureNow.Format(time.RFC3339))
	app, calls := fakeWith(t, "", nil, nil, fakeOpts{listRaw: []string{list(createdNow)}})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitOK, stderr.String())
	}
	if want := int(testGrace/testInterval) + 1; calls.list != want {
		t.Errorf("gh run list calls = %d, want %d", calls.list, want)
	}
}

func TestWatchOldCommitWaitsGraceOnce(t *testing.T) {
	// Every run is long finished, but a re-push could still add new ones.
	app, calls := fake(t, "run-list-success.json", nil, nil)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitOK, stderr.String())
	}
	if got := calls.clock.waited(); got != testGrace {
		t.Errorf("waited %v, want the grace period %v once, from ciwatch's start", got, testGrace)
	}
	if want := int(testGrace/testInterval) + 1; calls.list != want {
		t.Errorf("gh run list calls = %d, want %d", calls.list, want)
	}
}

func TestWatchRepushedCommitFindsNewRuns(t *testing.T) {
	// CI finished long ago; the re-push's Docs run appears on the third poll.
	app, calls := fakeWith(t, "", nil, nil, fakeOpts{
		listRaw: []string{list(ciDone), list(ciDone), list(ciDone, docsQueued), list(ciDone, docsFailed)},
	})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	if calls.list < 4 {
		t.Errorf("gh run list calls = %d, want at least 4: it stopped at the first list, where every run had completed", calls.list)
	}
}

func TestWatchGraceShorterThanIntervalStillWaitsAnInterval(t *testing.T) {
	// The run was created exactly one grace period before ciwatch started, so
	// its grace period ends at the start.
	const grace = 5 * time.Second
	created := strings.ReplaceAll(ciDone, "2026-11-21T21:23:21Z", fixtureNow.Add(-grace).Format(time.RFC3339))
	app, calls := fakeWith(t, "", nil, nil, fakeOpts{listRaw: []string{list(created)}})
	app.Grace = grace
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitOK, stderr.String())
	}
	if got := calls.clock.waited(); got < testInterval {
		t.Errorf("waited %v, want at least one interval %v", got, testInterval)
	}
}

func TestWatchGraceWaitsAnIntervalWhateverTheClocks(t *testing.T) {
	// The local clock is two minutes ahead of GitHub's, so the run's createdAt
	// plus the grace period is already past when ciwatch starts.
	createdNow := strings.ReplaceAll(ciDone, "2026-11-21T21:23:21Z", fixtureNow.Format(time.RFC3339))
	app, calls := fakeWith(t, "", nil, nil, fakeOpts{
		listRaw: []string{list(createdNow), list(createdNow, docsFailed)},
		now:     fixtureNow.Add(2 * time.Minute),
	})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	if got := calls.clock.waited(); got < testInterval {
		t.Errorf("waited %v, want at least one interval %v before the grace period can end", got, testInterval)
	}
}

func TestWatchNoRunsAfterGrace(t *testing.T) {
	app, calls := fake(t, "run-list-empty.json", nil, nil)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUnchecked)
	}
	if !strings.Contains(stderr.String(), "no runs found") {
		t.Errorf("stderr = %q, want it to say no runs were found", stderr.String())
	}
	if got := calls.clock.waited(); got < testGrace {
		t.Errorf("waited %v, want at least the grace period %v", got, testGrace)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}

// progressSequence is three polls with Fuzz running, then Docs queued late,
// then everything done.
func progressSequence() fakeOpts {
	return fakeOpts{listRaw: []string{
		list(fuzzRunning, ciDone),
		list(fuzzRunning, ciDone),
		list(fuzzRunning, ciDone),
		list(fuzzRunning, ciDone, docsQueued),
		list(fuzzDone, ciDone, docsDone),
	}}
}

func TestWatchProgressOnlyOnChange(t *testing.T) {
	app, _ := fakeWith(t, "", nil, nil, progressSequence())
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitOK, stderr.String())
	}
	want := "ciwatch: run 45017063720 (Fuzz): in_progress\n" +
		"ciwatch: run 44951283922 (CI): completed success\n" +
		"ciwatch: run 44951284032 (Docs): queued\n" +
		"ciwatch: run 45017063720 (Fuzz): completed success\n" +
		"ciwatch: run 44951284032 (Docs): completed success\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestWatchJSONIsOneDocument(t *testing.T) {
	app, _ := fakeWith(t, "", nil, nil, progressSequence())
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--json", "HEAD"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitOK, stderr.String())
	}
	dec := json.NewDecoder(&stdout)
	var doc struct {
		Runs []json.RawMessage `json:"runs"`
	}
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	if len(doc.Runs) != 3 {
		t.Errorf("document has %d runs, want 3", len(doc.Runs))
	}
	if dec.More() {
		t.Error("stdout has more than one JSON document")
	}
	if !strings.Contains(stderr.String(), "ciwatch: run 44951284032 (Docs): queued") {
		t.Errorf("stderr = %q, want the progress lines", stderr.String())
	}
}

func TestWatchCancelledWhileWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The wait never ends by itself, so only the ctx.Done() case can return.
	waiting := make(chan struct{})
	app, calls := fakeWith(t, "run-list-in-progress.json", nil, nil, fakeOpts{holdWait: true, onWait: func() { close(waiting) }})
	var stdout, stderr bytes.Buffer
	done := make(chan int)
	go func() { done <- app.Run(ctx, []string{"HEAD"}, &stdout, &stderr) }()
	<-waiting
	cancel()
	select {
	case code := <-done:
		if code != cli.ExitUnchecked {
			t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitUnchecked, stderr.String())
		}
	case <-time.After(5 * time.Second): // a failsafe on the real clock, only reached when the bug is
		t.Fatal("watch did not return after the context was cancelled while it waited")
	}
	if !strings.Contains(stderr.String(), "context canceled") {
		t.Errorf("stderr = %q, want the cancellation", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
	if calls.view != 0 {
		t.Errorf("gh run view calls = %d, want 0", calls.view)
	}
}

func TestWatchListErrorMidWatch(t *testing.T) {
	app, _ := fakeWith(t, "run-list-in-progress.json", nil, nil, fakeOpts{
		listErr: map[int]error{2: errors.New("HTTP 401")},
	})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUnchecked)
	}
	if !strings.Contains(stderr.String(), "HTTP 401") {
		t.Errorf("stderr = %q, want HTTP 401", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}

func TestOnceDoesNotWait(t *testing.T) {
	app, calls := fake(t, "run-list-in-progress.json", nil, nil)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--once", "HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUnchecked)
	}
	if calls.list != 1 || len(calls.clock.waits) != 0 {
		t.Errorf("gh run list calls = %d and waits = %v, want 1 and none", calls.list, calls.clock.waits)
	}
	if want := "ciwatch: 1 runs still in progress; drop --once to wait for them"; !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
	}
}

func TestWatchRunsWithoutTimesCountGraceFromStart(t *testing.T) {
	noTime := `{"conclusion":"success","databaseId":44951283922,"status":"completed","url":"u","workflowName":"CI"}`
	app, calls := fakeWith(t, "", nil, nil, fakeOpts{listRaw: []string{list(noTime)}})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitOK, stderr.String())
	}
	if want := int(testGrace/testInterval) + 1; calls.list != want {
		t.Errorf("gh run list calls = %d, want %d", calls.list, want)
	}
}

func TestOnceWithNoRunsDoesNotWait(t *testing.T) {
	app, calls := fake(t, "run-list-empty.json", nil, nil)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--once", "HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUnchecked)
	}
	if calls.list != 1 || len(calls.clock.waits) != 0 || !strings.Contains(stderr.String(), "no runs found") {
		t.Errorf("gh run list calls = %d, waits = %v, stderr = %q, want 1, none and no runs found", calls.list, calls.clock.waits, stderr.String())
	}
}

func TestWatchFirstListErrorExits(t *testing.T) {
	app, calls := fakeWith(t, "run-list-success.json", nil, nil, fakeOpts{listErr: map[int]error{1: errors.New("HTTP 401")}})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUnchecked)
	}
	if !strings.Contains(stderr.String(), "HTTP 401") || calls.list != 1 || stdout.Len() != 0 {
		t.Errorf("stderr = %q, list calls = %d, stdout = %q", stderr.String(), calls.list, stdout.String())
	}
}

// networkErr is the error gh fails with when the network is down, from the
// recorded stderr.
func networkErr(t *testing.T) error {
	t.Helper()
	return stderrErr(t, "stderr-network-dial.txt")
}

// stderrErr is the error gh fails with when it prints the recorded stderr.
func stderrErr(t *testing.T, name string) error {
	t.Helper()
	return fmt.Errorf("gh: exit status 1: %s", strings.TrimSpace(string(readFixture(t, name))))
}

func TestWatchRetriesNetworkError(t *testing.T) {
	app, calls := fakeWith(t, "run-list-success.json", nil, nil, fakeOpts{
		listErr: map[int]error{1: networkErr(t)},
	})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitOK, stderr.String())
	}
	if calls.list < 2 {
		t.Errorf("gh run list calls = %d, want 2 or more", calls.list)
	}
	if len(calls.clock.waits) == 0 || calls.clock.waits[0] != app.Backoff {
		t.Errorf("waits = %v, want the first to be %v", calls.clock.waits, app.Backoff)
	}
	for _, want := range []string{"gh run list: network error, retrying in 1s (retry 1 of 8)", "can't assign requested address"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
		}
	}
}

func TestRetryLineNamesTheCallOnce(t *testing.T) {
	app, _ := fakeWith(t, "run-list-success.json", nil, nil, fakeOpts{listErr: map[int]error{1: networkErr(t)}})
	var stdout, stderr bytes.Buffer
	app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr)
	want := "ciwatch: gh run list: network error, retrying in 1s (retry 1 of 8): gh: exit status 1: Get "
	if !strings.Contains(stderr.String(), want) || strings.Contains(stderr.String(), "gh run list: gh run list") {
		t.Errorf("stderr = %q, want a line starting %q, naming the call once", stderr.String(), want)
	}
}

func TestRetryBackoffDoublesAndCaps(t *testing.T) {
	errs := map[int]error{}
	for i := 1; i <= 5; i++ {
		errs[i] = networkErr(t)
	}
	app, calls := fakeWith(t, "run-list-success.json", nil, nil, fakeOpts{listErr: errs})
	app.Backoff = time.Second
	app.MaxBackoff = 4 * time.Second
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitOK, stderr.String())
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second, 4 * time.Second}
	if len(calls.clock.waits) < len(want) || !reflect.DeepEqual(calls.clock.waits[:len(want)], want) {
		t.Errorf("waits = %v, want them to start %v", calls.clock.waits, want)
	}
}

func TestRetriesRunOutExitsUnchecked(t *testing.T) {
	app, calls := fakeWith(t, "run-list-success.json", networkErr(t), nil, fakeOpts{})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUnchecked)
	}
	if want := app.Retries + 1; calls.list != want {
		t.Errorf("gh run list calls = %d, want %d", calls.list, want)
	}
	if len(calls.clock.waits) != app.Retries {
		t.Errorf("waits = %v, want %d of them", calls.clock.waits, app.Retries)
	}
	if want := "gave up after 8 retries: gh run list: "; !strings.Contains(stderr.String(), want) || strings.Count(stderr.String(), "gh run list") != 9 { // 8 retry lines and the give-up line
		t.Errorf("stderr = %q, want it to contain %q, naming the call once per line", stderr.String(), want)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}

func TestOnceRetriesNetworkError(t *testing.T) {
	app, calls := fakeWith(t, "run-list-in-progress.json", nil, nil, fakeOpts{
		listErr: map[int]error{1: networkErr(t)},
	})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--once", "HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
		t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitUnchecked, stderr.String())
	}
	if calls.list != 2 || len(calls.clock.waits) != 1 {
		t.Errorf("gh run list calls = %d and waits = %v, want 2 and one", calls.list, calls.clock.waits)
	}
}

func TestRunJobsRetriesNetworkError(t *testing.T) {
	app, calls := fakeWith(t, "run-list-failure.json", nil, nil, fakeOpts{
		viewErrCalls: map[string]int{"44729305066": 1},
	})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	if strings.Contains(stdout.String(), "jobs unavailable") {
		t.Errorf("stdout = %q, want the jobs, not (jobs unavailable)", stdout.String())
	}
	want := []row{runRow("Fuzz", "completed", "failure")}
	want = append(want, jobRowsOf(t, "run-view-44729305066.json")...)
	want = append(want, runRow("CI", "completed", "success"))
	want = append(want, jobRowsOf(t, "run-view-44709305564.json")...)
	checkTable(t, stdout.String(), want)
	// The watch's poll waits come first; the backoff wait is the last one.
	if n := len(calls.clock.waits); calls.view != 3 || n == 0 || calls.clock.waits[n-1] != app.Backoff || !strings.Contains(stderr.String(), "gh run view 44729305066: network error, retrying in 1s") {
		t.Errorf("gh run view calls = %d, waits = %v, stderr = %q, want 3, a last wait of the backoff and the retry line", calls.view, calls.clock.waits, stderr.String())
	}
}

func TestRunJobsRetriesRunOutKeepRunExitCode(t *testing.T) {
	app, calls := fakeWith(t, "run-list-failure.json", nil, nil, fakeOpts{
		viewErrCalls: map[string]int{"44729305066": 1000},
	})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	if !strings.Contains(stdout.String(), "(jobs unavailable)") || !strings.Contains(stderr.String(), "can't list jobs for run 44729305066 (Fuzz): gave up after 8 retries: gh run view 44729305066: ") {
		t.Errorf("stdout = %q, stderr = %q, want (jobs unavailable) and the gave-up warning", stdout.String(), stderr.String())
	}
	if want := app.Retries + 1 + 1; calls.view != want { // the failing run's tries, and the other run's one call
		t.Errorf("gh run view calls = %d, want %d", calls.view, want)
	}
}

func TestRunJobsGivesUpOnceForAllRuns(t *testing.T) {
	app, calls := fakeWith(t, "run-list-failure.json", nil, nil, fakeOpts{
		viewErrCalls: map[string]int{"44729305066": 1000, "44709305564": 1000},
	})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	// The first run uses up the retries; the network is known to be down, so the
	// second run gets one try and no waits.
	if want := app.Retries + 1 + 1; calls.view != want {
		t.Errorf("gh run view calls = %d, want %d", calls.view, want)
	}
	if n := strings.Count(stdout.String(), "(jobs unavailable)"); n != 2 {
		t.Errorf("stdout = %q, want (jobs unavailable) twice", stdout.String())
	}
}

func TestRunJobsRetriesAgainAfterACallSucceeds(t *testing.T) {
	app, calls := fakeWith(t, "run-list-success.json", nil, nil, fakeOpts{
		// The first run runs out of retries, the second answers, and the third
		// hits one network error: the network is back, so it must be retried.
		viewErrCalls: map[string]int{"45017063720": 1000, "44951284032": 1},
	})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitOK {
		t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitOK, stderr.String())
	}
	if n := strings.Count(stdout.String(), "(jobs unavailable)"); n != 1 {
		t.Errorf("stdout = %q, want (jobs unavailable) for the first run only", stdout.String())
	}
	if want := app.Retries + 1 + 1 + 2; calls.view != want { // the first run's tries, the second's call, the third's two
		t.Errorf("gh run view calls = %d, want %d", calls.view, want)
	}
}

func TestNewRetryDefaults(t *testing.T) {
	app := cli.New()
	if app.Backoff != time.Second || app.MaxBackoff != 30*time.Second || app.Retries != 8 {
		t.Errorf("Backoff, MaxBackoff, Retries = %v, %v, %d, want 1s, 30s, 8", app.Backoff, app.MaxBackoff, app.Retries)
	}
}

func TestRetriesTemporaryErrors(t *testing.T) {
	for _, name := range []string{"stderr-502.txt", "stderr-503.txt", "stderr-504.txt"} {
		t.Run(name, func(t *testing.T) {
			app, calls := fakeWith(t, "run-list-success.json", nil, nil, fakeOpts{
				listErr: map[int]error{1: stderrErr(t, name)},
			})
			var stdout, stderr bytes.Buffer
			if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitOK {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitOK, stderr.String())
			}
			if calls.list < 2 || len(calls.clock.waits) == 0 || calls.clock.waits[0] != app.Backoff {
				t.Errorf("gh run list calls = %d and waits = %v, want 2 or more and a first wait of %v", calls.list, calls.clock.waits, app.Backoff)
			}
			if want := "gh run list: temporary error, retrying in 1s (retry 1 of 8)"; !strings.Contains(stderr.String(), want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
			}
		})
	}
}

func TestCancelledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	waiting := make(chan struct{})
	app, calls := fakeWith(t, "run-list-success.json", networkErr(t), nil, fakeOpts{holdWait: true, onWait: func() { close(waiting) }})
	var stdout, stderr bytes.Buffer
	done := make(chan int)
	go func() { done <- app.Run(ctx, []string{"HEAD"}, &stdout, &stderr) }()
	<-waiting
	cancel()
	select {
	case code := <-done:
		if code != cli.ExitUnchecked {
			t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitUnchecked, stderr.String())
		}
	case <-time.After(5 * time.Second): // a failsafe on the real clock, only reached when the bug is
		t.Fatal("ciwatch did not return after the context was cancelled during a backoff wait")
	}
	if !strings.Contains(stderr.String(), "context canceled") {
		t.Errorf("stderr = %q, want the cancellation", stderr.String())
	}
	if calls.list != 1 || len(calls.clock.waits) != 1 {
		t.Errorf("gh run list calls = %d and waits = %v, want 1 and one: no retry after the cancel", calls.list, calls.clock.waits)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}

func TestNonNetworkErrorsFailAtOnce(t *testing.T) {
	for _, name := range []string{"stderr-401.txt", "stderr-404.txt"} {
		t.Run(name, func(t *testing.T) {
			app, calls := fakeWith(t, "run-list-success.json", stderrErr(t, name), nil, fakeOpts{})
			var stdout, stderr bytes.Buffer
			if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
				t.Errorf("exit code = %d, want %d", code, cli.ExitUnchecked)
			}
			if calls.list != 1 || len(calls.clock.waits) != 0 {
				t.Errorf("gh run list calls = %d and waits = %v, want 1 and none", calls.list, calls.clock.waits)
			}
			if strings.Contains(stderr.String(), "retrying") {
				t.Errorf("stderr = %q, want no retry line", stderr.String())
			}
		})
	}
}

// The recorded CI run 44707997310 with its check (ubuntu-latest, ...) job
// failed, and the log that job printed.
const (
	failedRun     = "44707997310"
	failedJob     = "136192285080"
	failedJobName = "check (ubuntu-latest, America/Los_Angeles)"
	otherJob      = "136192284970" // check (macos-latest, UTC), which failed in run-view-44707997310-two-failures.json
)

// failingTestsOpts is the fake gh for run-list-test-failure.json: the ubuntu
// job failed, and printed log.
func failingTestsOpts(log string) fakeOpts {
	return fakeOpts{
		viewFixture: map[string]string{failedRun: "run-view-44707997310-test-failure.json"},
		logFixture:  map[string]string{failedJob: log},
	}
}

// failingTest is one entry of a job's "tests" in --json.
type failingTest struct {
	Package     string   `json:"package"`
	Name        string   `json:"name"`
	Lines       []string `json:"lines"`
	RunningTest string   `json:"running_test"`
}

// jsonJobs runs ciwatch --json and returns the jobs of every run, by job id, as
// raw keys so a missing key can be told from an empty one.
func jsonJobs(t *testing.T, app cli.App, wantCode int, flags ...string) (map[string]map[string]json.RawMessage, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), append([]string{"--json"}, append(flags, "HEAD")...), &stdout, &stderr); code != wantCode {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, wantCode, stderr.String())
	}
	var got struct {
		Runs []struct {
			Jobs []map[string]json.RawMessage `json:"jobs"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout %q is not JSON: %v", stdout.String(), err)
	}
	jobs := map[string]map[string]json.RawMessage{}
	for _, r := range got.Runs {
		for _, j := range r.Jobs {
			jobs[string(j["id"])] = j
		}
	}
	return jobs, stderr.String()
}

func jsonTests(t *testing.T, job map[string]json.RawMessage) []failingTest {
	t.Helper()
	raw, ok := job["tests"]
	if !ok {
		t.Fatalf("job %s has no tests key", job["id"])
	}
	var tests []failingTest
	if err := json.Unmarshal(raw, &tests); err != nil {
		t.Fatal(err)
	}
	return tests
}

func TestRunShowsFailingTestsInTable(t *testing.T) {
	app, _ := fakeWith(t, "run-list-test-failure.json", nil, nil, failingTestsOpts("gotestsum-failures.log"))
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--go-tests", "HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	out := stdout.String()
	table, blocks, ok := strings.Cut(out, "\n\n")
	if !ok {
		t.Fatalf("stdout = %q, want the table, a blank line and a block", out)
	}
	// The table is as it was: a run row and its five job rows, aligned.
	want := []row{runRow("CI", "completed", "failure")}
	want = append(want, jobRowsOf(t, "run-view-44707997310-test-failure.json")...)
	checkTable(t, table+"\n", want)
	for _, s := range []string{
		failedJobName + " (CI, run " + failedRun + "):",
		"FAIL internal/vm TestAgentImportFailsWhole",
		"agentimport_test.go:957: changed:",
		"agentimport_test.go:963: changed true: the store has",
	} {
		if !strings.Contains(blocks, s) {
			t.Errorf("block = %q, want it to contain %q", blocks, s)
		}
	}
	// The log's lines are outside the failed step's window in the jobs JSON, so
	// no step's lines show: only the Go parser's failing tests do.
	for _, s := range []string{"DONE 4183", "SKIP", "##[error]", "TestAgentImportCaseOnly"} {
		if strings.Contains(out, s) {
			t.Errorf("stdout = %q, want it to leave out %q", out, s)
		}
	}
}

func TestRunJSONHasFailingTests(t *testing.T) {
	app, _ := fakeWith(t, "run-list-test-failure.json", nil, nil, failingTestsOpts("gotestsum-failures.log"))
	jobs, _ := jsonJobs(t, app, cli.ExitFailed, "--go-tests")
	tests := jsonTests(t, jobs[failedJob])
	if len(tests) != 1 {
		t.Fatalf("tests = %+v, want 1", tests)
	}
	got := tests[0]
	if got.Package != "internal/vm" || got.Name != "TestAgentImportFailsWhole" || len(got.Lines) != 2 ||
		!strings.Contains(got.Lines[0], "agentimport_test.go:957: changed:") || !strings.Contains(got.Lines[1], "agentimport_test.go:963:") || got.RunningTest != "" {
		t.Errorf("tests[0] = %+v", got)
	}
	if _, ok := jobs[failedJob]["running_test"]; ok {
		t.Error("job has running_test, want it only inside a test")
	}
	if len(jobs) != 5 {
		t.Fatalf("got %d jobs, want 5", len(jobs))
	}
	for id, j := range jobs {
		if _, ok := j["tests"]; ok && id != failedJob {
			t.Errorf("job %s has a tests key, want none for a job that didn't fail", id)
		}
		if _, ok := j["log_error"]; ok {
			t.Errorf("job %s has log_error: %s", id, j["log_error"])
		}
	}
}

func TestRunFetchesLogsOnlyForFailedJobs(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		opts    fakeOpts
		wantIDs []string
	}{
		{"one failed job", "run-list-failure.json", fakeOpts{}, []string{"136264712505"}},
		{"cancelled job is not fetched", "run-list-test-failure.json", failingTestsOpts("gotestsum-failures.log"), []string{failedJob}},
		{"two failed jobs", "run-list-test-failure.json", fakeOpts{viewFixture: map[string]string{failedRun: "run-view-44707997310-two-failures.json"}}, []string{otherJob, failedJob}},
		{"all passed", "run-list-success.json", fakeOpts{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, calls := fakeWith(t, tt.fixture, nil, nil, tt.opts)
			var stdout, stderr bytes.Buffer
			app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr)
			if calls.logs != len(tt.wantIDs) {
				t.Errorf("gh run view --log-failed calls = %d (jobs %v), want %d", calls.logs, calls.logIDs, len(tt.wantIDs))
			}
			got := slices.Clone(calls.logIDs)
			slices.Sort(got)
			want := slices.Clone(tt.wantIDs)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("logs fetched for jobs %v, want %v", got, want)
			}
		})
	}
}

func TestOnceSkipsLogsOfRunsInProgress(t *testing.T) {
	app, calls := fakeWith(t, "run-list-in-progress.json", nil, nil, fakeOpts{
		viewFixture: map[string]string{"45017063720": "run-view-45017063720-in-progress-failed.json"},
		logFixture:  map[string]string{"137191532679": "gotestsum-failures.log"},
	})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--once", "HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
		t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitUnchecked, stderr.String())
	}
	if calls.logs != 0 {
		t.Errorf("gh run view --log-failed calls = %d (jobs %v), want 0 for a run in progress", calls.logs, calls.logIDs)
	}
	if strings.Contains(stdout.String(), "FAIL internal/vm") || strings.Contains(stdout.String(), "failing tests") {
		t.Errorf("stdout = %q, want no failing-test block", stdout.String())
	}
}

// TestRunNoFailedStepInTheLog covers AC-7: the job's failed step has no line in
// its window, either because the log is empty or because its lines are from
// another time.
func TestRunNoFailedStepInTheLog(t *testing.T) {
	tests := []struct {
		name string
		opts fakeOpts
	}{
		{"AC-7 the lines are outside the step's window", failingTestsOpts("gotestsum-pass.log")},
		{"AC-7 an empty log", fakeOpts{viewFixture: map[string]string{failedRun: "run-view-44707997310-test-failure.json"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, _ := fakeWith(t, "run-list-test-failure.json", nil, nil, tt.opts)
			var stdout, stderr bytes.Buffer
			if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
			}
			if !strings.Contains(stdout.String(), failedJobName+" (CI, run "+failedRun+"):\n  (no failed step in the log)") {
				t.Errorf("stdout = %q, want the job's block to say (no failed step in the log)", stdout.String())
			}
			app, _ = fakeWith(t, "run-list-test-failure.json", nil, nil, tt.opts)
			jobs, _ := jsonJobs(t, app, cli.ExitFailed)
			if raw, ok := jobs[failedJob]["steps"]; !ok || string(raw) != "[]" {
				t.Errorf("steps = %s (present %v), want []", raw, ok)
			}
			if _, ok := jobs[failedJob]["tests"]; ok {
				t.Errorf("job has tests %s, want none without --go-tests", jobs[failedJob]["tests"])
			}
		})
	}
}

// step is one entry of a job's "steps" in --json.
type step struct {
	Name    string   `json:"name"`
	Lines   []string `json:"lines"`
	Omitted int      `json:"omitted"`
}

func jsonSteps(t *testing.T, job map[string]json.RawMessage) []step {
	t.Helper()
	raw, ok := job["steps"]
	if !ok {
		t.Fatalf("job %s has no steps key", job["id"])
	}
	var steps []step
	if err := json.Unmarshal(raw, &steps); err != nil {
		t.Fatal(err)
	}
	return steps
}

const (
	devvmRun  = "44705166834"
	devvmJob  = "136182840710"
	devvmList = "run-list-devvm-failure.json"
)

// devvmOpts is the fake gh for the recorded CI run of devvmhq/devvm whose
// ubuntu job's "Run mise run test" step failed, printing log.
func devvmOpts(log string) fakeOpts {
	return fakeOpts{logFixture: map[string]string{devvmJob: log}}
}

// TestRunShowsFailedStepTail covers AC-1 and AC-3.
func TestRunShowsFailedStepTail(t *testing.T) {
	app, calls := fakeWith(t, devvmList, nil, nil, devvmOpts("devvm-44705166834.log"))
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	if want := []string{devvmJob}; !reflect.DeepEqual(calls.logIDs, want) {
		t.Errorf("logs fetched for jobs %v, want exactly %v", calls.logIDs, want)
	}
	out := stdout.String()
	_, block, ok := strings.Cut(out, "\n\n")
	if !ok {
		t.Fatalf("stdout = %q, want the table, a blank line and a block", out)
	}
	if want := failedJobName + " (CI, run " + devvmRun + "):\n  Run mise run test:\n"; !strings.HasPrefix(block, want) {
		t.Errorf("block = %q, want it to start with %q", block, want)
	}
	if want := "    ##[error]Process completed with exit code 1.\n"; !strings.HasSuffix(block, want) {
		t.Errorf("block = %q, want it to end with %q", block, want)
	}
	for _, s := range []string{
		"\n    === FAIL: internal/vm TestAgentImportFailsWhole (0.01s)\n",
		"\n        agentimport_test.go:957: changed:",
		"\n        agentimport_test.go:963: changed true: the store has",
		"\n    DONE 4183 tests, 55 skipped, 1 failure in 263.817s\n",
	} {
		if !strings.Contains(block, s) {
			t.Errorf("block = %q, want it to contain %q", block, s)
		}
	}
	// Nothing from the steps that passed or ran after, and nothing of gh's
	// prefix, the timestamps or the colour codes.
	for _, s := range []string{
		"Post job cleanup", "Cleaning up orphan processes", "Runner Image", "actions/checkout", "mise run lint",
		"UNKNOWN STEP", "2026-11-20T", "^[[", "\x1b[", "\r", "  FAIL ",
	} {
		if strings.Contains(block, s) {
			t.Errorf("block = %q, want it to leave out %q", block, s)
		}
	}
	if strings.Count(out, "\n\n") != 1 {
		t.Errorf("stdout = %q, want one block", out)
	}
}

// TestRunStepOmittedLines covers AC-2: the table and --json show the same last
// 40 lines and the count of the lines before them.
func TestRunStepOmittedLines(t *testing.T) {
	opts := devvmOpts("devvm-44705166834.log")
	app, _ := fakeWith(t, devvmList, nil, nil, opts)
	jobs, _ := jsonJobs(t, app, cli.ExitFailed)
	steps := jsonSteps(t, jobs[devvmJob])
	if len(steps) != 1 || steps[0].Name != "Run mise run test" || len(steps[0].Lines) != 40 || steps[0].Omitted <= 0 {
		t.Fatalf("steps = %+v, want one step, Run mise run test, with 40 lines and some omitted", steps)
	}
	lines := steps[0].Lines
	if last := lines[len(lines)-1]; last != "##[error]Process completed with exit code 1." {
		t.Errorf("last line = %q, want the ##[error] line", last)
	}
	if _, ok := jobs[devvmJob]["tests"]; ok {
		t.Errorf("job has tests %s, want none without --go-tests", jobs[devvmJob]["tests"])
	}

	app, _ = fakeWith(t, devvmList, nil, nil, opts)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	want := fmt.Sprintf("  Run mise run test:\n    … %d earlier lines\n", steps[0].Omitted)
	for _, l := range lines {
		want += "    " + l + "\n"
	}
	if !strings.HasSuffix(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to end with %q", stdout.String(), want)
	}
}

// TestRunStepWithFewLinesOmitsNone covers AC-2: a step of 40 lines or fewer has
// no earlier-lines note and omitted is 0.
func TestRunStepWithFewLinesOmitsNone(t *testing.T) {
	opts := devvmOpts("devvm-short-step.log")
	app, _ := fakeWith(t, devvmList, nil, nil, opts)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	want := "\n" + failedJobName + " (CI, run " + devvmRun + "):\n  Run mise run test:\n" +
		"    ##[group]Run mise run test\n" +
		"    === FAIL: internal/vm TestAgentImportFailsWhole (0.01s)\n" +
		"        agentimport_test.go:957: changed:\n" +
		"    ##[error]Process completed with exit code 1.\n"
	if !strings.HasSuffix(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to end with %q", stdout.String(), want)
	}
	if strings.Contains(stdout.String(), "earlier line") {
		t.Errorf("stdout = %q, want no earlier-lines note", stdout.String())
	}
	app, _ = fakeWith(t, devvmList, nil, nil, opts)
	jobs, _ := jsonJobs(t, app, cli.ExitFailed)
	steps := jsonSteps(t, jobs[devvmJob])
	if len(steps) != 1 || len(steps[0].Lines) != 4 || !strings.Contains(string(jobs[devvmJob]["steps"]), `"omitted":0`) {
		t.Errorf("steps = %s, want one step of 4 lines with omitted 0", jobs[devvmJob]["steps"])
	}
}

// TestRunFailedJobWithoutFailedStepShowsJobLog covers AC-8: a failed job whose
// jobs JSON has no failed step shows the end of the whole log.
func TestRunFailedJobWithoutFailedStepShowsJobLog(t *testing.T) {
	opts := devvmOpts("devvm-44705166834.log")
	opts.viewFixture = map[string]string{devvmRun: "run-view-44705166834-no-failed-step.json"}
	app, _ := fakeWith(t, devvmList, nil, nil, opts)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	_, block, _ := strings.Cut(stdout.String(), "\n\n")
	if want := failedJobName + " (CI, run " + devvmRun + "):\n  (job log):\n    … 677 earlier lines\n"; !strings.HasPrefix(block, want) {
		t.Errorf("block = %q, want it to start with %q", block, want)
	}
	if want := "    Cleaning up orphan processes\n"; !strings.HasSuffix(block, want) {
		t.Errorf("block = %q, want it to end with %q", block, want)
	}
	if strings.Contains(block, "no failed step") || strings.Contains(block, "UNKNOWN STEP") {
		t.Errorf("block = %q, want the log's lines only", block)
	}
	app, _ = fakeWith(t, devvmList, nil, nil, opts)
	jobs, _ := jsonJobs(t, app, cli.ExitFailed)
	steps := jsonSteps(t, jobs[devvmJob])
	if len(steps) != 1 || steps[0].Name != "(job log)" || len(steps[0].Lines) != 40 || steps[0].Omitted != 677 {
		t.Errorf("steps = %s, want one step, (job log), with 40 lines and 677 omitted", jobs[devvmJob]["steps"])
	}
}

// TestRunNonGoStep covers AC-4: the failed step of an npm project's job, from
// a log recorded from expressjs/express.
func TestRunNonGoStep(t *testing.T) {
	const run, job, name = "44581271038", "135762241698", "Node.js 17 - windows-latest"
	opts := fakeOpts{logFixture: map[string]string{job: "npm-test-failure.log"}}
	app, _ := fakeWith(t, "run-list-npm-failure.json", nil, nil, opts)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	out := stdout.String()
	_, rest, _ := strings.Cut(out, "\n"+name+" (legacy, run "+run+"):\n")
	if !strings.HasPrefix(rest, "  Run tests:\n") {
		t.Fatalf("stdout = %q, want the block of job %q to start with its step, Run tests", out, name)
	}
	block, _, _ := strings.Cut(rest, "\n\n")
	if want := "    ##[error]Process completed with exit code 9.\n"; !strings.HasSuffix(block+"\n", want) {
		t.Errorf("block = %q, want it to end with %q", block, want)
	}
	for _, s := range []string{"TypeError: Cannot read properties of undefined (reading 'router')", "test\\app.listRoutes.js:271:43"} {
		if !strings.Contains(block, s) {
			t.Errorf("block = %q, want it to contain %q", block, s)
		}
	}
	for _, s := range []string{"UNKNOWN STEP", "2026-11-19T", "^[["} {
		if strings.Contains(block, s) {
			t.Errorf("block = %q, want it to leave out %q", block, s)
		}
	}
	if strings.Contains(out, "  FAIL ") {
		t.Errorf("stdout = %q, want no FAIL block", out)
	}

	app, _ = fakeWith(t, "run-list-npm-failure.json", nil, nil, opts)
	jobs, _ := jsonJobs(t, app, cli.ExitFailed)
	steps := jsonSteps(t, jobs[job])
	if len(steps) != 1 || steps[0].Name != "Run tests" || len(steps[0].Lines) == 0 || len(steps[0].Lines) > 40 {
		t.Errorf("steps = %+v, want the one step Run tests with up to 40 lines", steps)
	}
	if _, ok := jobs[job]["tests"]; ok {
		t.Errorf("job has tests %s, want no tests key", jobs[job]["tests"])
	}
}

// TestRunGoTestsFlag covers AC-5.
func TestRunGoTestsFlag(t *testing.T) {
	opts := failingTestsOpts("gotestsum-failures.log")
	t.Run("AC-5 with --go-tests the failing tests are added", func(t *testing.T) {
		app, _ := fakeWith(t, "run-list-test-failure.json", nil, nil, opts)
		jobs, _ := jsonJobs(t, app, cli.ExitFailed, "--go-tests")
		tests := jsonTests(t, jobs[failedJob])
		if len(tests) != 1 || tests[0].Name != "TestAgentImportFailsWhole" {
			t.Errorf("tests = %+v, want TestAgentImportFailsWhole", tests)
		}
		if _, ok := jobs[failedJob]["steps"]; !ok {
			t.Error("job has no steps key, want steps and tests both")
		}
		app, _ = fakeWith(t, "run-list-test-failure.json", nil, nil, opts)
		var stdout, stderr bytes.Buffer
		if code := app.Run(t.Context(), []string{"--go-tests", "HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
			t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
		}
		if !strings.Contains(stdout.String(), "  FAIL internal/vm TestAgentImportFailsWhole\n") {
			t.Errorf("stdout = %q, want the FAIL header", stdout.String())
		}
	})
	t.Run("AC-5 without it there are no tests and no FAIL block", func(t *testing.T) {
		app, _ := fakeWith(t, "run-list-test-failure.json", nil, nil, opts)
		jobs, _ := jsonJobs(t, app, cli.ExitFailed)
		if raw, ok := jobs[failedJob]["tests"]; ok {
			t.Errorf("job has tests %s, want no tests key without --go-tests", raw)
		}
		app, _ = fakeWith(t, "run-list-test-failure.json", nil, nil, opts)
		var stdout, stderr bytes.Buffer
		if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
			t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
		}
		if strings.Contains(stdout.String(), "  FAIL ") || strings.Contains(stdout.String(), "TestAgentImportFailsWhole") {
			t.Errorf("stdout = %q, want no FAIL block", stdout.String())
		}
	})
	t.Run("AC-5 the tests follow the steps", func(t *testing.T) {
		app, _ := fakeWith(t, devvmList, nil, nil, devvmOpts("devvm-44705166834.log"))
		var stdout, stderr bytes.Buffer
		if code := app.Run(t.Context(), []string{"--go-tests", "HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
			t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
		}
		out := stdout.String()
		stepAt := strings.Index(out, "  Run mise run test:\n")
		testAt := strings.Index(out, "  FAIL internal/vm TestAgentImportFailsWhole\n")
		if stepAt < 0 || testAt < stepAt {
			t.Errorf("stdout = %q, want the step at %d before the FAIL block at %d", out, stepAt, testAt)
		}
	})
	t.Run("AC-5 the usage names the flag", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		cli.App{}.Run(t.Context(), nil, &stdout, &stderr)
		if !strings.Contains(stderr.String(), "--go-tests") {
			t.Errorf("stderr = %q, want the usage to name --go-tests", stderr.String())
		}
	})
}

func TestRunCrashNamesRunningTest(t *testing.T) {
	opts := failingTestsOpts("gotest-verbose-crash.log")
	app, _ := fakeWith(t, "run-list-test-failure.json", nil, nil, opts)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--go-tests", "HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	for _, s := range []string{"(package)", "crashed while running TestSlow", "panic: boom"} {
		if !strings.Contains(stdout.String(), s) {
			t.Errorf("stdout = %q, want it to contain %q", stdout.String(), s)
		}
	}
	app, _ = fakeWith(t, "run-list-test-failure.json", nil, nil, opts)
	jobs, _ := jsonJobs(t, app, cli.ExitFailed, "--go-tests")
	tests := jsonTests(t, jobs[failedJob])
	if len(tests) != 1 || tests[0].Name != "(package)" || tests[0].RunningTest != "TestSlow" {
		t.Errorf("tests = %+v, want the (package) crash with running_test TestSlow", tests)
	}
}

func TestRunTableCapsLinesOfEachTest(t *testing.T) {
	opts := failingTestsOpts("gotest-long-panic.log")
	app, _ := fakeWith(t, "run-list-test-failure.json", nil, nil, opts)
	jobs, _ := jsonJobs(t, app, cli.ExitFailed, "--go-tests")
	tests := jsonTests(t, jobs[failedJob])
	if len(tests) != 1 || len(tests[0].Lines) <= testLines+1 {
		t.Fatalf("tests = %+v, want one test with more than %d lines, all of them in --json", tests, testLines+1)
	}
	lines := tests[0].Lines

	app, _ = fakeWith(t, "run-list-test-failure.json", nil, nil, opts)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--go-tests", "HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	out := stdout.String()
	more := fmt.Sprintf("… %d more lines (--json has them all)", len(lines)-testLines)
	if !strings.Contains(out, more) {
		t.Errorf("stdout = %q, want it to contain %q", out, more)
	}
	if !strings.Contains(out, strings.TrimSpace(lines[testLines-1])) {
		t.Errorf("stdout = %q, want the %dth line %q", out, testLines, lines[testLines-1])
	}
	if last := strings.TrimSpace(lines[len(lines)-1]); strings.Contains(out, last) {
		t.Errorf("stdout = %q, want it to leave out the last line %q", out, last)
	}
}

func TestRunTableShortTestHasNoMoreLinesNote(t *testing.T) {
	app, _ := fakeWith(t, "run-list-test-failure.json", nil, nil, failingTestsOpts("gotestsum-failures.log"))
	var stdout, stderr bytes.Buffer
	app.Run(t.Context(), []string{"--go-tests", "HEAD"}, &stdout, &stderr)
	if !strings.Contains(stdout.String(), "agentimport_test.go:963") || strings.Contains(stdout.String(), "more lines") {
		t.Errorf("stdout = %q, want both lines and no note", stdout.String())
	}
}

func TestNewMaxTestLines(t *testing.T) {
	if got := cli.New().MaxTestLines; got != 20 {
		t.Errorf("MaxTestLines = %d, want 20", got)
	}
}

// TestRunFailedLogErrorKeepsExitCode covers AC-6.
func TestRunFailedLogErrorKeepsExitCode(t *testing.T) {
	opts := failingTestsOpts("gotestsum-failures.log")
	opts.logErr = map[string]error{failedJob: stderrErr(t, "stderr-404.txt")}
	app, calls := fakeWith(t, "run-list-test-failure.json", nil, nil, opts)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	if !strings.Contains(stdout.String(), "  (log unavailable)") {
		t.Errorf("stdout = %q, want (log unavailable)", stdout.String())
	}
	if !strings.Contains(stderr.String(), "ciwatch: can't fetch the log of job "+failedJob+" ("+failedJobName+") of run "+failedRun) ||
		!strings.Contains(stderr.String(), "gh run view "+failedRun+" --job "+failedJob+":") {
		t.Errorf("stderr = %q, want a warning that it can't fetch the log, with the error", stderr.String())
	}
	if calls.logs != 1 {
		t.Errorf("gh run view --log-failed calls = %d, want 1 (a non-network error is not retried)", calls.logs)
	}
	app, _ = fakeWith(t, "run-list-test-failure.json", nil, nil, opts)
	jobs, _ := jsonJobs(t, app, cli.ExitFailed)
	job := jobs[failedJob]
	for _, key := range []string{"tests", "steps", "tests_error"} {
		if _, ok := job[key]; ok {
			t.Errorf("job has %s %s, want none when the fetch failed", key, job[key])
		}
	}
	var msg string
	if err := json.Unmarshal(job["log_error"], &msg); err != nil || !strings.Contains(msg, "gh run view "+failedRun+" --job "+failedJob) {
		t.Errorf("log_error = %s (%v), want the error", job["log_error"], err)
	}
}

func TestRunFailedLogRetriesNetworkError(t *testing.T) {
	opts := failingTestsOpts("gotestsum-failures.log")
	opts.logErrCalls = map[string]int{failedJob: 1}
	app, calls := fakeWith(t, "run-list-test-failure.json", nil, nil, opts)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--go-tests", "HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	if strings.Contains(stdout.String(), "unavailable") || !strings.Contains(stdout.String(), "TestAgentImportFailsWhole") {
		t.Errorf("stdout = %q, want the failing test, not (log unavailable)", stdout.String())
	}
	n := len(calls.clock.waits)
	if calls.logs != 2 || n == 0 || calls.clock.waits[n-1] != app.Backoff ||
		!strings.Contains(stderr.String(), "gh run view "+failedRun+" --job "+failedJob+": network error, retrying in 1s") {
		t.Errorf("log calls = %d, waits = %v, stderr = %q, want 2, a last wait of the backoff and the retry line", calls.logs, calls.clock.waits, stderr.String())
	}
}

func TestRunFailedLogRetriesRunOutKeepExitCode(t *testing.T) {
	opts := failingTestsOpts("gotestsum-failures.log")
	opts.logErrCalls = map[string]int{failedJob: 1000}
	app, calls := fakeWith(t, "run-list-test-failure.json", nil, nil, opts)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	if !strings.Contains(stdout.String(), "(log unavailable)") || !strings.Contains(stderr.String(), "gave up after 8 retries: gh run view "+failedRun+" --job "+failedJob+": ") {
		t.Errorf("stdout = %q, stderr = %q, want (log unavailable) and the gave-up warning", stdout.String(), stderr.String())
	}
	if want := app.Retries + 1; calls.logs != want {
		t.Errorf("log calls = %d, want %d", calls.logs, want)
	}
}

func TestRunFailedLogGivesUpOnceForAllJobs(t *testing.T) {
	opts := fakeOpts{
		viewFixture: map[string]string{failedRun: "run-view-44707997310-two-failures.json"},
		logErrCalls: map[string]int{failedJob: 1000, otherJob: 1000},
	}
	app, calls := fakeWith(t, "run-list-test-failure.json", nil, nil, opts)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	// The first job uses up the retries; the network is known to be down, so
	// the second gets one try and no waits.
	if want := app.Retries + 1 + 1; calls.logs != want {
		t.Errorf("log calls = %d, want %d", calls.logs, want)
	}
	if n := strings.Count(stdout.String(), "(log unavailable)"); n != 2 {
		t.Errorf("stdout = %q, want (log unavailable) twice", stdout.String())
	}
}

func TestRunCancelledWhileFetchingLogs(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	opts := failingTestsOpts("gotestsum-failures.log")
	opts.onLog = func(string) { cancel() }
	app, _ := fakeWith(t, "run-list-test-failure.json", nil, nil, opts)
	var stdout, stderr bytes.Buffer
	if code := app.Run(ctx, []string{"HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
		t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitUnchecked, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
	if !strings.Contains(stderr.String(), "context canceled") {
		t.Errorf("stderr = %q, want the cancellation", stderr.String())
	}
}

func TestRunTimedOutJobGetsFailingTests(t *testing.T) {
	opts := fakeOpts{
		listFixtures: []string{"run-list-timed-out.json"},
		viewFixture:  map[string]string{failedRun: "run-view-44707997310-timed-out.json"},
		logFixture:   map[string]string{failedJob: "gotestsum-failures.log"},
	}
	app, calls := fakeWith(t, "", nil, nil, opts)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--go-tests", "HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	// Only the timed-out job's log is fetched, not the skipped job's.
	if want := []string{failedJob}; !reflect.DeepEqual(calls.logIDs, want) {
		t.Errorf("logs fetched for jobs %v, want exactly %v", calls.logIDs, want)
	}
	if !strings.Contains(stdout.String(), "FAIL internal/vm TestAgentImportFailsWhole") {
		t.Errorf("stdout = %q, want the timed-out job's failing test", stdout.String())
	}
}
