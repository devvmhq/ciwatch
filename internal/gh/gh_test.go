package gh_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

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
		{ID: 44729305066, WorkflowID: 409041459, Workflow: "Fuzz", Status: "completed", Conclusion: "failure", URL: "https://github.com/devvmhq/devvm/actions/runs/44729305066", Created: time.Date(2026, 11, 20, 11, 38, 53, 0, time.UTC), Updated: time.Date(2026, 11, 20, 12, 13, 8, 0, time.UTC), HeadBranch: "main", HeadSHA: "c4b4c0242478a612ca295d5ff223709e84a2d172", Event: "schedule"},
		{ID: 44709305564, WorkflowID: 409041453, Workflow: "CI", Status: "completed", Conclusion: "success", URL: "https://github.com/devvmhq/devvm/actions/runs/44709305564", Created: time.Date(2026, 11, 20, 9, 16, 24, 0, time.UTC), Updated: time.Date(2026, 11, 20, 9, 29, 21, 0, time.UTC), HeadBranch: "main", HeadSHA: "c4b4c0242478a612ca295d5ff223709e84a2d172", Event: "push"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("runs = %+v, want %+v", got, want)
	}
}

func TestRunListPassesCommitAndFields(t *testing.T) {
	want := []string{"run", "list", "--commit", sha, "--limit", "100", "--json", headFields}
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

func TestRunsSinceArgs(t *testing.T) { // AC-1
	want := []string{"run", "list", "--branch", "main", "--workflow", "409041453", "--created", ">=2026-11-13T03:47:33Z", "--limit", "100", "--json", headFields}
	r := gh.Runner{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "gh" || !reflect.DeepEqual(args, want) {
			t.Errorf("exec %s %q, want gh %q", name, args, want)
		}
		return fixture(t, "run-list-since-43361281082.json"), nil
	}}
	// The time is given in another zone; the flag carries it in UTC.
	since := time.Date(2026, 11, 13, 5, 47, 33, 0, time.FixedZone("CEST", 2*60*60))
	got, err := r.RunsSince(t.Context(), "main", 409041453, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 25 || got[len(got)-1].ID != 43361281082 || got[len(got)-2].ID != 43361860563 {
		t.Errorf("runs = %+v, want the 25 recorded runs", got)
	}
}

func TestRunListDecodesCreatedAt(t *testing.T) {
	var args []string
	r := gh.Runner{Exec: func(_ context.Context, _ string, a ...string) ([]byte, error) {
		args = a
		return fixture(t, "run-list-in-progress.json"), nil
	}}
	got, err := r.RunList(t.Context(), sha)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), "createdAt") {
		t.Errorf("gh args = %q, want them to ask for createdAt", args)
	}
	want := []time.Time{time.Date(2026, 11, 22, 11, 23, 10, 0, time.UTC), time.Date(2026, 11, 21, 21, 23, 21, 0, time.UTC)}
	if len(got) != len(want) {
		t.Fatalf("got %d runs, want %d", len(got), len(want))
	}
	for i, w := range want {
		if !got[i].Created.Equal(w) {
			t.Errorf("run %d created = %v, want %v", i, got[i].Created, w)
		}
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

func TestResolveCommitErrorKinds(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantBad bool
	}{
		{"needed a single revision", errors.New("git: exit status 128: fatal: Needed a single revision"), true},
		{"bad revision", errors.New("fatal: bad revision 'x'"), true},
		{"not a repository", errors.New("git: exit status 128: fatal: not a git repository"), false},
		{"git missing", exec.ErrNotFound, false},
		{"cancelled", context.Canceled, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) { return nil, tt.err }}
			_, err := r.ResolveCommit(t.Context(), "x")
			if err == nil || errors.Is(err, gh.ErrBadRevision) != tt.wantBad {
				t.Errorf("err = %v, want bad revision = %v", err, tt.wantBad)
			}
		})
	}
}

func TestRunJobsPassesRunIDAndFields(t *testing.T) {
	want := []string{"run", "view", "44729305066", "--json", "jobs"}
	r := gh.Runner{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "gh" || !reflect.DeepEqual(args, want) {
			t.Errorf("exec %s %q, want gh %q", name, args, want)
		}
		return []byte(`{"jobs":[]}`), nil
	}}
	if _, err := r.RunJobs(t.Context(), 44729305066); err != nil {
		t.Fatal(err)
	}
}

func TestRunJobsAttemptPassesAttempt(t *testing.T) {
	want := []string{"run", "view", "7", "--attempt", "1", "--json", "jobs"}
	r := gh.Runner{Exec: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if !reflect.DeepEqual(args, want) {
			t.Errorf("exec gh %q, want %q", args, want)
		}
		return []byte(`{"jobs":[]}`), nil
	}}
	if _, err := r.RunJobsAttempt(t.Context(), 7, 1); err != nil {
		t.Fatal(err)
	}
}

