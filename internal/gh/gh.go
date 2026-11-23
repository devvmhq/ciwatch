// Package gh runs the gh and git commands ciwatch needs and decodes their
// output. Every call goes through Runner.Exec, which tests replace.
package gh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RunListLimit is how many runs RunList asks for. A list this long may have
// been cut off.
const RunListLimit = 100

// ErrBadRevision marks a ResolveCommit failure caused by the revision itself
// (unknown or malformed), as opposed to git or the repository being unusable.
var ErrBadRevision = errors.New("bad revision")

// ErrNetwork marks a gh failure caused by the network (no route, no DNS, a
// dropped connection), which may pass if tried again.
var ErrNetwork = errors.New("network error")

// ErrTemporary marks a gh failure where GitHub answered that it is
// temporarily unavailable (HTTP 502, 503 or 504), which may pass if tried again.
var ErrTemporary = errors.New("temporary error")

var networkMarkers = []string{
	"dial tcp", "dial udp", "no such host", "connection refused",
	"connection reset by peer", "network is unreachable", "i/o timeout",
	"TLS handshake timeout", "can't assign requested address",
	"error connecting to api.github.com",
}

// quotedURL is a URL gh quotes in an error, in quotes or parentheses. It names
// what was asked for, not what went wrong, so the markers aren't looked for in it.
var quotedURL = regexp.MustCompile(`https?://[^")\s]*`)

var temporaryMarkers = []string{"HTTP 502", "HTTP 503", "HTTP 504"}

// marked is err that also matches the sentinel kind under errors.Is. Its text
// is err's alone, so messages don't repeat the kind.
type marked struct {
	kind error
	err  error
}

func (m marked) Error() string   { return m.err.Error() }
func (m marked) Unwrap() []error { return []error{m.kind, m.err} }

// markNetwork marks err with ErrNetwork or ErrTemporary when its text says the
// failure was a network error or a temporary GitHub one. The markers are looked
// for in gh's message only, not in the URLs it quotes. Context errors are never
// marked.
func markNetwork(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	msg := quotedURL.ReplaceAllString(err.Error(), "")
	for _, m := range networkMarkers {
		if strings.Contains(msg, m) {
			return marked{ErrNetwork, err}
		}
	}
	for _, m := range temporaryMarkers {
		if strings.Contains(msg, m) {
			return marked{ErrTemporary, err}
		}
	}
	return err
}

// Runner runs gh and git. Exec is the test seam.
type Runner struct {
	Exec func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Run is one GitHub Actions workflow run.
type Run struct {
	ID         int64
	WorkflowID int64
	Workflow   string
	Status     string
	Conclusion string
	URL        string
	Created    time.Time
	Updated    time.Time
	HeadBranch string
	HeadSHA    string
	Event      string
	Attempt    int // 1 for the first attempt, higher after a rerun; 0 if gh didn't say
}

// Job is one job of a workflow run.
type Job struct {
	ID         int64
	Name       string
	Status     string
	Conclusion string
	URL        string
}

// Exec runs the command name with args and returns its stdout. When it fails,
// the error includes the command's stderr.
func Exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // argv list, no shell, so arguments can't be interpreted as commands
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s: %w: %s", name, err, msg)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return stdout.Bytes(), nil
}

const runFields = "databaseId,workflowDatabaseId,workflowName,status,conclusion,url,createdAt,updatedAt,headBranch,headSha,event,attempt"

// RunList lists the workflow runs for the commit sha, once.
func (r Runner) RunList(ctx context.Context, sha string) ([]Run, error) {
	return r.listRuns(ctx, "--commit", sha, "--limit", strconv.Itoa(RunListLimit))
}

// LatestRun finds the newest run of the workflow workflowID on branch, once. ok
// is false if there is none. The workflow is named by ID because its name can
// be a file path, or shared by two workflows, which gh can't select by.
func (r Runner) LatestRun(ctx context.Context, branch string, workflowID int64) (run Run, ok bool, err error) {
	runs, err := r.listRuns(ctx, "--branch", branch, "--workflow", strconv.FormatInt(workflowID, 10), "--limit", "1")
	if err != nil || len(runs) == 0 {
		return Run{}, false, err
	}
	return runs[0], true, nil
}

