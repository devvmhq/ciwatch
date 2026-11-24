package gh_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/devvmhq/ciwatch/internal/gh"
)

const annotationsJQ = "[.[] | {message}]"

// TestAnnotationsArgsAndDecoding covers AC-1: the recorded annotations of the
// timed-out job decode into their two messages, in the API's order.
func TestAnnotationsArgsAndDecoding(t *testing.T) {
	want := []string{"api", "repos/{owner}/{repo}/check-runs/138258094336/annotations?per_page=100", "--jq", annotationsJQ}
	calls := 0
	r := gh.Runner{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls++
		if name != "gh" || !reflect.DeepEqual(args, want) {
			t.Errorf("exec %s %q, want gh %q", name, args, want)
		}
		return fixture(t, "annotations-138258094336.json"), nil
	}}
	got, err := r.Annotations(t.Context(), 138258094336)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("gh ran %d times, want once", calls)
	}
	wantMessages := []string{"The job has exceeded the maximum execution time of 1m0s", "The operation was canceled."}
	if !reflect.DeepEqual(got, wantMessages) {
		t.Errorf("messages = %q, want %q", got, wantMessages)
	}
}

// TestAnnotationsMarksNetworkErrors covers AC-3: a network error is marked so
// the caller retries it, and another error isn't.
func TestAnnotationsMarksNetworkErrors(t *testing.T) {
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
			_, err := r.Annotations(t.Context(), 138258094336)
			if err == nil {
				t.Fatal("err = nil, want the gh error")
			}
			if got := errors.Is(err, gh.ErrNetwork); got != tt.wantNetwork {
				t.Errorf("errors.Is(err, ErrNetwork) = %v, want %v (err %v)", got, tt.wantNetwork, err)
			}
		})
	}
}

// TestAnnotationsRejectsMalformedOutput covers AC-3: output that isn't JSON is
// an error, which the caller turns into a warning.
func TestAnnotationsRejectsMalformedOutput(t *testing.T) {
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("not json"), nil
	}}
	if _, err := r.Annotations(t.Context(), 1); err == nil {
		t.Error("err = nil, want a decoding error")
	}
}

// TestJobLogArgs covers AC-1: the full log of the job is asked for with --log,
// with --attempt only when the attempt is 1 or more.
func TestJobLogArgs(t *testing.T) {
	tests := []struct {
		name    string
		attempt int
		want    []string
	}{
		{"no attempt", 0, []string{"run", "view", "45339081512", "--log", "--job", "138258094336"}},
		{"attempt 2", 2, []string{"run", "view", "45339081512", "--attempt", "2", "--log", "--job", "138258094336"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			r := gh.Runner{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
				calls++
				if name != "gh" || !reflect.DeepEqual(args, tt.want) {
					t.Errorf("exec %s %q, want gh %q", name, args, tt.want)
				}
				return []byte("the log\n"), nil
			}}
			got, err := r.JobLog(t.Context(), 45339081512, tt.attempt, 138258094336)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Errorf("gh ran %d times, want once", calls)
			}
			if string(got) != "the log\n" {
				t.Errorf("log = %q, want what gh printed", got)
			}
		})
	}
}

// TestJobLogMarksNetworkErrors covers AC-4: the full-log call marks network
// errors like the failed-log call.
func TestJobLogMarksNetworkErrors(t *testing.T) {
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return nil, ghFailure(t, "stderr-network-dial.txt")
	}}
	_, err := r.JobLog(t.Context(), 45339081512, 0, 138258094336)
	if !errors.Is(err, gh.ErrNetwork) {
		t.Errorf("err = %v, want one that is ErrNetwork", err)
	}
	r = gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return nil, ghFailure(t, "stderr-404.txt")
	}}
	if _, err := r.JobLog(t.Context(), 45339081512, 0, 138258094336); err == nil || errors.Is(err, gh.ErrNetwork) {
		t.Errorf("err = %v, want a gh error that isn't ErrNetwork", err)
	}
}
