package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/devvmhq/ciwatch/internal/cli"
)

// fuzzRun is the failed Fuzz run of run-list-failure.json, whose artifacts
// are recorded in api-artifacts-44729305066.json.
const fuzzRun = "44729305066"

const (
	oneCrash    = `{"total_count":1,"artifacts":[{"name":"crash-FuzzParse"}]}`
	parseFuzzGo = "package p\n\nimport \"testing\"\n\nfunc FuzzParse(f *testing.F) {\n}\n"
	fuzzTable   = "\nFuzz crash inputs:\n  FuzzPromisorHostSite (crash-FuzzPromisorHostSite, run " + fuzzRun + ")\n"
)

var fuzzFlagHint = "ciwatch: 1 fuzz crash input not fetched; to fetch it: ciwatch --fetch-crashes " + sha

func recordedCrash() fakeOpts {
	return fakeOpts{artifacts: map[string]string{fuzzRun: "api-artifacts-44729305066.json"}}
}

func crashOf(artifacts string, checkout fstest.MapFS) fakeOpts {
	return fakeOpts{artifacts: map[string]string{fuzzRun: artifacts}, checkout: checkout}
}

// runFuzz runs ciwatch on the failed Fuzz run and returns the output.
func runFuzz(t *testing.T, opts fakeOpts, wantCode int, args ...string) (stdout, stderr string, calls *counts) {
	t.Helper()
	app, calls := fakeWith(t, "run-list-failure.json", nil, nil, opts)
	var out, errOut bytes.Buffer
	if code := app.Run(t.Context(), append(args, "HEAD"), &out, &errOut); code != wantCode {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, wantCode, errOut.String())
	}
	return out.String(), errOut.String(), calls
}