// listRuns runs gh run list with the filter flags and decodes the runs.
func (r Runner) listRuns(ctx context.Context, filter ...string) ([]Run, error) {
	args := append([]string{"run", "list"}, filter...)
	args = append(args, "--json", runFields)
	out, err := r.Exec(ctx, "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("gh run list: %w", markNetwork(err))
	}
	var raw []struct {
		ID         int64     `json:"databaseId"`
		WorkflowID int64     `json:"workflowDatabaseId"`
		Workflow   string    `json:"workflowName"`
		Status     string    `json:"status"`
		Conclusion string    `json:"conclusion"`
		URL        string    `json:"url"`
		Created    time.Time `json:"createdAt"`
		Updated    time.Time `json:"updatedAt"`
		HeadBranch string    `json:"headBranch"`
		HeadSHA    string    `json:"headSha"`
		Event      string    `json:"event"`
		Attempt    int       `json:"attempt"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("decoding gh run list output: %w", err)
	}
	runs := make([]Run, len(raw))
	for i, x := range raw {
		runs[i] = Run(x)
	}
	return runs, nil
}

// Step is one step of a job. Its times are whole seconds.
type Step struct {
	Name       string
	Conclusion string
	Started    time.Time
	Completed  time.Time
}

// JobSteps is a job and its steps.
type JobSteps struct {
	Job
	Steps []Step
}

// RunJobs lists the jobs of the latest attempt of the workflow run runID.
// Only tests call it; ciwatch itself uses RunJobsWithSteps.
func (r Runner) RunJobs(ctx context.Context, runID int64) ([]Job, error) {
	withSteps, err := r.RunJobsWithSteps(ctx, runID)
	if err != nil {
		return nil, err
	}
	jobs := make([]Job, len(withSteps))
	for i, j := range withSteps {
		jobs[i] = j.Job
	}
	return jobs, nil
}

// RunJobsWithSteps lists the jobs of the latest attempt of the workflow run
// runID, each with its steps. gh can't narrow the fields inside a job, so the
// one call gives both.
func (r Runner) RunJobsWithSteps(ctx context.Context, runID int64) ([]JobSteps, error) {
	out, err := r.Exec(ctx, "gh", "run", "view", strconv.FormatInt(runID, 10), "--json", "jobs")
	if err != nil {
		return nil, fmt.Errorf("gh run view %d: %w", runID, markNetwork(err))
	}
	var raw struct {
		Jobs []struct {
			ID         int64  `json:"databaseId"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			URL        string `json:"url"`
			Steps      []struct {
				Name        string    `json:"name"`
				Conclusion  string    `json:"conclusion"`
				StartedAt   time.Time `json:"startedAt"`
				CompletedAt time.Time `json:"completedAt"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("decoding gh run view output: %w", err)
	}
	jobs := make([]JobSteps, len(raw.Jobs))
	for i, x := range raw.Jobs {
		jobs[i].Job = Job{ID: x.ID, Name: x.Name, Status: x.Status, Conclusion: x.Conclusion, URL: x.URL}
		for _, s := range x.Steps {
			jobs[i].Steps = append(jobs[i].Steps, Step{Name: s.Name, Conclusion: s.Conclusion, Started: s.StartedAt, Completed: s.CompletedAt})
		}
	}
	return jobs, nil
}

// FailedLog returns the log of the failed steps of the job jobID in the run
// runID, as gh prints it. This is the one gh call without --json: gh run view
// --log-failed prints text and has no JSON form. gh serves it only once the run
// has completed.
func (r Runner) FailedLog(ctx context.Context, runID, jobID int64) ([]byte, error) {
	run, job := strconv.FormatInt(runID, 10), strconv.FormatInt(jobID, 10)
	out, err := r.Exec(ctx, "gh", "run", "view", run, "--log-failed", "--job", job)
	if err != nil {
		return nil, fmt.Errorf("gh run view %s --job %s: %w", run, job, markNetwork(err))
	}
	return out, nil
}

// RerunFailed asks GitHub to rerun the failed jobs of the workflow run runID,
// once. gh run rerun prints nothing useful and has no JSON form, so this is
// one of the calls without --json. A rerun isn't safe to repeat, so callers
// don't retry it.
func (r Runner) RerunFailed(ctx context.Context, runID int64) error {
	id := strconv.FormatInt(runID, 10)
	// The error is gh's alone: the caller's message already names the run.
	_, err := r.Exec(ctx, "gh", "run", "rerun", id, "--failed")
	return markNetwork(err)
}

// ArtifactsLimit is how many artifacts Artifacts asks for. A run with more has
// artifacts it doesn't return.
const ArtifactsLimit = 100

// Artifact is one artifact a workflow run uploaded.
type Artifact struct {
	Name string
}

// Artifacts lists the first ArtifactsLimit artifacts of the run runID, once,
// and total is how many the run has. gh api prints the API's JSON, which --jq
// cuts down to the fields ciwatch reads.
func (r Runner) Artifacts(ctx context.Context, runID int64) (arts []Artifact, total int, err error) {
	id := strconv.FormatInt(runID, 10)
	out, err := r.Exec(ctx, "gh", "api", "repos/{owner}/{repo}/actions/runs/"+id+"/artifacts?per_page="+strconv.Itoa(ArtifactsLimit),
		"--jq", "{total_count, artifacts: [.artifacts[] | {name}]}")
	if err != nil {
		return nil, 0, fmt.Errorf("gh api artifacts of run %s: %w", id, markNetwork(err))
	}
	var raw struct {
		Total     int `json:"total_count"`
		Artifacts []struct {
			Name string `json:"name"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, 0, fmt.Errorf("decoding gh api output: %w", err)
	}
	for _, a := range raw.Artifacts {
		arts = append(arts, Artifact{Name: a.Name})
	}
	return arts, raw.Total, nil
}

// DownloadArtifact downloads the artifact name of the run runID into dir.
func (r Runner) DownloadArtifact(ctx context.Context, runID int64, name, dir string) error {
	id := strconv.FormatInt(runID, 10)
	if _, err := r.Exec(ctx, "gh", "run", "download", id, "-n", name, "-D", dir); err != nil {
		return fmt.Errorf("gh run download %s -n %s: %w", id, name, markNetwork(err))
	}
	return nil
}

// Toplevel returns the root directory of the git checkout the command runs in.
func (r Runner) Toplevel(ctx context.Context) (string, error) {
	out, err := r.Exec(ctx, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ResolveCommit turns ref into a full commit SHA.
func (r Runner) ResolveCommit(ctx context.Context, ref string) (string, error) {
	out, err := r.Exec(ctx, "git", "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		err = fmt.Errorf("cannot resolve %q: %w", ref, err)
		// git says one of these when the revision is the problem; anything else
		// (no repository, no git, a cancelled context) is a failure to check.
		if msg := err.Error(); strings.Contains(msg, "Needed a single revision") || strings.Contains(msg, "bad revision") {
			return "", errors.Join(ErrBadRevision, err)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
