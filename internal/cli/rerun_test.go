package cli_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/devvmhq/ciwatch/internal/cli"
)

// The runs of these tests are real ones of devvmhq/devvm (their jobs are
// recorded in testdata/gh/run-view-<id>.json), with the states the tests
// need: a rerun's in-between states (attempt 1 completed failure, attempt 2
// in progress) can't be recorded after the fact, so they are derived from
// the recorded attempt 2 runs.
const (
	ciRun   = "44858310690" // CI, attempt 2 success is recorded in run-list-rerun-attempt2.json
	ciRun2  = "43529187615" // CI, attempt 2 failure is recorded in run-list-rerun-attempt2-failure.json
	docsRun = "44951284032" // Docs
	fuzzRn  = "45017063720" // Fuzz
)

// attemptRun is a run in the shape gh run list prints it.
func attemptRun(id, workflow, status, conclusion string, attempt int) string {
	return fmt.Sprintf(`{"attempt":%d,"conclusion":%q,"createdAt":"2026-11-21T02:53:41Z","databaseId":%s,"status":%q,"url":"u","workflowName":%q}`,
		attempt, conclusion, id, status, workflow)
}

func failed(id, workflow string, attempt int) string {
	return attemptRun(id, workflow, "completed", "failure", attempt)
}

func passed(id, workflow string, attempt int) string {
	return attemptRun(id, workflow, "completed", "success", attempt)
}

// lists is what gh run list prints: the first list for as long as the first
// watch takes (the commit is old, so it waits out the grace period: seven
// lists), then each of the rest once, the last one repeated.
func lists(first string, rest ...string) []string {
	return append(slices.Repeat([]string{first}, 7), rest...)
}

// runRerun runs ciwatch and returns what it printed.
func runRerun(t *testing.T, opts fakeOpts, wantCode int, args ...string) (stdout, stderr string, calls *counts) {
	t.Helper()
	app, calls := fakeWith(t, "", nil, nil, opts)
	var out, errOut bytes.Buffer
	if code := app.Run(t.Context(), append(args, "HEAD"), &out, &errOut); code != wantCode {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, wantCode, errOut.String())
	}
	return out.String(), errOut.String(), calls
}

// TestFailedRunHintsRerun covers AC-1.
func TestFailedRunHintsRerun(t *testing.T) {
	const crashHint = "ciwatch: 1 fuzz crash input not fetched; to fetch it: ciwatch --fetch-crashes " + sha
	oneFailed := "ciwatch: 1 run failed; to rerun its failed jobs: ciwatch --rerun-failed " + sha
	twoFailed := "ciwatch: 2 runs failed; to rerun their failed jobs: ciwatch --rerun-failed " + sha
	for _, tt := range []struct {
		name string
		opts fakeOpts
		want string
		then string // the line before the hint
	}{
		{"one failed run", fakeOpts{listFixtures: []string{"run-list-failure.json"}}, oneFailed, ""},
		{"after the crash hint", fakeOpts{listFixtures: []string{"run-list-failure.json"}, artifacts: map[string]string{fuzzRun: "api-artifacts-44729305066.json"}}, oneFailed, crashHint},
		{"timed out", fakeOpts{listFixtures: []string{"run-list-timed-out.json"}, viewFixture: map[string]string{failedRun: "run-view-44707997310-timed-out.json"}}, oneFailed, ""},
		{"two failed runs", fakeOpts{listRaw: []string{list(failed(ciRun, "CI", 1), failed(docsRun, "Docs", 1))}}, twoFailed, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, stderr, calls := runRerun(t, tt.opts, cli.ExitFailed)
			if got := lastLine(stderr); got != tt.want {
				t.Errorf("last stderr line = %q, want %q (stderr %q)", got, tt.want, stderr)
			}
			if tt.then != "" && !strings.Contains(stderr, tt.then+"\n"+tt.want+"\n") {
				t.Errorf("stderr = %q, want the crash hint right before the rerun hint", stderr)
			}
			if len(calls.reruns) != 0 {
				t.Errorf("gh run rerun calls for %q, want none", calls.reruns)
			}
		})
	}
}