// fuzzJSON runs ciwatch --json on the failed Fuzz run and returns its first run's keys.
func fuzzJSON(t *testing.T, opts fakeOpts, args ...string) (run map[string]json.RawMessage, stderr string, calls *counts) {
	t.Helper()
	out, stderr, calls := runFuzz(t, opts, cli.ExitFailed, append([]string{"--json"}, args...)...)
	var doc struct {
		Runs []map[string]json.RawMessage `json:"runs"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc.Runs) != 2 {
		t.Fatalf("stdout %q is not the JSON of 2 runs: %v", out, err)
	}
	return doc.Runs[0], stderr, calls
}

type jsonCrash struct {
	Target    string `json:"target"`
	Artifact  string `json:"artifact"`
	RunID     int64  `json:"run_id"`
	FetchedTo string `json:"fetched_to"`
	FetchErr  string `json:"fetch_error"`

	AlreadyPresent bool `json:"already_present"`
}

func crashesOf(t *testing.T, run map[string]json.RawMessage) (crashes []jsonCrash, keys map[string]bool) {
	t.Helper()
	raw, ok := run["fuzz_crashes"]
	if !ok {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &crashes); err != nil {
		t.Fatal(err)
	}
	var rawEntries []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawEntries); err != nil {
		t.Fatal(err)
	}
	keys = map[string]bool{}
	for _, e := range rawEntries {
		for k := range e {
			keys[k] = true
		}
	}
	return crashes, keys
}

// TestFuzzArtifactsListedOncePerFailedRun covers AC-1.
func TestFuzzArtifactsListedOncePerFailedRun(t *testing.T) {
	_, _, calls := runFuzz(t, recordedCrash(), cli.ExitFailed)
	// The Fuzz run failed; the CI run beside it passed and gets no call.
	if want := []string{fuzzRun}; !slices.Equal(calls.artifactIDs, want) {
		t.Errorf("artifacts calls for runs %q, want %q", calls.artifactIDs, want)
	}
}

// TestFuzzArtifactsOfTimedOutRun covers AC-1.
func TestFuzzArtifactsOfTimedOutRun(t *testing.T) {
	opts := fakeOpts{
		listFixtures: []string{"run-list-timed-out.json"},
		viewFixture:  map[string]string{failedRun: "run-view-44707997310-timed-out.json"},
		artifacts:    map[string]string{failedRun: oneCrash},
	}
	app, calls := fakeWith(t, "", nil, nil, opts)
	var out, errOut bytes.Buffer
	if code := app.Run(t.Context(), []string{"HEAD"}, &out, &errOut); code != cli.ExitFailed {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, cli.ExitFailed, errOut.String())
	}
	if want := []string{failedRun}; !slices.Equal(calls.artifactIDs, want) {
		t.Errorf("artifacts calls for runs %q, want %q", calls.artifactIDs, want)
	}
	if !strings.Contains(out.String(), "FuzzParse (crash-FuzzParse, run "+failedRun+")") {
		t.Errorf("stdout = %q, want the crash input of the timed-out run", out.String())
	}
}

// TestFuzzArtifactsNotListedForOtherRuns covers AC-1.
func TestFuzzArtifactsNotListedForOtherRuns(t *testing.T) {
	for _, tt := range []struct {
		name, list string
		code       int
	}{
		{"passed", "run-list-success.json", cli.ExitOK},
		{"cancelled", "run-list-cancelled.json", cli.ExitCancelled},
		{"in progress", "run-list-in-progress.json", cli.ExitUnchecked},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := fakeOpts{artifacts: map[string]string{
				"45017063720": oneCrash, "44951283922": oneCrash, "44951284032": oneCrash, failedRun: oneCrash,
			}}
			app, calls := fakeWith(t, tt.list, nil, nil, opts)
			var out, errOut bytes.Buffer
			if code := app.Run(t.Context(), []string{"--once", "HEAD"}, &out, &errOut); code != tt.code {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, tt.code, errOut.String())
			}
			if len(calls.artifactIDs) != 0 {
				t.Errorf("artifacts calls for runs %q, want none", calls.artifactIDs)
			}
			if strings.Contains(out.String(), "Fuzz crash inputs") {
				t.Errorf("stdout = %q, want no fuzz section", out.String())
			}
		})
	}
}

// TestFuzzOnlyCrashFuzzNamesCount covers AC-1.
func TestFuzzOnlyCrashFuzzNamesCount(t *testing.T) {
	arts := `{"total_count":9,"artifacts":[{"name":"coverage"},{"name":"crash-FuzzB_1"},{"name":"crash-parse"},` +
		`{"name":"result-FuzzParse"},{"name":"crash-Fuzz-x"},{"name":"crash-FuzzA"},{"name":"crash-Fuzz../x"},` +
		`{"name":"crash-FuzzA/b"},{"name":"xcrash-FuzzC"}]}`
	run, _, _ := fuzzJSON(t, crashOf(arts, nil))
	got, _ := crashesOf(t, run)
	want := []jsonCrash{
		{Target: "FuzzB_1", Artifact: "crash-FuzzB_1", RunID: 44729305066},
		{Target: "FuzzA", Artifact: "crash-FuzzA", RunID: 44729305066},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fuzz_crashes = %+v, want %+v, in the API's order", got, want)
	}
}

// TestFuzzCrashesInTableWithHint covers AC-2.
func TestFuzzCrashesInTableWithHint(t *testing.T) {
	stdout, stderr, calls := runFuzz(t, recordedCrash(), cli.ExitFailed)
	if !strings.HasSuffix(stdout, fuzzTable) {
		t.Errorf("stdout = %q, want it to end with %q", stdout, fuzzTable)
	}
	// The rerun hint of the failed run comes after the crash hint.
	if want := fuzzFlagHint + "\nciwatch: 1 run failed; to rerun its failed jobs: ciwatch --rerun-failed " + sha + "\n"; !strings.HasSuffix(stderr, want) {
		t.Errorf("stderr = %q, want it to end with %q", stderr, want)
	}
	if len(calls.downloads) != 0 {
		t.Errorf("downloads = %q, want none without --fetch-crashes", calls.downloads)
	}
}

// TestFuzzCrashesDesignExample covers AC-2: the exact lines of the design.
func TestFuzzCrashesDesignExample(t *testing.T) {
	stdout, _, _ := runFuzz(t, crashOf(oneCrash, nil), cli.ExitFailed)
	if want := "\nFuzz crash inputs:\n  FuzzParse (crash-FuzzParse, run " + fuzzRun + ")\n"; !strings.HasSuffix(stdout, want) {
		t.Errorf("stdout = %q, want it to end with %q", stdout, want)
	}
}

// TestFuzzCrashesGolden covers AC-2.
func TestFuzzCrashesGolden(t *testing.T) {
	stdout, _, calls := runFuzz(t, recordedCrash(), cli.ExitFailed, "--json")
	want, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "run-list-fuzz-failure.json"))
	if err != nil {
		t.Fatal(err)
	}
	if stdout != string(want) {
		t.Errorf("output differs from the golden file:\n got: %s\nwant: %s", stdout, want)
	}
	if len(calls.downloads) != 0 {
		t.Errorf("downloads = %q, want none", calls.downloads)
	}
}

// TestFuzzCrashesJSONFields covers AC-2.
func TestFuzzCrashesJSONFields(t *testing.T) {
	run, _, _ := fuzzJSON(t, crashOf(oneCrash, nil))
	crashes, keys := crashesOf(t, run)
	if want := []jsonCrash{{Target: "FuzzParse", Artifact: "crash-FuzzParse", RunID: 44729305066}}; !reflect.DeepEqual(crashes, want) {
		t.Errorf("fuzz_crashes = %+v, want %+v", crashes, want)
	}
	for _, k := range []string{"fetched_to", "fetch_error"} {
		if keys[k] {
			t.Errorf("fuzz_crashes has %q without --fetch-crashes", k)
		}
	}
	if string(run["fuzz_crashes"]) != `[{"target":"FuzzParse","artifact":"crash-FuzzParse","run_id":44729305066}]` {
		t.Errorf("fuzz_crashes = %s", run["fuzz_crashes"])
	}
}

var fetchCheckout = fstest.MapFS{
	"internal/p/p_test.go": {Data: []byte(parseFuzzGo)},
	"internal/p/p.go":      {Data: []byte("package p\n")},
}

// TestFuzzFetchCrashesDownloads covers AC-3.
func TestFuzzFetchCrashesDownloads(t *testing.T) {
	opts, root, temp := realCheckout(t, nil, map[string]string{"abc": "x"})
	stdout, stderr, calls := runFuzz(t, opts, cli.ExitFailed, "--fetch-crashes")
	if len(calls.downloads) != 1 || len(calls.downloads[0]) != 7 || !slices.Equal(calls.downloads[0][:6], []string{"run", "download", fuzzRun, "-n", "crash-FuzzParse", "-D"}) {
		t.Fatalf("downloads = %q, want one gh run download of crash-FuzzParse", calls.downloads)
	}
	if dir := calls.downloads[0][6]; filepath.Dir(dir) != temp || dir == root {
		t.Errorf("download -D = %q, want a new directory under %q", dir, temp)
	}
	if got := readFile(t, filepath.Join(root, "internal/p/testdata/fuzz/FuzzParse/abc")); got != "x" {
		t.Errorf("fetched file = %q, want the downloaded bytes", got)
	}
	if !slices.Equal(calls.opened, []string{root}) {
		t.Errorf("opened %q, want the git toplevel %q", calls.opened, root)
	}
	want := "  FuzzParse (crash-FuzzParse, run " + fuzzRun + "): fetched to internal/p/testdata/fuzz/FuzzParse\n"
	if !strings.HasSuffix(stdout, "\nFuzz crash inputs:\n"+want) {
		t.Errorf("stdout = %q, want it to end with the section with %q", stdout, want)
	}
	if strings.Contains(stderr, "--fetch-crashes") {
		t.Errorf("stderr = %q, want no hint once the input is fetched", stderr)
	}
}

// TestFuzzFetchCrashesJSON covers AC-3.
func TestFuzzFetchCrashesJSON(t *testing.T) {
	opts, _, _ := realCheckout(t, nil, map[string]string{"abc": "x"})
	run, stderr, _ := fuzzJSON(t, opts, "--fetch-crashes")
	crashes, keys := crashesOf(t, run)
	want := []jsonCrash{{Target: "FuzzParse", Artifact: "crash-FuzzParse", RunID: 44729305066, FetchedTo: "internal/p/testdata/fuzz/FuzzParse"}}
	if !reflect.DeepEqual(crashes, want) {
		t.Errorf("fuzz_crashes = %+v, want %+v", crashes, want)
	}
	if keys["already_present"] {
		t.Error("fuzz_crashes has already_present after a new file was fetched")
	}
	if keys["fetch_error"] {
		t.Error("fuzz_crashes has fetch_error after a good download")
	}
	if strings.Contains(stderr, "--fetch-crashes") {
		t.Errorf("stderr = %q, want no hint", stderr)
	}
}

// TestFuzzWithoutTheFlagOpensNothing covers AC-3: only --fetch-crashes
// downloads.
func TestFuzzWithoutTheFlagOpensNothing(t *testing.T) {
	_, _, calls := runFuzz(t, crashOf(oneCrash, fetchCheckout), cli.ExitFailed)
	if len(calls.downloads) != 0 {
		t.Errorf("downloads = %q, want none", calls.downloads)
	}
}

// TestUsageNamesFetchCrashes covers AC-3.
func TestUsageNamesFetchCrashes(t *testing.T) {
	app, _ := fake(t, "run-list-failure.json", nil, nil)
	var out, errOut bytes.Buffer
	if code := app.Run(t.Context(), nil, &out, &errOut); code != cli.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitUsage)
	}
	if want := "usage: ciwatch [--json] [--once] [--go-tests] [--fetch-crashes] [--rerun-failed] <commit>\n"; !strings.HasPrefix(errOut.String(), want) {
		t.Errorf("stderr = %q, want it to start with %q", errOut.String(), want)
	}
}

// TestFuzzFetchCrashesNeedsExactlyOneDeclaringFile covers AC-4.
func TestFuzzFetchCrashesNeedsExactlyOneDeclaringFile(t *testing.T) {
	tests := []struct {
		name     string
		checkout fstest.MapFS
		warning  string
	}{
		{
			"none",
			fstest.MapFS{"p_test.go": {Data: []byte("package p\n")}},
			"ciwatch: can't fetch crash-FuzzParse of run " + fuzzRun + ": no _test.go file declares func FuzzParse(f *testing.F)\n",
		},
		{
			"two",
			fstest.MapFS{"a_test.go": {Data: []byte(parseFuzzGo)}, "b/b_test.go": {Data: []byte(parseFuzzGo)}},
			"ciwatch: can't fetch crash-FuzzParse of run " + fuzzRun + ": FuzzParse is declared in a_test.go, b/b_test.go\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, calls := runFuzz(t, crashOf(oneCrash, tt.checkout), cli.ExitFailed, "--fetch-crashes")
			if !strings.Contains(stderr, tt.warning) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tt.warning)
			}
			if len(calls.downloads) != 0 {
				t.Errorf("downloads = %q, want none", calls.downloads)
			}
			if want := "  FuzzParse (crash-FuzzParse, run " + fuzzRun + "): not fetched\n"; !strings.HasSuffix(stdout, "\nFuzz crash inputs:\n"+want) {
				t.Errorf("stdout = %q, want it to end with the section with %q", stdout, want)
			}

			run, _, _ := fuzzJSON(t, crashOf(oneCrash, tt.checkout), "--fetch-crashes")
			crashes, keys := crashesOf(t, run)
			if len(crashes) != 1 || crashes[0].FetchErr == "" || keys["fetched_to"] {
				t.Errorf("fuzz_crashes = %+v (keys %v), want a fetch_error and no fetched_to", crashes, keys)
			}
		})
	}
}

// TestFuzzFetchCrashesWithoutACheckout covers AC-4: with no git checkout there is
// nowhere to put the input, so it warns and downloads nothing.
func TestFuzzFetchCrashesWithoutACheckout(t *testing.T) {
	opts := crashOf(oneCrash, fetchCheckout)
	opts.toplevelErr = errors.New("git rev-parse --show-toplevel: fatal: not a git repository")
	stdout, stderr, calls := runFuzz(t, opts, cli.ExitFailed, "--fetch-crashes")
	if want := "ciwatch: can't fetch crash-FuzzParse of run " + fuzzRun + ": "; !strings.Contains(stderr, want) || !strings.Contains(stderr, "not a git repository") {
		t.Errorf("stderr = %q, want %q and the git error", stderr, want)
	}
	if len(calls.downloads) != 0 {
		t.Errorf("downloads = %q, want none", calls.downloads)
	}
	if !strings.HasSuffix(stdout, "(crash-FuzzParse, run "+fuzzRun+"): not fetched\n") {
		t.Errorf("stdout = %q, want the input listed as not fetched", stdout)
	}
}

// TestFuzzNoCrashArtifactsChangesNothing covers AC-5a.
func TestFuzzNoCrashArtifactsChangesNothing(t *testing.T) {
	arts := `{"total_count":2,"artifacts":[{"name":"coverage"},{"name":"result-FuzzParse"}]}`
	for _, flags := range [][]string{nil, {"--fetch-crashes"}} {
		baseline, baselineErr, _ := runFuzz(t, fakeOpts{}, cli.ExitFailed, flags...)
		stdout, stderr, calls := runFuzz(t, crashOf(arts, fetchCheckout), cli.ExitFailed, flags...)
		if stdout != baseline || stderr != baselineErr {
			t.Errorf("%q: output differs from a run without artifacts:\n stdout %q\nbaseline %q\n stderr %q\nbaseline %q", flags, stdout, baseline, stderr, baselineErr)
		}
		if len(calls.downloads) != 0 || len(calls.opened) != 0 {
			t.Errorf("%q: downloads %q, opened %q, want neither", flags, calls.downloads, calls.opened)
		}
		for _, s := range []string{"Fuzz crash inputs", "fuzz crash", "--fetch-crashes"} {
			if strings.Contains(stdout+stderr, s) {
				t.Errorf("%q: output contains %q, want nothing about fuzzing", flags, s)
			}
		}
	}

	run, _, _ := fuzzJSON(t, crashOf(arts, nil))
	for _, k := range []string{"fuzz_crashes", "artifacts_error"} {
		if _, ok := run[k]; ok {
			t.Errorf("run has the key %q, want none", k)
		}
	}
	golden, _, _ := runFuzz(t, crashOf(arts, nil), cli.ExitFailed, "--json")
	want, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "run-list-failure.json"))
	if err != nil {
		t.Fatal(err)
	}
	if golden != string(want) {
		t.Errorf("--json output = %s, want the golden file of a run without crash inputs", golden)
	}
}

// TestFuzzDownloadFailureWarns covers AC-5b.
func TestFuzzDownloadFailureWarns(t *testing.T) {
	ghErr := errors.New("gh: exit status 1: HTTP 410: Gone (https://api.github.com/repos/o/r/actions/artifacts/1/zip)")
	opts := crashOf(oneCrash, fetchCheckout)
	opts.downloadErr = map[string]error{"crash-FuzzParse": ghErr}
	stdout, stderr, calls := runFuzz(t, opts, cli.ExitFailed, "--fetch-crashes")
	if len(calls.downloads) != 1 {
		t.Errorf("downloads = %q, want the one that failed", calls.downloads)
	}
	if want := "ciwatch: can't fetch crash-FuzzParse of run " + fuzzRun + ": "; !strings.Contains(stderr, want) || !strings.Contains(stderr, "HTTP 410: Gone") {
		t.Errorf("stderr = %q, want %q and the gh error", stderr, want)
	}
	if want := "  FuzzParse (crash-FuzzParse, run " + fuzzRun + "): not fetched\n"; !strings.HasSuffix(stdout, "\nFuzz crash inputs:\n"+want) {
		t.Errorf("stdout = %q, want it to end with the section with %q", stdout, want)
	}

	run, _, _ := fuzzJSON(t, opts, "--fetch-crashes")
	crashes, keys := crashesOf(t, run)
	if len(crashes) != 1 || !strings.Contains(crashes[0].FetchErr, "HTTP 410") || keys["fetched_to"] {
		t.Errorf("fuzz_crashes = %+v (keys %v), want a fetch_error with the gh error and no fetched_to", crashes, keys)
	}
}

// TestFuzzListingFailureWarns covers AC-6.
func TestFuzzListingFailureWarns(t *testing.T) {
	opts := fakeOpts{artifactsErr: map[string]error{fuzzRun: errors.New("gh: exit status 1: HTTP 404: Not Found")}}
	_, stderr, _ := runFuzz(t, opts, cli.ExitFailed)
	if want := "ciwatch: can't list artifacts for run " + fuzzRun + " (Fuzz): "; !strings.Contains(stderr, want) || !strings.Contains(stderr, "HTTP 404") {
		t.Errorf("stderr = %q, want %q and the gh error", stderr, want)
	}

	run, _, _ := fuzzJSON(t, opts)
	var msg string
	if raw, ok := run["artifacts_error"]; !ok || json.Unmarshal(raw, &msg) != nil || !strings.Contains(msg, "HTTP 404") {
		t.Errorf("artifacts_error = %s (present %v), want the gh error", raw, ok)
	}
	if _, ok := run["fuzz_crashes"]; ok {
		t.Error("run has fuzz_crashes after the listing failed")
	}
}

// TestFuzzListingRetriesNetworkErrors covers AC-6.
func TestFuzzListingRetriesNetworkErrors(t *testing.T) {
	opts := fakeOpts{artifactsErr: map[string]error{fuzzRun: networkErr(t)}}
	run, _, calls := fuzzJSON(t, opts)
	if want := 1 + 8; len(calls.artifactIDs) != want {
		t.Errorf("artifacts calls = %d, want %d: the first and 8 retries", len(calls.artifactIDs), want)
	}
	if calls.clock.waited() == 0 {
		t.Error("the fake clock was never waited on, want a backoff between the retries")
	}
	if _, ok := run["artifacts_error"]; !ok {
		t.Error("run has no artifacts_error after the retries ran out")
	}
}

// TestFuzzDownloadRetriesNetworkErrors covers AC-6: the download is retried
// like the listing.
func TestFuzzDownloadRetriesNetworkErrors(t *testing.T) {
	opts := crashOf(oneCrash, fetchCheckout)
	opts.downloadErr = map[string]error{"crash-FuzzParse": networkErr(t)}
	_, _, calls := runFuzz(t, opts, cli.ExitFailed, "--fetch-crashes")
	if want := 1 + 8; len(calls.downloads) != want {
		t.Errorf("downloads = %d, want %d: the first and 8 retries", len(calls.downloads), want)
	}
}

// TestFuzzMoreThan100ArtifactsWarns covers AC-6 (the non-goal's one-line warning).
func TestFuzzMoreThan100ArtifactsWarns(t *testing.T) {
	arts := `{"total_count":150,"artifacts":[{"name":"crash-FuzzParse"}]}`
	stdout, stderr, _ := runFuzz(t, crashOf(arts, nil), cli.ExitFailed)
	n := 0
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, "150") {
			n++
			if !strings.HasPrefix(line, "ciwatch: ") || !strings.Contains(line, fuzzRun) {
				t.Errorf("warning = %q, want a ciwatch line naming run %s", line, fuzzRun)
			}
		}
	}
	if n != 1 {
		t.Errorf("stderr = %q, want one line saying the run has 150 artifacts", stderr)
	}
	if !strings.Contains(stdout, "FuzzParse (crash-FuzzParse") {
		t.Errorf("stdout = %q, want the crash input of the first 100 still listed", stdout)
	}
}

const (
	fuzzDir      = "internal/p/testdata/fuzz/FuzzParse"
	rerunHint    = "ciwatch: 1 run failed; to rerun its failed jobs: ciwatch --rerun-failed " + sha
	fuzzTableEnd = "\nFuzz crash inputs:\n  FuzzParse (crash-FuzzParse, run " + fuzzRun + "): "
	otherFuzzGo  = "package q\n\nimport \"testing\"\n\nfunc FuzzOther(f *testing.F) {\n}\n"
)

// realCheckout returns options whose checkout is a real directory holding
// the files (path to content) plus the p package declaring FuzzParse, and
// whose gh run download of crash-FuzzParse writes the download files. It
// returns the checkout's root and the App's TempDir.
func realCheckout(t *testing.T, files, download map[string]string) (opts fakeOpts, root, temp string) {
	t.Helper()
	root, temp = t.TempDir(), t.TempDir()
	all := map[string]string{"internal/p/p_test.go": parseFuzzGo, "internal/p/p.go": "package p\n"}
	for name, content := range files {
		all[name] = content
	}
	for name, content := range all {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	opts = crashOf(oneCrash, nil)
	opts.root, opts.tempDir = root, temp
	opts.downloadFiles = map[string]map[string]string{"crash-FuzzParse": download}
	return opts, root, temp
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // reads a file of the test's own temporary checkout
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func isEmptyDir(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries) == 0
}

// lastLines splits stderr into its lines.
func lastLines(stderr string) []string {
	return strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
}

// TestFuzzFetchAlreadyPresent covers AC-1 and AC-3 (no hint for an input
// that is already present).
func TestFuzzFetchAlreadyPresent(t *testing.T) {
	opts, root, _ := realCheckout(t, map[string]string{fuzzDir + "/abc": "original"}, map[string]string{"abc": "different"})
	stdout, stderr, _ := runFuzz(t, opts, cli.ExitFailed, "--fetch-crashes")
	if want := fuzzTableEnd + "already present in " + fuzzDir + "\n"; !strings.HasSuffix(stdout, want) {
		t.Errorf("stdout = %q, want it to end with %q", stdout, want)
	}
	if strings.Contains(stderr, "can't fetch") || strings.Contains(stderr, "--fetch-crashes") || strings.Contains(stderr, "not fetched") {
		t.Errorf("stderr = %q, want nothing about the input", stderr)
	}
	if got := readFile(t, filepath.Join(root, fuzzDir, "abc")); got != "original" {
		t.Errorf("existing file = %q, want it unchanged", got)
	}

	run, _, _ := fuzzJSON(t, opts, "--fetch-crashes")
	crashes, keys := crashesOf(t, run)
	if len(crashes) != 1 || crashes[0].FetchedTo != fuzzDir || !crashes[0].AlreadyPresent || keys["fetch_error"] {
		t.Errorf("fuzz_crashes = %+v (keys %v), want fetched_to, already_present and no fetch_error", crashes, keys)
	}
	if want := `"fetched_to":"` + fuzzDir + `","already_present":true`; !strings.Contains(string(run["fuzz_crashes"]), want) {
		t.Errorf("fuzz_crashes = %s, want it to contain %s", run["fuzz_crashes"], want)
	}
}

// TestFuzzFetchNewFile covers AC-2: a new file is written, an existing one
// is left alone, and the missing directory is created.
func TestFuzzFetchNewFile(t *testing.T) {
	opts, root, _ := realCheckout(t, map[string]string{fuzzDir + "/abc": "original"}, map[string]string{"abc": "different", "def": "new"})
	stdout, stderr, _ := runFuzz(t, opts, cli.ExitFailed, "--fetch-crashes")
	if want := fuzzTableEnd + "fetched to " + fuzzDir + "\n"; !strings.HasSuffix(stdout, want) {
		t.Errorf("stdout = %q, want it to end with %q", stdout, want)
	}
	if strings.Contains(stderr, "--fetch-crashes") {
		t.Errorf("stderr = %q, want no hint", stderr)
	}
	if got := readFile(t, filepath.Join(root, fuzzDir, "def")); got != "new" {
		t.Errorf("new file = %q, want the downloaded bytes", got)
	}
	if got := readFile(t, filepath.Join(root, fuzzDir, "abc")); got != "original" {
		t.Errorf("existing file = %q, want it unchanged", got)
	}
	// The first run wrote def, so the JSON run starts from a fresh checkout.
	jsonOpts, _, _ := realCheckout(t, map[string]string{fuzzDir + "/abc": "original"}, map[string]string{"abc": "different", "def": "new"})
	run, _, _ := fuzzJSON(t, jsonOpts, "--fetch-crashes")
	if _, keys := crashesOf(t, run); keys["already_present"] || keys["fetch_error"] || !keys["fetched_to"] {
		t.Errorf("keys = %v, want fetched_to only", keys)
	}

	// The target directory is created when it's missing.
	fresh, freshRoot, _ := realCheckout(t, nil, map[string]string{"abc": "x"})
	runFuzz(t, fresh, cli.ExitFailed, "--fetch-crashes")
	if got := readFile(t, filepath.Join(freshRoot, fuzzDir, "abc")); got != "x" {
		t.Errorf("file in the new directory = %q, want the downloaded bytes", got)
	}
}

// TestFuzzFetchHint covers AC-3: the last stderr line says what to do next.
func TestFuzzFetchHint(t *testing.T) {
	gone := errors.New("gh: exit status 1: HTTP 410: Gone (https://api.github.com/repos/o/r/actions/artifacts/1/zip)")
	two := `{"total_count":2,"artifacts":[{"name":"crash-FuzzParse"},{"name":"crash-FuzzOther"}]}`
	retry := "ciwatch --fetch-crashes " + sha
	tests := []struct {
		name  string
		setup func(o *fakeOpts)
		want  string
	}{
		{"no declaration", func(o *fakeOpts) {
			o.downloadFiles = nil
			if err := os.Remove(filepath.Join(o.root, "internal/p/p_test.go")); err != nil {
				t.Fatal(err)
			}
		}, "ciwatch: crash-FuzzParse of run " + fuzzRun + " not fetched; declare func FuzzParse(f *testing.F) in a _test.go file, then run: " + retry},
		{"two declarations", func(o *fakeOpts) {
			if err := os.WriteFile(filepath.Join(o.root, "internal/p/q_test.go"), []byte(parseFuzzGo), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "ciwatch: crash-FuzzParse of run " + fuzzRun + " not fetched; FuzzParse is declared in more than one file, so download it by hand: gh run download " + fuzzRun + " -n crash-FuzzParse -D <package>/testdata/fuzz/FuzzParse"},
		{"no checkout", func(o *fakeOpts) {
			o.toplevelErr = errors.New("fatal: not a git repository")
		}, "ciwatch: crash-FuzzParse of run " + fuzzRun + " not fetched; run ciwatch --fetch-crashes " + sha + " inside the repository's checkout"},
		{"download failed", func(o *fakeOpts) {
			o.downloadErr = map[string]error{"crash-FuzzParse": gone}
		}, "ciwatch: crash-FuzzParse of run " + fuzzRun + " not fetched; to try again: " + retry},
		{"two with the same reason", func(o *fakeOpts) {
			o.artifacts = map[string]string{fuzzRun: two}
			o.downloadErr = map[string]error{"crash-FuzzParse": gone, "crash-FuzzOther": gone}
			writeOther(t, o.root)
		}, "ciwatch: 2 fuzz crash inputs not fetched; to try again: " + retry},
		{"two with different reasons", func(o *fakeOpts) {
			o.artifacts = map[string]string{fuzzRun: two}
			o.downloadErr = map[string]error{"crash-FuzzParse": gone}
			// FuzzOther is declared nowhere.
		}, "ciwatch: 2 fuzz crash inputs not fetched; see the warnings above, then run: " + retry},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, _, _ := realCheckout(t, nil, map[string]string{"abc": "x"})
			tt.setup(&opts)
			_, stderr, _ := runFuzz(t, opts, cli.ExitFailed, "--fetch-crashes")
			lines := lastLines(stderr)
			if len(lines) < 3 {
				t.Fatalf("stderr = %q, want warnings, the rerun hint and the fetch hint", stderr)
			}
			if got := lines[len(lines)-1]; got != tt.want {
				t.Errorf("last stderr line = %q, want %q", got, tt.want)
			}
			if got := lines[len(lines)-2]; got != rerunHint {
				t.Errorf("line before it = %q, want the rerun hint %q", got, rerunHint)
			}
			if !strings.Contains(stderr, "ciwatch: can't fetch crash-FuzzParse of run "+fuzzRun+": ") {
				t.Errorf("stderr = %q, want the per-input warning above the hint", stderr)
			}
		})
	}
}

func writeOther(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, "internal/q/q_test.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(otherFuzzGo), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestFuzzFetchEmptyArtifact covers AC-4.
func TestFuzzFetchEmptyArtifact(t *testing.T) {
	opts, _, _ := realCheckout(t, nil, nil)
	stdout, stderr, _ := runFuzz(t, opts, cli.ExitFailed, "--fetch-crashes")
	if want := fuzzTableEnd + "not fetched\n"; !strings.HasSuffix(stdout, want) {
		t.Errorf("stdout = %q, want it to end with %q", stdout, want)
	}
	if want := "ciwatch: crash-FuzzParse of run " + fuzzRun + " not fetched; to try again: ciwatch --fetch-crashes " + sha; !strings.HasSuffix(stderr, want+"\n") {
		t.Errorf("stderr = %q, want it to end with %q", stderr, want)
	}
	run, _, _ := fuzzJSON(t, opts, "--fetch-crashes")
	crashes, keys := crashesOf(t, run)
	if len(crashes) != 1 || crashes[0].FetchErr != "artifact crash-FuzzParse has no files" || keys["fetched_to"] {
		t.Errorf("fuzz_crashes = %+v (keys %v), want the fetch_error and no fetched_to", crashes, keys)
	}
}

// TestFuzzFetchLeavesNoStaging covers AC-5: a failed or interrupted
// download leaves the checkout unchanged and the staging directory removed.
func TestFuzzFetchLeavesNoStaging(t *testing.T) {
	t.Run("download fails", func(t *testing.T) {
		opts, root, temp := realCheckout(t, nil, map[string]string{"abc": "x"})
		opts.downloadErr = map[string]error{"crash-FuzzParse": errors.New("gh: exit status 1: HTTP 410: Gone")}
		runFuzz(t, opts, cli.ExitFailed, "--fetch-crashes")
		if !isEmptyDir(t, temp) {
			t.Errorf("TempDir %q is not empty", temp)
		}
		if _, err := os.Stat(filepath.Join(root, "internal/p/testdata")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("checkout has testdata after a failed download (stat error %v)", err)
		}
	})
	t.Run("interrupted", func(t *testing.T) {
		opts, root, temp := realCheckout(t, nil, map[string]string{"abc": "x"})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		opts.onDownload = cancel
		app, _ := fakeWith(t, "run-list-failure.json", nil, nil, opts)
		var out, errOut bytes.Buffer
		if code := app.Run(ctx, []string{"--fetch-crashes", "HEAD"}, &out, &errOut); code != cli.ExitUnchecked {
			t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitUnchecked, errOut.String())
		}
		if out.Len() != 0 {
			t.Errorf("stdout = %q, want no table", out.String())
		}
		if !isEmptyDir(t, temp) {
			t.Errorf("TempDir %q is not empty", temp)
		}
		if _, err := os.Stat(filepath.Join(root, "internal/p/testdata")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("checkout has testdata after an interrupted download (stat error %v)", err)
		}
	})
}

// TestFuzzFetchCopyFailureLeavesCheckoutUnchanged covers AC-5's promise that
// a failed fetch leaves the checkout unchanged, when the copy fails partway.
func TestFuzzFetchCopyFailureLeavesCheckoutUnchanged(t *testing.T) {
	// "sub" is a file in the target directory, so sub/b can't be written after a.
	opts, root, temp := realCheckout(t, map[string]string{fuzzDir + "/sub": "file"}, map[string]string{"a": "x", "sub/b": "y"})
	stdout, stderr, _ := runFuzz(t, opts, cli.ExitFailed, "--fetch-crashes")
	if want := fuzzTableEnd + "not fetched\n"; !strings.HasSuffix(stdout, want) {
		t.Errorf("stdout = %q, want it to end with %q", stdout, want)
	}
	if want := "to try again: ciwatch --fetch-crashes " + sha + "\n"; !strings.HasSuffix(stderr, want) {
		t.Errorf("stderr = %q, want it to end with %q", stderr, want)
	}
	if _, err := os.Stat(filepath.Join(root, fuzzDir, "a")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file a was left in the checkout (stat error %v)", err)
	}
	if !isEmptyDir(t, temp) {
		t.Errorf("TempDir %q is not empty", temp)
	}
}

// TestFuzzFetchExitCode covers AC-4's sibling AC-6: the exit code comes from
// the runs alone.
func TestFuzzFetchExitCode(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
	}{
		{"already present", map[string]string{fuzzDir + "/abc": "original"}},
		{"fetched", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, _, _ := realCheckout(t, tt.files, map[string]string{"abc": "x"})
			runFuzz(t, opts, cli.ExitFailed, "--fetch-crashes")
		})
	}
	t.Run("not fetched", func(t *testing.T) {
		opts, _, _ := realCheckout(t, nil, map[string]string{"abc": "x"})
		opts.downloadErr = map[string]error{"crash-FuzzParse": errors.New("gh: exit status 1: HTTP 410: Gone")}
		runFuzz(t, opts, cli.ExitFailed, "--fetch-crashes")
	})
}

// TestJSONDocumentsFuzzFields covers AC-2: the new fields are in cli.Report
// and in docs/json.md.
func TestJSONDocumentsFuzzFields(t *testing.T) {
	want := map[string]string{
		"runs[].fuzz_crashes":                   "array of objects",
		"runs[].fuzz_crashes[].target":          "string",
		"runs[].fuzz_crashes[].artifact":        "string",
		"runs[].fuzz_crashes[].run_id":          "integer",
		"runs[].fuzz_crashes[].fetched_to":      "string",
		"runs[].fuzz_crashes[].fetch_error":     "string",
		"runs[].fuzz_crashes[].already_present": "boolean",
		"runs[].fuzz_crashes[].earlier_attempt": "boolean",
		"runs[].artifacts_error":                "string",
	}
	have := map[string]string{}
	walkFields("", reflect.TypeFor[cli.Report](), have)
	doc := map[string]string{}
	for _, cells := range docTable(t, "Fields") {
		if len(cells) >= 3 && cells[2] != "" {
			doc[strings.Trim(cells[0], "`")] = cells[1]
		}
	}
	for path, typ := range want {
		if have[path] != typ {
			t.Errorf("cli.Report field %q is %q, want %q", path, have[path], typ)
		}
		if doc[path] != typ {
			t.Errorf("docs/json.md field %q is %q, want %q with a note on when it is present", path, doc[path], typ)
		}
	}
}

