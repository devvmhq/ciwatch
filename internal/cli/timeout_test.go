package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/devvmhq/ciwatch/internal/cli"
)

// The recorded job of devvmhq/ciwatch that hit its timeout-minutes of 1m0s:
// the run, the job, the file of its full log, and the line ciwatch writes.
const (
	timeoutRun     = "45339081512"
	timeoutJob     = "138258094336"
	timeoutLog     = "job-45339081512-138258094336.log"
	timeoutList    = "run-list-timeout.json"
	timeoutStderr  = "ciwatch: job " + timeoutJob + " (slow) of run " + timeoutRun + " (Timeout) timed out after 1m0s"
	timeoutHeader  = "slow (Timeout, run " + timeoutRun + "): timed out after 1m0s"
	annotationFile = "annotations-138258094336.json"
)

// timeoutOpts is the fake gh for the recorded timeout: its annotations and its
// full log.
func timeoutOpts() fakeOpts {
	return fakeOpts{
		annotations: map[string]string{timeoutJob: annotationFile},
		jobLog:      map[string]string{timeoutJob: timeoutLog},
	}
}

// timeoutTable is what the table shows after the rows for the recorded
// timeout: the cancelled step's lines, colour codes removed.
const timeoutTable = `WORKFLOW  STATUS     CONCLUSION
Timeout   completed  cancelled
  slow    completed  cancelled

` + timeoutHeader + `
  Sleep past the limit:
    ##[group]Run echo "starting to sleep"
    echo "starting to sleep"
    sleep 150
    echo "never printed"
    shell: /usr/bin/bash -e {0}
    ##[endgroup]
    starting to sleep
    ##[error]The operation was canceled.
`

// TestTimeoutReportsTheJob covers AC-1 and AC-5: the recorded timeout is
// reported on stderr and in the table, from the full log of the job, and the
// exit code stays 3.
func TestTimeoutReportsTheJob(t *testing.T) {
	app, calls := fakeWith(t, timeoutList, nil, nil, timeoutOpts())
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	if !strings.Contains(stderr.String(), timeoutStderr+"\n") {
		t.Errorf("stderr = %q, want the line %q", stderr.String(), timeoutStderr)
	}
	if got := stdout.String(); got != timeoutTable {
		t.Errorf("stdout = %q, want %q", got, timeoutTable)
	}
	for _, unwanted := range []string{"before the slow step", "Complete job", "Cleaning up orphan processes"} {
		if strings.Contains(stdout.String(), unwanted) {
			t.Errorf("stdout = %q, want no %q", stdout.String(), unwanted)
		}
	}
	if want := []string{timeoutJob}; !slices.Equal(calls.annotationIDs, want) {
		t.Errorf("annotations fetched for jobs %v, want exactly %v", calls.annotationIDs, want)
	}
	if want := []string{timeoutJob}; !slices.Equal(calls.jobLogIDs, want) {
		t.Errorf("full logs fetched for jobs %v, want exactly %v", calls.jobLogIDs, want)
	}
	if calls.logs != 0 {
		t.Errorf("--log-failed calls = %d, want none: it prints nothing for a cancelled job", calls.logs)
	}
	if want := []string{"run", "view", timeoutRun, "--attempt", "1", "--log", "--job", timeoutJob}; len(calls.jobLogArgs) != 1 || !slices.Equal(calls.jobLogArgs[0], want) {
		t.Errorf("full log call = %q, want %q", calls.jobLogArgs, want)
	}
}

// TestTimeoutFindsTheStepWithoutStepLabels covers AC-1: the cancelled step is
// found by its times when no line carries its name, as when gh labels every
// line UNKNOWN STEP: the block is still shown and ends at the error line.
func TestTimeoutFindsTheStepWithoutStepLabels(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "logs", timeoutLog))
	if err != nil {
		t.Fatal(err)
	}
	var relabelled []byte
	for _, l := range bytes.SplitAfter(raw, []byte("\n")) {
		if parts := bytes.SplitN(l, []byte("\t"), 3); len(parts) == 3 {
			l = bytes.Join([][]byte{parts[0], []byte("UNKNOWN STEP"), parts[2]}, []byte("\t"))
		}
		relabelled = append(relabelled, l...)
	}
	opts := timeoutOpts()
	opts.jobLogBytes = map[string][]byte{timeoutJob: relabelled}
	app, _ := fakeWith(t, timeoutList, nil, nil, opts)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	got := stdout.String()
	if strings.Contains(got, "(no failed step in the log)") || !strings.Contains(got, timeoutHeader+"\n  Sleep past the limit:\n") {
		t.Errorf("stdout = %q, want the cancelled step's block", got)
	}
	if !strings.HasSuffix(got, "    starting to sleep\n    ##[error]The operation was canceled.\n") || strings.Contains(got, "Cleaning up orphan") {
		t.Errorf("stdout = %q, want the lines to end at the error line", got)
	}
}

