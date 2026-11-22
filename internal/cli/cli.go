// Package cli is ciwatch's command line: it reads the arguments, runs the
// command and returns the process's exit code.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/devvmhq/ciwatch/internal/gh"
)

// Exit codes.
const (
	ExitOK        = 0
	ExitFailed    = 1
	ExitUsage     = 2
	ExitCancelled = 3
	ExitUnchecked = 4
)

const usage = `usage: ciwatch [--json] [--once] <commit>

Watches every GitHub Actions run for a commit until it finishes.
`

// Clock is the time, which tests control.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// App is ciwatch with its dependencies.
type App struct {
	GH       gh.Runner
	Clock    Clock
	Interval time.Duration // between polls
	Grace    time.Duration // after the push, during which late runs are still looked for

	Backoff    time.Duration // the first wait before retrying a gh call that failed with a network error
	MaxBackoff time.Duration // the longest wait; each wait doubles until it reaches this
	Retries    int           // how many times to retry one gh call
}

// New returns an App that runs the real gh and git.
func New() App {
	return App{
		GH: gh.Runner{Exec: gh.Exec}, Clock: realClock{}, Interval: 10 * time.Second, Grace: 60 * time.Second,
		Backoff: time.Second, MaxBackoff: 30 * time.Second, Retries: 8,
	}
}

// Run runs ciwatch with args (without the program name), writing its output to
// stdout and its errors to stderr, and returns the exit code.
func (a App) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args)
	if err != nil {
		if !errors.Is(err, errUsage) && !errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", err)
		}
		_, _ = fmt.Fprint(stderr, usage)
		return ExitUsage
	}
	sha, err := a.GH.ResolveCommit(ctx, opts.commit)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", err)
		if errors.Is(err, gh.ErrBadRevision) {
			return ExitUsage
		}
		return ExitUnchecked
	}
	runs, err := a.watch(ctx, sha, opts.once, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", err)
		return ExitUnchecked
	}

	withJobs := make([]runWithJobs, len(runs))
	jobsApp := a
	for i, r := range runs {
		withJobs[i].Run = r
		jobs, err := retry(ctx, jobsApp, stderr, fmt.Sprintf("gh run view %d", r.ID), func() ([]gh.Job, error) {
			return a.GH.RunJobs(ctx, r.ID)
		})
		if err != nil && ctx.Err() != nil { // Ctrl-C: no partial table, and no warning per remaining run
			_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", err)
			return ExitUnchecked
		}
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "ciwatch: can't list jobs for run %d (%s): %v\n", r.ID, r.Workflow, err)
			withJobs[i].JobsErr = err.Error()
			if errors.Is(err, gh.ErrNetwork) || errors.Is(err, gh.ErrTemporary) {
				// The retries ran out, so the network is likely still down: the
				// remaining runs get one try each, not a full backoff apiece.
				jobsApp.Retries = 0
			}
			continue
		}
		withJobs[i].Jobs = jobs
	}

	if opts.json {
		err = writeJSON(stdout, sha, withJobs)
	} else {
		err = writeTable(stdout, withJobs)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", err)
		return ExitUnchecked
	}

	code, pending := exitCode(runs)
	if pending > 0 {
		_, _ = fmt.Fprintf(stderr, "ciwatch: %d runs still in progress; drop --once to wait for them\n", pending)
	}
	if code == ExitOK && len(runs) >= gh.RunListLimit {
		_, _ = fmt.Fprintf(stderr, "ciwatch: gh listed %d runs, the most it returns, so there may be more; can't vouch for a pass\n", len(runs))
		return ExitUnchecked
	}
	return code
}

// runWithJobs is a run and its jobs. The jobs are empty if they couldn't be
// listed, and JobsErr then says why.
type runWithJobs struct {
	gh.Run
	Jobs    []gh.Job
	JobsErr string
}

var errUsage = errors.New("usage")

type options struct {
	commit string
	json   bool
	once   bool
}

