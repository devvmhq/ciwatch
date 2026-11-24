package gh_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/devvmhq/ciwatch/internal/gh"
)

const artifactsJQ = "{total_count, artifacts: [.artifacts[] | {name, created_at}]}"

// TestArtifactsArgsAndDecoding covers AC-1 and AC-2 (created_at is decoded).
func TestArtifactsArgsAndDecoding(t *testing.T) {
	want := []string{"api", "repos/{owner}/{repo}/actions/runs/44729305066/artifacts?per_page=100", "--jq", artifactsJQ}
	calls := 0
	r := gh.Runner{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls++
		if name != "gh" || !reflect.DeepEqual(args, want) {
			t.Errorf("exec %s %q, want gh %q", name, args, want)
		}
		return fixture(t, "api-artifacts-44729305066.json"), nil
	}}
	arts, total, err := r.Artifacts(t.Context(), 44729305066)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("gh ran %d times, want once", calls)
	}
	if total != 42 || len(arts) != 42 {
		t.Fatalf("total = %d with %d artifacts, want 42 and 42", total, len(arts))
	}
	if arts[0] != (gh.Artifact{Name: "result-FuzzParsePorts"}) || arts[1] != (gh.Artifact{Name: "crash-FuzzPromisorHostSite"}) {
		t.Errorf("first artifacts = %+v, %+v, want result-FuzzParsePorts then crash-FuzzPromisorHostSite, in the API's order", arts[0], arts[1])
	}
}

// TestArtifactsReportsTotalCount covers AC-1: the total can exceed what came back.
func TestArtifactsReportsTotalCount(t *testing.T) {
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"total_count":150,"artifacts":[{"name":"a"},{"name":"b"}]}`), nil
	}}
	arts, total, err := r.Artifacts(t.Context(), 1)
	if err != nil || total != 150 || len(arts) != 2 {
		t.Errorf("artifacts = %+v, total = %d, err = %v, want 2 artifacts and a total of 150", arts, total, err)
	}
}

// TestArtifactsRejectsMalformedOutput covers AC-6.
func TestArtifactsRejectsMalformedOutput(t *testing.T) {
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("not json"), nil
	}}
	if _, _, err := r.Artifacts(t.Context(), 1); err == nil {
		t.Error("err = nil, want a decoding error")
	}
}

// TestArtifactsMarksNetworkErrors covers AC-6.
func TestArtifactsMarksNetworkErrors(t *testing.T) {
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return nil, ghFailure(t, "stderr-network-dial.txt")
	}}
	_, _, err := r.Artifacts(t.Context(), 44729305066)
	if !errors.Is(err, gh.ErrNetwork) {
		t.Errorf("err = %v, want one that is ErrNetwork", err)
	}
}

// TestDownloadArtifactArgs covers AC-3.
func TestDownloadArtifactArgs(t *testing.T) {
	want := []string{"run", "download", "44729305066", "-n", "crash-FuzzPromisorHostSite", "-D", "/repo/internal/p/testdata/fuzz/FuzzPromisorHostSite"}
	calls := 0
	r := gh.Runner{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls++
		if name != "gh" || !reflect.DeepEqual(args, want) {
			t.Errorf("exec %s %q, want gh %q", name, args, want)
		}
		return nil, nil
	}}
	if err := r.DownloadArtifact(t.Context(), 44729305066, "crash-FuzzPromisorHostSite", "/repo/internal/p/testdata/fuzz/FuzzPromisorHostSite"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("gh ran %d times, want once", calls)
	}
}

// TestDownloadArtifactErrors covers AC-5b and AC-6.
func TestDownloadArtifactErrors(t *testing.T) {
	tests := []struct {
		name        string
		file        string
		wantNetwork bool
	}{
		{"dial", "stderr-network-dial.txt", true},
		{"HTTP 404", "stderr-404.txt", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
				return nil, ghFailure(t, tt.file)
			}}
			err := r.DownloadArtifact(t.Context(), 1, "crash-FuzzParse", "/tmp/x")
			if err == nil {
				t.Fatal("err = nil, want the gh error")
			}
			if got := errors.Is(err, gh.ErrNetwork); got != tt.wantNetwork {
				t.Errorf("errors.Is(err, ErrNetwork) = %v, want %v (err %v)", got, tt.wantNetwork, err)
			}
		})
	}
}

// TestToplevelArgs covers AC-3.
func TestToplevelArgs(t *testing.T) {
	want := []string{"rev-parse", "--show-toplevel"}
	r := gh.Runner{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "git" || !reflect.DeepEqual(args, want) {
			t.Errorf("exec %s %q, want git %q", name, args, want)
		}
		return []byte("/Users/me/repo\n"), nil
	}}
	got, err := r.Toplevel(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Users/me/repo" {
		t.Errorf("toplevel = %q, want /Users/me/repo without the newline", got)
	}
}

// TestToplevelReturnsGitError covers AC-3.
func TestToplevelReturnsGitError(t *testing.T) {
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("fatal: not a git repository")
	}}
	_, err := r.Toplevel(t.Context())
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("err = %v, want the git error", err)
	}
}