// TestTimeoutJSONMatchesGolden covers AC-1: the job in --json has
// timed_out_after and its one step, and the output is the golden file.
func TestTimeoutJSONMatchesGolden(t *testing.T) {
	app, _ := fakeWith(t, timeoutList, nil, nil, timeoutOpts())
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--json", "HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "run-list-timeout.json"))
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != string(want) {
		t.Errorf("output differs from the golden file:\n got: %s\nwant: %s", stdout.String(), want)
	}
	app, _ = fakeWith(t, timeoutList, nil, nil, timeoutOpts())
	jobs, _ := jsonJobs(t, app, cli.ExitCancelled)
	if got := string(jobs[timeoutJob]["timed_out_after"]); got != `"1m0s"` {
		t.Errorf("timed_out_after = %s, want \"1m0s\"", got)
	}
	steps := jsonSteps(t, jobs[timeoutJob])
	if len(steps) != 1 || steps[0].Name != "Sleep past the limit" || steps[0].Omitted != 0 || len(steps[0].Lines) != 8 {
		t.Errorf("steps = %+v, want the one cancelled step with 8 lines and none omitted", steps)
	}
	if got := string(jobs[timeoutJob]["conclusion"]); got != `"cancelled"` {
		t.Errorf("conclusion = %s, want \"cancelled\", as GitHub says", got)
	}
}

// TestCancelledWithoutTimeoutAnnotationIsUnchanged covers AC-2: the
// concurrency cancel, whose recorded annotations have no timeout message, is
// reported as before: no block, no timed_out_after, no full log.
func TestCancelledWithoutTimeoutAnnotationIsUnchanged(t *testing.T) {
	const cancelledJob = "136192284970"
	opts := fakeOpts{annotations: map[string]string{cancelledJob: "annotations-136192284970.json"}}
	app, calls := fakeWith(t, "run-list-cancelled.json", nil, nil, opts)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	if !slices.Contains(calls.annotationIDs, cancelledJob) {
		t.Errorf("annotations fetched for jobs %v, want the cancelled job %s among them", calls.annotationIDs, cancelledJob)
	}
	if len(calls.jobLogIDs) != 0 || calls.logs != 0 {
		t.Errorf("full log calls = %v, --log-failed calls = %d, want none", calls.jobLogIDs, calls.logs)
	}
	if strings.Contains(stdout.String(), "timed out") || strings.Contains(stderr.String(), "timed out") {
		t.Errorf("stdout = %q, stderr = %q, want no timeout", stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "\n\n") {
		t.Errorf("stdout = %q, want the table without a block", stdout.String())
	}
	app, _ = fakeWith(t, "run-list-cancelled.json", nil, nil, opts)
	jobs, _ := jsonJobs(t, app, cli.ExitCancelled)
	for id, job := range jobs {
		if _, ok := job["timed_out_after"]; ok {
			t.Errorf("job %s has timed_out_after %s, want none", id, job["timed_out_after"])
		}
	}

	// Byte for byte as today: with no annotations at all (what ciwatch saw
	// before it asked for them), stdout, stderr and the JSON are the same as
	// with the recorded annotations, newer-run line included.
	base := func(args ...string) (string, string) {
		app, _ := fakeWith(t, "run-list-cancelled.json", nil, nil, fakeOpts{})
		var out, errOut bytes.Buffer
		if code := app.Run(t.Context(), args, &out, &errOut); code != cli.ExitCancelled {
			t.Fatalf("baseline exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, errOut.String())
		}
		return out.String(), errOut.String()
	}
	withAnn := func(args ...string) (string, string) {
		app, _ := fakeWith(t, "run-list-cancelled.json", nil, nil, opts)
		var out, errOut bytes.Buffer
		if code := app.Run(t.Context(), args, &out, &errOut); code != cli.ExitCancelled {
			t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, errOut.String())
		}
		return out.String(), errOut.String()
	}
	for _, args := range [][]string{{"HEAD"}, {"--json", "HEAD"}} {
		wantOut, wantErr := base(args...)
		gotOut, gotErr := withAnn(args...)
		if !strings.Contains(wantOut, "cancelled") {
			t.Fatalf("baseline stdout = %q, want the cancelled run", wantOut)
		}
		if gotOut != wantOut {
			t.Errorf("%v: stdout = %q, want the unchanged %q", args, gotOut, wantOut)
		}
		if gotErr != wantErr {
			t.Errorf("%v: stderr = %q, want the unchanged %q", args, gotErr, wantErr)
		}
	}
}

// TestAnnotationsFailureReportsTheJobAsToday covers AC-3: when the annotations
// call fails, for a reason retries can't cure or after the retries run out,
// ciwatch warns and reports the job as a plain cancelled job.
func TestAnnotationsFailureReportsTheJobAsToday(t *testing.T) {
	tests := []struct {
		name      string
		opts      func(t *testing.T) fakeOpts
		wantCalls int
		wantInErr string
	}{
		{"other error", func(t *testing.T) fakeOpts {
			o := timeoutOpts()
			o.annotationsErr = map[string]error{timeoutJob: stderrErr(t, "stderr-404.txt")}
			return o
		}, 1, "HTTP 404"},
		{"network error until the retries run out", func(*testing.T) fakeOpts {
			o := timeoutOpts()
			o.annotationsNet = map[string]int{timeoutJob: 1000}
			return o
		}, 9, "gave up after 8 retries"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, calls := fakeWith(t, timeoutList, nil, nil, tt.opts(t))
			var stdout, stderr bytes.Buffer
			if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
			}
			warning := "ciwatch: can't fetch the annotations of job " + timeoutJob + " (slow) of run " + timeoutRun + " (Timeout): "
			if !strings.Contains(stderr.String(), warning) || !strings.Contains(stderr.String(), tt.wantInErr) {
				t.Errorf("stderr = %q, want %q followed by the error (%q)", stderr.String(), warning, tt.wantInErr)
			}
			if strings.Contains(stderr.String(), "timed out after") {
				t.Errorf("stderr = %q, want no timeout", stderr.String())
			}
			if want := "WORKFLOW  STATUS     CONCLUSION\nTimeout   completed  cancelled\n  slow    completed  cancelled\n"; stdout.String() != want {
				t.Errorf("stdout = %q, want the table of a plain cancelled job: %q", stdout.String(), want)
			}
			if len(calls.annotationIDs) != tt.wantCalls {
				t.Errorf("annotations calls = %d, want %d", len(calls.annotationIDs), tt.wantCalls)
			}
			if len(calls.jobLogIDs) != 0 {
				t.Errorf("full logs fetched for jobs %v, want none", calls.jobLogIDs)
			}
			app, _ = fakeWith(t, timeoutList, nil, nil, tt.opts(t))
			jobs, _ := jsonJobs(t, app, cli.ExitCancelled)
			if _, ok := jobs[timeoutJob]["timed_out_after"]; ok {
				t.Errorf("job has timed_out_after %s, want none", jobs[timeoutJob]["timed_out_after"])
			}
		})
	}
}