// TestRerunFailedWatchesNewAttempt covers AC-2 and AC-5.
func TestRerunFailedWatchesNewAttempt(t *testing.T) {
	opts := fakeOpts{listRaw: lists(list(failed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
		// The rerun is accepted, but gh still lists attempt 1, then attempt 2 queued.
		list(failed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
		list(attemptRun(ciRun, "CI", "in_progress", "", 2), passed(docsRun, "Docs", 1)),
		list(passed(ciRun, "CI", 2), passed(docsRun, "Docs", 1)),
	)}
	stdout, stderr, calls := runRerun(t, opts, cli.ExitOK, "--rerun-failed")
	if want := []string{ciRun}; !slices.Equal(calls.reruns, want) {
		t.Fatalf("gh run rerun calls for %q, want %q", calls.reruns, want)
	}
	if want := []int{7}; !slices.Equal(calls.rerunAfterLists, want) {
		t.Errorf("the rerun came after %v gh run list calls, want %v: straight after the first watch", calls.rerunAfterLists, want)
	}
	if calls.list != 10 {
		t.Errorf("gh run list calls = %d, want 10: three more polls after the rerun", calls.list)
	}
	// The jobs are fetched once, from the new attempt, after the last list.
	want := append(append(slices.Repeat([]string{"list"}, 7), "rerun"), slices.Repeat([]string{"list"}, 3)...)
	want = append(want, "view", "view")
	if !reflect.DeepEqual(calls.events, want) {
		t.Errorf("gh calls = %q, want %q", calls.events, want)
	}
	checkTable(t, stdout, slices.Concat(
		[]row{runRow("CI", "completed", "success")}, jobRowsOf(t, "run-view-"+ciRun+".json"),
		[]row{runRow("Docs", "completed", "success")}, jobRowsOf(t, "run-view-"+docsRun+".json"),
	))
	for _, line := range []string{
		"ciwatch: rerunning the failed jobs of run " + ciRun + " (CI)\n",
		"ciwatch: run " + ciRun + " (CI): in_progress (attempt 2)\n",
		"ciwatch: run " + ciRun + " (CI): completed success (attempt 2)\n",
	} {
		if !strings.Contains(stderr, line) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, line)
		}
	}
	if strings.Contains(stderr, "--rerun-failed") {
		t.Errorf("stderr = %q, want no rerun hint once the new attempt passed", stderr)
	}
	if i := strings.Index(stderr, "rerunning"); strings.Contains(stderr[:i], "attempt") {
		t.Errorf("stderr before the rerun = %q, want no attempt noted for attempt 1", stderr[:i])
	}
}

// TestRerunFailedNewAttemptFails covers AC-2 and AC-5: a second failure is
// reported with exit code 1 and is not rerun again.
func TestRerunFailedNewAttemptFails(t *testing.T) {
	opts := fakeOpts{
		listRaw: lists(list(failed(ciRun2, "CI", 1)),
			list(failed(ciRun2, "CI", 1)),
			list(attemptRun(ciRun2, "CI", "queued", "", 2)),
			list(failed(ciRun2, "CI", 2)),
		),
	}
	stdout, stderr, calls := runRerun(t, opts, cli.ExitFailed, "--rerun-failed")
	if want := []string{ciRun2}; !slices.Equal(calls.reruns, want) {
		t.Errorf("gh run rerun calls for %q, want %q", calls.reruns, want)
	}
	if calls.list != 10 {
		t.Errorf("gh run list calls = %d, want 10", calls.list)
	}
	checkTable(t, stdout, slices.Concat([]row{runRow("CI", "completed", "failure")}, jobRowsOf(t, "run-view-"+ciRun2+".json")))
	if want := "ciwatch: run " + ciRun2 + " (CI): completed failure (attempt 2)\n"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
}

// TestRerunFailedRerunsEachRunOnce covers AC-5: a run first seen failing
// while the new attempts are awaited is not rerun, and nor is a second failure.
func TestRerunFailedRerunsEachRunOnce(t *testing.T) {
	opts := fakeOpts{listRaw: lists(list(failed(ciRun2, "CI", 1), passed(docsRun, "Docs", 1)),
		list(failed(ciRun2, "CI", 1), passed(docsRun, "Docs", 1)),
		list(attemptRun(ciRun2, "CI", "in_progress", "", 2), passed(docsRun, "Docs", 1), attemptRun(fuzzRn, "Fuzz", "in_progress", "", 1)),
		list(failed(ciRun2, "CI", 2), passed(docsRun, "Docs", 1), failed(fuzzRn, "Fuzz", 1)),
	)}
	_, stderr, calls := runRerun(t, opts, cli.ExitFailed, "--rerun-failed")
	if want := []string{ciRun2}; !slices.Equal(calls.reruns, want) {
		t.Errorf("gh run rerun calls for %q, want only the first failure, once: %q", calls.reruns, want)
	}
	if calls.list != 10 {
		t.Errorf("gh run list calls = %d, want 10: it stops once every run is completed, with no further wait", calls.list)
	}
	if n := strings.Count(stderr, "rerunning the failed jobs"); n != 1 {
		t.Errorf("stderr = %q, want one rerunning line, got %d", stderr, n)
	}
}

