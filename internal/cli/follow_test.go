package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/devvmhq/ciwatch/internal/cli"
	"github.com/devvmhq/ciwatch/internal/gh"
)

// Commits in the recorded history of devvmhq/devvm's main branch. 8da71a0's CI
// run was cancelled by the push of 90694a4.
const (
	shaCancelled = "8da71a01bb18eedaa5b282a4da705350f2dd3f90"
	shaNewer     = "90694a414f84ff5d765cdb6c8741e8d3888a2cbb"
	shaChainA    = "57cf548b319786663ef2828b0599e90033fb972c"
	shaChainB    = "5fb0732580cb3df23a563ee25775c45671d4ec7c"
	shaChainC    = "c3a262bec9aaabe65abc9567dbd6f8e240571383"
	shaChainD    = "12764741238132b863313b57c7934ff377d8890a"
	shaByHand    = "a1c09016a1866e55ef9e2b5ae4c123472ab9b69c"
)

const branchFields = "databaseId,workflowDatabaseId,workflowName,status,conclusion,url,createdAt,updatedAt,headBranch,headSha,event"

// site is what the fake GitHub holds: the runs of each commit, and what gh run
// list --branch prints, one answer per call (the last repeated).
type site struct {
	head    string            // what git rev-parse prints
	commits map[string]string // sha to the fixture, or raw output if it starts with "["
	branch  []string          // fixtures, or raw output if one starts with "["
	branchE error             // if set, gh run list --branch fails with it
}

type siteCalls struct {
	commits []string // the sha of each gh run list --commit, repeats in a row dropped
	branch  int
	flows   []string // the --workflow of each gh run list --branch
	created []string // and its --created
	clock   *fakeClock
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
			if n := len(c.commits); n == 0 || c.commits[n-1] != args[3] { // polling lists a commit more than once
				c.commits = append(c.commits, args[3])
			}
			want := []string{"run", "list", "--commit", args[3], "--limit", "100", "--json", branchFields}
			if !reflect.DeepEqual(args, want) {
				t.Errorf("gh args = %q, want %q", args, want)
			}
			f, ok := s.commits[args[3]]
			if !ok {
				t.Errorf("gh run list for unexpected commit %s", args[3])
				return nil, errors.New("unexpected commit")
			}
			return load(f), nil
		case name == "gh" && len(args) >= 3 && args[1] == "list" && args[2] == "--branch":
			c.branch++
			c.flows = append(c.flows, args[5])
			c.created = append(c.created, args[7])
			want := []string{"run", "list", "--branch", "main", "--workflow", args[5], "--created", args[7], "--event", "push", "--limit", "20", "--json", branchFields}
			if !reflect.DeepEqual(args, want) || args[4] != "--workflow" {
				t.Errorf("gh args = %q, want %q", args, want)
			}
			if s.branchE != nil {
				return nil, s.branchE
			}
			return load(s.branch[min(c.branch, len(s.branch))-1]), nil
		case name == "gh" && len(args) == 5 && args[1] == "view":
			return []byte(noJobs), nil
		}
		t.Errorf("unexpected command %s %q", name, args)
		return nil, errors.New("unexpected command")
	}}}
	app, c.clock = withClock(app, fixtureNow)
	return app, c
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

func TestCancelledByNewerPushNamesCommit(t *testing.T) {
	app, calls := site{
		head:    shaCancelled,
		commits: map[string]string{shaCancelled: "run-list-superseded.json"},
		branch:  []string{"run-list-branch-superseded.json"},
	}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	checkTable(t, stdout.String(), []row{
		runRow("Docs", "completed", "success"),
		{fields: []string{"CI", "completed", "cancelled", "(superseded", "by", "90694a4)"}},
	})
	if got := lastLine(stderr.String()); !strings.Contains(got, "--follow") || !strings.Contains(got, "90694a4") {
		t.Errorf("last stderr line = %q, want the hint naming --follow and 90694a4", got)
	}
	if calls.branch != 1 {
		t.Errorf("gh run list --branch calls = %d, want 1: only the cancelled run is looked up", calls.branch)
	}
	if want := []string{"2026-11-13T03:47:33Z..2026-11-13T03:54:18Z"}; !reflect.DeepEqual(calls.created, want) {
		t.Errorf("gh run list --branch --created = %q, want %q: from the run's start to a minute after it stopped", calls.created, want)
	}
	if want := []string{"409041453"}; !reflect.DeepEqual(calls.flows, want) {
		t.Errorf("gh run list --branch --workflow = %q, want the CI workflow's ID %q, not its name", calls.flows, want)
	}
}

