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
	Schema       int      `json:"schema"`
	SHA          string   `json:"sha"`
	FollowedFrom []string `json:"followed_from,omitempty"`
	Runs         []Run    `json:"runs"`
}

// Run is one workflow run and its jobs.
type Run struct {
	ID         int64  `json:"id"`
	Workflow   string `json:"workflow"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
	Jobs       []Job  `json:"jobs"`
	JobsError  string `json:"jobs_error,omitempty"`

	SupersededBy string `json:"superseded_by,omitempty"`
}

// Job is one job of a run. A job with Tests or TestsError is one whose failing
// tests were looked for.
type Job struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`

	Tests      *[]Test `json:"tests,omitempty"` // only for a failed job whose log was read
	TestsError string  `json:"tests_error,omitempty"`
}

// Test is one failing test and the log lines around it.
type Test struct {
	Package     string   `json:"package"`
	Name        string   `json:"name"`
	Lines       []string `json:"lines"`
	RunningTest string   `json:"running_test,omitempty"`
}

func buildReport(sha string, followed []string, runs []runWithJobs) Report {
	out := Report{Schema: Schema, SHA: sha, FollowedFrom: followed, Runs: make([]Run, len(runs))}
	for i, r := range runs {
		jobs := make([]Job, len(r.Jobs))
		for k, j := range r.Jobs {
			jobs[k] = Job{ID: j.ID, Name: j.Name, Status: j.Status, Conclusion: j.Conclusion, URL: j.URL, TestsError: j.TestsErr}
			if j.Failed && j.TestsErr == "" {
				tests := make([]Test, len(j.Tests))
				for n, f := range j.Tests {
					tests[n] = Test{Package: f.Package, Name: f.Name, Lines: append([]string{}, f.Lines...), RunningTest: f.RunningTest}
				}
				jobs[k].Tests = &tests
			}
		}
		out.Runs[i] = Run{ID: r.ID, Workflow: r.Workflow, Status: r.Status, Conclusion: r.Conclusion, URL: r.URL, Jobs: jobs, JobsError: r.JobsErr, SupersededBy: r.SupersededBy}
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
		if r.SupersededBy != "" {
			note = "  (superseded by " + short(r.SupersededBy) + ")"
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
			if j.Tests != nil || j.TestsError != "" {
				writeFailureBlock(w, r, j, maxLines)
			}
		}
	}
	return nil
}

// writeFailureBlock writes the failing tests of the failed job j of r.
func writeFailureBlock(w io.Writer, r Run, j Job, maxLines int) {
	_, _ = fmt.Fprintf(w, "\n%s (%s, run %d):\n", j.Name, r.Workflow, r.ID)
	var tests []Test
	if j.Tests != nil {
		tests = *j.Tests
	}
	switch {
	case j.TestsError != "":
		_, _ = fmt.Fprintln(w, "  (failing tests unavailable)")
	case len(tests) == 0:
		_, _ = fmt.Fprintln(w, "  (no failing tests found in the log)")
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
