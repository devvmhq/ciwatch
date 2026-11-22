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
	"strconv"
	"strings"
)

// RunListLimit is how many runs RunList asks for. A list this long may have
// been cut off.
const RunListLimit = 100

// ErrBadRevision marks a ResolveCommit failure caused by the revision itself
// (unknown or malformed), as opposed to git or the repository being unusable.
var ErrBadRevision = errors.New("bad revision")

// Runner runs gh and git. Exec is the test seam.
type Runner struct {
	Exec func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Run is one GitHub Actions workflow run.
type Run struct {
	ID         int64
	Workflow   string
	Status     string
	Conclusion string
	URL        string
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

// RunList lists the workflow runs for the commit sha, once.
func (r Runner) RunList(ctx context.Context, sha string) ([]Run, error) {
	out, err := r.Exec(ctx, "gh", "run", "list", "--commit", sha, "--limit", strconv.Itoa(RunListLimit),
		"--json", "databaseId,workflowName,status,conclusion,url")
	if err != nil {
		return nil, fmt.Errorf("gh run list: %w", err)
	}
	var raw []struct {
		ID         int64  `json:"databaseId"`
		Workflow   string `json:"workflowName"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		URL        string `json:"url"`
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

// RunJobs lists the jobs of the latest attempt of the workflow run runID.
// gh can't narrow the fields inside a job, so the output also has steps and
// timestamps, which are ignored.
func (r Runner) RunJobs(ctx context.Context, runID int64) ([]Job, error) {
	out, err := r.Exec(ctx, "gh", "run", "view", strconv.FormatInt(runID, 10), "--json", "jobs")
	if err != nil {
		return nil, fmt.Errorf("gh run view %d: %w", runID, err)
	}
	var raw struct {
		Jobs []struct {
			ID         int64  `json:"databaseId"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			URL        string `json:"url"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("decoding gh run view output: %w", err)
	}
	jobs := make([]Job, len(raw.Jobs))
	for i, x := range raw.Jobs {
		jobs[i] = Job(x)
	}
	return jobs, nil
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
