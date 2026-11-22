package gh_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/devvmhq/ciwatch/internal/gh"
)

const sha = "3bb2dccd75a2754b8d45e522b3c26c22bc66a21a"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "gh", name)) //nolint:gosec // reads a fixture from testdata/ by a name the test supplies
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRunListDecodesRecordedOutput(t *testing.T) {
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return fixture(t, "run-list-failure.json"), nil
	}}
	got, err := r.RunList(t.Context(), sha)
	if err != nil {
		t.Fatal(err)
	}
	want := []gh.Run{
		{ID: 44729305066, Workflow: "Fuzz", Status: "completed", Conclusion: "failure", URL: "https://github.com/devvmhq/devvm/actions/runs/44729305066"},
		{ID: 44709305564, Workflow: "CI", Status: "completed", Conclusion: "success", URL: "https://github.com/devvmhq/devvm/actions/runs/44709305564"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("runs = %+v, want %+v", got, want)
	}
}

func TestRunListPassesCommitAndFields(t *testing.T) {
	want := []string{"run", "list", "--commit", sha, "--limit", "100", "--json", "databaseId,workflowName,status,conclusion,url"}
	r := gh.Runner{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "gh" || !reflect.DeepEqual(args, want) {
			t.Errorf("exec %s %q, want gh %q", name, args, want)
		}
		return []byte("[]"), nil
	}}
	if _, err := r.RunList(t.Context(), sha); err != nil {
		t.Fatal(err)
	}
}

func TestRunListReturnsGhError(t *testing.T) {
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("HTTP 401")
	}}
	_, err := r.RunList(t.Context(), sha)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("err = %v, want one containing HTTP 401", err)
	}
}

func TestResolveCommitReturnsFullSHA(t *testing.T) {
	want := []string{"rev-parse", "--verify", "--end-of-options", "HEAD^{commit}"}
	r := gh.Runner{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "git" || !reflect.DeepEqual(args, want) {
			t.Errorf("exec %s %q, want git %q", name, args, want)
		}
		return []byte(sha + "\n"), nil
	}}
	got, err := r.ResolveCommit(t.Context(), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got != sha {
		t.Errorf("sha = %q, want %q", got, sha)
	}
}

func TestRunListRejectsMalformedOutput(t *testing.T) {
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("not json"), nil
	}}
	if _, err := r.RunList(t.Context(), sha); err == nil {
		t.Error("err = nil, want a decoding error")
	}
}

func TestResolveCommitReturnsGitError(t *testing.T) {
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("fatal: bad revision")
	}}
	_, err := r.ResolveCommit(t.Context(), "nope")
	if err == nil || !strings.Contains(err.Error(), "nope") || !strings.Contains(err.Error(), "bad revision") {
		t.Errorf("err = %v, want one naming the ref and git's message", err)
	}
}

func TestExecPutsStderrInTheError(t *testing.T) {
	_, err := gh.Exec(t.Context(), "git", "--no-such-option")
	if err == nil || !strings.Contains(err.Error(), "no-such-option") {
		t.Errorf("err = %v, want one containing git's stderr", err)
	}
}