// TestRerunFailedSkipsPassedAndCancelled covers AC-3.
func TestRerunFailedSkipsPassedAndCancelled(t *testing.T) {
	skipped := fakeOpts{listRaw: []string{list(passed(ciRun, "CI", 1), attemptRun(docsRun, "Docs", "completed", "skipped", 1))}}
	for _, tt := range []struct {
		name string
		opts func() fakeOpts
		code int
	}{
		{"success", func() fakeOpts { return fakeOpts{listFixtures: []string{"run-list-success.json"}} }, cli.ExitOK},
		{"cancelled", func() fakeOpts { return fakeOpts{listFixtures: []string{"run-list-cancelled.json"}} }, cli.ExitCancelled},
		{"skipped", func() fakeOpts { return skipped }, cli.ExitOK},
		{"in progress, then success", func() fakeOpts {
			return fakeOpts{listFixtures: []string{"run-list-in-progress.json", "run-list-in-progress.json", "run-list-success.json"}}
		}, cli.ExitOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wantOut, wantErr, want := runRerun(t, tt.opts(), tt.code)
			gotOut, gotErr, got := runRerun(t, tt.opts(), tt.code, "--rerun-failed")
			if len(got.reruns) != 0 {
				t.Errorf("gh run rerun calls for %q, want none", got.reruns)
			}
			if got.list != want.list || got.view != want.view || !slices.Equal(got.events, want.events) || !slices.Equal(got.clock.waits, want.clock.waits) {
				t.Errorf("with the flag: %d lists, %d views, waits %v; without: %d, %d, %v: want the same",
					got.list, got.view, got.clock.waits, want.list, want.view, want.clock.waits)
			}
			if gotOut != wantOut || gotErr != wantErr {
				t.Errorf("with the flag: stdout %q, stderr %q; without: %q, %q: want the same", gotOut, gotErr, wantOut, wantErr)
			}
		})
	}
}

// TestRerunFailedCallFails covers AC-4.
func TestRerunFailedCallFails(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  func(t *testing.T) error
	}{
		{"refused", func(*testing.T) error { return errors.New("gh: exit status 1: run " + ciRun + " cannot be rerun") }},
		{"network", networkErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rerr := tt.err(t)
			opts := fakeOpts{
				rerunErr: map[string]error{ciRun: rerr},
				listRaw: lists(list(failed(ciRun, "CI", 1), failed(fuzzRn, "Fuzz", 1)),
					// Only the Fuzz run is waited for; the CI run stays at attempt 1.
					list(failed(ciRun, "CI", 1), failed(fuzzRn, "Fuzz", 1)),
					list(failed(ciRun, "CI", 1), attemptRun(fuzzRn, "Fuzz", "in_progress", "", 2)),
					list(failed(ciRun, "CI", 1), passed(fuzzRn, "Fuzz", 2)),
				),
			}
			stdout, stderr, calls := runRerun(t, opts, cli.ExitFailed, "--rerun-failed")
			if want := []string{ciRun, fuzzRn}; !slices.Equal(calls.reruns, want) {
				t.Errorf("gh run rerun calls for %q, want one each, with no retry: %q", calls.reruns, want)
			}
			if want := "ciwatch: can't rerun the failed jobs of run " + ciRun + " (CI): " + rerr.Error() + "\n"; !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, want)
			}
			if want := "ciwatch: rerunning the failed jobs of run " + fuzzRn + " (Fuzz)\n"; !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, want)
			}
			if calls.list != 10 {
				t.Errorf("gh run list calls = %d, want 10: a wait for the Fuzz run's new attempt only", calls.list)
			}
			if strings.Contains(stderr, "rerun after all") {
				t.Errorf("stderr = %q, want no rerun-after-all line (AC-5)", stderr)
			}
			for _, d := range calls.clock.waits {
				if d != testInterval {
					t.Errorf("waits = %v, want only poll intervals: a failed rerun call isn't retried after a backoff", calls.clock.waits)
					break
				}
			}
			checkTable(t, stdout, slices.Concat(
				[]row{runRow("CI", "completed", "failure")}, jobRowsOf(t, "run-view-"+ciRun+".json"),
				[]row{runRow("Fuzz", "completed", "success")}, jobRowsOf(t, "run-view-"+fuzzRn+".json"),
			))
		})
	}
}

// TestRerunFailedCallFailsOnEveryRun covers AC-5: with every call refused
// there is no new attempt to wait for, but one extra list checks for one.
func TestRerunFailedCallFailsOnEveryRun(t *testing.T) {
	opts := fakeOpts{
		rerunErr: map[string]error{ciRun: errors.New("gh: exit status 1: run cannot be rerun")},
		listRaw:  lists(list(failed(ciRun, "CI", 1))),
	}
	_, stderr, calls := runRerun(t, opts, cli.ExitFailed, "--rerun-failed")
	if calls.list != 8 {
		t.Errorf("gh run list calls = %d, want 8: one extra list after the failed rerun call", calls.list)
	}
	if !strings.Contains(stderr, "ciwatch: can't rerun the failed jobs of run "+ciRun+" (CI): ") {
		t.Errorf("stderr = %q, want the warning", stderr)
	}
	if strings.Contains(stderr, "rerun after all") {
		t.Errorf("stderr = %q, want no rerun-after-all line", stderr)
	}
}