func TestRunJobsWithStepsDecodesSteps(t *testing.T) {
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return fixture(t, "run-view-44705166834.json"), nil
	}}
	got, err := r.RunJobsWithSteps(t.Context(), 44705166834)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d jobs, want 5", len(got))
	}
	job := got[1]
	if job.ID != 136182840710 || job.Conclusion != "failure" {
		t.Errorf("job 1 = %+v, want the failed ubuntu job", job.Job)
	}
	if len(job.Steps) != 10 {
		t.Fatalf("got %d steps, want 10", len(job.Steps))
	}
	want := gh.Step{
		Name:       "Run mise run test",
		Conclusion: "failure",
		Started:    time.Date(2026, 11, 20, 8, 47, 47, 0, time.UTC),
		Completed:  time.Date(2026, 11, 20, 8, 52, 20, 0, time.UTC),
	}
	if step := job.Steps[5]; step != want {
		t.Errorf("step 5 = %+v, want %+v", step, want)
	}
}

func TestRunJobsDecodesRecordedOutput(t *testing.T) {
	view := func(name string) gh.Runner {
		return gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
			return fixture(t, name), nil
		}}
	}
	t.Run("all jobs of a successful run", func(t *testing.T) {
		got, err := view("run-view-44709305564.json").RunJobs(t.Context(), 44709305564)
		if err != nil {
			t.Fatal(err)
		}
		url := "https://github.com/devvmhq/devvm/actions/runs/44709305564/job/"
		want := []gh.Job{
			{ID: 136196796677, Name: "nocgo", Status: "completed", Conclusion: "success", URL: url + "136196796677"},
			{ID: 136196796835, Name: "nilaway", Status: "completed", Conclusion: "success", URL: url + "136196796835"},
			{ID: 136196796910, Name: "vuln", Status: "completed", Conclusion: "success", URL: url + "136196796910"},
			{ID: 136196796943, Name: "check (macos-latest, UTC)", Status: "completed", Conclusion: "success", URL: url + "136196796943"},
			{ID: 136196797115, Name: "check (ubuntu-latest, America/Los_Angeles)", Status: "completed", Conclusion: "success", URL: url + "136196797115"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("jobs = %+v, want %+v", got, want)
		}
	})
	t.Run("a failed job", func(t *testing.T) {
		got, err := view("run-view-44729305066.json").RunJobs(t.Context(), 44729305066)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 43 {
			t.Fatalf("got %d jobs, want 43", len(got))
		}
		url := "https://github.com/devvmhq/devvm/actions/runs/44729305066/job/"
		if want := (gh.Job{ID: 136264653915, Name: "targets", Status: "completed", Conclusion: "success", URL: url + "136264653915"}); got[0] != want {
			t.Errorf("first job = %+v, want %+v", got[0], want)
		}
		failed := gh.Job{ID: 136264712505, Name: "FuzzPromisorHostSite", Status: "completed", Conclusion: "failure", URL: url + "136264712505"}
		if got[40] != failed {
			t.Errorf("job 40 = %+v, want %+v", got[40], failed)
		}
	})
	t.Run("a run in progress", func(t *testing.T) {
		got, err := view("run-view-45017063720-in-progress.json").RunJobs(t.Context(), 45017063720)
		if err != nil {
			t.Fatal(err)
		}
		if got[1].Status != "in_progress" || got[1].Conclusion != "" || got[4].Status != "queued" || got[4].Conclusion != "" {
			t.Errorf("jobs 1 and 4 = %+v and %+v, want in_progress and queued with no conclusion", got[1], got[4])
		}
	})
}

func TestRunJobsReturnsGhError(t *testing.T) {
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("HTTP 502")
	}}
	_, err := r.RunJobs(t.Context(), 1)
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("err = %v, want one containing HTTP 502", err)
	}
}

func TestRunJobsRejectsMalformedOutput(t *testing.T) {
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("not json"), nil
	}}
	if _, err := r.RunJobs(t.Context(), 1); err == nil {
		t.Error("err = nil, want a decoding error")
	}
}

// ghFailure is the error Exec returns when gh fails with the recorded stderr.
func ghFailure(t *testing.T, name string) error {
	t.Helper()
	return fmt.Errorf("gh: exit status 1: %s", strings.TrimSpace(string(fixture(t, name))))
}

