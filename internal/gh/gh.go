// Package gh runs the gh and git commands ciwatch needs and decodes their
// output. Every call goes through Runner.Exec, which tests replace.
package gh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

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
	out, err := r.Exec(ctx, "gh", "run", "list", "--commit", sha, "--limit", "100",
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

// ResolveCommit turns ref into a full commit SHA.
func (r Runner) ResolveCommit(ctx context.Context, ref string) (string, error) {
	out, err := r.Exec(ctx, "git", "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("cannot resolve %q: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}