// afterAll is the line for a run whose rerun call failed but that was rerun.
func afterAll(id, workflow string, attempt int) string {
	return fmt.Sprintf("ciwatch: run %s (%s) was rerun after all (attempt %d)\n", id, workflow, attempt)
}

// TestRerunCallFailsRerunAfterAll covers AC-1 and AC-2: the rerun call
// failed, a later list shows attempt 2, which is announced once and waited for.
func TestRerunCallFailsRerunAfterAll(t *testing.T) {
	rerr := errors.New("gh: exit status 1: connection reset by peer")
	opts := fakeOpts{
		rerunErr: map[string]error{ciRun: rerr},
		listRaw: lists(list(failed(ciRun, "CI", 1)),
			list(attemptRun(ciRun, "CI", "in_progress", "", 2)),
			list(attemptRun(ciRun, "CI", "in_progress", "", 2)),
			list(passed(ciRun, "CI", 2)),
		),
	}
	stdout, stderr, calls := runRerun(t, opts, cli.ExitOK, "--rerun-failed")
	warn := "ciwatch: can't rerun the failed jobs of run " + ciRun + " (CI): " + rerr.Error() + "\n"
	line := afterAll(ciRun, "CI", 2)
	if n := strings.Count(stderr, line); n != 1 {
		t.Errorf("stderr = %q, want %q once, got %d", stderr, line, n)
	}
	w, l := strings.Index(stderr, warn), strings.Index(stderr, line)
	if w < 0 || l < w {
		t.Errorf("stderr = %q, want the warning, then the rerun-after-all line", stderr)
	}
	if got := calls.attempt[ciRun]; got != "2" {
		t.Errorf("gh run view --attempt = %q, want 2", got)
	}
	if calls.list != 10 {
		t.Errorf("gh run list calls = %d, want 10: it waits until attempt 2 completes", calls.list)
	}
	checkTable(t, stdout, slices.Concat([]row{runRow("CI", "completed", "success")}, jobRowsOf(t, "run-view-"+ciRun+".json")))
}

// TestRerunCallFailsRerunAfterAllFails covers AC-3: attempt 2 fails, and its
// jobs and log are reported.
func TestRerunCallFailsRerunAfterAllFails(t *testing.T) {
	opts := fakeOpts{
		rerunErr:    map[string]error{failedRun: errors.New("gh: exit status 1: connection reset by peer")},
		viewFixture: map[string]string{failedRun: "run-view-44707997310-test-failure.json"},
		logFixture:  map[string]string{failedJob: "gotestsum-failures.log"},
		latestOnly:  map[string]bool{failedRun: true},
		listRaw: lists(list(failed(failedRun, "CI", 1)),
			list(attemptRun(failedRun, "CI", "in_progress", "", 2)),
			list(failed(failedRun, "CI", 2)),
		),
	}
	stdout, stderr, calls := runRerun(t, opts, cli.ExitFailed, "--rerun-failed", "--go-tests")
	if n := strings.Count(stderr, afterAll(failedRun, "CI", 2)); n != 1 {
		t.Errorf("stderr = %q, want the rerun-after-all line once, got %d", stderr, n)
	}
	if got := calls.attempt[failedRun]; got != "2" {
		t.Errorf("gh run view --attempt for the jobs = %q, want 2", got)
	}
	if got := calls.logAttempt[failedJob]; got != "2" {
		t.Errorf("gh run view --attempt for the log = %q, want 2", got)
	}
	checkRow(t, stdout, "CI", "completed", "failure")
}

// TestRerunCallFailsMixedRerunAfterAll covers AC-4: CI's call fails and
// Fuzz's is accepted; while Fuzz's attempt 2 is awaited, CI shows attempt 2.
func TestRerunCallFailsMixedRerunAfterAll(t *testing.T) {
	opts := fakeOpts{
		rerunErr: map[string]error{ciRun: errors.New("gh: exit status 1: connection reset by peer")},
		listRaw: lists(list(failed(ciRun, "CI", 1), failed(fuzzRn, "Fuzz", 1)),
			list(failed(ciRun, "CI", 1), attemptRun(fuzzRn, "Fuzz", "in_progress", "", 2)),
			list(attemptRun(ciRun, "CI", "in_progress", "", 2), attemptRun(fuzzRn, "Fuzz", "in_progress", "", 2)),
			list(attemptRun(ciRun, "CI", "in_progress", "", 2), passed(fuzzRn, "Fuzz", 2)),
			list(passed(ciRun, "CI", 2), passed(fuzzRn, "Fuzz", 2)),
		),
	}
	stdout, stderr, calls := runRerun(t, opts, cli.ExitOK, "--rerun-failed")
	if n := strings.Count(stderr, afterAll(ciRun, "CI", 2)); n != 1 {
		t.Errorf("stderr = %q, want the line for CI once, got %d", stderr, n)
	}
	if strings.Contains(stderr, "(Fuzz) was rerun after all") {
		t.Errorf("stderr = %q, want no line for Fuzz", stderr)
	}
	for _, run := range []string{ciRun, fuzzRn} {
		if got := calls.attempt[run]; got != "2" {
			t.Errorf("gh run view --attempt for run %s = %q, want 2", run, got)
		}
	}
	checkRow(t, stdout, "CI", "completed", "success")
	checkRow(t, stdout, "Fuzz", "completed", "success")
}