func TestCancelledByHandHasNoHint(t *testing.T) {
	app, calls := site{
		head:    shaByHand,
		commits: map[string]string{shaByHand: "run-list-cancelled.json"},
		branch:  []string{"run-list-branch-by-hand.json"},
	}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	if calls.branch != 1 {
		t.Errorf("gh run list --branch calls = %d, want 1: a newer push is looked for", calls.branch)
	}
	if strings.Contains(stderr.String(), "--follow") || strings.Contains(stdout.String(), "superseded") {
		t.Errorf("stdout = %q, stderr = %q, want no mention of a newer push", stdout.String(), stderr.String())
	}
}

func TestFollowWatchesNewerCommit(t *testing.T) {
	app, calls := site{
		head: shaCancelled,
		commits: map[string]string{
			shaCancelled: "run-list-superseded.json",
			shaNewer:     "run-list-newer-success.json",
		},
		branch: []string{"run-list-branch-superseded.json"},
	}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--follow", "HEAD"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitOK, stderr.String())
	}
	checkTable(t, stdout.String(), []row{
		runRow("Docs", "completed", "success"),
		runRow("CI", "completed", "success"),
	})
	if !strings.Contains(stderr.String(), "was cancelled by a newer push; following 90694a4") {
		t.Errorf("stderr = %q, want the following line naming 90694a4", stderr.String())
	}
	if strings.Contains(stderr.String(), "rerun with --follow") {
		t.Errorf("stderr = %q, want no hint when following", stderr.String())
	}
	if want := []string{shaCancelled, shaNewer}; !reflect.DeepEqual(calls.commits, want) {
		t.Errorf("gh run list --commit calls = %q, want %q", calls.commits, want)
	}
}

func TestFollowChainsToNewest(t *testing.T) {
	app, calls := site{
		head: shaChainA,
		commits: map[string]string{
			shaChainA: "run-list-chain-a.json",
			shaChainB: "run-list-chain-b.json",
			shaChainC: "run-list-chain-c.json",
			shaChainD: "run-list-chain-d.json",
		},
		branch: []string{"run-list-branch-chain.json"},
	}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--follow", "--json", "HEAD"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitOK, stderr.String())
	}
	var out struct {
		SHA          string   `json:"sha"`
		FollowedFrom []string `json:"followed_from"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout = %q: %v", stdout.String(), err)
	}
	if out.SHA != shaChainD {
		t.Errorf("sha = %q, want %q", out.SHA, shaChainD)
	}
	if want := []string{shaChainA, shaChainB, shaChainC}; !reflect.DeepEqual(out.FollowedFrom, want) {
		t.Errorf("followed_from = %q, want %q", out.FollowedFrom, want)
	}
	if want := []string{shaChainA, shaChainB, shaChainC, shaChainD}; !reflect.DeepEqual(calls.commits, want) {
		t.Errorf("gh run list --commit calls = %q, want %q", calls.commits, want)
	}
}

func TestFollowJSONSupersededBy(t *testing.T) {
	app, _ := site{
		head:    shaCancelled,
		commits: map[string]string{shaCancelled: "run-list-superseded.json"},
		branch:  []string{"run-list-branch-superseded.json"},
	}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--json", "HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	var out struct {
		Runs         []map[string]any `json:"runs"`
		FollowedFrom []string         `json:"followed_from"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout = %q: %v", stdout.String(), err)
	}
	if len(out.Runs) != 2 {
		t.Fatalf("runs = %v, want 2", out.Runs)
	}
	for _, r := range out.Runs {
		got, has := r["superseded_by"]
		switch r["workflow"] {
		case "CI":
			if got != shaNewer {
				t.Errorf("CI superseded_by = %v, want %q", got, shaNewer)
			}
		default:
			if has {
				t.Errorf("%v run has superseded_by = %v, want the field left out", r["workflow"], got)
			}
		}
	}
	if out.FollowedFrom != nil {
		t.Errorf("followed_from = %q, want it left out without --follow", out.FollowedFrom)
	}
}

