package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devvmhq/ciwatch/internal/cli"
	"github.com/devvmhq/ciwatch/internal/gh"
)

const sha = "3bb2dccd75a2754b8d45e522b3c26c22bc66a21a"

var ghArgs = []string{"run", "list", "--commit", sha, "--limit", "100", "--json", "databaseId,workflowName,status,conclusion,url,createdAt"}

// counts is how many times the fake ran gh run list and gh run view, and for
// which runs.
type counts struct {
	list    int
	view    int
	viewIDs []string
	events  []string // "list" and "view", in the order gh ran them
	clock   *fakeClock
}

// testInterval and testGrace are what the fake helpers set on the App.
const (
	testInterval = 10 * time.Second
	testGrace    = 60 * time.Second
)

// fixtureNow is after the createdAt of every recorded run, so the grace period
// of a recorded commit is long over.
var fixtureNow = time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)

// fakeClock is the clock the tests control. After never waits: it records d,
// moves the time on by d, runs the hook, and returns a channel that is ready.
type fakeClock struct {
	now   time.Time
	waits []time.Duration
	hook  func()
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.waits = append(c.waits, d)
	c.now = c.now.Add(d)
	if c.hook != nil {
		c.hook()
	}
	ch := make(chan time.Time, 1)
	ch <- c.now
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
	return app, c
}

// fakeOpts changes what the fake gh prints for gh run view <id> --json jobs.
type fakeOpts struct {
	viewFixture map[string]string // run id to a fixture other than run-view-<id>.json
	viewErr     map[string]error  // run id to the error gh run view fails with
	onView      func(id string)   // runs before gh run view answers

	listFixtures []string      // gh run list prints these fixtures, one per call, the last repeated
	listRaw      []string      // like listFixtures, but the output itself
	listErr      map[int]error // call number (from 1) to the error gh run list fails with
	now          time.Time     // the fake clock's time, fixtureNow if zero
	onWait       func()        // runs when the fake clock is waited on
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "gh", name)) //nolint:gosec // reads a fixture from testdata/ by a name the test supplies
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
		case name == "git":
			if gitErr != nil {
				return nil, gitErr
			}
			return []byte(sha + "\n"), nil
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
	app, c.clock = withClock(app, now)
	c.clock.hook = opts.onWait
	return app, c
}

// inline returns an App whose gh run list prints list and whose gh run view
// prints jobs for every run.
func inline(list, jobs string) cli.App {
	app, _ := withClock(cli.App{GH: gh.Runner{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" {
			return []byte(sha + "\n"), nil
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
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
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
	rows := strings.Count(stdout.String(), "\n") - 1 // minus the header
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
	app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr)
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
	if code := app.Run(t.Context(), []string{"--", "HEAD"}, &stdout, &stderr); code != cli.ExitOK || calls.list != 1 {
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
			if warned := stderr.Len() > 0; warned != tt.wantWarned {
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
	app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr)
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
		viewErr: map[string]error{"44729305066": errors.New("HTTP 502")},
	})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Errorf("exit code = %d, want %d", code, cli.ExitFailed)
	}
	if want := "ciwatch: can't list jobs for run 44729305066 (Fuzz): "; !strings.Contains(stderr.String(), want) || !strings.Contains(stderr.String(), "HTTP 502") {
		t.Errorf("stderr = %q, want it to contain %q and HTTP 502", stderr.String(), want)
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
		viewErr: map[string]error{"44729305066": errors.New("HTTP 502")},
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
	if err := json.Unmarshal(got.Runs[0]["jobs_error"], &msg); err != nil || !strings.Contains(msg, "HTTP 502") {
		t.Errorf("failed run's jobs_error = %s, want a string containing HTTP 502", got.Runs[0]["jobs_error"])
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
				viewErr: map[string]error{"44729305066": errors.New("HTTP 502"), "44709305564": errors.New("HTTP 502")},
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
	if calls.list != 3 {
		t.Errorf("gh run list calls = %d, want 3", calls.list)
	}
	if want := []time.Duration{testInterval, testInterval}; !reflect.DeepEqual(calls.clock.waits, want) {
		t.Errorf("waits = %v, want %v", calls.clock.waits, want)
	}
	if want := []string{"list", "list", "list", "view", "view", "view"}; !reflect.DeepEqual(calls.events, want) {
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

func TestWatchOldCommitDoesNotWait(t *testing.T) {
	app, calls := fake(t, "run-list-success.json", nil, nil)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitOK, stderr.String())
	}
	if calls.list != 1 || len(calls.clock.waits) != 0 {
		t.Errorf("gh run list calls = %d and waits = %v, want 1 and none", calls.list, calls.clock.waits)
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
	app, calls := fakeWith(t, "run-list-in-progress.json", nil, nil, fakeOpts{onWait: cancel})
	var stdout, stderr bytes.Buffer
	if code := app.Run(ctx, []string{"HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
		t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitUnchecked, stderr.String())
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
		listErr: map[int]error{2: errors.New("HTTP 502")},
	})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUnchecked)
	}
	if !strings.Contains(stderr.String(), "HTTP 502") {
		t.Errorf("stderr = %q, want HTTP 502", stderr.String())
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