// parseArgs accepts --json, --once and one commit, in any order.
func parseArgs(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("ciwatch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&o.json, "json", false, "")
	fs.BoolVar(&o.once, "once", false, "")
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return options{}, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		if args[0] == "--" {
			positional = append(positional, args[1:]...)
			break
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
	if len(positional) != 1 {
		return options{}, errUsage
	}
	o.commit = positional[0]
	return o, nil
}

// watch lists the runs for sha and, unless once, keeps listing them until
// every run has completed and the grace period is over, writing a line to w
// for each change (none with once). The grace period is described at graceEnd.
// With once it lists a single time, and an empty list is an error either way.
func (a App) watch(ctx context.Context, sha string, once bool, w io.Writer) ([]gh.Run, error) {
	start := a.Clock.Now()
	var prev []gh.Run
	for {
		runs, err := retry(ctx, a, w, "gh run list", func() ([]gh.Run, error) {
			return a.GH.RunList(ctx, sha)
		})
		if err != nil {
			return nil, err
		}
		if once {
			if len(runs) == 0 {
				return nil, fmt.Errorf("no runs found for %s", sha)
			}
			return runs, nil
		}
		progress(w, prev, runs)
		prev = runs
		if over := !a.Clock.Now().Before(a.graceEnd(runs, start)); over {
			if len(runs) == 0 {
				return nil, fmt.Errorf("no runs found for %s", sha)
			}
			if allCompleted(runs) {
				return runs, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-a.Clock.After(a.Interval):
		}
	}
}

// retry calls call, and calls it again after a backoff wait while it fails
// with a network or temporary error, up to a.Retries times. The waits start at
// a.Backoff and double up to a.MaxBackoff. what names the call in the lines
// written to w. A cancelled ctx ends it at once with the context's error.
func retry[T any](ctx context.Context, a App, w io.Writer, what string, call func() (T, error)) (T, error) {
	wait := a.Backoff
	for n := 0; ; n++ {
		v, err := call()
		if err == nil || ctx.Err() != nil {
			return v, err
		}
		kind := "network"
		switch {
		case errors.Is(err, gh.ErrNetwork):
		case errors.Is(err, gh.ErrTemporary):
			kind = "temporary"
		default:
			return v, err
		}
		if n >= a.Retries {
			var zero T
			if a.Retries == 0 { // retrying is off here: nothing was retried to give up on
				return zero, err
			}
			return zero, fmt.Errorf("gave up after %d retries: %w", a.Retries, err)
		}
		cause := strings.TrimPrefix(err.Error(), what+": ")
		_, _ = fmt.Fprintf(w, "ciwatch: %s: %s error, retrying in %s (retry %d of %d): %s\n", what, kind, wait, n+1, a.Retries, cause)
		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case <-a.Clock.After(wait):
		}
		wait = min(wait*2, a.MaxBackoff)
	}
}

func allCompleted(runs []gh.Run) bool {
	for _, r := range runs {
		if r.Status != "completed" {
			return false
		}
	}
	return true
}

// graceEnd is when the period for late runs ends. If a run was created within
// the grace period before start, the push was just now and the period counts
// from the earliest such run's creation. Otherwise the commit was pushed
// earlier (or a run has no time), so it counts from start, which lets a
// re-push of an old commit show its new runs. Either way it never ends before
// one interval has passed since start, whatever GitHub's and the local clock say.
func (a App) graceEnd(runs []gh.Run, start time.Time) time.Time {
	var recent time.Time
	for _, r := range runs {
		if r.Created.IsZero() || r.Created.Before(start.Add(-a.Grace)) {
			continue
		}
		if recent.IsZero() || r.Created.Before(recent) {
			recent = r.Created
		}
	}
	end := start.Add(a.Grace)
	if !recent.IsZero() {
		end = recent.Add(a.Grace)
	}
	if floor := start.Add(a.Interval); end.Before(floor) {
		end = floor
	}
	return end
}

// progress writes a line for each run in cur that is new or whose status or
// conclusion differs from prev.
func progress(w io.Writer, prev, cur []gh.Run) {
	old := make(map[int64]gh.Run, len(prev))
	for _, r := range prev {
		old[r.ID] = r
	}
	for _, r := range cur {
		if p, ok := old[r.ID]; ok && p.Status == r.Status && p.Conclusion == r.Conclusion {
			continue
		}
		line := r.Status
		if r.Conclusion != "" {
			line += " " + r.Conclusion
		}
		_, _ = fmt.Fprintf(w, "ciwatch: run %d (%s): %s\n", r.ID, r.Workflow, line)
	}
}

// exitCode sums up runs as an exit code. pending is the number of runs that
// haven't finished, which only matters when the code is ExitUnchecked.
func exitCode(runs []gh.Run) (code, pending int) {
	var failed, cancelled bool
	for _, r := range runs {
		if r.Status != "completed" {
			pending++
			continue
		}
		switch r.Conclusion {
		case "success", "neutral", "skipped":
		case "cancelled":
			cancelled = true
		default: // failure, timed_out, stale, and whatever GitHub adds later
			failed = true
		}
	}
	switch {
	case failed:
		return ExitFailed, pending
	case cancelled:
		return ExitCancelled, pending
	case pending > 0:
		return ExitUnchecked, pending
	}
	return ExitOK, 0
}

func writeTable(w io.Writer, runs []runWithJobs) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "WORKFLOW\tSTATUS\tCONCLUSION")
	for _, r := range runs {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Workflow, r.Status, r.Conclusion)
		if r.JobsErr != "" {
			_, _ = fmt.Fprintln(tw, "  (jobs unavailable)\t\t")
		}
		for _, j := range r.Jobs {
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", j.Name, j.Status, j.Conclusion)
		}
	}
	return tw.Flush()
}

type jsonJob struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
}

type jsonRun struct {
	ID         int64     `json:"id"`
	Workflow   string    `json:"workflow"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	URL        string    `json:"url"`
	Jobs       []jsonJob `json:"jobs"`
	JobsError  string    `json:"jobs_error,omitempty"`
}

func writeJSON(w io.Writer, sha string, runs []runWithJobs) error {
	out := struct {
		SHA  string    `json:"sha"`
		Runs []jsonRun `json:"runs"`
	}{SHA: sha, Runs: make([]jsonRun, len(runs))}
	for i, r := range runs {
		jobs := make([]jsonJob, len(r.Jobs))
		for k, j := range r.Jobs {
			jobs[k] = jsonJob(j)
		}
		out.Runs[i] = jsonRun{ID: r.ID, Workflow: r.Workflow, Status: r.Status, Conclusion: r.Conclusion, URL: r.URL, Jobs: jobs, JobsError: r.JobsErr}
	}
	return json.NewEncoder(w).Encode(out)
}
