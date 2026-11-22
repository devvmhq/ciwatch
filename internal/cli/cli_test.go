package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/devvmhq/ciwatch/internal/cli"
	"github.com/devvmhq/ciwatch/internal/gh"
)

const sha = "3bb2dccd75a2754b8d45e522b3c26c22bc66a21a"

var ghArgs = []string{"run", "list", "--commit", sha, "--limit", "100", "--json", "databaseId,workflowName,status,conclusion,url"}

// fake returns an App whose git prints sha (or fails with gitErr) and whose gh
// prints the fixture (or fails with ghErr). It fails the test on any other
// command, and counts the gh calls.
func fake(t *testing.T, fixture string, ghErr, gitErr error) (cli.App, *int) {
	t.Helper()
	calls := new(int)
	return cli.App{GH: gh.Runner{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "git":
			if gitErr != nil {
				return nil, gitErr
			}
			return []byte(sha + "\n"), nil
		case "gh":
			*calls++
			if !reflect.DeepEqual(args, ghArgs) {
				t.Errorf("gh args = %q, want %q", args, ghArgs)
			}
			if ghErr != nil {
				return nil, ghErr
			}
			b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "gh", fixture)) //nolint:gosec // reads a fixture from testdata/ by a name the test supplies
			if err != nil {
				t.Fatal(err)
			}
			return b, nil
		}
		t.Errorf("unexpected command %s %q", name, args)
		return nil, errors.New("unexpected command")
	}}}, calls
}

func TestRunExitCodes(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		fixture    string
		ghErr      error
		gitErr     error
		want       int
		stderr     string
		wantGHCall bool
	}{
		{"success", []string{"HEAD"}, "run-list-success.json", nil, nil, cli.ExitOK, "", true},
		{"failure", []string{"HEAD"}, "run-list-failure.json", nil, nil, cli.ExitFailed, "", true},
		{"cancelled", []string{"HEAD"}, "run-list-cancelled.json", nil, nil, cli.ExitCancelled, "", true},
		{"in progress", []string{"HEAD"}, "run-list-in-progress.json", nil, nil, cli.ExitUnchecked, "in progress", true},
		{"empty", []string{"HEAD"}, "run-list-empty.json", nil, nil, cli.ExitUnchecked, "no runs", true},
		{"gh error", []string{"HEAD"}, "", errors.New("HTTP 401"), nil, cli.ExitUnchecked, "HTTP 401", true},
		{"bad commit", []string{"nope"}, "", nil, errors.New("fatal: bad revision"), cli.ExitUsage, "ciwatch:", false},
		{"no args", nil, "", nil, nil, cli.ExitUsage, "usage: ciwatch", false},
		{"two commits", []string{"a", "b"}, "", nil, nil, cli.ExitUsage, "usage: ciwatch", false},
		{"unknown flag", []string{"--bogus", "HEAD"}, "", nil, nil, cli.ExitUsage, "bogus", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, calls := fake(t, tt.fixture, tt.ghErr, tt.gitErr)
			var stdout, stderr bytes.Buffer
			code := app.Run(t.Context(), tt.args, &stdout, &stderr)
			if code != tt.want {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.want, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.stderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.stderr)
			}
			if (*calls > 0) != tt.wantGHCall {
				t.Errorf("gh calls = %d, want called = %v", *calls, tt.wantGHCall)
			}
			// Runs that haven't finished are a result worth showing, so they
			// are the one exit-4 case that prints the table.
			if code != cli.ExitOK && code != cli.ExitFailed && code != cli.ExitCancelled && tt.name != "in progress" && stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing on error", stdout.String())
			}
		})
	}
}

func TestRunWithoutArgumentsPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.App{}.Run(t.Context(), nil, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUsage)
	}
	if !strings.HasPrefix(stderr.String(), "usage: ciwatch") {
		t.Errorf("stderr = %q, want the usage", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}

func TestRunPrintsTable(t *testing.T) {
	app, _ := fake(t, "run-list-failure.json", nil, nil)
	var stdout, stderr bytes.Buffer
	app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr)
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	want := [][]string{
		{"WORKFLOW", "STATUS", "CONCLUSION"},
		{"Fuzz", "completed", "failure"},
		{"CI", "completed", "success"},
	}
	if len(lines) != len(want) {
		t.Fatalf("stdout = %q, want %d lines", stdout.String(), len(want))
	}
	for i, l := range lines {
		if got := strings.Fields(l); !reflect.DeepEqual(got, want[i]) {
			t.Errorf("line %d = %q, want fields %q", i, l, want[i])
		}
	}
}