func TestSupersededLookupFailureKeepsCancelled(t *testing.T) {
	app, calls := site{
		head:    shaCancelled,
		commits: map[string]string{shaCancelled: "run-list-superseded.json"},
		branchE: networkErr(t),
	}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--follow", "HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	if want := 1 + app.Retries; calls.branch != want {
		t.Errorf("gh run list --branch calls = %d, want %d: the retries run out", calls.branch, want)
	}
	if !strings.Contains(stderr.String(), "gave up after 8 retries") {
		t.Errorf("stderr = %q, want a warning that the lookup gave up", stderr.String())
	}
	if strings.Contains(stderr.String(), "rerun with --follow") || strings.Contains(stderr.String(), "following") || strings.Contains(stdout.String(), "superseded") {
		t.Errorf("stdout = %q, stderr = %q, want no mention of a newer push", stdout.String(), stderr.String())
	}
}

func TestSupersededIgnoresOlderAndSameSHARuns(t *testing.T) {
	// The cancelled run is 8da71a0's, created 2026-11-13T03:47:33Z. The branch
	// holds a later run of the same commit and an earlier run of another one.
	const branch = `[
{"conclusion":"success","createdAt":"2026-11-13T03:49:00Z","updatedAt":"2026-11-13T03:55:00Z","databaseId":43361999999,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"completed","url":"u","workflowName":"CI"},
{"conclusion":"cancelled","createdAt":"2026-11-13T03:47:33Z","updatedAt":"2026-11-13T03:53:18Z","databaseId":43361281082,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"completed","url":"u","workflowName":"CI"},
{"conclusion":"success","createdAt":"2026-11-13T00:53:46Z","updatedAt":"2026-11-13T01:06:05Z","databaseId":43340691235,"event":"push","headBranch":"main","headSha":"` + shaChainB + `","status":"completed","url":"u","workflowName":"CI"}
]`
	app, calls := site{
		head:    shaCancelled,
		commits: map[string]string{shaCancelled: "run-list-superseded.json"},
		branch:  []string{branch},
	}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--follow", "HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	if calls.branch != 1 {
		t.Errorf("gh run list --branch calls = %d, want 1", calls.branch)
	}
	if strings.Contains(stdout.String(), "superseded") || strings.Contains(stderr.String(), "newer push") {
		t.Errorf("stdout = %q, stderr = %q, want no mention of a newer push", stdout.String(), stderr.String())
	}
	if len(calls.commits) != 1 {
		t.Errorf("gh run list --commit calls = %q, want only the first commit", calls.commits)
	}
}

func TestFollowOnceChecksEachCommitOnce(t *testing.T) {
	app, calls := site{
		head: shaCancelled,
		commits: map[string]string{
			shaCancelled: "run-list-superseded.json",
			shaNewer:     "run-list-newer-success.json",
		},
		branch: []string{"run-list-branch-superseded.json"},
	}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--once", "--follow", "HEAD"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitOK, stderr.String())
	}
	if want := []string{shaCancelled, shaNewer}; !reflect.DeepEqual(calls.commits, want) {
		t.Errorf("gh run list --commit calls = %q, want %q: one check each", calls.commits, want)
	}
	if len(calls.clock.waits) != 0 {
		t.Errorf("waits = %v, want none with --once", calls.clock.waits)
	}
}

func TestSupersededIsTheFirstPushAfterTheRunStarted(t *testing.T) {
	// The branch list is the recorded one: 90694a4 was pushed five minutes after
	// 8da71a0 and cancelled its run, and a1c0901 was pushed a week later.
	app, _ := site{
		head:    shaCancelled,
		commits: map[string]string{shaCancelled: "run-list-superseded.json"},
		branch:  []string{"run-list-branch-by-hand.json"},
	}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	if !strings.Contains(stdout.String(), "(superseded by 90694a4)") || strings.Contains(stdout.String(), "a1c0901") {
		t.Errorf("stdout = %q, want the run superseded by 90694a4, the push that cancelled it", stdout.String())
	}
}

