package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
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
	if got := lastLine(stderr); got != fuzzFlagHint {
		t.Errorf("last stderr line = %q, want %q", got, fuzzFlagHint)
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
	stdout, stderr, calls := runFuzz(t, crashOf(oneCrash, fetchCheckout), cli.ExitFailed, "--fetch-crashes")
	wantArgs := []string{"run", "download", fuzzRun, "-n", "crash-FuzzParse", "-D", "/repo/internal/p/testdata/fuzz/FuzzParse"}
	if len(calls.downloads) != 1 || !slices.Equal(calls.downloads[0], wantArgs) {
		t.Errorf("downloads = %q, want one: %q", calls.downloads, wantArgs)
	}
	if !slices.Equal(calls.opened, []string{checkoutRoot}) {
		t.Errorf("opened %q, want the git toplevel %q", calls.opened, checkoutRoot)
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
	run, stderr, _ := fuzzJSON(t, crashOf(oneCrash, fetchCheckout), "--fetch-crashes")
	crashes, keys := crashesOf(t, run)
	want := []jsonCrash{{Target: "FuzzParse", Artifact: "crash-FuzzParse", RunID: 44729305066, FetchedTo: "internal/p/testdata/fuzz/FuzzParse"}}
	if !reflect.DeepEqual(crashes, want) {
		t.Errorf("fuzz_crashes = %+v, want %+v", crashes, want)
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
	if want := "usage: ciwatch [--json] [--once] [--go-tests] [--fetch-crashes] <commit>\n"; !strings.HasPrefix(errOut.String(), want) {
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

// TestJSONDocumentsFuzzFields covers AC-2: the new fields are in cli.Report
// and in docs/json.md.
func TestJSONDocumentsFuzzFields(t *testing.T) {
	want := map[string]string{
		"runs[].fuzz_crashes":               "array of objects",
		"runs[].fuzz_crashes[].target":      "string",
		"runs[].fuzz_crashes[].artifact":    "string",
		"runs[].fuzz_crashes[].run_id":      "integer",
		"runs[].fuzz_crashes[].fetched_to":  "string",
		"runs[].fuzz_crashes[].fetch_error": "string",
		"runs[].artifacts_error":            "string",
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
