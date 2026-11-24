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
	"slices"
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

const usage = `usage: ciwatch [--json] [--once] [--go-tests] [--fetch-crashes] [--rerun-failed] <commit>

Watches every GitHub Actions run for a commit until it finishes. For a
cancelled run, it names the newer run of the same workflow on the same branch,
if there is one, and the command to watch it. A failed job shows the last
lines of each failed step; with --go-tests, it also shows the failing Go tests
found in its log. A failed run's crash-<Target> artifacts are listed as fuzz
crash inputs; with --fetch-crashes, each is downloaded into
testdata/fuzz/<Target>/ beside the test file that declares the target; a file
that is already there is left alone and reported as already present. With
--rerun-failed, the failed jobs of each failed run are rerun once, ciwatch
waits for the new attempts, and the report is of those. A rerun run that stops
being listed isn't waited for; it is reported at its last known state. The
jobs and logs shown are those of the attempt reported. A run whose rerun call
failed but that GitHub reran anyway is said so and waited for like the others.
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
	TempDir string             // the parent of the staging directories for downloads; os.TempDir() if empty
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
	if opts.rerunFailed {
		if want, failedCalls := a.rerun(ctx, runs, stderr); len(want) > 0 || len(failedCalls) > 0 {
			if runs, err = a.watchAttempts(ctx, sha, runs, want, failedCalls, stderr); err != nil {
				_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", err)
				return ExitUnchecked
			}
		} else if ctx.Err() != nil {
			_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", ctx.Err())
			return ExitUnchecked
		}
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
			// GitHub may hold later attempts than the one reported.
			return a.GH.RunJobsAttempt(ctx, r.ID, r.Attempt)
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

	if err := a.timeouts(ctx, withJobs, stderr); err != nil { // Ctrl-C: no partial table
		_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", err)
		return ExitUnchecked
	}

	if err := a.failedLogs(ctx, withJobs, opts.goTests, stderr); err != nil { // Ctrl-C: no partial table
		_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", err)
		return ExitUnchecked
	}

	if err := a.crashes(ctx, withJobs, opts.fetchCrashes, stderr); err != nil { // Ctrl-C: no partial table
		reportWritten(stderr, withJobs)
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
	if pending > 0 && opts.once { // without --once, a pending run is one that dropped out of the list
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
	if !opts.rerunFailed {
		switch n := countFailed(runs); {
		case n == 1:
			_, _ = fmt.Fprintf(stderr, "ciwatch: 1 run failed; to rerun its failed jobs: ciwatch --rerun-failed %s\n", sha)
		case n > 1:
			_, _ = fmt.Fprintf(stderr, "ciwatch: %d runs failed; to rerun their failed jobs: ciwatch --rerun-failed %s\n", n, sha)
		}
	}
	if opts.fetchCrashes {
		if hint := fetchHint(sha, withJobs); hint != "" {
			_, _ = fmt.Fprintln(stderr, hint)
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
	Newer   *gh.Run // the first run of the workflow on the branch after this one, for another commit, if any

	Crashes      []crash // the fuzz crash inputs among the run's artifacts
	ArtifactsErr string  // why the artifacts couldn't be listed
}

// crash is a crash-<Target> artifact. FetchedTo is the directory it was
// downloaded to, or already was in, relative to the checkout; FetchErr says why
// it wasn't fetched, and failKind which next step the closing hint names.
type crash struct {
	Target, Artifact string
	FetchedTo        string
	AlreadyPresent   bool // every file of the artifact was already in FetchedTo
	FetchErr         string
	failKind         failKind
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
			return ctx.Err() // gh may fail with its own error when killed; report the cancel
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

// reportWritten lists, in the order they were fetched, the crash inputs this
// invocation wrote into the checkout: a crash with FetchedTo set and not
// AlreadyPresent is in the checkout, because files are placed only after the
// download succeeded.
func reportWritten(stderr io.Writer, runs []runWithJobs) {
	for _, r := range runs {
		for _, c := range r.Crashes {
			if c.FetchedTo != "" && !c.AlreadyPresent {
				_, _ = fmt.Fprintf(stderr, "ciwatch: %s of run %d was fetched to %s\n", c.Artifact, r.ID, c.FetchedTo)
			}
		}
	}
}

// fetchCrashes downloads the crash inputs of runs, once the checkout is known.
func (a App) fetchCrashes(ctx context.Context, runs []runWithJobs, stderr io.Writer) error {
	var root string
	var rootErr error
	var fsys fs.FS
	opened := false
	warned := map[string]bool{}
	dlApp := a
	for i, r := range runs {
		for k, c := range r.Crashes {
			if err := ctx.Err(); err != nil {
				return err
			}
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
			if err := ctx.Err(); err != nil { // Ctrl-C while finding the checkout: not a failed input
				return err
			}
			if rootErr != nil {
				fail(rootErr.Error())
				runs[i].Crashes[k].failKind = failNoCheckout
				continue
			}
			res, err := fuzztarget.Declaring(fsys, c.Target)
			if ctxErr := ctx.Err(); ctxErr != nil { // Ctrl-C during the search: not a skipped input
				return ctxErr
			}
			for _, s := range res.Skipped {
				if !warned[s.Path] {
					warned[s.Path] = true
					_, _ = fmt.Fprintf(stderr, "ciwatch: skipped %s while looking for fuzz targets: %s\n", s.Path, reason(s.Err))
				}
			}
			files := res.Files
			switch {
			case err != nil:
				fail(fmt.Sprintf("looking for %s in the checkout: %v", c.Target, err))
				runs[i].Crashes[k].failKind = failRetry
				continue
			case len(files) == 0:
				fail(fmt.Sprintf("no _test.go file declares func %s(f *testing.F)", c.Target))
				runs[i].Crashes[k].failKind = failNoDecl
				continue
			case len(files) > 1:
				fail(fmt.Sprintf("%s is declared in %s", c.Target, strings.Join(files, ", ")))
				runs[i].Crashes[k].failKind = failManyDecl
				continue
			}
			rel := path.Join(path.Dir(files[0]), "testdata", "fuzz", c.Target)
			written, present, err := a.stageAndPlace(ctx, dlApp, stderr, r.ID, c.Artifact, filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil && ctx.Err() != nil {
				return ctx.Err() // gh may fail with its own error when killed; report the cancel
			}
			if err != nil {
				fail(err.Error())
				runs[i].Crashes[k].failKind = failRetry
				if errors.Is(err, gh.ErrNetwork) || errors.Is(err, gh.ErrTemporary) {
					dlApp.Retries = 0
				}
				continue
			}
			dlApp.Retries = a.Retries
			if written == 0 && present == 0 {
				fail(fmt.Sprintf("artifact %s has no files", c.Artifact))
				runs[i].Crashes[k].failKind = failRetry
				continue
			}
			runs[i].Crashes[k].FetchedTo = rel
			runs[i].Crashes[k].AlreadyPresent = written == 0
		}
	}
	return nil
}

// reason is err's text without the "open path:" prefix of a *fs.PathError.
func reason(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// stageAndPlace downloads the artifact into a new, empty directory under
// a.TempDir, copies its files into dst, and removes the directory again.
func (a App) stageAndPlace(ctx context.Context, dlApp App, stderr io.Writer, runID int64, artifact, dst string) (written, present int, err error) {
	stage, err := os.MkdirTemp(a.TempDir, "ciwatch-crash-")
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	_, err = retry(ctx, dlApp, stderr, fmt.Sprintf("gh run download %d -n %s", runID, artifact), func() (struct{}, error) {
		// Every attempt starts from an empty directory: gh won't overwrite what a failed attempt extracted.
		if err := os.RemoveAll(stage); err != nil {
			return struct{}{}, err
		}
		if err := os.Mkdir(stage, 0o700); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, a.GH.DownloadArtifact(ctx, runID, artifact, stage)
	})
	if err != nil {
		return 0, 0, err
	}
	return placeFiles(stage, dst)
}

// placeFiles copies each regular file under stage to the same path under dst,
// creating it only if it doesn't exist; a file that exists is counted as
// present and left alone. If a copy fails it removes the files it created, so
// the checkout is as it was.
func placeFiles(stage, dst string) (written, present int, err error) {
	var created []string
	defer func() {
		if err != nil {
			for _, p := range created {
				_ = os.Remove(p)
			}
			written = 0
		}
	}()
	err = filepath.WalkDir(stage, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(stage, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // a crash input is committed with the repository, so it's readable like its other files
		if errors.Is(err, fs.ErrExist) {
			present++
			return nil
		}
		if err != nil {
			return err
		}
		if err := copyFile(out, p); err != nil {
			_ = os.Remove(target)
			return err
		}
		created = append(created, target)
		written++
		return nil
	})
	return written, present, err
}

// copyFile copies the file at src into out and closes out.
func copyFile(out *os.File, src string) error {
	in, err := os.Open(src) //nolint:gosec // src is a file of the staging directory ciwatch made
	if err != nil {
		_ = out.Close()
		return err
	}
	defer func() { _ = in.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// failKind is why a crash input wasn't fetched, which decides the hint.
type failKind int

const (
	failNone failKind = iota
	failNoDecl
	failManyDecl
	failNoCheckout
	failRetry
)

// crashRef is a crash and the run it belongs to.
type crashRef struct {
	crash
	run int64
}

// fetchHint is the line that ends stderr when crash inputs were not fetched:
// what to do next. It is empty if every input was fetched or is present.
func fetchHint(sha string, runs []runWithJobs) string {
	var failed []crashRef
	for _, r := range runs {
		for _, c := range r.Crashes {
			if c.failKind != failNone {
				failed = append(failed, crashRef{c, r.ID})
			}
		}
	}
	if len(failed) == 0 {
		return ""
	}
	again := "ciwatch --fetch-crashes " + sha
	kind := failed[0].failKind
	same := true
	for _, f := range failed {
		same = same && f.failKind == kind
	}
	if len(failed) == 1 {
		f := failed[0]
		head := fmt.Sprintf("ciwatch: %s of run %d not fetched; ", f.Artifact, f.run)
		switch kind {
		case failNoDecl:
			return head + fmt.Sprintf("declare func %s(f *testing.F) in a _test.go file, then run: %s", f.Target, again)
		case failManyDecl:
			return head + fmt.Sprintf("%s is declared in more than one file, so download it by hand: gh run download %d -n %s -D <package>/testdata/fuzz/%s", f.Target, f.run, f.Artifact, f.Target)
		case failNoCheckout:
			return head + "run " + again + " inside the repository's checkout"
		case failNone, failRetry:
		}
		return head + "to try again: " + again
	}
	head := fmt.Sprintf("ciwatch: %d fuzz crash inputs not fetched; ", len(failed))
	if !same {
		return head + "see the warnings above, then run: " + again
	}
	switch kind {
	case failNoDecl:
		return head + "declare each target in a _test.go file, then run: " + again
	case failManyDecl:
		return head + "download them by hand: gh run download <run> -n <artifact> -D <package>/testdata/fuzz/<Target>"
	case failNoCheckout:
		return head + "run " + again + " inside the repository's checkout"
	case failNone, failRetry:
	}
	return head + "to try again: " + again
}

// jobResult is a job and, if it failed in a completed run, the end of its
// failed steps' logs, and with --go-tests its failing tests. LogErr says why
// the log couldn't be fetched; Steps and Tests are then empty.
type jobResult struct {
	gh.Job
	Ran    []gh.Step // the job's steps, from the jobs JSON
	Failed bool      // the job failed in a completed run, so its log was fetched
	// TimedOut is the limit, as GitHub's annotation gives it, of a cancelled job
	// whose annotations say it exceeded its maximum execution time. Its full log
	// is fetched.
	TimedOut string
	Steps    []steplog.Step
	Tests    []testlog.Failure
	LogErr   string
}

// jobLogName names the lines of a failed job that has no failed step.
const jobLogName = "(job log)"

// timeoutMessage is what GitHub's annotation says of a job that ran past its
// timeout-minutes; the capture is the limit. GitHub never reports timed_out for
// such a job, so this text is the only sign. A reworded message is missed,
// never invented.
var timeoutMessage = regexp.MustCompile(`^The job has exceeded the maximum execution time of (\S+)$`)

// timeouts fetches the annotations of each cancelled job of a completed run
// and sets TimedOut on those that say the job timed out, writing a line to
// stderr for each. A failed call is a warning and leaves the job as it is. It
// returns an error only when ctx is cancelled.
func (a App) timeouts(ctx context.Context, runs []runWithJobs, stderr io.Writer) error {
	annApp := a
	for i, r := range runs {
		if r.Status != "completed" {
			continue
		}
		for k, j := range r.Jobs {
			if j.Conclusion != "cancelled" {
				continue
			}
			msgs, err := retry(ctx, annApp, stderr, fmt.Sprintf("gh api annotations of job %d", j.ID), func() ([]string, error) {
				return a.GH.Annotations(ctx, j.ID)
			})
			if err != nil && ctx.Err() != nil {
				return err
			}
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "ciwatch: can't fetch the annotations of job %d (%s) of run %d (%s): %v\n", j.ID, j.Name, r.ID, r.Workflow, err)
				if errors.Is(err, gh.ErrNetwork) || errors.Is(err, gh.ErrTemporary) {
					annApp.Retries = 0 // the network is likely still down
				}
				continue
			}
			annApp.Retries = a.Retries
			for _, m := range msgs {
				if match := timeoutMessage.FindStringSubmatch(m); match != nil {
					runs[i].Jobs[k].TimedOut = match[1]
					_, _ = fmt.Fprintf(stderr, "ciwatch: job %d (%s) of run %d (%s) timed out after %s\n", j.ID, j.Name, r.ID, r.Workflow, match[1])
					break
				}
			}
		}
	}
	return nil
}

// failedSpans is when each step of a job that ended with conclusion ran.
func failedSpans(steps []gh.Step, conclusion string) []steplog.Span {
	var spans []steplog.Span
	for _, s := range steps {
		if s.Conclusion == conclusion {
			spans = append(spans, steplog.Span{Name: s.Name, Started: s.Started, Completed: s.Completed})
		}
	}
	return spans
}

// stepLines keeps the lines of the step called name from a full job log, whose
// lines read <job>\t<step>\t<timestamp> <text>. A line without those tabs
// continues the line before it.
func stepLines(log []byte, name string) []byte {
	var out []byte
	keep := false
	for _, l := range bytes.SplitAfter(log, []byte("\n")) {
		if parts := bytes.SplitN(l, []byte("\t"), 3); len(parts) == 3 {
			keep = string(parts[1]) == name
		}
		if keep {
			out = append(out, l...)
		}
	}
	return out
}

// readLog finds the end of the failed steps in log, and with goTests the
// failing tests.
func readLog(j jobResult, log []byte, goTests bool) (steps []steplog.Step, tests []testlog.Failure, err error) {
	conclusion := "failure"
	if j.TimedOut != "" {
		conclusion = "cancelled"
	}
	if spans := failedSpans(j.Ran, conclusion); len(spans) > 0 && j.TimedOut != "" {
		// The full log labels each line with its step, and a cancelled step's
		// start second is shared with the steps before it, so each span is
		// parsed from the lines of its own step only.
		// If gh labelled no line with the step's name (UNKNOWN STEP), the whole
		// log is parsed by the step's times alone.
		for _, sp := range spans {
			own := stepLines(log, sp.Name)
			if len(own) == 0 {
				own = log
			}
			var one []steplog.Step
			if one, err = steplog.Parse(bytes.NewReader(own), []steplog.Span{sp}); err != nil {
				break
			}
			steps = append(steps, one...)
		}
	} else if len(spans) > 0 {
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
			if !j.Failed && j.TimedOut == "" {
				continue
			}
			log, err := retry(ctx, logApp, stderr, fmt.Sprintf("gh run view %d --job %d", r.ID, j.ID), func() ([]byte, error) {
				if j.TimedOut != "" { // gh run view --log-failed prints nothing for a cancelled job
					return a.GH.JobLog(ctx, r.ID, r.Attempt, j.ID)
				}
				return a.GH.FailedLog(ctx, r.ID, r.Attempt, j.ID)
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
	rerunFailed  bool
}

// parseArgs accepts --json, --once, --go-tests, --fetch-crashes, --rerun-failed and one commit, in any order.
func parseArgs(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("ciwatch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&o.json, "json", false, "")
	fs.BoolVar(&o.once, "once", false, "")
	fs.BoolVar(&o.goTests, "go-tests", false, "")
	fs.BoolVar(&o.fetchCrashes, "fetch-crashes", false, "")
	fs.BoolVar(&o.rerunFailed, "rerun-failed", false, "")
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
	if o.rerunFailed && o.once {
		return options{}, errors.New("--rerun-failed waits for the new attempts, so it can't be used with --once")
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

// rerunnable is whether r is a completed run that --rerun-failed reruns.
func rerunnable(r gh.Run) bool {
	return r.Status == "completed" && (r.Conclusion == "failure" || r.Conclusion == "timed_out")
}

func countFailed(runs []gh.Run) int {
	n := 0
	for _, r := range runs {
		if rerunnable(r) {
			n++
		}
	}
	return n
}

// rerun asks for the failed jobs of each failed run to be rerun, with one call
// per run and never through retry, because a rerun isn't safe to repeat. A call
// that fails is a warning. It returns, by run ID, the attempt each accepted
// rerun must go past, and the attempt of each run whose call failed.
func (a App) rerun(ctx context.Context, runs []gh.Run, w io.Writer) (want, failedCalls map[int64]int) {
	want = map[int64]int{}
	failedCalls = map[int64]int{}
	for _, r := range runs {
		if !rerunnable(r) || ctx.Err() != nil {
			continue
		}
		if err := a.GH.RerunFailed(ctx, r.ID); err != nil {
			if ctx.Err() == nil {
				_, _ = fmt.Fprintf(w, "ciwatch: can't rerun the failed jobs of run %d (%s): %v\n", r.ID, r.Workflow, err)
				failedCalls[r.ID] = r.Attempt
			}
			continue
		}
		_, _ = fmt.Fprintf(w, "ciwatch: rerunning the failed jobs of run %d (%s)\n", r.ID, r.Workflow)
		want[r.ID] = r.Attempt
	}
	return want, failedCalls
}

// watchAttempts lists the runs for sha, as watch does but with no grace period,
// until every run has completed and every run in want has an attempt past the
// one in want. prev is the runs the lines of progress are measured against. A
// run in want that a list doesn't have is warned about once, not waited for
// while it is missing, and returned at its last known state if it still is.
// A run in failedCalls, whose rerun call failed at the attempt it maps to, is
// announced once when a list shows a higher attempt, and then moved into want
// like an accepted rerun. With want empty there is one list, to look for such
// a run; if that list fails, the error is a warning and prev is returned,
// unless ctx is done.
func (a App) watchAttempts(ctx context.Context, sha string, prev []gh.Run, want, failedCalls map[int64]int, w io.Writer) ([]gh.Run, error) {
	first := prev
	checkOnly := len(want) == 0
	last := map[int64]gh.Run{} // the last state seen of each run in want or failedCalls
	for _, r := range prev {
		_, w1 := want[r.ID]
		_, w2 := failedCalls[r.ID]
		if w1 || w2 {
			last[r.ID] = r
		}
	}
	gone := map[int64]bool{} // the runs in want or failedCalls the latest list doesn't have
	for {
		runs, err := retry(ctx, a, w, "gh run list", func() ([]gh.Run, error) {
			return a.GH.RunList(ctx, sha)
		})
		if err != nil {
			if checkOnly && len(want) == 0 && ctx.Err() == nil {
				_, _ = fmt.Fprintf(w, "ciwatch: can't check whether the failed rerun calls took effect: %v\n", err)
				return first, nil
			}
			return nil, err
		}
		progress(w, prev, runs)
		prev = runs
		for _, r := range runs {
			if n, ok := failedCalls[r.ID]; ok && r.Attempt > n {
				_, _ = fmt.Fprintf(w, "ciwatch: run %d (%s) was rerun after all (attempt %d)\n", r.ID, r.Workflow, r.Attempt)
				want[r.ID] = n
				delete(failedCalls, r.ID)
			}
		}
		listed := map[int64]bool{}
		for _, r := range runs {
			_, inWant := want[r.ID]
			_, inFailed := failedCalls[r.ID]
			if inWant || inFailed {
				last[r.ID] = r
				listed[r.ID] = true
				delete(gone, r.ID)
			}
		}
		waitFor := map[int64]int{}
		for id, n := range want {
			if listed[id] {
				waitFor[id] = n
			}
		}
		// A run that isn't listed isn't waited for, and is put back at its
		// last known state; a failed call's run is a known failure, not a pass.
		for _, ids := range []map[int64]int{want, failedCalls} {
			for id := range ids {
				if !listed[id] && !gone[id] {
					gone[id] = true
					_, _ = fmt.Fprintf(w, "ciwatch: run %d (%s) is no longer listed for this commit; not waiting for it\n", id, last[id].Workflow)
				}
			}
		}
		if allCompleted(runs) && attemptsPast(runs, waitFor) {
			return withGone(first, runs, gone, last), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-a.Clock.After(a.Interval):
		}
	}
}

// withGone is runs with each run in gone put back at its last known state, in
// the place it had in first, the runs of before the watch.
func withGone(first, runs []gh.Run, gone map[int64]bool, last map[int64]gh.Run) []gh.Run {
	out := slices.Clone(runs)
	for i, r := range first {
		if !gone[r.ID] {
			continue
		}
		at := len(out) // before the first later run of first that is still in out
		for _, next := range first[i+1:] {
			if k := slices.IndexFunc(out, func(o gh.Run) bool { return o.ID == next.ID }); k >= 0 {
				at = k
				break
			}
		}
		out = slices.Insert(out, at, last[r.ID])
	}
	return out
}

// attemptsPast is whether every run in want is in runs with a later attempt.
func attemptsPast(runs []gh.Run, want map[int64]int) bool {
	seen := 0
	for _, r := range runs {
		if n, ok := want[r.ID]; ok {
			if r.Attempt <= n {
				return false
			}
			seen++
		}
	}
	return seen == len(want)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// newerRuns looks up, for each completed cancelled run on a branch, the runs of
// its workflow on that branch since it, and returns by run ID the first one
// after it that is for another commit. A lookup that fails is a warning, and so
// is a full page of 100 runs, which may have lost the oldest; either way the run
// stays plainly cancelled.
func (a App) newerRuns(ctx context.Context, runs []gh.Run, w io.Writer) map[int64]gh.Run {
	newer := map[int64]gh.Run{}
	lookupApp := a
	for _, r := range runs {
		if r.Status != "completed" || r.Conclusion != "cancelled" || r.HeadBranch == "" {
			continue
		}
		got, err := retry(ctx, lookupApp, w, "gh run list", func() ([]gh.Run, error) {
			return a.GH.RunsSince(ctx, r.HeadBranch, r.WorkflowID, r.Created)
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
		if len(got) >= gh.RunListLimit { // the oldest runs may have been cut off
			_, _ = fmt.Fprintf(w, "ciwatch: can't name the run after run %d (%s): %d or more runs since it\n", r.ID, r.Workflow, gh.RunListLimit)
			continue
		}
		if next, ok := firstAfter(r, got); ok {
			newer[r.ID] = next
		}
	}
	return newer
}

// firstAfter picks, from runs, the one for another commit than cancelled that
// was created strictly after it, the earliest, the lowest ID on a tie. The order
// of runs doesn't matter.
func firstAfter(cancelled gh.Run, runs []gh.Run) (gh.Run, bool) {
	var best gh.Run
	found := false
	for _, r := range runs {
		if r.HeadSHA == "" || r.HeadSHA == cancelled.HeadSHA || !r.Created.After(cancelled.Created) {
			continue
		}
		if !found || r.Created.Before(best.Created) || r.Created.Equal(best.Created) && r.ID < best.ID {
			best, found = r, true
		}
	}
	return best, found
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
		if p, ok := old[r.ID]; ok && p.Status == r.Status && p.Conclusion == r.Conclusion && p.Attempt == r.Attempt {
			continue
		}
		line := r.Status
		if r.Conclusion != "" {
			line += " " + r.Conclusion
		}
		if r.Attempt > 1 {
			line += fmt.Sprintf(" (attempt %d)", r.Attempt)
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