func TestRunListMarksNetworkErrors(t *testing.T) {
	tests := []struct {
		name          string
		err           func(t *testing.T) error
		wantNetwork   bool
		wantTemporary bool
	}{
		{"dial", func(t *testing.T) error { return ghFailure(t, "stderr-network-dial.txt") }, true, false},
		{"no such host", func(t *testing.T) error { return ghFailure(t, "stderr-no-such-host.txt") }, true, false},
		{"connection reset", func(t *testing.T) error { return ghFailure(t, "stderr-connection-reset.txt") }, true, false},
		{"error connecting", func(t *testing.T) error { return ghFailure(t, "stderr-error-connecting.txt") }, true, false},
		{"HTTP 502", func(t *testing.T) error { return ghFailure(t, "stderr-502.txt") }, false, true},
		{"HTTP 503", func(t *testing.T) error { return ghFailure(t, "stderr-503.txt") }, false, true},
		{"HTTP 504", func(t *testing.T) error { return ghFailure(t, "stderr-504.txt") }, false, true},
		{"HTTP 401", func(t *testing.T) error { return ghFailure(t, "stderr-401.txt") }, false, false},
		{"HTTP 404", func(t *testing.T) error { return ghFailure(t, "stderr-404.txt") }, false, false},
		{"cancelled", func(*testing.T) error { return context.Canceled }, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.err(t)
			r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) { return nil, want }}
			_, err := r.RunList(t.Context(), sha)
			if err == nil {
				t.Fatal("err = nil, want the gh error")
			}
			if got := errors.Is(err, gh.ErrNetwork); got != tt.wantNetwork {
				t.Errorf("errors.Is(err, ErrNetwork) = %v, want %v (err %v)", got, tt.wantNetwork, err)
			}
			if got := errors.Is(err, gh.ErrTemporary); got != tt.wantTemporary {
				t.Errorf("errors.Is(err, ErrTemporary) = %v, want %v (err %v)", got, tt.wantTemporary, err)
			}
			if !errors.Is(err, want) {
				t.Errorf("err = %v, want it to still wrap the gh error", err)
			}
		})
	}
}

func TestRunJobsMarksNetworkErrors(t *testing.T) {
	tests := []struct {
		name          string
		file          string
		wantNetwork   bool
		wantTemporary bool
	}{
		{"dial", "stderr-network-dial.txt", true, false},
		{"HTTP 503", "stderr-503.txt", false, true},
		{"HTTP 404", "stderr-404.txt", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
				return nil, ghFailure(t, tt.file)
			}}
			_, err := r.RunJobs(t.Context(), 1)
			if err == nil {
				t.Fatal("err = nil, want the gh error")
			}
			if got := errors.Is(err, gh.ErrNetwork); got != tt.wantNetwork {
				t.Errorf("errors.Is(err, ErrNetwork) = %v, want %v (err %v)", got, tt.wantNetwork, err)
			}
			if got := errors.Is(err, gh.ErrTemporary); got != tt.wantTemporary {
				t.Errorf("errors.Is(err, ErrTemporary) = %v, want %v (err %v)", got, tt.wantTemporary, err)
			}
		})
	}
}

func TestMarkNetworkIgnoresMarkersInURLs(t *testing.T) {
	// A 404 whose URL quotes a marker (here a path that says no such host) is not a network error.
	err := errors.New("gh: exit status 1: HTTP 404: Not Found (https://api.github.com/repos/devvmhq/no such host/actions/runs?per_page=100)")
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) { return nil, err }}
	for name, call := range map[string]func() error{
		"run list": func() error { _, err := r.RunList(t.Context(), sha); return err },
		"run view": func() error { _, err := r.RunJobs(t.Context(), 1); return err },
	} {
		t.Run(name, func(t *testing.T) {
			got := call()
			if errors.Is(got, gh.ErrNetwork) || errors.Is(got, gh.ErrTemporary) {
				t.Errorf("err = %v, want it unmarked: the marker is only in a URL", got)
			}
		})
	}
}

const headFields = "databaseId,workflowDatabaseId,workflowName,status,conclusion,url,createdAt,updatedAt,headBranch,headSha,event,attempt"