// TestTimeoutLogFailureIsStillReported covers AC-4: with the annotation saying
// timeout and the full log failing, the job is reported as timed out with
// (log unavailable), a warning, log_error, and the same exit code.
func TestTimeoutLogFailureIsStillReported(t *testing.T) {
	opts := timeoutOpts()
	opts.jobLogErr = map[string]error{timeoutJob: stderrErr(t, "stderr-404.txt")}
	app, _ := fakeWith(t, timeoutList, nil, nil, opts)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
	}
	if !strings.Contains(stderr.String(), timeoutStderr+"\n") {
		t.Errorf("stderr = %q, want the line %q", stderr.String(), timeoutStderr)
	}
	if !strings.Contains(stderr.String(), "ciwatch: can't fetch the log of job "+timeoutJob+" (slow) of run "+timeoutRun+" (Timeout)") {
		t.Errorf("stderr = %q, want the warning that it can't fetch the log", stderr.String())
	}
	if want := timeoutHeader + "\n  (log unavailable)\n"; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
	}
	app, _ = fakeWith(t, timeoutList, nil, nil, opts)
	jobs, _ := jsonJobs(t, app, cli.ExitCancelled)
	job := jobs[timeoutJob]
	if got := string(job["timed_out_after"]); got != `"1m0s"` {
		t.Errorf("timed_out_after = %s, want \"1m0s\"", got)
	}
	if got := string(job["log_error"]); got == "" || got == `""` {
		t.Errorf("log_error = %q, want the error", got)
	}
}

