package gh_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/devvmhq/ciwatch/internal/gh"
)

// TestArtifactsDecodesCreatedAt covers AC-2 and AC-8: created_at is decoded,
// and a missing or unparseable one leaves the zero time instead of an error.
func TestArtifactsDecodesCreatedAt(t *testing.T) {
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return fixture(t, "api-artifacts-43529187615.json"), nil
	}}
	arts, total, err := r.Artifacts(t.Context(), 43529187615)
	if err != nil {
		t.Fatal(err)
	}
	want := []gh.Artifact{
		{Name: "crash-FuzzOther", CreatedAt: time.Date(2026, 11, 14, 0, 21, 2, 0, time.UTC)},
		{Name: "crash-FuzzParse", CreatedAt: time.Date(2026, 11, 13, 23, 58, 40, 0, time.UTC)},
		{Name: "result-FuzzParse", CreatedAt: time.Date(2026, 11, 14, 0, 20, 31, 0, time.UTC)},
	}
	if total != 3 || len(arts) != len(want) {
		t.Fatalf("total = %d with %d artifacts, want 3 and 3", total, len(arts))
	}
	for i := range want {
		if arts[i].Name != want[i].Name || !arts[i].CreatedAt.Equal(want[i].CreatedAt) {
			t.Errorf("artifact %d = %+v, want %+v", i, arts[i], want[i])
		}
	}
}

// TestArtifactsMissingOrBadCreatedAt covers AC-8.
func TestArtifactsMissingOrBadCreatedAt(t *testing.T) {
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"total_count":3,"artifacts":[{"name":"a"},{"name":"b","created_at":"not a time"},{"name":"c","created_at":null}]}`), nil
	}}
	arts, _, err := r.Artifacts(t.Context(), 1)
	if err != nil || len(arts) != 3 {
		t.Fatalf("artifacts = %+v, err = %v, want 3 artifacts and no error", arts, err)
	}
	for _, a := range arts {
		if !a.CreatedAt.IsZero() {
			t.Errorf("artifact %s CreatedAt = %v, want the zero time", a.Name, a.CreatedAt)
		}
	}
}

// TestAttemptStartedArgsAndDecoding covers AC-2: the call, and the recorded
// raw output parsed as RFC 3339.
func TestAttemptStartedArgsAndDecoding(t *testing.T) {
	want := []string{"api", "repos/{owner}/{repo}/actions/runs/43529187615/attempts/2", "--jq", ".run_started_at"}
	calls := 0
	r := gh.Runner{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls++
		if name != "gh" || !reflect.DeepEqual(args, want) {
			t.Errorf("exec %s %q, want gh %q", name, args, want)
		}
		return fixture(t, "api-attempt-43529187615-2.txt"), nil
	}}
	got, err := r.AttemptStarted(t.Context(), 43529187615, 2)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("gh ran %d times, want once", calls)
	}
	if want := time.Date(2026, 11, 14, 0, 15, 4, 0, time.UTC); !got.Equal(want) {
		t.Errorf("started = %v, want %v", got, want)
	}
}

// TestAttemptStartedRejectsMalformedOutput covers AC-5.
func TestAttemptStartedRejectsMalformedOutput(t *testing.T) {
	for _, out := range []string{"", "null\n", "yesterday\n"} {
		r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(out), nil
		}}
		if _, err := r.AttemptStarted(t.Context(), 1, 2); err == nil {
			t.Errorf("output %q: err = nil, want a parse error", out)
		}
	}
}

// TestAttemptStartedMarksNetworkErrors covers AC-5.
func TestAttemptStartedMarksNetworkErrors(t *testing.T) {
	r := gh.Runner{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return nil, ghFailure(t, "stderr-network-dial.txt")
	}}
	_, err := r.AttemptStarted(t.Context(), 43529187615, 2)
	if !errors.Is(err, gh.ErrNetwork) {
		t.Errorf("err = %v, want one that is ErrNetwork", err)
	}
}