const twoCrashes = `{"total_count":2,"artifacts":[{"name":"crash-FuzzParse"},{"name":"crash-FuzzOther"}]}`

// writtenLine is what Ctrl-C lists for an input this run wrote into dir.
func writtenLine(artifact, run, dir string) string {
	return "ciwatch: " + artifact + " of run " + run + " was fetched to " + dir
}

// runCancelled runs ciwatch --fetch-crashes with ctx and returns the exit code
// and output.
func runCancelled(ctx context.Context, t *testing.T, opts fakeOpts, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	app, _ := fakeWith(t, "run-list-failure.json", nil, nil, opts)
	var out, errOut bytes.Buffer
	code = app.Run(ctx, append([]string{"--fetch-crashes"}, append(args, "HEAD")...), &out, &errOut)
	return code, out.String(), errOut.String()
}

// writeFuzz declares func <target> in internal/<pkg>/<pkg>_test.go.
func writeFuzz(t *testing.T, root, pkg, target string) {
	t.Helper()
	path := filepath.Join(root, "internal", pkg, pkg+"_test.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	src := "package " + pkg + "\n\nimport \"testing\"\n\nfunc " + target + "(f *testing.F) {\n}\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestFuzzInterruptListsWritten covers AC-1 and AC-3: Ctrl-C while the second
// input downloads lists the first, which is in the checkout, and not the second.
func TestFuzzInterruptListsWritten(t *testing.T) {
	opts, root, temp := realCheckout(t, nil, map[string]string{"abc": "x"})
	opts.artifacts = map[string]string{fuzzRun: twoCrashes}
	opts.downloadFiles["crash-FuzzOther"] = map[string]string{"def": "y"}
	writeOther(t, root)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	n := 0
	opts.onDownload = func() {
		if n++; n == 2 {
			cancel()
		}
	}
	code, stdout, stderr := runCancelled(ctx, t, opts)
	if code != cli.ExitUnchecked {
		t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitUnchecked, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want it empty", stdout)
	}
	lines := lastLines(stderr)
	if got := lines[len(lines)-1]; !strings.HasSuffix(got, "context canceled") {
		t.Errorf("last stderr line = %q, want the context canceled line", got)
	}
	if want := writtenLine("crash-FuzzParse", fuzzRun, fuzzDir); !slices.Contains(lines, want) {
		t.Errorf("stderr = %q, want the line %q", stderr, want)
	}
	if strings.Contains(stderr, "crash-FuzzOther of run "+fuzzRun+" was fetched") {
		t.Errorf("stderr = %q, want no line for the interrupted input", stderr)
	}
	if got := readFile(t, filepath.Join(root, fuzzDir, "abc")); got != "x" {
		t.Errorf("written file = %q, want the downloaded bytes", got)
	}
	if _, err := os.Stat(filepath.Join(root, "internal/q/testdata")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("checkout has testdata for the interrupted input (stat error %v)", err)
	}
	if !isEmptyDir(t, temp) {
		t.Errorf("TempDir %q is not empty", temp)
	}
}

// TestFuzzInterruptBeforeAnyWrite covers AC-2: with nothing written, Ctrl-C
// lists nothing, whether it comes during the first download or before
// fetching starts.
func TestFuzzInterruptBeforeAnyWrite(t *testing.T) {
	tests := []struct {
		name   string
		cancel func(o *fakeOpts, cancel context.CancelFunc)
	}{
		{"first download", func(o *fakeOpts, cancel context.CancelFunc) { o.onDownload = cancel }},
		{"listing jobs", func(o *fakeOpts, cancel context.CancelFunc) { o.onView = func(string) { cancel() } }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, root, temp := realCheckout(t, nil, map[string]string{"abc": "x"})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			tt.cancel(&opts, cancel)
			code, stdout, stderr := runCancelled(ctx, t, opts)
			if code != cli.ExitUnchecked {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitUnchecked, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want it empty", stdout)
			}
			if strings.Contains(stderr, "was fetched to") {
				t.Errorf("stderr = %q, want no was fetched to line", stderr)
			}
			if got := lastLines(stderr); !strings.HasSuffix(got[len(got)-1], "context canceled") {
				t.Errorf("stderr = %q, want the context canceled line last", stderr)
			}
			if _, err := os.Stat(filepath.Join(root, "internal/p/testdata")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("checkout has testdata (stat error %v)", err)
			}
			if !isEmptyDir(t, temp) {
				t.Errorf("TempDir %q is not empty", temp)
			}
		})
	}
}

// TestFuzzInterruptSkipsAlreadyPresent covers AC-4: an input whose files were
// all in the checkout already isn't listed; one with a new file is.
func TestFuzzInterruptSkipsAlreadyPresent(t *testing.T) {
	tests := []struct {
		name     string
		download map[string]string
		listed   bool
	}{
		{"all present", map[string]string{"abc": "different"}, false},
		{"one new", map[string]string{"abc": "different", "def": "new"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, root, _ := realCheckout(t, map[string]string{fuzzDir + "/abc": "original"}, tt.download)
			opts.artifacts = map[string]string{fuzzRun: twoCrashes}
			opts.downloadFiles["crash-FuzzOther"] = map[string]string{"z": "z"}
			writeOther(t, root)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			n := 0
			opts.onDownload = func() {
				if n++; n == 2 {
					cancel()
				}
			}
			code, stdout, stderr := runCancelled(ctx, t, opts)
			if code != cli.ExitUnchecked || stdout != "" {
				t.Errorf("exit code = %d, stdout = %q, want %d and no stdout (stderr %q)", code, stdout, cli.ExitUnchecked, stderr)
			}
			if got := strings.Contains(stderr, writtenLine("crash-FuzzParse", fuzzRun, fuzzDir)); got != tt.listed {
				t.Errorf("stderr = %q, listed = %v, want %v", stderr, got, tt.listed)
			}
		})
	}
}

// TestFuzzInterruptBeforeSkippedInput covers AC-5: Ctrl-C after one input is
// written, with the next skipped before its download for want of a
// declaration, is still an interrupt: the written input is listed, the exit
// code is 4 and there's no table.
func TestFuzzInterruptBeforeSkippedInput(t *testing.T) {
	opts, _, _ := realCheckout(t, nil, map[string]string{"abc": "x"})
	opts.artifacts = map[string]string{fuzzRun: twoCrashes} // FuzzOther is declared nowhere
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	opts.afterDownload = cancel
	code, stdout, stderr := runCancelled(ctx, t, opts)
	if code != cli.ExitUnchecked {
		t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitUnchecked, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want no table", stdout)
	}
	lines := lastLines(stderr)
	if want := writtenLine("crash-FuzzParse", fuzzRun, fuzzDir); !slices.Contains(lines, want) {
		t.Errorf("stderr = %q, want the line %q", stderr, want)
	}
	if got := lines[len(lines)-1]; !strings.HasSuffix(got, "context canceled") {
		t.Errorf("last stderr line = %q, want the context canceled line", got)
	}
}

// TestFuzzInterruptWhileChecking: Ctrl-C while the checkout is opened for the
// only input, whose target no file declares, is still an interrupt: exit 4,
// no table, not a skipped input.
func TestFuzzInterruptWhileChecking(t *testing.T) {
	opts, _, _ := realCheckout(t, nil, nil)
	opts.artifacts = map[string]string{fuzzRun: `{"total_count":1,"artifacts":[{"name":"crash-FuzzOther"}]}`} // declared nowhere
	app, _ := fakeWith(t, "run-list-failure.json", nil, nil, opts)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	open := app.OpenDir
	app.OpenDir = func(root string) fs.FS {
		cancel()
		return open(root)
	}
	var out, errOut bytes.Buffer
	code := app.Run(ctx, []string{"--fetch-crashes", "HEAD"}, &out, &errOut)
	if code != cli.ExitUnchecked {
		t.Errorf("exit code = %d, want %d (stderr %q)", code, cli.ExitUnchecked, errOut.String())
	}
	if out.String() != "" {
		t.Errorf("stdout = %q, want no table", out.String())
	}
	lines := lastLines(errOut.String())
	if got := lines[len(lines)-1]; !strings.HasSuffix(got, "context canceled") {
		t.Errorf("last stderr line = %q, want the context canceled line", got)
	}
}

// TestFuzzInterruptTwoRuns covers AC-6: inputs of two runs are listed one line
// each, in the order they were fetched.
func TestFuzzInterruptTwoRuns(t *testing.T) {
	const second = "44709305564"
	opts, root, _ := realCheckout(t, nil, map[string]string{"a": "1"})
	opts.listRaw = []string{`[` +
		`{"conclusion":"failure","createdAt":"2026-11-20T11:38:53Z","databaseId":` + fuzzRun + `,"event":"schedule","headBranch":"main","headSha":"c4b4c0242478a612ca295d5ff223709e84a2d172","status":"completed","updatedAt":"2026-11-20T12:13:08Z","url":"https://github.com/devvmhq/devvm/actions/runs/` + fuzzRun + `","workflowDatabaseId":409041459,"workflowName":"Fuzz"},` +
		`{"conclusion":"failure","createdAt":"2026-11-20T09:16:24Z","databaseId":` + second + `,"event":"push","headBranch":"main","headSha":"c4b4c0242478a612ca295d5ff223709e84a2d172","status":"completed","updatedAt":"2026-11-20T09:29:21Z","url":"https://github.com/devvmhq/devvm/actions/runs/` + second + `","workflowDatabaseId":409041453,"workflowName":"CI"}]`}
	opts.viewFixture = map[string]string{second: "run-view-" + fuzzRun + ".json"}
	opts.artifacts = map[string]string{
		fuzzRun: twoCrashes,
		second:  `{"total_count":2,"artifacts":[{"name":"crash-FuzzThird"},{"name":"crash-FuzzFourth"}]}`,
	}
	writeOther(t, root)
	writeFuzz(t, root, "r", "FuzzThird")
	writeFuzz(t, root, "s", "FuzzFourth")
	opts.downloadFiles["crash-FuzzOther"] = map[string]string{"b": "2"}
	opts.downloadFiles["crash-FuzzThird"] = map[string]string{"c": "3"}
	opts.downloadFiles["crash-FuzzFourth"] = map[string]string{"d": "4"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	n := 0
	opts.onDownload = func() {
		if n++; n == 4 {
			cancel()
		}
	}
	code, stdout, stderr := runCancelled(ctx, t, opts)
	if code != cli.ExitUnchecked || stdout != "" {
		t.Errorf("exit code = %d, stdout = %q, want %d and no stdout (stderr %q)", code, stdout, cli.ExitUnchecked, stderr)
	}
	var got []string
	for _, l := range lastLines(stderr) {
		if strings.Contains(l, "was fetched to") {
			got = append(got, l)
		}
	}
	want := []string{
		writtenLine("crash-FuzzParse", fuzzRun, fuzzDir),
		writtenLine("crash-FuzzOther", fuzzRun, "internal/q/testdata/fuzz/FuzzOther"),
		writtenLine("crash-FuzzThird", second, "internal/r/testdata/fuzz/FuzzThird"),
	}
	if !slices.Equal(got, want) {
		t.Errorf("was fetched to lines = %q, want %q", got, want)
	}
}
