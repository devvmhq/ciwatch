package cli_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/devvmhq/ciwatch/internal/cli"
)

// unlisted is the warning for a run that isn't a rerun and dropped out.
func unlisted(id, workflow string) string {
	return "ciwatch: run " + id + " (" + workflow + ") is no longer listed for this commit\n"
}

// TestWatchRunVanishesAfterFailure covers AC-1: a failed run that drops out
// is kept at its last state, and the exit code is 1.
func TestWatchRunVanishesAfterFailure(t *testing.T) {
	opts := fakeOpts{listRaw: []string{
		list(failed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
		list(passed(docsRun, "Docs", 1)),
	}}
	stdout, stderr, _ := runRerun(t, opts, cli.ExitFailed)
	if n := strings.Count(stderr, unlisted(ciRun, "CI")); n != 1 {
		t.Errorf("stderr = %q, want the warning once, got %d", stderr, n)
	}
	if n := strings.Count(stderr, "is no longer listed"); n != 1 {
		t.Errorf("stderr = %q, want exactly one warning, got %d", stderr, n)
	}
	checkTable(t, stdout, slices.Concat(
		[]row{runRow("CI", "completed", "failure")}, jobRowsOf(t, "run-view-"+ciRun+".json"),
		[]row{runRow("Docs", "completed", "success")}, jobRowsOf(t, "run-view-"+docsRun+".json"),
	))
}

// TestWatchRunVanishesAfterSuccess covers AC-2: a passed run that drops out
// is kept, and the exit code is 0.
func TestWatchRunVanishesAfterSuccess(t *testing.T) {
	opts := fakeOpts{listRaw: []string{
		list(passed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
		list(passed(ciRun, "CI", 1)),
	}}
	stdout, stderr, _ := runRerun(t, opts, cli.ExitOK)
	if n := strings.Count(stderr, unlisted(docsRun, "Docs")); n != 1 {
		t.Errorf("stderr = %q, want the warning once, got %d", stderr, n)
	}
	checkTable(t, stdout, slices.Concat(
		[]row{runRow("CI", "completed", "success")}, jobRowsOf(t, "run-view-"+ciRun+".json"),
		[]row{runRow("Docs", "completed", "success")}, jobRowsOf(t, "run-view-"+docsRun+".json"),
	))
}

// TestWatchRunVanishesInProgressNoJobs covers AC-1 and AC-7: a kept run that
// wasn't completed when last listed shows no job rows, and its jobs aren't
// fetched.
func TestWatchRunVanishesInProgressNoJobs(t *testing.T) {
	for _, status := range []string{"in_progress", "queued", "waiting", "requested", "pending"} {
		t.Run(status, func(t *testing.T) {
			opts := fakeOpts{listRaw: []string{
				list(attemptRun(ciRun, "CI", status, "", 1), passed(docsRun, "Docs", 1)),
				list(passed(docsRun, "Docs", 1)),
			}}
			stdout, stderr, calls := runVanish(t, opts, cli.ExitUnchecked, 30)
			if n := strings.Count(stderr, unlisted(ciRun, "CI")); n != 1 {
				t.Errorf("stderr = %q, want the warning once, got %d", stderr, n)
			}
			checkTable(t, stdout, slices.Concat(
				[]row{runRow("CI", status, "")},
				[]row{runRow("Docs", "completed", "success")}, jobRowsOf(t, "run-view-"+docsRun+".json"),
			))
			if slices.Contains(calls.viewIDs, ciRun) {
				t.Errorf("gh run view ids = %q, want none for the kept run %s", calls.viewIDs, ciRun)
			}
		})
	}
}

// TestWatchRunVanishesInProgressJSON covers AC-2: the kept run's jobs are an
// empty array, with no jobs_error.
func TestWatchRunVanishesInProgressJSON(t *testing.T) {
	opts := fakeOpts{listRaw: []string{
		list(attemptRun(ciRun, "CI", "in_progress", "", 1), passed(docsRun, "Docs", 1)),
		list(passed(docsRun, "Docs", 1)),
	}}
	stdout, _, _ := runVanish(t, opts, cli.ExitUnchecked, 30, "--json")
	var got struct {
		Runs []map[string]json.RawMessage `json:"runs"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout %q is not JSON: %v", stdout, err)
	}
	found := false
	for _, r := range got.Runs {
		if string(r["workflow"]) != `"CI"` {
			continue
		}
		found = true
		if jobs := strings.TrimSpace(string(r["jobs"])); jobs != "[]" {
			t.Errorf("CI jobs = %s, want []", jobs)
		}
		if e, ok := r["jobs_error"]; ok {
			t.Errorf("CI has jobs_error %s, want it omitted", e)
		}
	}
	if !found {
		t.Errorf("stdout = %q, want a CI run", stdout)
	}
}

// TestRerunVanishesInProgressNoJobs covers AC-3: a rerun's new attempt that
// was last seen in progress and dropped out shows no job rows.
func TestRerunVanishesInProgressNoJobs(t *testing.T) {
	opts := fakeOpts{listRaw: lists(list(failed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
		list(attemptRun(ciRun, "CI", "in_progress", "", 2), passed(docsRun, "Docs", 1)),
		list(passed(docsRun, "Docs", 1)),
	)}
	stdout, stderr, calls := runVanish(t, opts, cli.ExitUnchecked, 30, "--rerun-failed")
	if !strings.Contains(stderr, unlistedCI) {
		t.Errorf("stderr = %q, want the warning ending in \"not waiting for it\"", stderr)
	}
	checkTable(t, stdout, slices.Concat(
		[]row{runRow("CI", "in_progress", "")},
		[]row{runRow("Docs", "completed", "success")}, jobRowsOf(t, "run-view-"+docsRun+".json"),
	))
	if slices.Contains(calls.viewIDs, ciRun) {
		t.Errorf("gh run view ids = %q, want none for the kept run %s", calls.viewIDs, ciRun)
	}
}

// TestWatchRunVanishesInProgress covers AC-3: a run last seen in progress
// that stays out isn't waited for.
func TestWatchRunVanishesInProgress(t *testing.T) {
	opts := fakeOpts{listRaw: []string{
		list(attemptRun(ciRun, "CI", "in_progress", "", 1), passed(docsRun, "Docs", 1)),
		list(passed(docsRun, "Docs", 1)),
	}}
	stdout, stderr, calls := runVanish(t, opts, cli.ExitUnchecked, 30)
	if n := strings.Count(stderr, unlisted(ciRun, "CI")); n != 1 {
		t.Errorf("stderr = %q, want the warning once, got %d", stderr, n)
	}
	if strings.Contains(stderr, "drop --once") {
		t.Errorf("stderr = %q, want no advice to drop --once", stderr)
	}
	if want := int(testGrace/testInterval) + 1; calls.list != want {
		t.Errorf("gh run list calls = %d, want %d: it ends at the grace period", calls.list, want)
	}
	checkRow(t, stdout, "CI", "in_progress")
}

// TestWatchRunReappears covers AC-4: one warning per disappearance, and the
// report comes from the last listed state.
func TestWatchRunReappears(t *testing.T) {
	opts := fakeOpts{listRaw: []string{
		list(passed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
		list(passed(ciRun, "CI", 1)),
		list(passed(ciRun, "CI", 1), failed(docsRun, "Docs", 1)),
		list(passed(ciRun, "CI", 1)),
		list(passed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
	}}
	stdout, stderr, _ := runRerun(t, opts, cli.ExitOK)
	if n := strings.Count(stderr, unlisted(docsRun, "Docs")); n != 2 {
		t.Errorf("stderr = %q, want the warning twice, got %d", stderr, n)
	}
	checkRow(t, stdout, "Docs", "completed", "success")
}

// TestWatchAllRunsVanish covers AC-5: an empty list after a non-empty one
// keeps every run, and isn't "no runs found".
func TestWatchAllRunsVanish(t *testing.T) {
	opts := fakeOpts{listRaw: []string{
		list(failed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
		list(),
	}}
	stdout, stderr, _ := runRerun(t, opts, cli.ExitFailed)
	for _, w := range []string{unlisted(ciRun, "CI"), unlisted(docsRun, "Docs")} {
		if n := strings.Count(stderr, w); n != 1 {
			t.Errorf("stderr = %q, want %q once, got %d", stderr, w, n)
		}
	}
	if strings.Contains(stderr, "no runs found") {
		t.Errorf("stderr = %q, want no \"no runs found\"", stderr)
	}
	checkRow(t, stdout, "CI", "completed", "failure")
	checkRow(t, stdout, "Docs", "completed", "success")
}

// TestRerunWatchOtherRunVanishes covers AC-6: a non-rerun run that drops out
// while another run's rerun is waited for gets the plain warning.
func TestRerunWatchOtherRunVanishes(t *testing.T) {
	opts := fakeOpts{listRaw: lists(list(failed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
		list(attemptRun(ciRun, "CI", "in_progress", "", 2)),
		list(passed(ciRun, "CI", 2)),
	)}
	stdout, stderr, _ := runVanish(t, opts, cli.ExitOK, 30, "--rerun-failed")
	if n := strings.Count(stderr, unlisted(docsRun, "Docs")); n != 1 {
		t.Errorf("stderr = %q, want the plain warning for Docs once, got %d", stderr, n)
	}
	if strings.Contains(stderr, "not waiting") {
		t.Errorf("stderr = %q, want no \"not waiting\" warning", stderr)
	}
	checkRow(t, stdout, "CI", "completed", "success")
	checkRow(t, stdout, "Docs", "completed", "success")
}

// TestRerunAfterWatchVanished covers AC-7: a failed run that dropped out
// during the first watch gets one warning in total.
func TestRerunAfterWatchVanished(t *testing.T) {
	opts := fakeOpts{listRaw: []string{
		list(failed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
		list(passed(docsRun, "Docs", 1)),
	}}
	stdout, stderr, _ := runVanish(t, opts, cli.ExitFailed, 30, "--rerun-failed")
	if n := strings.Count(stderr, "is no longer listed"); n != 1 {
		t.Errorf("stderr = %q, want one warning in total, got %d", stderr, n)
	}
	checkRow(t, stdout, "CI", "completed", "failure")
}
