package gh_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/devvmhq/ciwatch/internal/gh"
)

// TestRunListDecodesAttempt covers AC-2: the attempt number is how ciwatch
// knows a rerun has started. The fixtures are real reruns of devvmhq/devvm.
func TestRunListDecodesAttempt(t *testing.T) {
	for _, tt := range []struct {
		file       string
		id         int64
		conclusion string
		attempt    int
	}{
		{"run-list-rerun-attempt2.json", 44858310690, "success", 2},
		{"run-list-rerun-attempt2-failure.json", 43529187615, "failure", 2},
		{"run-list-failure.json", 44729305066, "failure", 0}, // recorded before the field was asked for
	} {
		t.Run(tt.file, func(t *testing.T) {
			r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
				return fixture(t, tt.file), nil
			}}
			got, err := r.RunList(t.Context(), sha)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) == 0 || got[0].ID != tt.id || got[0].Conclusion != tt.conclusion || got[0].Attempt != tt.attempt {
				t.Errorf("first run = %+v, want id %d, conclusion %s, attempt %d", got, tt.id, tt.conclusion, tt.attempt)
			}
		})
	}
}

// TestRerunFailedArgs covers AC-2: one gh run rerun <id> --failed call.
func TestRerunFailedArgs(t *testing.T) {
	want := []string{"run", "rerun", "123", "--failed"}
	calls := 0
	r := gh.Runner{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls++
		if name != "gh" || !reflect.DeepEqual(args, want) {
			t.Errorf("exec %s %q, want gh %q", name, args, want)
		}
		return nil, nil
	}}
	if err := r.RerunFailed(t.Context(), 123); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

// TestRerunFailedMarksErrors covers AC-4: the error is gh's, and a network
// one is marked.
func TestRerunFailedMarksErrors(t *testing.T) {
	for _, tt := range []struct {
		name        string
		file        string
		wantNetwork bool
	}{
		{"network", "stderr-network-dial.txt", true},
		{"not found", "stderr-404.txt", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
				return nil, ghFailure(t, tt.file)
			}}
			err := r.RerunFailed(t.Context(), 123)
			if err == nil {
				t.Fatal("err = nil, want the gh error")
			}
			if errors.Is(err, gh.ErrNetwork) != tt.wantNetwork {
				t.Errorf("errors.Is(err, ErrNetwork) = %v, want %v (err %v)", !tt.wantNetwork, tt.wantNetwork, err)
			}
			if want := strings.TrimSpace(string(fixture(t, tt.file))); !strings.Contains(err.Error(), want) {
				t.Errorf("err = %q, want it to contain gh's message %q", err, want)
			}
		})
	}
}
