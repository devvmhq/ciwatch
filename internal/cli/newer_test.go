package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/devvmhq/ciwatch/internal/cli"
	"github.com/devvmhq/ciwatch/internal/gh"
)

// Commits in the recorded history of devvmhq/devvm's main branch. 8da71a0's CI
// run was cancelled shortly before 90694a4 was pushed.
const (
	shaCancelled = "8da71a01bb18eedaa5b282a4da705350f2dd3f90"
	shaNewer     = "90694a414f84ff5d765cdb6c8741e8d3888a2cbb"
	shaOlder     = "5fb0732580cb3df23a563ee25775c45671d4ec7c"
	shaByHand    = "a1c09016a1866e55ef9e2b5ae4c123472ab9b69c"
)

const branchFields = "databaseId,workflowDatabaseId,workflowName,status,conclusion,url,createdAt,updatedAt,headBranch,headSha,event,attempt"

// site is what the fake GitHub holds: the runs of the commit, and what gh run
// list --branch prints for each workflow ID.
type site struct {
	head    string            // what git rev-parse prints
	commit  string            // the runs of head: a fixture, or raw output if it starts with "["
	created string            // the cancelled run's createdAt, which --created must carry; shaCancelled's run's if empty
	since   map[string]string // workflow ID to a fixture, or raw output if it starts with "["
	sinceE  error             // if set, gh run list --branch fails with it
}

type siteCalls struct {
	since []string // the --workflow of each gh run list --branch
}

func (s site) app(t *testing.T) (cli.App, *siteCalls) {
	t.Helper()
	c := new(siteCalls)
	load := func(v string) []byte {
		if strings.HasPrefix(v, "[") {
			return []byte(v)
		}
		return readFixture(t, v)
	}
	app := cli.App{GH: gh.Runner{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "git":
			return []byte(s.head + "\n"), nil
		case name == "gh" && len(args) >= 3 && args[1] == "list" && args[2] == "--commit":
			return load(s.commit), nil
		case name == "gh" && len(args) >= 3 && args[1] == "list" && args[2] == "--branch":
			if len(args) < 6 {
				t.Errorf("gh args = %q, want --workflow and an ID after the branch", args)
				return nil, errors.New("bad args")
			}
			c.since = append(c.since, args[5])
			created := s.created
			if created == "" {
				created = "2026-11-13T03:47:33Z"
			}
			want := []string{"run", "list", "--branch", "main", "--workflow", args[5], "--created", ">=" + created, "--limit", "100", "--json", branchFields}
			if !reflect.DeepEqual(args, want) {
				t.Errorf("gh args = %q, want %q", args, want)
			}
			if s.sinceE != nil {
				return nil, s.sinceE
			}
			f, ok := s.since[args[5]]
			if !ok {
				t.Errorf("gh run list --branch for unexpected workflow %s", args[5])
				return nil, errors.New("unexpected workflow")
			}
			return load(f), nil
		case name == "gh" && len(args) == 5 && args[1] == "view":
			return []byte(noJobs), nil
		case name == "gh" && len(args) == 4 && args[0] == "api": // a failed run's artifacts
			return []byte(`{"total_count":0,"artifacts":[]}`), nil
		}
		t.Errorf("unexpected command %s %q", name, args)
		return nil, errors.New("unexpected command")
	}}}
	app, _ = withClock(app, fixtureNow)
	return app, c
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

const (
	ciID       = "409041453"
	newerHint  = "ciwatch: run 43361281082 (CI) was cancelled; to watch the newer run: ciwatch " + shaNewer
	newerNote  = "(newer run 43361860563 for 90694a4)"
	supersedCI = "run-list-superseded.json"
)

func supersededSite() site {
	return site{
		head:   shaCancelled,
		commit: supersedCI,
		since:  map[string]string{ciID: sinceFixture},
	}
}

const sinceFixture = "run-list-since-43361281082.json"

func TestCancelledLooksUpRunsSinceOnce(t *testing.T) { // AC-1
	app, calls := supersededSite().app(t) // the fake checks the exact arguments
	var stdout, stderr bytes.Buffer
	app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr)
	if want := []string{ciID}; !reflect.DeepEqual(calls.since, want) {
		t.Errorf("gh run list --branch --workflow = %q, want %q: one lookup, for the cancelled CI run only", calls.since, want)
	}
}