func TestCancelledByHandIsNotSupersededByALaterPush(t *testing.T) {
	// a1c0901's run was cancelled at 09:16:45 by hand. A push an hour and a half
	// later started its own run, but it cancelled nothing.
	const branch = `[
{"conclusion":"success","createdAt":"2026-11-20T10:50:00Z","updatedAt":"2026-11-20T11:00:00Z","databaseId":44800000001,"event":"push","headBranch":"main","headSha":"` + shaChainB + `","status":"completed","url":"u","workflowName":"CI"},
{"conclusion":"cancelled","createdAt":"2026-11-20T09:07:13Z","updatedAt":"2026-11-20T09:16:45Z","databaseId":44707997310,"event":"push","headBranch":"main","headSha":"` + shaByHand + `","status":"completed","url":"u","workflowName":"CI"}
]`
	app, calls := site{
		head:    shaByHand,
		commits: map[string]string{shaByHand: "run-list-cancelled.json"},
		branch:  []string{branch},
	}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--follow", "HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	if strings.Contains(stdout.String(), "superseded") || strings.Contains(stderr.String(), "newer push") {
		t.Errorf("stdout = %q, stderr = %q, want no mention of a newer push", stdout.String(), stderr.String())
	}
	if len(calls.commits) != 1 {
		t.Errorf("gh run list --commit calls = %q, want only the first commit", calls.commits)
	}
}

func TestFollowTakesTheNewestOfSeveralSupersedingCommits(t *testing.T) {
	// Two runs of one commit, both cancelled at once, by pushes of different
	// commits: Lint's by shaChainB at 03:49, CI's by shaNewer at 03:52. The
	// commit to follow is the newer push, whatever order the runs are listed in: Lint
	// (the older push) is listed first. Each run is looked up by its workflow ID.
	const cancelledRuns = `[
{"conclusion":"cancelled","createdAt":"2026-11-13T03:47:33Z","updatedAt":"2026-11-13T03:53:18Z","databaseId":2,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"completed","url":"u","workflowDatabaseId":500,"workflowName":"Lint"},
{"conclusion":"cancelled","createdAt":"2026-11-13T03:47:33Z","updatedAt":"2026-11-13T03:53:18Z","databaseId":1,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"completed","url":"u","workflowDatabaseId":409041453,"workflowName":"CI"}
]`
	const ciBranch = `[{"conclusion":"success","createdAt":"2026-11-13T03:52:00Z","updatedAt":"2026-11-13T04:00:00Z","databaseId":3,"event":"push","headBranch":"main","headSha":"` + shaNewer + `","status":"completed","url":"u","workflowName":"CI"}]`
	const lintBranch = `[{"conclusion":"success","createdAt":"2026-11-13T03:49:00Z","updatedAt":"2026-11-13T04:00:00Z","databaseId":4,"event":"push","headBranch":"main","headSha":"` + shaChainB + `","status":"completed","url":"u","workflowName":"Lint"}]`
	app, calls := site{
		head: shaCancelled,
		commits: map[string]string{
			shaCancelled: cancelledRuns,
			shaNewer:     "run-list-newer-success.json",
		},
		branch: []string{lintBranch, ciBranch},
	}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--follow", "HEAD"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitOK, stderr.String())
	}
	if want := []string{shaCancelled, shaNewer}; !reflect.DeepEqual(calls.commits, want) {
		t.Errorf("gh run list --commit calls = %q, want %q", calls.commits, want)
	}
	if want := []string{"500", "409041453"}; !reflect.DeepEqual(calls.flows, want) {
		t.Errorf("gh run list --branch --workflow = %q, want %q: the workflow IDs, not the names", calls.flows, want)
	}
}

// cancelledRun is a CI run of commit sha cancelled at 03:53:18.
func cancelledRun(id int, sha string) string {
	return fmt.Sprintf(`{"conclusion":"cancelled","createdAt":"2026-11-13T03:47:33Z","updatedAt":"2026-11-13T03:53:18Z","databaseId":%d,"event":"push","headBranch":"main","headSha":%q,"status":"completed","url":"u","workflowDatabaseId":409041453,"workflowName":"CI"}`, id, sha)
}

// laterRun is a cancelled CI run of commit sha, created at 03:50, inside the
// window of cancelledRun.
func laterRun(sha string) string {
	return fmt.Sprintf(`[{"conclusion":"cancelled","createdAt":"2026-11-13T03:50:00Z","updatedAt":"2026-11-13T03:53:18Z","databaseId":9,"event":"push","headBranch":"main","headSha":%q,"status":"completed","url":"u","workflowName":"CI"}]`, sha)
}