// TestRerunAfterAllVanishes covers AC-6: a run detected as rerun after all
// that stops being listed is reported at the last state seen.
func TestRerunAfterAllVanishes(t *testing.T) {
	opts := fakeOpts{
		rerunErr: map[string]error{ciRun: errors.New("gh: exit status 1: connection reset by peer")},
		listRaw: lists(list(failed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
			list(attemptRun(ciRun, "CI", "in_progress", "", 2), passed(docsRun, "Docs", 1)),
			list(passed(docsRun, "Docs", 1)),
		),
	}
	stdout, stderr, _ := runVanish(t, opts, cli.ExitUnchecked, 30, "--rerun-failed")
	if n := strings.Count(stderr, afterAll(ciRun, "CI", 2)); n != 1 {
		t.Errorf("stderr = %q, want the rerun-after-all line once, got %d", stderr, n)
	}
	if n := strings.Count(stderr, unlistedCI); n != 1 {
		t.Errorf("stderr = %q, want the no-longer-listed warning once, got %d", stderr, n)
	}
	checkRow(t, stdout, "CI", "in_progress")
}

// TestRerunCallFailsRunVanishesAtExtraList covers a failed-call run that the
// extra list doesn't have: it is warned about and reported as it was, failed.
func TestRerunCallFailsRunVanishesAtExtraList(t *testing.T) {
	opts := fakeOpts{
		rerunErr: map[string]error{ciRun: errors.New("gh: exit status 1: run cannot be rerun")},
		listRaw: lists(list(failed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
			list(passed(docsRun, "Docs", 1)),
		),
	}
	stdout, stderr, _ := runRerun(t, opts, cli.ExitFailed, "--rerun-failed")
	if n := strings.Count(stderr, unlistedCI); n != 1 {
		t.Errorf("stderr = %q, want the no-longer-listed warning once, got %d", stderr, n)
	}
	checkRow(t, stdout, "CI", "completed", "failure")
	checkRow(t, stdout, "Docs", "completed", "success")
}

// TestRerunCallFailsExtraListFails covers AC-7: when the extra list fails, a
// warning names the error and the original attempt is reported, exit 1.
func TestRerunCallFailsExtraListFails(t *testing.T) {
	network := map[int]error{}
	for i := 8; i <= 16; i++ { // the call and its 8 retries
		network[i] = networkErr(t)
	}
	for _, tt := range []struct {
		name string
		errs map[int]error
		want string
	}{
		{"HTTP error", map[int]error{8: errors.New("HTTP 401")}, "HTTP 401"},
		{"network error after retries", network, "gave up after 8 retries"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := fakeOpts{
				rerunErr: map[string]error{ciRun: errors.New("gh: exit status 1: run cannot be rerun")},
				listErr:  tt.errs,
				listRaw:  lists(list(failed(ciRun, "CI", 1))),
			}
			stdout, stderr, _ := runRerun(t, opts, cli.ExitFailed, "--rerun-failed")
			if !strings.Contains(stderr, tt.want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tt.want)
			}
			checkRow(t, stdout, "CI", "completed", "failure")
		})
	}
}

// TestRerunCallFailsExtraListCtrlC covers AC-7: Ctrl-C during the extra list
// exits 4 with context canceled.
func TestRerunCallFailsExtraListCtrlC(t *testing.T) {
	opts := fakeOpts{
		rerunErr: map[string]error{ciRun: errors.New("gh: exit status 1: run cannot be rerun")},
		listErr:  map[int]error{8: networkErr(t)},
		listRaw:  lists(list(failed(ciRun, "CI", 1))),
	}
	// Six waits for the first watch, then the extra list's retry wait: Ctrl-C.
	_, stderr, _ := runVanish(t, opts, cli.ExitUnchecked, 7, "--rerun-failed")
	if !strings.Contains(stderr, "context canceled\n") {
		t.Errorf("stderr = %q, want context canceled", stderr)
	}
}