func TestCancelledNamesFirstRunAfter(t *testing.T) { // AC-1, AC-2
	// The recorded lookup, then the same runs oldest first: gh's order
	// doesn't matter.
	var runs []map[string]any
	if err := json.Unmarshal(readFixture(t, sinceFixture), &runs); err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(runs)-1; i < j; i, j = i+1, j-1 {
		runs[i], runs[j] = runs[j], runs[i]
	}
	oldestFirst, err := json.Marshal(runs)
	if err != nil {
		t.Fatal(err)
	}
	for name, since := range map[string]string{"as recorded": sinceFixture, "oldest first": string(oldestFirst)} {
		t.Run(name, func(t *testing.T) {
			s := supersededSite()
			s.since = map[string]string{ciID: since}
			app, _ := s.app(t)
			var stdout, stderr bytes.Buffer
			if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
			}
			checkTable(t, stdout.String(), []row{
				runRow("Docs", "completed", "success"),
				{fields: append([]string{"CI", "completed", "cancelled"}, strings.Fields(newerNote)...)},
			})
			if got := lastLine(stderr.String()); got != newerHint {
				t.Errorf("last stderr line = %q, want %q", got, newerHint)
			}
		})
	}
}

func TestCancelledNamesLowestIDOnATie(t *testing.T) { // AC-1
	const (
		shaLow  = "1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		shaHigh = "2222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	run := func(id int, sha string) string {
		return fmt.Sprintf(`{"conclusion":"success","createdAt":"2026-11-13T03:50:00Z","updatedAt":"2026-11-13T04:06:05Z","databaseId":%d,"event":"push","headBranch":"main","headSha":"%s","status":"completed","url":"u","workflowDatabaseId":409041453,"workflowName":"CI"}`, id, sha)
	}
	low, high := run(100, shaLow), run(200, shaHigh)
	for name, since := range map[string]string{
		"lowest ID first":  "[" + low + "," + high + "]",
		"highest ID first": "[" + high + "," + low + "]",
	} {
		t.Run(name, func(t *testing.T) {
			s := supersededSite()
			s.since = map[string]string{ciID: since}
			app, _ := s.app(t)
			var stdout, stderr bytes.Buffer
			if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
			}
			checkTable(t, stdout.String(), []row{
				runRow("Docs", "completed", "success"),
				{fields: []string{"CI", "completed", "cancelled", "(newer", "run", "100", "for", "1111111)"}},
			})
			const hint = "ciwatch: run 43361281082 (CI) was cancelled; to watch the newer run: ciwatch " + shaLow
			if got := lastLine(stderr.String()); got != hint {
				t.Errorf("last stderr line = %q, want %q", got, hint)
			}
		})
	}
}

func TestRunsSinceFullPage(t *testing.T) { // AC-4
	// 100 runs of another commit, newest first, as gh would cut the list off.
	var runs []string
	for i := range 100 {
		runs = append(runs, fmt.Sprintf(`{"conclusion":"success","createdAt":"2026-11-14T%02d:%02d:00Z","updatedAt":"2026-11-14T23:59:00Z","databaseId":%d,"event":"push","headBranch":"main","headSha":"%s","status":"completed","url":"u","workflowDatabaseId":409041453,"workflowName":"CI"}`, 23-i/60, 59-i%60, 50000000000+int64(100-i), shaNewer))
	}
	s := supersededSite()
	s.since = map[string]string{ciID: "[" + strings.Join(runs, ",") + "]"}
	app, _ := s.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	const warning = "ciwatch: can't name the run after run 43361281082 (CI): 100 or more runs since it"
	if !strings.Contains(stderr.String(), warning+"\n") {
		t.Errorf("stderr = %q, want the line %q", stderr.String(), warning)
	}
	if strings.Contains(stdout.String(), "newer run") || strings.Contains(stderr.String(), "to watch the newer run") {
		t.Errorf("stdout = %q, stderr = %q, want the run plainly cancelled", stdout.String(), stderr.String())
	}
}

