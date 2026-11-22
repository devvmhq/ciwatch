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

const usage = `usage: ciwatch [--json] [--once] [--follow] <commit>

Watches every GitHub Actions run for a commit until it finishes. A run that a
newer push cancelled is named; with --follow, that commit is watched instead.
`

// MaxFollowed is the most commits --follow moves on from before it stops.
const MaxFollowed = 20

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
	var followed []string
	var runs []gh.Run
	var newer map[int64]string
	seen := map[string]bool{sha: true} // commits already watched, never followed back to
	for {
		runs, err = a.watch(ctx, sha, opts.once, stderr)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", err)
			return ExitUnchecked
		}
		var next string
		var from gh.Run
		newer, next, from = a.findSuperseded(ctx, runs, opts.follow && len(followed) < MaxFollowed, seen, stderr)
		if ctx.Err() != nil { // Ctrl-C during the lookup: no table, and no commit to follow
			_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", ctx.Err())
			return ExitUnchecked
		}
		if next == "" {
			break
		}
		if opts.once { // nothing waited for them, so they are dropped, not finished
			for _, r := range runs {
				if r.Status != "completed" {
					_, _ = fmt.Fprintf(stderr, "ciwatch: run %d (%s) of %s is still in progress; not waiting for it, as --once follows a cancelled run at once\n", r.ID, r.Workflow, short(sha))
				}
			}
		}
		followed = append(followed, sha)
		_, _ = fmt.Fprintf(stderr, "ciwatch: run %d (%s) was cancelled by a newer push; following %s…\n", from.ID, from.Workflow, short(next))
		sha = next
		seen[sha] = true
	}
	if opts.follow && len(followed) >= MaxFollowed && len(newer) > 0 {
		_, _ = fmt.Fprintf(stderr, "ciwatch: followed %d commits, the most it follows; stopping at %s, whose runs were cancelled by a newer push too\n", len(followed), short(sha))
	}

	withJobs := make([]runWithJobs, len(runs))
	jobsApp := a
	for i, r := range runs {
		withJobs[i].Run = r
		withJobs[i].SupersededBy = newer[r.ID]
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
		jobsApp.Retries = a.Retries // a call got through, so the network is back: full retries again
	}

	if opts.json {
		err = writeJSON(stdout, sha, followed, withJobs)
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
	if !opts.follow {
		for _, r := range runs {
			if by := newer[r.ID]; by != "" {
				_, _ = fmt.Fprintf(stderr, "ciwatch: run %d (%s) was cancelled by a newer push, %s; rerun with --follow to watch it\n", r.ID, r.Workflow, short(by))
			}
		}
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
	Jobs         []gh.Job
	JobsErr      string
	SupersededBy string // the commit whose push cancelled the run, if one did
}

var errUsage = errors.New("usage")

type options struct {
	commit string
	json   bool
	once   bool
	follow bool
}

// parseArgs accepts --json, --once, --follow and one commit, in any order.
func parseArgs(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("ciwatch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&o.json, "json", false, "")
	fs.BoolVar(&o.once, "once", false, "")
	fs.BoolVar(&o.follow, "follow", false, "")
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

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// cancelSlack is how long after a cancelled run was last updated a newer push's
// run may still have been created and count as having cancelled it, which
// allows for GitHub's and the runner's clocks being a little apart.
const cancelSlack = time.Minute

// supersededBy finds the run whose push cancelled the cancelled run: the first
// run of the same workflow on the same branch that was created after run and
// no later than when run stopped (a concurrency group cancels a run as the
// newer one is queued), is for another commit and has the same event (so for a
// cancelled push, a fork's pull request from a branch of the same name is not
// taken for the push that cancelled it). A later push that came after the run
// had already been cancelled, say by hand, is not it. ok is false if
// there is none.
func (a App) supersededBy(ctx context.Context, run gh.Run, w io.Writer) (by gh.Run, ok bool, err error) {
	if run.HeadBranch == "" {
		return gh.Run{}, false, nil
	}
	runs, err := retry(ctx, a, w, "gh run list", func() ([]gh.Run, error) {
		return a.GH.BranchRuns(ctx, run.HeadBranch, run.WorkflowID, run.Event, run.Created, run.Updated.Add(cancelSlack))
	})
	if err != nil {
		return gh.Run{}, false, err
	}
	if len(runs) >= gh.BranchRunsLimit && oldestCreated(runs).After(run.Created) {
		// gh lists newest first and stopped at its limit inside the window, so the
		// earliest push, the one that did the cancelling, may be cut off.
		return gh.Run{}, false, fmt.Errorf("gh listed %d runs, the most it returns, so the first newer push may be missing", len(runs))
	}
	for _, r := range runs {
		if !r.Created.After(run.Created) || r.Created.After(run.Updated.Add(cancelSlack)) || r.HeadSHA == run.HeadSHA || r.HeadSHA == "" || r.Event != run.Event {
			continue
		}
		if !ok || r.Created.Before(by.Created) {
			by, ok = r, true
		}
	}
	return by, ok, nil
}

func oldestCreated(runs []gh.Run) time.Time {
	oldest := runs[0].Created
	for _, r := range runs[1:] {
		if r.Created.Before(oldest) {
			oldest = r.Created
		}
	}
	return oldest
}

// replaced reports whether runs holds a later run of run's workflow for the
// same commit: the commit was pushed again, so the cancelled run was not left
// behind for a newer commit.
func replaced(run gh.Run, runs []gh.Run) bool {
	for _, r := range runs {
		if r.HeadSHA == run.HeadSHA && r.WorkflowID == run.WorkflowID && r.Created.After(run.Created) {
			return true
		}
	}
	return false
}

// findSuperseded looks for a newer push behind each cancelled run, and returns
// the commit for each run it found by run ID. next is the commit to follow, if
// follow is set: the newest of those pushes, unless it is a commit in seen
// (a line to w says so), and from is the run that push cancelled. A cancelled
// run that a later run of the same commit replaced is not looked into. A
// lookup that fails is a warning, and the run stays plainly cancelled.
func (a App) findSuperseded(ctx context.Context, runs []gh.Run, follow bool, seen map[string]bool, w io.Writer) (by map[int64]string, next string, from gh.Run) {
	by = map[int64]string{}
	var nextRun gh.Run
	lookupApp := a
	for _, r := range runs {
		if r.Status != "completed" || r.Conclusion != "cancelled" || replaced(r, runs) {
			continue
		}
		newer, ok, err := lookupApp.supersededBy(ctx, r, w)
		if err != nil {
			if ctx.Err() != nil { // Ctrl-C: no warning per remaining run, and nothing to follow
				return map[int64]string{}, "", gh.Run{}
			}
			_, _ = fmt.Fprintf(w, "ciwatch: can't look for a newer push after run %d (%s): %v\n", r.ID, r.Workflow, err)
			if errors.Is(err, gh.ErrNetwork) || errors.Is(err, gh.ErrTemporary) {
				lookupApp.Retries = 0 // as for jobs: the network is likely still down
			}
			continue
		}
		lookupApp.Retries = a.Retries
		if !ok {
			continue
		}
		by[r.ID] = newer.HeadSHA
		if follow && seen[newer.HeadSHA] {
			_, _ = fmt.Fprintf(w, "ciwatch: run %d (%s) was cancelled by a newer push, %s, which was already watched; not following it again\n", r.ID, r.Workflow, short(newer.HeadSHA))
		}
		if follow && !seen[newer.HeadSHA] && newer.Created.After(nextRun.Created) {
			from, nextRun, next = r, newer, newer.HeadSHA
		}
	}
	return by, next, from
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
		note := ""
		if r.SupersededBy != "" {
			note = "  (superseded by " + short(r.SupersededBy) + ")"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s%s\n", r.Workflow, r.Status, r.Conclusion, note)
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

	SupersededBy string `json:"superseded_by,omitempty"`
}

func writeJSON(w io.Writer, sha string, followed []string, runs []runWithJobs) error {
	out := struct {
		SHA          string    `json:"sha"`
		FollowedFrom []string  `json:"followed_from,omitempty"`
		Runs         []jsonRun `json:"runs"`
	}{SHA: sha, FollowedFrom: followed, Runs: make([]jsonRun, len(runs))}
	for i, r := range runs {
		jobs := make([]jsonJob, len(r.Jobs))
		for k, j := range r.Jobs {
			jobs[k] = jsonJob(j)
		}
		out.Runs[i] = jsonRun{ID: r.ID, Workflow: r.Workflow, Status: r.Status, Conclusion: r.Conclusion, URL: r.URL, Jobs: jobs, JobsError: r.JobsErr, SupersededBy: r.SupersededBy}
	}
	return json.NewEncoder(w).Encode(out)
}