// TestRerunFailedWithOnceIsUsage covers AC-6.
func TestRerunFailedWithOnceIsUsage(t *testing.T) {
	for _, args := range [][]string{{"--rerun-failed", "--once", "HEAD"}, {"--once", "--rerun-failed", "HEAD"}} {
		app, calls := fake(t, "run-list-failure.json", nil, nil)
		var stdout, stderr bytes.Buffer
		if code := app.Run(t.Context(), args, &stdout, &stderr); code != cli.ExitUsage {
			t.Errorf("%q: exit code = %d, want %d", args, code, cli.ExitUsage)
		}
		if want := "ciwatch: --rerun-failed waits for the new attempts, so it can't be used with --once\nusage: ciwatch"; !strings.HasPrefix(stderr.String(), want) {
			t.Errorf("%q: stderr = %q, want it to start with %q", args, stderr.String(), want)
		}
		if !strings.Contains(stderr.String(), "--rerun-failed") || stdout.Len() != 0 {
			t.Errorf("%q: stdout = %q, stderr = %q, want the usage on stderr and nothing on stdout", args, stdout.String(), stderr.String())
		}
		if len(calls.events) != 0 || len(calls.reruns) != 0 {
			t.Errorf("%q: gh calls = %q, want none", args, calls.events)
		}
	}
}

// TestUsageNamesRerunFailed covers AC-1: the flag is in the usage.
func TestUsageNamesRerunFailed(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cli.App{}.Run(t.Context(), nil, &stdout, &stderr)
	if want := "usage: ciwatch [--json] [--once] [--go-tests] [--fetch-crashes] [--rerun-failed] <commit>"; !strings.HasPrefix(stderr.String(), want) {
		t.Errorf("stderr = %q, want it to start with %q", stderr.String(), want)
	}
}

// TestJSONGoldenAfterRerun covers AC-2: runs[].attempt is the attempt that
// was reported, and the output matches the golden file.
func TestJSONGoldenAfterRerun(t *testing.T) {
	app, _ := fakeWith(t, "", nil, nil, fakeOpts{listFixtures: []string{"run-list-rerun-attempt2.json"}})
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--json", "HEAD"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitOK, stderr.String())
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "run-list-rerun-attempt2.json"))
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != string(want) {
		t.Errorf("output differs from the golden file:\n got: %s\nwant: %s", stdout.String(), want)
	}
}

// runVanish is runRerun under a context that is cancelled at the cancelAt-th
// wait of the fake clock, so a watch that never ends fails instead of hanging.
func runVanish(t *testing.T, opts fakeOpts, wantCode, cancelAt int, args ...string) (stdout, stderr string, calls *counts) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	waits := 0
	opts.onWait = func() {
		if waits++; waits == cancelAt {
			cancel()
		}
	}
	app, calls := fakeWith(t, "", nil, nil, opts)
	var out, errOut bytes.Buffer
	if code := app.Run(ctx, append(args, "HEAD"), &out, &errOut); code != wantCode {
		t.Fatalf("exit code = %d, want %d (stdout %q, stderr %q)", code, wantCode, out.String(), errOut.String())
	}
	return out.String(), errOut.String(), calls
}

const unlistedCI = "ciwatch: run " + ciRun + " (CI) is no longer listed for this commit; not waiting for it\n"

// rowFields is the fields of the table row of a workflow, nil if absent.
func rowFields(stdout, workflow string) []string {
	for _, l := range strings.Split(stdout, "\n") {
		if f := strings.Fields(l); len(f) > 0 && !strings.HasPrefix(l, " ") && f[0] == workflow {
			return f
		}
	}
	return nil
}

func checkRow(t *testing.T, stdout, workflow string, want ...string) {
	t.Helper()
	if got := rowFields(stdout, workflow); !slices.Equal(got, append([]string{workflow}, want...)) {
		t.Errorf("%s row = %q, want %q (stdout %q)", workflow, got, want, stdout)
	}
}