func TestCancelledNewerRunJSON(t *testing.T) { // AC-2
	app, _ := supersededSite().app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--json", "HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	var out struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout = %q: %v", stdout.String(), err)
	}
	if len(out.Runs) != 2 {
		t.Fatalf("runs = %v, want 2", out.Runs)
	}
	want := map[string]any{"id": float64(43361860563), "sha": shaNewer}
	for _, r := range out.Runs {
		got, has := r["newer_run"]
		if r["workflow"] == "CI" {
			if !reflect.DeepEqual(got, want) {
				t.Errorf("CI newer_run = %v, want %v", got, want)
			}
		} else if has {
			t.Errorf("%v run has newer_run = %v, want the field left out", r["workflow"], got)
		}
		for _, gone := range []string{"superseded_by", "followed_from"} {
			if _, has := r[gone]; has {
				t.Errorf("%v run has %s, want it removed", r["workflow"], gone)
			}
		}
	}
	if strings.Contains(stdout.String(), "followed_from") || strings.Contains(stdout.String(), "superseded_by") {
		t.Errorf("stdout = %q, want no followed_from or superseded_by", stdout.String())
	}
}

func TestCancelledWithoutNewerRun(t *testing.T) { // AC-3, AC-4
	const olderRun = `[{"conclusion":"success","createdAt":"2026-11-13T00:53:46Z","updatedAt":"2026-11-13T01:06:05Z","databaseId":43340691235,"event":"push","headBranch":"main","headSha":"` + shaOlder + `","status":"completed","url":"u","workflowDatabaseId":409041453,"workflowName":"CI"}]`
	const sameSecondRun = `[{"conclusion":"success","createdAt":"2026-11-13T03:47:33Z","updatedAt":"2026-11-13T04:06:05Z","databaseId":43361860563,"event":"push","headBranch":"main","headSha":"` + shaNewer + `","status":"completed","url":"u","workflowDatabaseId":409041453,"workflowName":"CI"}]`
	const rerunRuns = `[{"conclusion":"success","createdAt":"2026-11-13T03:55:00Z","updatedAt":"2026-11-13T04:06:05Z","databaseId":43361281082,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"completed","url":"u","workflowDatabaseId":409041453,"workflowName":"CI","attempt":2},
{"conclusion":"cancelled","createdAt":"2026-11-13T03:47:33Z","updatedAt":"2026-11-13T03:53:18Z","databaseId":43361281082,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"completed","url":"u","workflowDatabaseId":409041453,"workflowName":"CI"}]`
	tests := []struct {
		name    string
		commit  string
		head    string
		created string
		since   string
	}{
		{"no run", supersedCI, shaCancelled, "", "[]"},
		{"a run of another commit created in the same second", supersedCI, shaCancelled, "", sameSecondRun},
		{"the cancelled run itself", "run-list-cancelled.json", shaByHand, "2026-11-20T09:07:13Z", "run-list-latest-self.json"},
		{"an older run of another commit", supersedCI, shaCancelled, "", olderRun},
		{"only a rerun of the same commit", supersedCI, shaCancelled, "", rerunRuns},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, calls := site{head: tt.head, commit: tt.commit, created: tt.created, since: map[string]string{ciID: tt.since}}.app(t)
			var stdout, stderr bytes.Buffer
			if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
			}
			if len(calls.since) != 1 {
				t.Errorf("lookups = %d, want 1", len(calls.since))
			}
			if strings.Contains(stdout.String(), "newer run") || strings.Contains(stderr.String(), "newer run") {
				t.Errorf("stdout = %q, stderr = %q, want no mention of a newer run", stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), "cancelled") {
				t.Errorf("stdout = %q, want the run reported as cancelled", stdout.String())
			}

			app, _ = site{head: tt.head, commit: tt.commit, created: tt.created, since: map[string]string{ciID: tt.since}}.app(t)
			stdout.Reset()
			stderr.Reset()
			app.Run(t.Context(), []string{"--json", "HEAD"}, &stdout, &stderr)
			if strings.Contains(stdout.String(), "newer_run") {
				t.Errorf("stdout = %q, want no newer_run", stdout.String())
			}
		})
	}
}

