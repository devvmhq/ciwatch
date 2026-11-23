package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)

// Schema is the version of the --json output; docs/json.md describes it.
const Schema = 1

// Report is everything ciwatch found about a commit's runs. Both the --json
// output and the table are written from it.
type Report struct {
	Schema int    `json:"schema"`
	SHA    string `json:"sha"`
	Runs   []Run  `json:"runs"`
}

// Run is one workflow run and its jobs.
type Run struct {
	ID         int64  `json:"id"`
	Attempt    int    `json:"attempt"`
	Workflow   string `json:"workflow"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
	Jobs       []Job  `json:"jobs"`
	JobsError  string `json:"jobs_error,omitempty"`

	FuzzCrashes    []FuzzCrash `json:"fuzz_crashes,omitempty"`    // only for a failed run with crash-<Target> artifacts
	ArtifactsError string      `json:"artifacts_error,omitempty"` // only when the run's artifacts could not be listed

	NewerRun *NewerRun `json:"newer_run,omitempty"` // only for a cancelled run with a newer run behind it
}

// FuzzCrash is a fuzz crash input a run uploaded as the artifact crash-<Target>.
// FetchedTo is where --fetch-crashes put it, relative to the checkout;
// FetchError says why it didn't.
type FuzzCrash struct {
	Target     string `json:"target"`
	Artifact   string `json:"artifact"`
	RunID      int64  `json:"run_id"`
	FetchedTo  string `json:"fetched_to,omitempty"`
	FetchError string `json:"fetch_error,omitempty"`
}

// NewerRun is the newest run of a cancelled run's workflow on its branch, when
// it is for another commit and was created after the cancelled run.
type NewerRun struct {
	ID  int64  `json:"id"`
	SHA string `json:"sha"`
}

// Job is one job of a run. A job with Steps or LogError is a failed one whose
// log was fetched.
type Job struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`

	Steps    *[]Step `json:"steps,omitempty"` // only for a failed job whose log was read
	LogError string  `json:"log_error,omitempty"`
	Tests    *[]Test `json:"tests,omitempty"` // only with --go-tests, for a failed job whose log was read
}

// Step is a failed step and the last lines of its log.
type Step struct {
	Name    string   `json:"name"`
	Lines   []string `json:"lines"`
	Omitted int      `json:"omitted"`
}

// Test is one failing test and the log lines around it.
type Test struct {
	Package     string   `json:"package"`
	Name        string   `json:"name"`
	Lines       []string `json:"lines"`
	RunningTest string   `json:"running_test,omitempty"`
}

// buildReport builds the report; goTests says whether the failing tests were
// looked for.
func buildReport(sha string, runs []runWithJobs, goTests bool) Report {
	out := Report{Schema: Schema, SHA: sha, Runs: make([]Run, len(runs))}
	for i, r := range runs {
		jobs := make([]Job, len(r.Jobs))
		for k, j := range r.Jobs {
			jobs[k] = Job{ID: j.ID, Name: j.Name, Status: j.Status, Conclusion: j.Conclusion, URL: j.URL, LogError: j.LogErr}
			if j.Failed && j.LogErr == "" {
				steps := make([]Step, len(j.Steps))
				for n, s := range j.Steps {
					steps[n] = Step{Name: s.Name, Lines: append([]string{}, s.Lines...), Omitted: s.Omitted}
				}
				jobs[k].Steps = &steps
				if goTests {
					tests := make([]Test, len(j.Tests))
					for n, f := range j.Tests {
						tests[n] = Test{Package: f.Package, Name: f.Name, Lines: append([]string{}, f.Lines...), RunningTest: f.RunningTest}
					}
					jobs[k].Tests = &tests
				}
			}
		}
		out.Runs[i] = Run{ID: r.ID, Attempt: r.Attempt, Workflow: r.Workflow, Status: r.Status, Conclusion: r.Conclusion, URL: r.URL, Jobs: jobs, JobsError: r.JobsErr, ArtifactsError: r.ArtifactsErr}
		for _, c := range r.Crashes {
			out.Runs[i].FuzzCrashes = append(out.Runs[i].FuzzCrashes, FuzzCrash{Target: c.Target, Artifact: c.Artifact, RunID: r.ID, FetchedTo: c.FetchedTo, FetchError: c.FetchErr})
		}
		if r.Newer != nil {
			out.Runs[i].NewerRun = &NewerRun{ID: r.Newer.ID, SHA: r.Newer.HeadSHA}
		}
	}
	return out
}

