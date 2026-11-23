// Package cli is ciwatch's command line: it reads the arguments, runs the
// command and returns the process's exit code.
package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/devvmhq/ciwatch/internal/fuzztarget"
	"github.com/devvmhq/ciwatch/internal/gh"
	"github.com/devvmhq/ciwatch/internal/steplog"
	"github.com/devvmhq/ciwatch/internal/testlog"
)

// Exit codes.
const (
	ExitOK        = 0
	ExitFailed    = 1
	ExitUsage     = 2
	ExitCancelled = 3
	ExitUnchecked = 4
)

const usage = `usage: ciwatch [--json] [--once] [--go-tests] [--fetch-crashes] <commit>

Watches every GitHub Actions run for a commit until it finishes. For a
cancelled run, it names the newer run of the same workflow on the same branch,
if there is one, and the command to watch it. A failed job shows the last
lines of each failed step; with --go-tests, it also shows the failing Go tests
found in its log. A failed run's crash-<Target> artifacts are listed as fuzz
crash inputs; with --fetch-crashes, each is downloaded into
testdata/fuzz/<Target>/ beside the test file that declares the target.
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

	MaxTestLines int // the most lines of one failing test the table shows; 0 shows them all

	OpenDir func(string) fs.FS // opens the checkout at a directory; os.DirFS if nil
}

// New returns an App that runs the real gh and git.
func New() App {
	return App{
		GH: gh.Runner{Exec: gh.Exec}, Clock: realClock{}, Interval: 10 * time.Second, Grace: 60 * time.Second,
		Backoff: time.Second, MaxBackoff: 30 * time.Second, Retries: 8, MaxTestLines: 20,
		OpenDir: os.DirFS,
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
	newer := a.newerRuns(ctx, runs, stderr)
	if ctx.Err() != nil { // Ctrl-C during the lookup: no table
		_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", ctx.Err())
		return ExitUnchecked
	}

	withJobs := make([]runWithJobs, len(runs))
	jobsApp := a
	for i, r := range runs {
		withJobs[i].Run = r
		if n, ok := newer[r.ID]; ok {
			withJobs[i].Newer = &n
		}
		jobs, err := retry(ctx, jobsApp, stderr, fmt.Sprintf("gh run view %d", r.ID), func() ([]gh.JobSteps, error) {
			return a.GH.RunJobsWithSteps(ctx, r.ID)
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
		withJobs[i].Jobs = make([]jobResult, len(jobs))
		for k, j := range jobs {
			withJobs[i].Jobs[k] = jobResult{Job: j.Job, Ran: j.Steps, Failed: r.Status == "completed" && (j.Conclusion == "failure" || j.Conclusion == "timed_out")}
		}
		jobsApp.Retries = a.Retries // a call got through, so the network is back: full retries again
	}

	if err := a.failedLogs(ctx, withJobs, opts.goTests, stderr); err != nil { // Ctrl-C: no partial table
		_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", err)
		return ExitUnchecked
	}

	if err := a.crashes(ctx, withJobs, opts.fetchCrashes, stderr); err != nil { // Ctrl-C: no partial table
		_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", err)
		return ExitUnchecked
	}

	report := buildReport(sha, withJobs, opts.goTests)
	if opts.json {
		err = report.WriteJSON(stdout)
	} else {
		err = report.WriteTable(stdout, a.MaxTestLines)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", err)
		return ExitUnchecked
	}

	code, pending := exitCode(runs)
	if pending > 0 {
		_, _ = fmt.Fprintf(stderr, "ciwatch: %d runs still in progress; drop --once to wait for them\n", pending)
	}
	for _, r := range runs {
		if n, ok := newer[r.ID]; ok {
			_, _ = fmt.Fprintf(stderr, "ciwatch: run %d (%s) was cancelled; to watch the newer run: ciwatch %s\n", r.ID, r.Workflow, n.HeadSHA)
		}
	}
	if !opts.fetchCrashes {
		n := 0
		for _, r := range withJobs {
			n += len(r.Crashes)
		}
		switch {
		case n == 1:
			_, _ = fmt.Fprintf(stderr, "ciwatch: 1 fuzz crash input not fetched; to fetch it: ciwatch --fetch-crashes %s\n", sha)
		case n > 1:
			_, _ = fmt.Fprintf(stderr, "ciwatch: %d fuzz crash inputs not fetched; to fetch them: ciwatch --fetch-crashes %s\n", n, sha)
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
	Jobs    []jobResult
	JobsErr string
	Newer   *gh.Run // the newest run of the workflow on the branch, if it is for a newer commit

	Crashes      []crash // the fuzz crash inputs among the run's artifacts
	ArtifactsErr string  // why the artifacts couldn't be listed
}

// crash is a crash-<Target> artifact. FetchedTo is the directory it was
// downloaded to, relative to the checkout; FetchErr says why it wasn't.
type crash struct {
	Target, Artifact string
	FetchedTo        string
	FetchErr         string
}

var crashName = regexp.MustCompile(`^crash-(Fuzz[A-Za-z0-9_]*)$`)

// crashes lists the artifacts of each failed run and takes those named
// crash-<Target> as fuzz crash inputs; with fetch it downloads each one beside
// the test file that declares its target. A failure is a warning on stderr and
// a field of the run. It returns an error only when ctx is cancelled.
func (a App) crashes(ctx context.Context, runs []runWithJobs, fetch bool, stderr io.Writer) error {
	listApp := a
	for i, r := range runs {
		if r.Status != "completed" || (r.Conclusion != "failure" && r.Conclusion != "timed_out") {
			continue
		}
		type listing struct {
			arts  []gh.Artifact
			total int
		}
		got, err := retry(ctx, listApp, stderr, fmt.Sprintf("gh api artifacts of run %d", r.ID), func() (listing, error) {
			arts, total, err := a.GH.Artifacts(ctx, r.ID)
			return listing{arts, total}, err
		})
		if err != nil && ctx.Err() != nil {
			return err
		}
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "ciwatch: can't list artifacts for run %d (%s): %v\n", r.ID, r.Workflow, err)
			runs[i].ArtifactsErr = err.Error()
			if errors.Is(err, gh.ErrNetwork) || errors.Is(err, gh.ErrTemporary) {
				listApp.Retries = 0 // as for jobs: the network is likely still down
			}
			continue
		}
		listApp.Retries = a.Retries
		if got.total > gh.ArtifactsLimit {
			_, _ = fmt.Fprintf(stderr, "ciwatch: run %d (%s) has %d artifacts; only the first %d were checked for crash inputs\n", r.ID, r.Workflow, got.total, gh.ArtifactsLimit)
		}
		for _, art := range got.arts {
			if m := crashName.FindStringSubmatch(art.Name); m != nil {
				runs[i].Crashes = append(runs[i].Crashes, crash{Target: m[1], Artifact: art.Name})
			}
		}
	}
	if !fetch {
		return nil
	}
	return a.fetchCrashes(ctx, runs, stderr)
}

// fetchCrashes downloads the crash inputs of runs, once the checkout is known.
func (a App) fetchCrashes(ctx context.Context, runs []runWithJobs, stderr io.Writer) error {
	var root string
	var rootErr error
	var fsys fs.FS
	opened := false
	dlApp := a
	for i, r := range runs {
		for k, c := range r.Crashes {
			fail := func(msg string) {
				_, _ = fmt.Fprintf(stderr, "ciwatch: can't fetch %s of run %d: %s\n", c.Artifact, r.ID, msg)
				runs[i].Crashes[k].FetchErr = msg
			}
			if !opened {
				opened = true
				if root, rootErr = a.GH.Toplevel(ctx); rootErr == nil {
					open := a.OpenDir
					if open == nil {
						open = os.DirFS
					}
					fsys = open(root)
				}
			}
			if rootErr != nil {
				fail(rootErr.Error())
				continue
			}
			files, err := fuzztarget.Declaring(fsys, c.Target)
			switch {
			case err != nil:
				fail(fmt.Sprintf("looking for %s in the checkout: %v", c.Target, err))
				continue
			case len(files) == 0:
				fail(fmt.Sprintf("no _test.go file declares func %s(f *testing.F)", c.Target))
				continue
			case len(files) > 1:
				fail(fmt.Sprintf("%s is declared in %s", c.Target, strings.Join(files, ", ")))
				continue
			}
			rel := path.Join(path.Dir(files[0]), "testdata", "fuzz", c.Target)
			_, err = retry(ctx, dlApp, stderr, fmt.Sprintf("gh run download %d -n %s", r.ID, c.Artifact), func() (struct{}, error) {
				return struct{}{}, a.GH.DownloadArtifact(ctx, r.ID, c.Artifact, filepath.Join(root, filepath.FromSlash(rel)))
			})
			if err != nil && ctx.Err() != nil {
				return err
			}
			if err != nil {
				fail(err.Error())
				if errors.Is(err, gh.ErrNetwork) || errors.Is(err, gh.ErrTemporary) {
					dlApp.Retries = 0
				}
				continue
			}
			dlApp.Retries = a.Retries
			runs[i].Crashes[k].FetchedTo = rel
		}
	}
	return nil
}

// jobResult is a job and, if it failed in a completed run, the end of its
// failed steps' logs, and with --go-tests its failing tests. LogErr says why
// the log couldn't be fetched; Steps and Tests are then empty.
type jobResult struct {
	gh.Job
	Ran    []gh.Step // the job's steps, from the jobs JSON
	Failed bool      // the job failed in a completed run, so its log was fetched
	Steps  []steplog.Step
	Tests  []testlog.Failure
	LogErr string
}

// jobLogName names the lines of a failed job that has no failed step.
const jobLogName = "(job log)"

// failedSpans is when each failed step of a job ran.
func failedSpans(steps []gh.Step) []steplog.Span {
	var spans []steplog.Span
	for _, s := range steps {
		if s.Conclusion == "failure" {
			spans = append(spans, steplog.Span{Name: s.Name, Started: s.Started, Completed: s.Completed})
		}
	}
	return spans
}

// readLog finds the end of the failed steps in log, and with goTests the
// failing tests.
func readLog(j jobResult, log []byte, goTests bool) (steps []steplog.Step, tests []testlog.Failure, err error) {
	if spans := failedSpans(j.Ran); len(spans) > 0 {
		steps, err = steplog.Parse(bytes.NewReader(log), spans)
	} else {
		var whole steplog.Step
		if whole, err = steplog.Whole(bytes.NewReader(log)); err == nil && len(whole.Lines) > 0 {
			whole.Name = jobLogName
			steps = []steplog.Step{whole}
		}
	}
	if err != nil {
		return nil, nil, err
	}
	if goTests {
		var res testlog.Result
		if res, err = testlog.Parse(bytes.NewReader(log)); err != nil {
			return nil, nil, err
		}
		tests = res.Failures
	}
	return steps, tests, nil
}

// failedLogs fetches the failed-step log of each failed job and reads it,
// retrying like the jobs fetch. It returns an error only when ctx is cancelled.
func (a App) failedLogs(ctx context.Context, runs []runWithJobs, goTests bool, stderr io.Writer) error {
	logApp := a
	for i, r := range runs {
		for k, j := range r.Jobs {
			if !j.Failed {
				continue
			}
			log, err := retry(ctx, logApp, stderr, fmt.Sprintf("gh run view %d --job %d", r.ID, j.ID), func() ([]byte, error) {
				return a.GH.FailedLog(ctx, r.ID, j.ID)
			})
			if err != nil && ctx.Err() != nil {
				return err
			}
			if err == nil {
				var steps []steplog.Step
				var tests []testlog.Failure
				if steps, tests, err = readLog(j, log, goTests); err == nil {
					runs[i].Jobs[k].Steps = steps
					runs[i].Jobs[k].Tests = tests
					logApp.Retries = a.Retries // a call got through, so the network is back
					continue
				}
				err = fmt.Errorf("reading the log of job %d: %w", j.ID, err)
			}
			_, _ = fmt.Fprintf(stderr, "ciwatch: can't fetch the log of job %d (%s) of run %d (%s): %v\n", j.ID, j.Name, r.ID, r.Workflow, err)
			runs[i].Jobs[k].LogErr = err.Error()
			if errors.Is(err, gh.ErrNetwork) || errors.Is(err, gh.ErrTemporary) {
				logApp.Retries = 0 // as for jobs: the network is likely still down
			}
		}
	}
	return nil
}

var errUsage = errors.New("usage")

type options struct {
	commit  string
	json    bool
	once    bool
	goTests bool

	fetchCrashes bool
}

// parseArgs accepts --json, --once, --go-tests, --fetch-crashes and one commit, in any order.
func parseArgs(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("ciwatch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&o.json, "json", false, "")
	fs.BoolVar(&o.once, "once", false, "")
	fs.BoolVar(&o.goTests, "go-tests", false, "")
	fs.BoolVar(&o.fetchCrashes, "fetch-crashes", false, "")
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

// newerRuns looks up, for each completed cancelled run on a branch, the newest
// run of its workflow on that branch, and returns by run ID those that are for
// another commit and were created after the cancelled run. A lookup that fails
// is a warning, and the run stays plainly cancelled.
func (a App) newerRuns(ctx context.Context, runs []gh.Run, w io.Writer) map[int64]gh.Run {
	newer := map[int64]gh.Run{}
	lookupApp := a
	for _, r := range runs {
		if r.Status != "completed" || r.Conclusion != "cancelled" || r.HeadBranch == "" {
			continue
		}
		type latest struct {
			run gh.Run
			ok  bool
		}
		got, err := retry(ctx, lookupApp, w, "gh run list", func() (latest, error) {
			run, ok, err := a.GH.LatestRun(ctx, r.HeadBranch, r.WorkflowID)
			return latest{run, ok}, err
		})
		if err != nil {
			if ctx.Err() != nil { // Ctrl-C: no warning per remaining run
				return map[int64]gh.Run{}
			}
			_, _ = fmt.Fprintf(w, "ciwatch: can't look for a newer run after run %d (%s): %v\n", r.ID, r.Workflow, err)
			if errors.Is(err, gh.ErrNetwork) || errors.Is(err, gh.ErrTemporary) {
				lookupApp.Retries = 0 // as for jobs: the network is likely still down
			}
			continue
		}
		lookupApp.Retries = a.Retries
		if got.ok && got.run.HeadSHA != r.HeadSHA && got.run.HeadSHA != "" && got.run.Created.After(r.Created) {
			newer[r.ID] = got.run
		}
	}
	return newer
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