func TestNewerRunHintWithFailure(t *testing.T) { // AC-4
	const runs = `[
{"conclusion":"failure","createdAt":"2026-11-13T03:47:33Z","updatedAt":"2026-11-13T03:49:18Z","databaseId":43361281019,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"completed","url":"u","workflowDatabaseId":409041454,"workflowName":"Docs"},
{"conclusion":"cancelled","createdAt":"2026-11-13T03:47:33Z","updatedAt":"2026-11-13T03:53:18Z","databaseId":43361281082,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"completed","url":"u","workflowDatabaseId":409041453,"workflowName":"CI"}
]`
	app, _ := site{head: shaCancelled, commit: runs, since: map[string]string{ciID: sinceFixture}}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Fatalf("exit code = %d, want %d: a failure beats a cancellation (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	// The failed run also gets the rerun hint, which comes last.
	want := newerHint + "\nciwatch: 1 run failed; to rerun its failed jobs: ciwatch --rerun-failed " + shaCancelled
	if got := stderr.String(); !strings.HasSuffix(got, want+"\n") {
		t.Errorf("stderr = %q, want it to end with %q", got, want)
	}
}

func TestFollowIsAUsageError(t *testing.T) { // AC-5
	app, _ := fake(t, "run-list-success.json", nil, nil)
	calls := 0
	inner := app.GH.Exec
	app.GH.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls++
		return inner(ctx, name, args...)
	}
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--follow", "HEAD"}, &stdout, &stderr); code != cli.ExitUsage {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitUsage, stderr.String())
	}
	if !strings.HasPrefix(stderr.String(), "ciwatch: flag provided but not defined: -follow\n") {
		t.Errorf("stderr = %q, want it to start with the undefined-flag error", stderr.String())
	}
	if _, rest, _ := strings.Cut(stderr.String(), "\n"); !strings.Contains(rest, "usage: ciwatch [--json] [--once] [--go-tests] [--fetch-crashes] [--rerun-failed] <commit>") {
		t.Errorf("stderr = %q, want the usage after the error", stderr.String())
	}
	if calls != 0 {
		t.Errorf("gh and git calls = %d, want none", calls)
	}
}

func TestUsageDoesNotMentionFollow(t *testing.T) { // AC-5
	var stdout, stderr bytes.Buffer
	cli.App{}.Run(t.Context(), nil, &stdout, &stderr)
	if got := stderr.String(); !strings.Contains(got, "usage") && !strings.Contains(got, "Usage") || strings.Contains(got, "follow") || strings.Contains(got, `\n`) {
		t.Errorf("stderr = %q, want a usage that doesn't mention --follow", got)
	}
}