// WriteJSON writes the report as one compact JSON document.
func (rep Report) WriteJSON(w io.Writer) error {
	return json.NewEncoder(w).Encode(rep)
}

// WriteTable writes the report as a table, then the failing tests of each
// failed job, with at most maxLines lines of each (all of them if maxLines is
// 0).
func (rep Report) WriteTable(w io.Writer, maxLines int) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "WORKFLOW\tSTATUS\tCONCLUSION")
	for _, r := range rep.Runs {
		note := ""
		if r.NewerRun != nil {
			note = fmt.Sprintf("  (newer run %d for %s)", r.NewerRun.ID, short(r.NewerRun.SHA))
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s%s\n", r.Workflow, r.Status, r.Conclusion, note)
		if r.JobsError != "" {
			_, _ = fmt.Fprintln(tw, "  (jobs unavailable)\t\t")
		}
		for _, j := range r.Jobs {
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", j.Name, j.Status, j.Conclusion)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	// The blocks are free-form, so they follow the flush, not the columns.
	for _, r := range rep.Runs {
		for _, j := range r.Jobs {
			if j.Steps != nil || j.LogError != "" {
				writeFailureBlock(w, r, j, maxLines)
			}
		}
	}
	first := true
	for _, r := range rep.Runs {
		for _, c := range r.FuzzCrashes {
			if first {
				_, _ = fmt.Fprint(w, "\nFuzz crash inputs:\n")
				first = false
			}
			line := fmt.Sprintf("  %s (%s, run %d)", c.Target, c.Artifact, c.RunID)
			switch {
			case c.FetchedTo != "":
				line += ": fetched to " + c.FetchedTo
			case c.FetchError != "":
				line += ": not fetched"
			}
			_, _ = fmt.Fprintln(w, line)
		}
	}
	return nil
}

// writeFailureBlock writes the failed steps of the failed job j of r, then its
// failing tests if they were looked for.
func writeFailureBlock(w io.Writer, r Run, j Job, maxLines int) {
	_, _ = fmt.Fprintf(w, "\n%s (%s, run %d):\n", j.Name, r.Workflow, r.ID)
	var steps []Step
	if j.Steps != nil {
		steps = *j.Steps
	}
	var tests []Test
	if j.Tests != nil {
		tests = *j.Tests
	}
	switch {
	case j.LogError != "":
		_, _ = fmt.Fprintln(w, "  (log unavailable)")
	case len(steps) == 0:
		_, _ = fmt.Fprintln(w, "  (no failed step in the log)")
	}
	for _, s := range steps {
		_, _ = fmt.Fprintf(w, "  %s:\n", s.Name)
		if s.Omitted > 0 {
			_, _ = fmt.Fprintf(w, "    … %d earlier lines\n", s.Omitted)
		}
		for _, l := range s.Lines {
			_, _ = fmt.Fprintln(w, "    "+l)
		}
	}
	for _, f := range tests {
		head := "  FAIL"
		if f.Package != "" {
			head += " " + f.Package
		}
		head += " " + f.Name
		if f.RunningTest != "" {
			head += ": crashed while running " + f.RunningTest
		}
		_, _ = fmt.Fprintln(w, head)
		lines := f.Lines
		if maxLines > 0 && len(lines) > maxLines {
			lines = lines[:maxLines]
		}
		for _, l := range lines {
			_, _ = fmt.Fprintln(w, "  "+l)
		}
		if more := len(f.Lines) - len(lines); more > 0 {
			_, _ = fmt.Fprintf(w, "  … %d more lines (--json has them all)\n", more)
		}
	}
}
