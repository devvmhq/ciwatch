package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/devvmhq/ciwatch/internal/cli"
)

// The CI run of run-list-rerun-attempt2-failure.json (ciRun2) is at attempt 2.
// Its artifacts are api-artifacts-43529187615.json: crash-FuzzParse created at
// 23:58:40Z, before attempt 2 started at 00:15:04Z (api-attempt-43529187615-2.txt),
// and crash-FuzzOther at 00:21:02Z, after it.

const (
	attemptArtifacts = "api-artifacts-43529187615.json"
	attemptStart     = "api-attempt-43529187615-2.txt"
	earlierWarnLine  = "ciwatch: crash-FuzzParse of run " + ciRun2 + " (CI) was uploaded by attempt 1, not attempt 2"
	cantTellPrefix   = "ciwatch: can't tell which crash inputs of run " + ciRun2 + " (CI) came from an earlier attempt: "
)

// attemptOpts is the fake for the run at attempt 2 with both crash artifacts
// and the recorded start of attempt 2.
func attemptOpts() fakeOpts {
	return fakeOpts{
		artifacts: map[string]string{ciRun2: attemptArtifacts},
		attempts:  map[string]string{ciRun2: attemptStart},
	}
}

// withAttempt makes the run of opts the CI run reported at attempt n; 0 is a
// run gh gave no attempt for.
func withAttempt(t *testing.T, opts fakeOpts, n int) fakeOpts {
	t.Helper()
	repl := ""
	if n > 0 {
		repl = `"attempt":` + strconv.Itoa(n) + ","
	}
	opts.listRaw = []string{strings.Replace(string(readFixture(t, "run-list-rerun-attempt2-failure.json")), `"attempt":2,`, repl, 1)}
	return opts
}

// runAttempt runs ciwatch on the CI run, at attempt 2 unless opts has its own
// list, and returns the output.
func runAttempt(t *testing.T, opts fakeOpts, wantCode int, args ...string) (stdout, stderr string, calls *counts) {
	t.Helper()
	if opts.listRaw == nil {
		opts = withAttempt(t, opts, 2)
	}
	return runRerun(t, opts, wantCode, args...)
}

type attemptCrash struct {
	Artifact       string `json:"artifact"`
	EarlierAttempt bool   `json:"earlier_attempt"`
	FetchedTo      string `json:"fetched_to"`
}