func TestDocsDescribeNewerRun(t *testing.T) { // AC-5
	read := func(parts ...string) string {
		b, err := os.ReadFile(filepath.Join(append([]string{"..", ".."}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	jsonDoc := read("docs", "json.md")
	for _, want := range []string{"runs[].newer_run", "runs[].newer_run.id", "runs[].newer_run.sha"} {
		if !strings.Contains(jsonDoc, want) {
			t.Errorf("docs/json.md does not mention %q", want)
		}
	}
	for name, doc := range map[string]string{"docs/json.md": jsonDoc, "README.md": read("README.md")} {
		for _, gone := range []string{"--follow", "followed_from", "superseded_by"} {
			if strings.Contains(doc, gone) {
				t.Errorf("%s still mentions %q", name, gone)
			}
		}
	}
}

func TestNewerRunLookupFailureKeepsCancelled(t *testing.T) { // AC-6
	s := supersededSite()
	s.sinceE = networkErr(t)
	app, calls := s.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	if want := 1 + app.Retries; len(calls.since) != want {
		t.Errorf("gh run list --branch calls = %d, want %d: the retries run out", len(calls.since), want)
	}
	if !strings.Contains(stderr.String(), "ciwatch: can't look for a newer run after run 43361281082 (CI): ") {
		t.Errorf("stderr = %q, want a warning that the lookup failed", stderr.String())
	}
	if strings.Contains(stderr.String(), "to watch the newer run") || strings.Contains(stdout.String(), "newer run") {
		t.Errorf("stdout = %q, stderr = %q, want no newer run named", stdout.String(), stderr.String())
	}
}

func TestInterruptDuringLookupWarnsOnce(t *testing.T) { // AC-6
	const cancelled = `[
{"conclusion":"cancelled","createdAt":"2026-11-13T03:47:33Z","updatedAt":"2026-11-13T03:53:18Z","databaseId":1,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"completed","url":"u","workflowDatabaseId":409041453,"workflowName":"CI"},
{"conclusion":"cancelled","createdAt":"2026-11-13T03:47:33Z","updatedAt":"2026-11-13T03:53:18Z","databaseId":2,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"completed","url":"u","workflowDatabaseId":500,"workflowName":"Lint"}
]`
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	app, calls := site{head: shaCancelled, commit: cancelled, sinceE: context.Canceled}.app(t)
	inner := app.GH.Exec
	app.GH.Exec = func(c context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 2 && args[2] == "--branch" {
			cancel()
		}
		return inner(c, name, args...)
	}
	var stdout, stderr bytes.Buffer
	if code := app.Run(ctx, []string{"HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitUnchecked, stderr.String())
	}
	if len(calls.since) != 1 {
		t.Errorf("gh run list --branch calls = %d, want 1: no lookups after Ctrl-C", len(calls.since))
	}
	if got := stderr.String(); strings.Contains(got, "can't look") || strings.Count(got, "context canceled") != 1 {
		t.Errorf("stderr = %q, want the interrupt reported once and no lookup warnings", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want no partial table", stdout.String())
	}
}

func TestTwoCancelledRunsTwoHints(t *testing.T) { // AC-7
	const runs = `[
{"conclusion":"","createdAt":"2026-11-13T03:47:33Z","updatedAt":"2026-11-13T03:49:18Z","databaseId":43361281019,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"in_progress","url":"u","workflowDatabaseId":409041454,"workflowName":"Docs"},
{"conclusion":"cancelled","createdAt":"2026-11-13T03:47:33Z","updatedAt":"2026-11-13T03:53:18Z","databaseId":43361281082,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"completed","url":"u","workflowDatabaseId":409041453,"workflowName":"CI"},
{"conclusion":"cancelled","createdAt":"2026-11-13T03:47:33Z","updatedAt":"2026-11-13T03:53:18Z","databaseId":43361281090,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"completed","url":"u","workflowDatabaseId":500,"workflowName":"Lint"}
]`
	const lintNewer = `[{"conclusion":"success","createdAt":"2026-11-13T03:52:58Z","updatedAt":"2026-11-13T04:04:30Z","databaseId":43361860570,"event":"push","headBranch":"main","headSha":"` + shaNewer + `","status":"completed","url":"u","workflowDatabaseId":500,"workflowName":"Lint"}]`
	app, calls := site{
		head:   shaCancelled,
		commit: runs,
		since:  map[string]string{ciID: sinceFixture, "500": lintNewer},
	}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--once", "HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	if len(calls.since) != 2 {
		t.Errorf("lookups = %q, want one for each cancelled run", calls.since)
	}
	hints := map[string]string{
		"CI":   newerHint,
		"Lint": "ciwatch: run 43361281090 (Lint) was cancelled; to watch the newer run: ciwatch " + shaNewer,
	}
	var want []string // in table order
	for _, l := range strings.Split(stdout.String(), "\n") {
		if f := strings.Fields(l); len(f) > 0 && hints[f[0]] != "" {
			want = append(want, hints[f[0]])
		}
	}
	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	if len(want) != 2 || len(lines) < 3 {
		t.Fatalf("stdout = %q, stderr = %q, want two cancelled rows and the hints after another line", stdout.String(), stderr.String())
	}
	if got := lines[len(lines)-2:]; !reflect.DeepEqual(got, want) {
		t.Errorf("last two stderr lines = %q, want %q", got, want)
	}
	if !strings.Contains(strings.Join(lines[:len(lines)-2], "\n"), "still in progress") {
		t.Errorf("stderr = %q, want the in-progress line before the hints", stderr.String())
	}
}