func TestRunJSON(t *testing.T) {
	for _, args := range [][]string{{"HEAD", "--json"}, {"--json", "HEAD"}} {
		app, _ := fake(t, "run-list-failure.json", nil, nil)
		var stdout, stderr bytes.Buffer
		code := app.Run(t.Context(), args, &stdout, &stderr)
		if code != cli.ExitFailed {
			t.Errorf("%q: exit code = %d, want %d", args, code, cli.ExitFailed)
		}
		var got struct {
			SHA  string `json:"sha"`
			Runs []struct {
				ID         int64  `json:"id"`
				Workflow   string `json:"workflow"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				URL        string `json:"url"`
			} `json:"runs"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("%q: stdout %q is not JSON: %v", args, stdout.String(), err)
		}
		if got.SHA != sha || len(got.Runs) != 2 {
			t.Fatalf("%q: got %+v, want sha %s and 2 runs", args, got, sha)
		}
		if r := got.Runs[0]; r.ID != 44729305066 || r.Workflow != "Fuzz" || r.Status != "completed" || r.Conclusion != "failure" || r.URL == "" {
			t.Errorf("%q: first run = %+v", args, r)
		}
	}
}

func TestRunPassesResolvedSHAToGh(t *testing.T) {
	app, calls := fake(t, "run-list-success.json", nil, nil) // fake asserts gh gets sha, not HEAD
	var stdout, stderr bytes.Buffer
	app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr)
	if *calls != 1 {
		t.Errorf("gh calls = %d, want 1", *calls)
	}
}

func TestRunExitCodePrecedence(t *testing.T) {
	run := func(status, conclusion string) string {
		return `{"databaseId":1,"workflowName":"w","status":"` + status + `","conclusion":"` + conclusion + `","url":"u"}`
	}
	tests := []struct {
		name string
		runs []string
		want int
	}{
		{"skipped and neutral pass", []string{run("completed", "skipped"), run("completed", "neutral")}, cli.ExitOK},
		{"timed out fails", []string{run("completed", "timed_out")}, cli.ExitFailed},
		{"startup failure fails", []string{run("completed", "startup_failure")}, cli.ExitFailed},
		{"action required fails", []string{run("completed", "action_required")}, cli.ExitFailed},
		{"failure beats cancelled", []string{run("completed", "cancelled"), run("completed", "failure")}, cli.ExitFailed},
		{"failure beats in progress", []string{run("in_progress", ""), run("completed", "failure")}, cli.ExitFailed},
		{"cancelled beats in progress", []string{run("queued", ""), run("completed", "cancelled")}, cli.ExitCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := cli.App{GH: gh.Runner{Exec: func(_ context.Context, name string, _ ...string) ([]byte, error) {
				if name == "git" {
					return []byte(sha + "\n"), nil
				}
				return []byte("[" + strings.Join(tt.runs, ",") + "]"), nil
			}}}
			var stdout, stderr bytes.Buffer
			if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != tt.want {
				t.Errorf("exit code = %d, want %d", code, tt.want)
			}
		})
	}
}

func TestRunMalformedGhOutputCouldNotCheck(t *testing.T) {
	app := cli.App{GH: gh.Runner{Exec: func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "git" {
			return []byte(sha + "\n"), nil
		}
		return []byte("oops"), nil
	}}}
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &stdout, &stderr); code != cli.ExitUnchecked {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUnchecked)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}

func TestRunDashDashEndsFlags(t *testing.T) {
	app, calls := fake(t, "run-list-success.json", nil, nil)
	var stdout, stderr bytes.Buffer
	if code := app.Run(t.Context(), []string{"--", "HEAD"}, &stdout, &stderr); code != cli.ExitOK || *calls != 1 {
		t.Errorf("exit code = %d, gh calls = %d, want 0 and 1 (stderr %q)", code, *calls, stderr.String())
	}
}