// TestRerunVanishesBeforeNewAttempt covers AC-1 and AC-2.
func TestRerunVanishesBeforeNewAttempt(t *testing.T) {
	opts := fakeOpts{listRaw: lists(list(failed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
		list(failed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
		list(passed(docsRun, "Docs", 1)),
	)}
	stdout, stderr, calls := runVanish(t, opts, cli.ExitFailed, 30, "--rerun-failed")
	if n := strings.Count(stderr, unlistedCI); n != 1 {
		t.Errorf("stderr = %q, want the warning once, got %d", stderr, n)
	}
	if calls.list != 9 {
		t.Errorf("gh run list calls = %d, want 9: it stops at the first poll without the run", calls.list)
	}
	checkTable(t, stdout, slices.Concat(
		[]row{runRow("CI", "completed", "failure")}, jobRowsOf(t, "run-view-"+ciRun+".json"),
		[]row{runRow("Docs", "completed", "success")}, jobRowsOf(t, "run-view-"+docsRun+".json"),
	))
	if strings.Contains(stderr, "--rerun-failed") {
		t.Errorf("stderr = %q, want no rerun hint", stderr)
	}
	// GitHub holds attempt 2 of the missing run by now, so its jobs are asked
	// for by attempt, and so is the listed run's (AC-1, AC-5).
	for _, run := range []string{ciRun, docsRun} {
		if got := calls.attempt[run]; got != "1" {
			t.Errorf("gh run view --attempt for run %s = %q, want 1", run, got)
		}
	}
}

// TestRerunVanishedRunLogByAttempt covers AC-5: the log of a run that stopped
// being listed after a rerun is fetched by its last known attempt.
func TestRerunVanishedRunLogByAttempt(t *testing.T) {
	opts := fakeOpts{
		viewFixture: map[string]string{failedRun: "run-view-44707997310-test-failure.json"},
		logFixture:  map[string]string{failedJob: "gotestsum-failures.log"},
		latestOnly:  map[string]bool{failedRun: true},
		listRaw: lists(list(failed(failedRun, "CI", 1), passed(docsRun, "Docs", 1)),
			list(failed(failedRun, "CI", 1), passed(docsRun, "Docs", 1)),
			list(passed(docsRun, "Docs", 1)),
		),
	}
	_, stderr, calls := runVanish(t, opts, cli.ExitFailed, 30, "--rerun-failed", "--go-tests")
	if got := calls.logAttempt[failedJob]; got != "1" {
		t.Errorf("gh run view --attempt for the log of the missing run's job = %q, want 1 (stderr %q)", got, stderr)
	}
}

// TestRerunCallFailsReportsListedAttempt covers AC-1 and AC-2: the rerun call
// fails, but GitHub holds attempt 2, in progress. The report is of the listed
// attempt 1: its jobs and the logs of its failed jobs.
func TestRerunCallFailsReportsListedAttempt(t *testing.T) {
	rerr := errors.New("gh: exit status 1: connection reset by peer")
	opts := fakeOpts{
		rerunErr:    map[string]error{failedRun: rerr},
		viewFixture: map[string]string{failedRun: "run-view-44707997310-test-failure.json"},
		logFixture:  map[string]string{failedJob: "gotestsum-failures.log"},
		latestOnly:  map[string]bool{failedRun: true},
		listRaw:     lists(list(failed(failedRun, "CI", 1), passed(docsRun, "Docs", 1))),
	}
	stdout, stderr, calls := runRerun(t, opts, cli.ExitFailed, "--rerun-failed", "--go-tests")
	if strings.Contains(stderr, "rerun after all") {
		t.Errorf("stderr = %q, want no rerun-after-all line (AC-5)", stderr)
	}
	for _, run := range []string{failedRun, docsRun} {
		if got := calls.attempt[run]; got != "1" {
			t.Errorf("gh run view --attempt for run %s = %q, want 1", run, got)
		}
	}
	if got := calls.logAttempt[failedJob]; got != "1" {
		t.Errorf("gh run view --attempt for the log of job %s = %q, want 1", failedJob, got)
	}
	table, _, _ := strings.Cut(stdout, "\n\n")
	checkTable(t, table+"\n", slices.Concat(
		[]row{runRow("CI", "completed", "failure")}, jobRowsOf(t, "run-view-44707997310-test-failure.json"),
		[]row{runRow("Docs", "completed", "success")}, jobRowsOf(t, "run-view-"+docsRun+".json"),
	))
	if !strings.Contains(stdout, "FAIL internal/vm TestAgentImportFailsWhole") {
		t.Errorf("stdout = %q, want the failing test of attempt 1's log", stdout)
	}
	if strings.Contains(stderr, "can't fetch the log") {
		t.Errorf("stderr = %q, want no warning that the log can't be fetched", stderr)
	}
}

// TestJobAndLogRetriesKeepAttempt covers AC-6: every retry of the jobs call
// and of the log call carries the same --attempt.
func TestJobAndLogRetriesKeepAttempt(t *testing.T) {
	opts := fakeOpts{
		viewFixture:  map[string]string{failedRun: "run-view-44707997310-test-failure.json"},
		logFixture:   map[string]string{failedJob: "gotestsum-failures.log"},
		viewErrCalls: map[string]int{failedRun: 2},
		logErrCalls:  map[string]int{failedJob: 2},
		listRaw:      []string{list(failed(failedRun, "CI", 1))},
	}
	_, stderr, calls := runRerun(t, opts, cli.ExitFailed, "--go-tests")
	if calls.view != 3 || calls.logs != 3 {
		t.Fatalf("jobs calls = %d, log calls = %d, want 3 each (stderr %q)", calls.view, calls.logs, stderr)
	}
	if want := []string{"1", "1", "1"}; !slices.Equal(calls.jobsCallAttempts, want) || !slices.Equal(calls.logCallAttempts, want) {
		t.Errorf("--attempt of the jobs calls = %q, of the log calls = %q, want %q on both", calls.jobsCallAttempts, calls.logCallAttempts, want)
	}
}

// TestRerunVanishesInProgress covers AC-3.
func TestRerunVanishesInProgress(t *testing.T) {
	opts := fakeOpts{listRaw: lists(list(failed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
		list(attemptRun(ciRun, "CI", "in_progress", "", 2), passed(docsRun, "Docs", 1)),
		list(passed(docsRun, "Docs", 1)),
	)}
	stdout, stderr, _ := runVanish(t, opts, cli.ExitUnchecked, 30, "--rerun-failed")
	if !strings.Contains(stderr, unlistedCI) {
		t.Errorf("stderr = %q, want the warning", stderr)
	}
	if strings.Contains(stderr, "drop --once") {
		t.Errorf("stderr = %q, want no advice to drop --once", stderr)
	}
	checkRow(t, stdout, "CI", "in_progress")
}

// TestRerunVanishesOtherStillWaited covers AC-4.
func TestRerunVanishesOtherStillWaited(t *testing.T) {
	opts := fakeOpts{listRaw: lists(list(failed(ciRun2, "CI", 1), failed(docsRun, "Docs", 1)),
		list(failed(ciRun2, "CI", 1), failed(docsRun, "Docs", 1)),
		list(attemptRun(docsRun, "Docs", "in_progress", "", 2)),
		list(passed(docsRun, "Docs", 2)),
	)}
	stdout, stderr, calls := runVanish(t, opts, cli.ExitFailed, 30, "--rerun-failed")
	if want := []string{ciRun2, docsRun}; !slices.Equal(calls.reruns, want) {
		t.Fatalf("gh run rerun calls for %q, want %q", calls.reruns, want)
	}
	if want := "ciwatch: run " + ciRun2 + " (CI) is no longer listed for this commit; not waiting for it\n"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
	if calls.list != 10 {
		t.Errorf("gh run list calls = %d, want 10: it waits for the Docs run's new attempt", calls.list)
	}
	checkRow(t, stdout, "CI", "completed", "failure")
	checkRow(t, stdout, "Docs", "completed", "success")
}

// TestRerunReappears covers AC-5.
func TestRerunReappears(t *testing.T) {
	opts := fakeOpts{listRaw: lists(list(failed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
		list(failed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
		list(passed(docsRun, "Docs", 1), attemptRun(fuzzRn, "Fuzz", "in_progress", "", 1)),
		list(attemptRun(ciRun, "CI", "in_progress", "", 2), passed(docsRun, "Docs", 1), attemptRun(fuzzRn, "Fuzz", "in_progress", "", 1)),
		list(passed(ciRun, "CI", 2), passed(docsRun, "Docs", 1), passed(fuzzRn, "Fuzz", 1)),
	)}
	stdout, stderr, calls := runVanish(t, opts, cli.ExitOK, 30, "--rerun-failed")
	if n := strings.Count(stderr, unlistedCI); n != 1 {
		t.Errorf("stderr = %q, want the warning once, got %d", stderr, n)
	}
	if calls.list != 11 {
		t.Errorf("gh run list calls = %d, want 11: it waits for the run's new attempt", calls.list)
	}
	checkRow(t, stdout, "CI", "completed", "success")
}

// TestRerunVanishesTwice covers AC-6.
func TestRerunVanishesTwice(t *testing.T) {
	opts := fakeOpts{listRaw: lists(list(failed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
		list(failed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
		list(passed(docsRun, "Docs", 1), attemptRun(fuzzRn, "Fuzz", "in_progress", "", 1)),
		list(attemptRun(ciRun, "CI", "in_progress", "", 2), passed(docsRun, "Docs", 1), attemptRun(fuzzRn, "Fuzz", "in_progress", "", 1)),
		list(passed(docsRun, "Docs", 1), attemptRun(fuzzRn, "Fuzz", "in_progress", "", 1)),
		list(passed(docsRun, "Docs", 1), passed(fuzzRn, "Fuzz", 1)),
	)}
	stdout, stderr, _ := runVanish(t, opts, cli.ExitUnchecked, 30, "--rerun-failed")
	if n := strings.Count(stderr, unlistedCI); n != 2 {
		t.Errorf("stderr = %q, want the warning twice, got %d", stderr, n)
	}
	checkRow(t, stdout, "CI", "in_progress")
}

// TestRerunVanishedCtrlC covers AC-7.
func TestRerunVanishedCtrlC(t *testing.T) {
	opts := fakeOpts{listRaw: lists(list(failed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
		list(failed(ciRun, "CI", 1), passed(docsRun, "Docs", 1)),
		list(passed(docsRun, "Docs", 1), attemptRun(fuzzRn, "Fuzz", "in_progress", "", 1)),
	)}
	// Six waits for the first watch, then one after the warning, then Ctrl-C.
	stdout, stderr, _ := runVanish(t, opts, cli.ExitUnchecked, 8, "--rerun-failed")
	if n := strings.Count(stderr, unlistedCI); n != 1 {
		t.Errorf("stderr = %q, want the warning once, got %d", stderr, n)
	}
	if !strings.Contains(stderr, "context canceled\n") || stdout != "" {
		t.Errorf("stdout = %q, stderr = %q, want context canceled and no table", stdout, stderr)
	}
}