func TestFollowStopsAtTheMostCommitsItFollows(t *testing.T) {
	// A chain of distinct commits, each cancelled by the next, longer than the cap.
	shas := make([]string, cli.MaxFollowed+2)
	commits := map[string]string{}
	var answers []string
	for i := range shas {
		shas[i] = fmt.Sprintf("%040x", i+1)
	}
	for i := range cli.MaxFollowed + 1 {
		commits[shas[i]] = "[" + cancelledRun(i+1, shas[i]) + "]"
		answers = append(answers, laterRun(shas[i+1]))
	}
	app, _ := site{head: shas[0], commits: commits, branch: answers}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--follow", "--json", "HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	var out struct {
		SHA          string   `json:"sha"`
		FollowedFrom []string `json:"followed_from"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout = %q: %v", stdout.String(), err)
	}
	if len(out.FollowedFrom) != cli.MaxFollowed || out.SHA != shas[cli.MaxFollowed] {
		t.Errorf("followed %d commits ending at %s, want %d (the cap) ending at %s", len(out.FollowedFrom), out.SHA, cli.MaxFollowed, shas[cli.MaxFollowed])
	}
	if !strings.Contains(stderr.String(), "followed") || !strings.Contains(stderr.String(), "the most") {
		t.Errorf("stderr = %q, want a warning that the cap was reached", stderr.String())
	}
}

func TestFollowDoesNotGoBackToACommitItLeft(t *testing.T) {
	// Each commit's cancelled run names the other as its successor, which a real
	// branch can't do; following must not bounce between them.
	app, calls := site{
		head: shaCancelled,
		commits: map[string]string{
			shaCancelled: "[" + cancelledRun(1, shaCancelled) + "]",
			shaNewer:     "[" + cancelledRun(2, shaNewer) + "]",
		},
		branch: []string{laterRun(shaNewer), laterRun(shaCancelled)},
	}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--follow", "HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	if want := []string{shaCancelled, shaNewer}; !reflect.DeepEqual(calls.commits, want) {
		t.Errorf("gh run list --commit calls = %q, want %q: no going back", calls.commits, want)
	}
}

func TestPushedAgainIsNotSuperseded(t *testing.T) {
	// A is pushed, then B cancels A's CI run, then A is pushed again and its new
	// CI run succeeds. A's old cancelled run was replaced by a run of A itself,
	// so nothing newer is behind it: no hint, and nothing to follow.
	const runs = `[` + `{"conclusion":"cancelled","createdAt":"2026-11-13T03:47:33Z","updatedAt":"2026-11-13T03:53:18Z","databaseId":1,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"completed","url":"u","workflowDatabaseId":409041453,"workflowName":"CI"},
{"conclusion":"success","createdAt":"2026-11-13T03:58:00Z","updatedAt":"2026-11-13T04:08:00Z","databaseId":3,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"completed","url":"u","workflowDatabaseId":409041453,"workflowName":"CI"}]`
	for _, args := range [][]string{{"HEAD"}, {"--follow", "HEAD"}} {
		app, calls := site{
			head:    shaCancelled,
			commits: map[string]string{shaCancelled: runs},
			branch:  []string{"run-list-branch-superseded.json"},
		}.app(t)
		var stdout, stderr bytes.Buffer
		app.Run(t.Context(), args, &stdout, &stderr)
		if strings.Contains(stdout.String(), "superseded") || strings.Contains(stderr.String(), "newer push") {
			t.Errorf("%v: stdout = %q, stderr = %q, want no mention of a newer push", args, stdout.String(), stderr.String())
		}
		if len(calls.commits) != 1 {
			t.Errorf("%v: gh run list --commit calls = %q, want only the first commit", args, calls.commits)
		}
	}
}

func TestSupersededLookupFullPageIsUncertain(t *testing.T) {
	// gh lists newest first and returns 20 at most. Here all 20 are inside the
	// window, so the earliest push may be cut off: naming one of them could name
	// the wrong commit, so the run stays plainly cancelled, with a warning.
	var page []string
	for i := range gh.BranchRunsLimit {
		page = append(page, fmt.Sprintf(`{"conclusion":"success","createdAt":"2026-11-13T03:49:%02dZ","updatedAt":"2026-11-13T03:55:00Z","databaseId":%d,"event":"push","headBranch":"main","headSha":"%040x","status":"completed","url":"u","workflowName":"CI"}`, i, 100+i, i+1))
	}
	app, _ := site{
		head:    shaCancelled,
		commits: map[string]string{shaCancelled: "run-list-superseded.json"},
		branch:  []string{"[" + strings.Join(page, ",") + "]"},
	}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--follow", "HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	if !strings.Contains(stderr.String(), "can't look for a newer push") || strings.Contains(stdout.String(), "superseded") || strings.Contains(stderr.String(), "following") {
		t.Errorf("stdout = %q, stderr = %q, want a warning and no newer push named", stdout.String(), stderr.String())
	}
}

func TestInterruptDuringLookupWarnsOnce(t *testing.T) {
	// Two cancelled runs; Ctrl-C arrives during the first lookup.
	const cancelled = `[
{"conclusion":"cancelled","createdAt":"2026-11-13T03:47:33Z","updatedAt":"2026-11-13T03:53:18Z","databaseId":1,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"completed","url":"u","workflowDatabaseId":409041453,"workflowName":"CI"},
{"conclusion":"cancelled","createdAt":"2026-11-13T03:47:33Z","updatedAt":"2026-11-13T03:53:18Z","databaseId":2,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"completed","url":"u","workflowDatabaseId":500,"workflowName":"Lint"}
]`
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	app, calls := site{
		head:    shaCancelled,
		commits: map[string]string{shaCancelled: cancelled},
		branchE: context.Canceled,
	}.app(t)
	inner := app.GH.Exec
	app.GH.Exec = func(c context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 2 && args[2] == "--branch" {
			cancel()
		}
		return inner(c, name, args...)
	}
	var stdout, stderr bytes.Buffer
	if code := app.Run(ctx, []string{"--follow", "HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitUnchecked, stderr.String())
	}
	if calls.branch != 1 {
		t.Errorf("gh run list --branch calls = %d, want 1: no lookups after Ctrl-C", calls.branch)
	}
	if got := stderr.String(); strings.Contains(got, "can't look") || strings.Contains(got, "following") || strings.Count(got, "context canceled") != 1 {
		t.Errorf("stderr = %q, want the interrupt reported once and no lookup warnings", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want no partial table", stdout.String())
	}
}

func TestSupersededIgnoresOtherEvents(t *testing.T) {
	// A fork's pull request from a branch also called main ran CI at 03:50, before
	// the push of 90694a4 at 03:52. It isn't a push to main, so it cancelled nothing.
	const branch = `[
{"conclusion":"success","createdAt":"2026-11-13T03:52:57Z","updatedAt":"2026-11-13T04:04:30Z","databaseId":3,"event":"push","headBranch":"main","headSha":"` + shaNewer + `","status":"completed","url":"u","workflowName":"CI"},
{"conclusion":"success","createdAt":"2026-11-13T03:50:00Z","updatedAt":"2026-11-13T04:00:00Z","databaseId":4,"event":"pull_request","headBranch":"main","headSha":"` + shaChainB + `","status":"completed","url":"u","workflowName":"CI"}
]`
	app, _ := site{
		head:    shaCancelled,
		commits: map[string]string{shaCancelled: "run-list-superseded.json"},
		branch:  []string{branch},
	}.app(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	if !strings.Contains(stdout.String(), "(superseded by 90694a4)") || strings.Contains(stdout.String(), "5fb0732") {
		t.Errorf("stdout = %q, want the run superseded by the push 90694a4, not the pull request", stdout.String())
	}
}

func TestHintIsTheLastLineEvenWithRunsInProgress(t *testing.T) {
	const runs = `[
{"conclusion":"","createdAt":"2026-11-13T03:47:33Z","updatedAt":"2026-11-13T03:49:18Z","databaseId":43361281019,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"in_progress","url":"u","workflowDatabaseId":409041454,"workflowName":"Docs"},
{"conclusion":"cancelled","createdAt":"2026-11-13T03:47:33Z","updatedAt":"2026-11-13T03:53:18Z","databaseId":43361281082,"event":"push","headBranch":"main","headSha":"` + shaCancelled + `","status":"completed","url":"u","workflowDatabaseId":409041453,"workflowName":"CI"}
]`
	app, _ := site{
		head:    shaCancelled,
		commits: map[string]string{shaCancelled: runs},
		branch:  []string{"run-list-branch-superseded.json"},
	}.app(t)
	var stdout, stderr bytes.Buffer
	app.Run(t.Context(), []string{"--once", "HEAD"}, &stdout, &stderr)
	const want = "ciwatch: run 43361281082 (CI) was cancelled by a newer push, 90694a4; rerun with --follow to watch it"
	if got := lastLine(stderr.String()); got != want {
		t.Errorf("last stderr line = %q, want %q (stderr %q)", got, want, stderr.String())
	}
	if !strings.Contains(stderr.String(), "1 runs still in progress") {
		t.Errorf("stderr = %q, want the in-progress line before the hint", stderr.String())
	}
}
