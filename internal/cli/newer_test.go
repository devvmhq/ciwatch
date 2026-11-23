package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

const branchFields = "databaseId,workflowDatabaseId,workflowName,status,conclusion,url,createdAt,updatedAt,headBranch,headSha,event"

// site is what the fake GitHub holds: the runs of the commit, and what gh run
// list --branch prints for each workflow ID.
type site struct {
	head    string            // what git rev-parse prints
	commit  string            // the runs of head: a fixture, or raw output if it starts with "["
	latest  map[string]string // workflow ID to a fixture, or raw output if it starts with "["
	latestE error             // if set, gh run list --branch fails with it
}

type siteCalls struct {
	latest []string // the --workflow of each gh run list --branch
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
			c.latest = append(c.latest, args[5])
			want := []string{"run", "list", "--branch", "main", "--workflow", args[5], "--limit", "1", "--json", branchFields}
			if !reflect.DeepEqual(args, want) {
				t.Errorf("gh args = %q, want %q", args, want)
			}
			if s.latestE != nil {
				return nil, s.latestE
			}
			f, ok := s.latest[args[5]]
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
		latest: map[string]string{ciID: "run-list-latest-newer.json"},
	}
}

func TestCancelledLooksUpLatestRunOnce(t *testing.T) { // AC-1
	app, calls := supersededSite().app(t) // the fake checks the exact arguments
	var stdout, stderr bytes.Buffer
	app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr)
	if want := []string{ciID}; !reflect.DeepEqual(calls.latest, want) {
		t.Errorf("gh run list --branch --workflow = %q, want %q: one lookup, for the cancelled CI run only", calls.latest, want)
	}
}

func TestCancelledNamesNewerRun(t *testing.T) { // AC-2, AC-4
	app, _ := supersededSite().app(t)
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
	tests := []struct {
		name   string
		commit string
		head   string
		latest string
	}{
		{"no run", supersedCI, shaCancelled, "[]"},
		{"a run of another commit created in the same second", supersedCI, shaCancelled, sameSecondRun},
		{"the cancelled run itself", "run-list-cancelled.json", shaByHand, "run-list-latest-self.json"},
		{"an older run of another commit", supersedCI, shaCancelled, olderRun},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, calls := site{head: tt.head, commit: tt.commit, latest: map[string]string{ciID: tt.latest}}.app(t)
			var stdout, stderr bytes.Buffer
			if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
			}
			if len(calls.latest) != 1 {
				t.Errorf("lookups = %d, want 1", len(calls.latest))
			}
			if strings.Contains(stdout.String(), "newer run") || strings.Contains(stderr.String(), "newer run") {
				t.Errorf("stdout = %q, stderr = %q, want no mention of a newer run", stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), "cancelled") {
				t.Errorf("stdout = %q, want the run reported as cancelled", stdout.String())
			}

			app, _ = site{head: tt.head, commit: tt.commit, latest: map[string]string{ciID: tt.latest}}.app(t)
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
	app, _ := site{head: shaCancelled, commit: runs, latest: map[string]string{ciID: "run-list-latest-newer.json"}}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
		t.Fatalf("exit code = %d, want %d: a failure beats a cancellation (stderr %q)", code, cli.ExitFailed, stderr.String())
	}
	if got := lastLine(stderr.String()); got != newerHint {
		t.Errorf("last stderr line = %q, want %q", got, newerHint)
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
	if _, rest, _ := strings.Cut(stderr.String(), "\n"); !strings.Contains(rest, "usage: ciwatch [--json] [--once] [--go-tests] [--fetch-crashes] <commit>") {
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
	s.latestE = networkErr(t)
	app, calls := s.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	if want := 1 + app.Retries; len(calls.latest) != want {
		t.Errorf("gh run list --branch calls = %d, want %d: the retries run out", len(calls.latest), want)
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
	app, calls := site{head: shaCancelled, commit: cancelled, latestE: context.Canceled}.app(t)
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
	if len(calls.latest) != 1 {
		t.Errorf("gh run list --branch calls = %d, want 1: no lookups after Ctrl-C", len(calls.latest))
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
		latest: map[string]string{ciID: "run-list-latest-newer.json", "500": lintNewer},
	}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--once", "HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	if len(calls.latest) != 2 {
		t.Errorf("lookups = %q, want one for each cancelled run", calls.latest)
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