// TestTimeoutExitCodes covers AC-5: a run whose only problem is a timed-out
// job exits 3; a failed run next to it exits as it does today, and the rerun
// hint counts only the failed run.
func TestTimeoutExitCodes(t *testing.T) {
	timedOut := attemptRun(timeoutRun, "Timeout", "completed", "cancelled", 1)
	t.Run("timeout only", func(t *testing.T) {
		opts := timeoutOpts()
		opts.listRaw = []string{list(timedOut)}
		app, _ := fakeWith(t, "", nil, nil, opts)
		var stdout, stderr bytes.Buffer
		if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
			t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
		}
		if !strings.Contains(stderr.String(), timeoutStderr) {
			t.Errorf("stderr = %q, want the timeout reported", stderr.String())
		}
		if strings.Contains(stderr.String(), "to rerun") {
			t.Errorf("stderr = %q, want no rerun hint", stderr.String())
		}
	})
	t.Run("failed run and timed-out run", func(t *testing.T) {
		opts := timeoutOpts()
		opts.listRaw = []string{list(failed(failedRun, "CI", 1), timedOut)}
		opts.viewFixture = map[string]string{failedRun: "run-view-44707997310-test-failure.json"}
		app, _ := fakeWith(t, "", nil, nil, opts)
		var stdout, stderr bytes.Buffer
		if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitFailed {
			t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, stderr.String())
		}
		if !strings.Contains(stderr.String(), timeoutStderr) {
			t.Errorf("stderr = %q, want the timeout reported", stderr.String())
		}
		if !strings.Contains(stderr.String(), "ciwatch: 1 run failed; to rerun its failed jobs: ciwatch --rerun-failed "+sha) {
			t.Errorf("stderr = %q, want the rerun hint to count 1 run", stderr.String())
		}
	})
}

// TestTimeoutCancelledByCtrlC covers AC-6: Ctrl-C during the annotations call
// or the full-log call prints no table and exits 4.
func TestTimeoutCancelledByCtrlC(t *testing.T) {
	tests := []struct {
		name string
		set  func(opts *fakeOpts, cancel context.CancelFunc)
		saw  func(c *counts) bool
	}{
		{
			"annotations call", func(o *fakeOpts, cancel context.CancelFunc) { o.onAnnotations = func(string) { cancel() } },
			func(c *counts) bool { return len(c.annotationIDs) > 0 },
		},
		{
			"full-log call", func(o *fakeOpts, cancel context.CancelFunc) { o.onJobLog = func(string) { cancel() } },
			func(c *counts) bool { return len(c.jobLogIDs) > 0 },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			opts := timeoutOpts()
			tt.set(&opts, cancel)
			app, calls := fakeWith(t, timeoutList, nil, nil, opts)
			var stdout, stderr bytes.Buffer
			if code := app.Run(ctx, []string{"HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitUnchecked, stderr.String())
			}
			if !tt.saw(calls) {
				t.Errorf("the %s was never made", tt.name)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", stdout.String())
			}
			if !strings.Contains(stderr.String(), "context canceled") {
				t.Errorf("stderr = %q, want the cancellation", stderr.String())
			}
		})
	}
}

// TestTimeoutNeedsTheExactMessage covers AC-7: annotations that only resemble
// the timeout message don't make a job timed out. The messages are the
// recorded ones, cut or changed by hand.
func TestTimeoutNeedsTheExactMessage(t *testing.T) {
	for _, msg := range []string{
		"The operation was canceled.",
		"The job has exceeded the maximum execution time",
		"The job has exceeded the maximum execution time of",
		"The job has exceeded the maximum execution time of ",
		"The job has exceeded the maximum execution time of 1m0s and then some",
		"Error: The job has exceeded the maximum execution time of 1m0s",
		"the job has exceeded the maximum execution time of 1m0s",
	} {
		t.Run(msg, func(t *testing.T) {
			raw, err := json.Marshal([]map[string]string{{"message": msg}})
			if err != nil {
				t.Fatal(err)
			}
			opts := timeoutOpts()
			opts.annotations = map[string]string{timeoutJob: string(raw)}
			app, calls := fakeWith(t, timeoutList, nil, nil, opts)
			var stdout, stderr bytes.Buffer
			if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitCancelled {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitCancelled, stderr.String())
			}
			if want := []string{timeoutJob}; !slices.Equal(calls.annotationIDs, want) {
				t.Errorf("annotations fetched for jobs %v, want %v: the cancelled job is checked", calls.annotationIDs, want)
			}
			if strings.Contains(stdout.String(), "timed out") || strings.Contains(stderr.String(), "timed out") || len(calls.jobLogIDs) != 0 {
				t.Errorf("stdout = %q, stderr = %q, full logs for %v, want no timeout", stdout.String(), stderr.String(), calls.jobLogIDs)
			}
		})
	}
	// The limit is whatever follows "of ", as given.
	opts := timeoutOpts()
	opts.annotations = map[string]string{timeoutJob: fmt.Sprintf(`[{"message":"The operation was canceled."},{"message":%q}]`, "The job has exceeded the maximum execution time of 6h0m0s")}
	app, _ := fakeWith(t, timeoutList, nil, nil, opts)
	var stdout, stderr bytes.Buffer
	app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr)
	if !strings.Contains(stderr.String(), "(Timeout) timed out after 6h0m0s\n") {
		t.Errorf("stderr = %q, want the limit 6h0m0s as given", stderr.String())
	}
}