func TestRunListDecodesHeadBranchAndSHA(t *testing.T) {
	var args []string
	r := gh.Runner{Exec: func(_ context.Context, _ string, a ...string) ([]byte, error) {
		args = a
		return fixture(t, "run-list-superseded.json"), nil
	}}
	got, err := r.RunList(t.Context(), sha)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"run", "list", "--commit", sha, "--limit", "100", "--json", headFields}; !reflect.DeepEqual(args, want) {
		t.Errorf("gh args = %q, want %q", args, want)
	}
	const head = "8da71a01bb18eedaa5b282a4da705350f2dd3f90"
	created := time.Date(2026, 11, 13, 3, 47, 33, 0, time.UTC)
	docsUpdated := time.Date(2026, 11, 13, 3, 49, 18, 0, time.UTC)
	ciUpdated := time.Date(2026, 11, 13, 3, 53, 18, 0, time.UTC)
	want := []gh.Run{
		{ID: 43361281019, WorkflowID: 409041454, Workflow: "Docs", Status: "completed", Conclusion: "success", URL: "https://github.com/devvmhq/devvm/actions/runs/43361281019", Created: created, Updated: docsUpdated, HeadBranch: "main", HeadSHA: head, Event: "push"},
		{ID: 43361281082, WorkflowID: 409041453, Workflow: "CI", Status: "completed", Conclusion: "cancelled", URL: "https://github.com/devvmhq/devvm/actions/runs/43361281082", Created: created, Updated: ciUpdated, HeadBranch: "main", HeadSHA: head, Event: "push"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("runs = %+v, want %+v", got, want)
	}
}

func TestRunsSinceEmpty(t *testing.T) {
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("[]"), nil
	}}
	got, err := r.RunsSince(t.Context(), "main", 409041453, time.Now())
	if err != nil || len(got) != 0 {
		t.Errorf("runs = %+v, err = %v, want no runs and no error", got, err)
	}
}

func TestRunsSinceMarksNetworkErrors(t *testing.T) {
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return nil, ghFailure(t, "stderr-network-dial.txt")
	}}
	_, err := r.RunsSince(t.Context(), "main", 409041453, time.Now())
	if !errors.Is(err, gh.ErrNetwork) {
		t.Errorf("err = %v, want one that is ErrNetwork", err)
	}
}

// TestFailedLogAttempt covers AC-2 and AC-3: an attempt of 1 or more is passed
// to gh before --log-failed, and 0 keeps the call without it. The recorded
// logs are those gh printed for attempts 1 and 2 of run 43529187615.
func TestFailedLogAttempt(t *testing.T) {
	tests := []struct {
		name    string
		run     int64
		attempt int
		log     string
		job     string
		want    []string
	}{
		{
			"attempt 0 asks for none", 44707997310, 0, "gotestsum-failures.log", "136192285080",
			[]string{"run", "view", "44707997310", "--log-failed", "--job", "136192285080"},
		},
		{
			"attempt 1", 43529187615, 1, "devvm-43529187615-attempt1.log", "132242348233",
			[]string{"run", "view", "43529187615", "--attempt", "1", "--log-failed", "--job", "132242348233"},
		},
		{
			"attempt 2", 43529187615, 2, "devvm-43529187615-attempt2.log", "132254736456",
			[]string{"run", "view", "43529187615", "--attempt", "2", "--log-failed", "--job", "132254736456"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, err := os.ReadFile(filepath.Join("..", "..", "testdata", "logs", tt.log))
			if err != nil {
				t.Fatal(err)
			}
			r := gh.Runner{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "gh" || !reflect.DeepEqual(args, tt.want) {
					t.Errorf("exec %s %q, want gh %q", name, args, tt.want)
				}
				return log, nil
			}}
			got, err := r.FailedLog(t.Context(), tt.run, tt.attempt, jobID(t, tt.job))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(log) {
				t.Errorf("log = %d bytes, want the %d bytes gh printed, unchanged", len(got), len(log))
			}
		})
	}
}

func jobID(t *testing.T, s string) int64 {
	t.Helper()
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestFailedLogMarksErrors(t *testing.T) {
	tests := []struct {
		name          string
		file          string
		wantNetwork   bool
		wantTemporary bool
	}{
		{"HTTP 502", "stderr-502.txt", false, true},
		{"dial", "stderr-network-dial.txt", true, false},
		{"HTTP 404", "stderr-404.txt", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
				return nil, ghFailure(t, tt.file)
			}}
			_, err := r.FailedLog(t.Context(), 44707997310, 0, 136192285080)
			if err == nil {
				t.Fatal("err = nil, want the gh error")
			}
			if !strings.Contains(err.Error(), "gh run view 44707997310 --job 136192285080: ") {
				t.Errorf("err = %q, want it to name the call", err)
			}
			if got := errors.Is(err, gh.ErrNetwork); got != tt.wantNetwork {
				t.Errorf("errors.Is(err, ErrNetwork) = %v, want %v (err %v)", got, tt.wantNetwork, err)
			}
			if got := errors.Is(err, gh.ErrTemporary); got != tt.wantTemporary {
				t.Errorf("errors.Is(err, ErrTemporary) = %v, want %v (err %v)", got, tt.wantTemporary, err)
			}
		})
	}
}