// attemptCrashes runs ciwatch --json and returns the crashes of the run by
// artifact name, and the keys each one has.
func attemptCrashes(t *testing.T, opts fakeOpts, args ...string) (crashes map[string]attemptCrash, keys map[string]map[string]bool, stderr string, calls *counts) {
	t.Helper()
	out, stderr, calls := runAttempt(t, opts, cli.ExitFailed, append([]string{"--json"}, args...)...)
	var doc struct {
		Runs []struct {
			Crashes []json.RawMessage `json:"fuzz_crashes"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc.Runs) != 1 {
		t.Fatalf("stdout %q is not the JSON of 1 run: %v", out, err)
	}
	crashes, keys = map[string]attemptCrash{}, map[string]map[string]bool{}
	for _, raw := range doc.Runs[0].Crashes {
		var c attemptCrash
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		crashes[c.Artifact] = c
		keys[c.Artifact] = map[string]bool{}
		for k := range fields {
			keys[c.Artifact][k] = true
		}
	}
	return crashes, keys, stderr, calls
}

// uploadedLines is the lines of stderr that say which attempt uploaded a crash.
func uploadedLines(stderr string) []string {
	var got []string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, "was uploaded by") {
			got = append(got, line)
		}
	}
	return got
}

// warnLines is the lines of stderr that say the attempt couldn't be checked.
func warnLines(stderr string) []string {
	var got []string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "ciwatch: can't tell which crash inputs") {
			got = append(got, line)
		}
	}
	return got
}

// TestFuzzAttemptOneUnchanged covers AC-1: at attempt 1 there is no attempts
// call, no line and no field, whatever the artifacts' created_at.
func TestFuzzAttemptOneUnchanged(t *testing.T) {
	opts := withAttempt(t, attemptOpts(), 1)
	crashes, keys, stderr, calls := attemptCrashes(t, opts)
	if len(calls.attemptCalls) != 0 {
		t.Errorf("attempts calls = %q, want none at attempt 1", calls.attemptCalls)
	}
	if len(uploadedLines(stderr)) != 0 || len(warnLines(stderr)) != 0 {
		t.Errorf("stderr = %q, want no line about attempts", stderr)
	}
	if len(crashes) != 2 {
		t.Fatalf("crashes = %+v, want crash-FuzzParse and crash-FuzzOther", crashes)
	}
	for name, k := range keys {
		if k["earlier_attempt"] {
			t.Errorf("%s has earlier_attempt at attempt 1", name)
		}
	}
	if want := []string{ciRun2}; !slices.Equal(calls.artifactIDs, want) {
		t.Errorf("artifacts calls for %q, want %q", calls.artifactIDs, want)
	}
}

// TestFuzzEarlierAttemptMarked covers AC-2 and AC-3: only the artifact created
// before attempt 2 started is named on stderr and marked in --json.
func TestFuzzEarlierAttemptMarked(t *testing.T) {
	crashes, keys, stderr, calls := attemptCrashes(t, attemptOpts())
	if got := uploadedLines(stderr); !slices.Equal(got, []string{earlierWarnLine}) {
		t.Errorf("stderr lines about uploads = %q, want exactly %q", got, earlierWarnLine)
	}
	if !crashes["crash-FuzzParse"].EarlierAttempt {
		t.Errorf("crash-FuzzParse = %+v, want earlier_attempt true", crashes["crash-FuzzParse"])
	}
	if keys["crash-FuzzOther"]["earlier_attempt"] || crashes["crash-FuzzOther"].EarlierAttempt {
		t.Errorf("crash-FuzzOther has earlier_attempt, want none: it was created after attempt 2 started")
	}
	if want := []string{ciRun2 + "/2"}; !slices.Equal(calls.attemptCalls, want) {
		t.Errorf("attempts calls = %q, want %q", calls.attemptCalls, want)
	}
}

// TestFuzzEarlierAttemptJSONShape covers AC-2: the field is a plain true on the
// crash, after run_id.
func TestFuzzEarlierAttemptJSONShape(t *testing.T) {
	opts := attemptOpts()
	opts.artifacts = map[string]string{ciRun2: `{"total_count":1,"artifacts":[{"name":"crash-FuzzParse","created_at":"2026-11-13T23:58:40Z"}]}`}
	out, _, _ := runAttempt(t, opts, cli.ExitFailed, "--json")
	want := `"fuzz_crashes":[{"target":"FuzzParse","artifact":"crash-FuzzParse","run_id":` + ciRun2 + `,"earlier_attempt":true}]`
	if !strings.Contains(out, want) {
		t.Errorf("stdout = %s, want it to contain %s", out, want)
	}
}

// TestFuzzEarlierAttemptTable covers AC-2: the exit code is as before, the
// input is still listed in the table with its hint, and the line comes on
// stderr.
func TestFuzzEarlierAttemptTable(t *testing.T) {
	stdout, stderr, _ := runAttempt(t, attemptOpts(), cli.ExitFailed)
	if !strings.Contains(stdout, "  FuzzParse (crash-FuzzParse, run "+ciRun2+")\n") {
		t.Errorf("stdout = %q, want the earlier-attempt input still listed", stdout)
	}
	if !strings.Contains(stderr, earlierWarnLine+"\n") {
		t.Errorf("stderr = %q, want %q", stderr, earlierWarnLine)
	}
}

// TestFuzzEarlierAttemptFetched covers AC-2: --fetch-crashes still downloads
// the input from the earlier attempt, to the same place as any other.
func TestFuzzEarlierAttemptFetched(t *testing.T) {
	opts, root, _ := realCheckout(t, nil, map[string]string{"abc": "x"})
	opts.artifacts = map[string]string{ciRun2: attemptArtifacts}
	opts.attempts = map[string]string{ciRun2: attemptStart}
	opts.downloadFiles["crash-FuzzOther"] = map[string]string{"def": "y"}
	writeOther(t, root)
	crashes, _, stderr, calls := attemptCrashes(t, opts, "--fetch-crashes")
	if len(calls.downloads) != 2 {
		t.Fatalf("downloads = %q, want both artifacts", calls.downloads)
	}
	if got := crashes["crash-FuzzParse"]; got.FetchedTo != "internal/p/testdata/fuzz/FuzzParse" || !got.EarlierAttempt {
		t.Errorf("crash-FuzzParse = %+v, want fetched_to set and earlier_attempt true", got)
	}
	if got := crashes["crash-FuzzOther"]; got.FetchedTo != "internal/q/testdata/fuzz/FuzzOther" || got.EarlierAttempt {
		t.Errorf("crash-FuzzOther = %+v, want fetched_to set and no earlier_attempt", got)
	}
	if got := uploadedLines(stderr); !slices.Equal(got, []string{earlierWarnLine}) {
		t.Errorf("stderr lines about uploads = %q, want exactly %q", got, earlierWarnLine)
	}
}

// TestFuzzEarlierAttemptThirdAttempt covers AC-4: from attempt 3, the line
// can't name the attempt.
func TestFuzzEarlierAttemptThirdAttempt(t *testing.T) {
	opts := withAttempt(t, attemptOpts(), 3)
	opts.attempts = map[string]string{ciRun2: attemptStart}
	_, stderr, calls := runAttempt(t, opts, cli.ExitFailed)
	want := "ciwatch: crash-FuzzParse of run " + ciRun2 + " (CI) was uploaded by an attempt before attempt 3"
	if got := uploadedLines(stderr); !slices.Equal(got, []string{want}) {
		t.Errorf("stderr lines about uploads = %q, want exactly %q", got, want)
	}
	if want := []string{ciRun2 + "/3"}; !slices.Equal(calls.attemptCalls, want) {
		t.Errorf("attempts calls = %q, want %q", calls.attemptCalls, want)
	}
}

// TestFuzzAttemptCallFails covers AC-5: a failed attempts call warns once and
// marks nothing; network errors are retried first.
func TestFuzzAttemptCallFails(t *testing.T) {
	tests := []struct {
		name      string
		set       func(*fakeOpts)
		wantCalls int
		wantMarks bool
		wantWarn  bool
		wantInErr string
	}{
		{"plain error", func(o *fakeOpts) {
			o.attemptsErr = map[string]error{ciRun2: errors.New("gh: exit status 1: HTTP 404: Not Found")}
		}, 1, false, true, "HTTP 404"},
		{"network error that clears", func(o *fakeOpts) {
			o.attemptsNet = map[string]int{ciRun2: 2}
		}, 3, true, false, ""},
		{"network error that never clears", func(o *fakeOpts) {
			o.attemptsNet = map[string]int{ciRun2: 100}
		}, 1 + 8, false, true, "dial"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := attemptOpts()
			tt.set(&opts)
			crashes, keys, stderr, calls := attemptCrashes(t, opts)
			if len(calls.attemptCalls) != tt.wantCalls {
				t.Errorf("attempts calls = %d, want %d", len(calls.attemptCalls), tt.wantCalls)
			}
			warns := warnLines(stderr)
			if tt.wantWarn {
				if len(warns) != 1 || !strings.HasPrefix(warns[0], cantTellPrefix) || !strings.Contains(warns[0], tt.wantInErr) {
					t.Errorf("warnings = %q, want one starting %q and naming %q", warns, cantTellPrefix, tt.wantInErr)
				}
			} else if len(warns) != 0 {
				t.Errorf("warnings = %q, want none", warns)
			}
			if got := crashes["crash-FuzzParse"].EarlierAttempt; got != tt.wantMarks {
				t.Errorf("crash-FuzzParse earlier_attempt = %v, want %v", got, tt.wantMarks)
			}
			if len(crashes) != 2 || keys["crash-FuzzOther"]["earlier_attempt"] {
				t.Errorf("crashes = %+v (keys %v), want both listed and crash-FuzzOther unmarked", crashes, keys)
			}
			if !tt.wantMarks && len(uploadedLines(stderr)) != 0 {
				t.Errorf("stderr = %q, want no upload line", stderr)
			}
		})
	}
}

// TestFuzzInterruptDuringAttemptCall covers AC-6: Ctrl-C during the attempts
// call, or its backoff, prints context canceled, no table, and exits 4,
// before anything is fetched.
func TestFuzzInterruptDuringAttemptCall(t *testing.T) {
	tests := []struct {
		name string
		set  func(opts *fakeOpts, cancel context.CancelFunc)
	}{
		{"during the call", func(opts *fakeOpts, cancel context.CancelFunc) {
			opts.onAttempt = cancel
		}},
		{"during the backoff", func(opts *fakeOpts, cancel context.CancelFunc) {
			opts.attemptsNet = map[string]int{ciRun2: 100}
			asked := false
			opts.onAttempt = func() { asked = true }
			opts.onWait = func() {
				if asked {
					cancel()
				}
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			opts, _, _ := realCheckout(t, nil, map[string]string{"abc": "x"})
			opts.artifacts = map[string]string{ciRun2: attemptArtifacts}
			opts.attempts = map[string]string{ciRun2: attemptStart}
			tt.set(&opts, cancel)
			opts = withAttempt(t, opts, 2)
			app, calls := fakeWith(t, "", nil, nil, opts)
			var out, errOut bytes.Buffer
			if code := app.Run(ctx, []string{"--fetch-crashes", "HEAD"}, &out, &errOut); code != cli.ExitUnchecked {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitUnchecked, errOut.String())
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want no table", out.String())
			}
			if lines := lastLines(errOut.String()); len(lines) == 0 || lines[len(lines)-1] != "ciwatch: context canceled" {
				t.Errorf("stderr = %q, want it to end with ciwatch: context canceled", errOut.String())
			}
			if len(calls.downloads) != 0 {
				t.Errorf("downloads = %q, want none", calls.downloads)
			}
		})
	}
}

// TestFuzzNoAttemptCallWithoutCrashesOrAttempt covers AC-7: no attempts call
// at attempt 0, or at attempt 2 with no crash artifact.
func TestFuzzNoAttemptCallWithoutCrashesOrAttempt(t *testing.T) {
	tests := []struct {
		name string
		opts func(t *testing.T) fakeOpts
	}{
		{"attempt 0", func(t *testing.T) fakeOpts { return withAttempt(t, attemptOpts(), 0) }},
		{"no artifacts", func(*testing.T) fakeOpts { return fakeOpts{} }},
		{"only a result artifact", func(*testing.T) fakeOpts {
			return fakeOpts{artifacts: map[string]string{ciRun2: `{"total_count":1,"artifacts":[{"name":"result-FuzzParse","created_at":"2026-11-13T23:58:40Z"}]}`}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, stderr, calls := runAttempt(t, tt.opts(t), cli.ExitFailed)
			if len(calls.attemptCalls) != 0 {
				t.Errorf("attempts calls = %q, want none", calls.attemptCalls)
			}
			if len(uploadedLines(stderr)) != 0 || len(warnLines(stderr)) != 0 {
				t.Errorf("stderr = %q, want no line about attempts", stderr)
			}
		})
	}
}

// TestFuzzMissingCreatedAtUnmarked covers AC-8: an artifact without a usable
// created_at is not marked and gets no line.
func TestFuzzMissingCreatedAtUnmarked(t *testing.T) {
	for name, art := range map[string]string{
		"missing":     `{"name":"crash-FuzzParse"}`,
		"unparseable": `{"name":"crash-FuzzParse","created_at":"last Tuesday"}`,
		"recorded":    "",
	} {
		t.Run(name, func(t *testing.T) {
			opts := attemptOpts()
			if art != "" {
				opts.artifacts = map[string]string{ciRun2: `{"total_count":1,"artifacts":[` + art + `]}`}
			} else {
				opts.artifacts = map[string]string{ciRun2: "api-artifacts-44729305066.json"}
			}
			crashes, keys, stderr, _ := attemptCrashes(t, opts)
			if len(crashes) == 0 {
				t.Fatal("no crash listed, want the artifact listed")
			}
			for artifact, k := range keys {
				if k["earlier_attempt"] {
					t.Errorf("%s has earlier_attempt, want none without a created_at", artifact)
				}
			}
			if len(uploadedLines(stderr)) != 0 {
				t.Errorf("stderr = %q, want no upload line", stderr)
			}
		})
	}
}
