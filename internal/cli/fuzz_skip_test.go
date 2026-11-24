package cli_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/devvmhq/ciwatch/internal/cli"
)

// denyFS refuses to open the chosen paths with a permission error. It
// implements only Open, so fs.WalkDir lists directories through Open too.
type denyFS struct {
	fs.FS
	denied map[string]bool
}

func (d denyFS) Open(name string) (fs.File, error) {
	if d.denied[name] {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
	}
	return d.FS.Open(name)
}

// runDenied runs ciwatch on the failed Fuzz run with the paths in the
// checkout unreadable.
func runDenied(t *testing.T, opts fakeOpts, denied []string, wantCode int, args ...string) (stdout, stderr string) {
	t.Helper()
	app, _ := fakeWith(t, "run-list-failure.json", nil, nil, opts)
	open := app.OpenDir
	set := map[string]bool{}
	for _, p := range denied {
		set[p] = true
	}
	app.OpenDir = func(root string) fs.FS { return denyFS{open(root), set} }
	var out, errOut bytes.Buffer
	if code := app.Run(t.Context(), append(args, "HEAD"), &out, &errOut); code != wantCode {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, wantCode, errOut.String())
	}
	return out.String(), errOut.String()
}

const (
	secretFuzzGo = "internal/secret/s_test.go"
	skipWarning  = "ciwatch: skipped internal/secret while looking for fuzz targets: permission denied\n"
)

// TestFuzzFetchSkipsUnreadableFolder covers AC-1.
func TestFuzzFetchSkipsUnreadableFolder(t *testing.T) {
	opts, root, _ := realCheckout(t, map[string]string{secretFuzzGo: parseFuzzGo}, map[string]string{"abc": "x"})
	stdout, stderr := runDenied(t, opts, []string{"internal/secret"}, cli.ExitFailed, "--fetch-crashes")
	if got := strings.Count(stderr, skipWarning); got != 1 {
		t.Errorf("stderr = %q, want the warning %q once", stderr, skipWarning)
	}
	if want := fuzzTableEnd + "fetched to " + fuzzDir + "\n"; !strings.HasSuffix(stdout, want) {
		t.Errorf("stdout = %q, want it to end with %q", stdout, want)
	}
	if got := readFile(t, filepath.Join(root, fuzzDir, "abc")); got != "x" {
		t.Errorf("fetched file = %q, want the downloaded bytes", got)
	}
}

// TestFuzzFetchSkipsUnreadableFile covers AC-3 (the design's unreadable
// _test.go file case).
func TestFuzzFetchSkipsUnreadableFile(t *testing.T) {
	opts, _, _ := realCheckout(t, map[string]string{"internal/q/q_test.go": otherFuzzGo}, map[string]string{"abc": "x"})
	stdout, stderr := runDenied(t, opts, []string{"internal/q/q_test.go"}, cli.ExitFailed, "--fetch-crashes")
	want := "ciwatch: skipped internal/q/q_test.go while looking for fuzz targets: permission denied\n"
	if strings.Count(stderr, want) != 1 {
		t.Errorf("stderr = %q, want the warning %q once", stderr, want)
	}
	if want := fuzzTableEnd + "fetched to " + fuzzDir + "\n"; !strings.HasSuffix(stdout, want) {
		t.Errorf("stdout = %q, want it to end with %q", stdout, want)
	}
}

// TestFuzzFetchOnlyDeclaredInUnreadable covers AC-2.
func TestFuzzFetchOnlyDeclaredInUnreadable(t *testing.T) {
	checkout := fstest.MapFS{secretFuzzGo: {Data: []byte(parseFuzzGo)}}
	stdout, stderr := runDenied(t, crashOf(oneCrash, checkout), []string{"internal/secret"}, cli.ExitFailed, "--fetch-crashes")
	failure := "ciwatch: can't fetch crash-FuzzParse of run " + fuzzRun + ": no _test.go file declares func FuzzParse(f *testing.F)\n"
	w, f := strings.Index(stderr, skipWarning), strings.Index(stderr, failure)
	if w < 0 || f < 0 || w > f {
		t.Errorf("stderr = %q, want %q before %q", stderr, skipWarning, failure)
	}
	lines := lastLines(stderr)
	want := "ciwatch: crash-FuzzParse of run " + fuzzRun + " not fetched; declare func FuzzParse(f *testing.F) in a _test.go file, then run: ciwatch --fetch-crashes " + sha
	if got := lines[len(lines)-1]; got != want {
		t.Errorf("hint = %q, want the usual %q", got, want)
	}
	if want := fuzzTableEnd + "not fetched\n"; !strings.HasSuffix(stdout, want) {
		t.Errorf("stdout = %q, want it to end with %q", stdout, want)
	}
}

// TestFuzzFetchSkipWarnsOnce covers AC-4: two inputs, one warning.
func TestFuzzFetchSkipWarnsOnce(t *testing.T) {
	opts, _, _ := realCheckout(t, map[string]string{secretFuzzGo: parseFuzzGo}, map[string]string{"abc": "x"})
	opts.artifacts = map[string]string{fuzzRun: twoCrashes} // FuzzOther is declared nowhere
	_, stderr := runDenied(t, opts, []string{"internal/secret"}, cli.ExitFailed, "--fetch-crashes")
	if got := strings.Count(stderr, "while looking for fuzz targets"); got != 1 {
		t.Errorf("stderr = %q, want one skip warning, got %d", stderr, got)
	}
	if !strings.Contains(stderr, "no _test.go file declares func FuzzOther(f *testing.F)") {
		t.Errorf("stderr = %q, want FuzzOther reported as declared nowhere", stderr)
	}
}

// TestFuzzFetchSkipDoesNotEnterIgnoredFolders covers AC-5: an unreadable
// folder inside testdata is never entered, so there is no warning.
func TestFuzzFetchSkipDoesNotEnterIgnoredFolders(t *testing.T) {
	opts, _, _ := realCheckout(t, map[string]string{"internal/p/testdata/x/a": "1", "vendor/v/a": "1", ".x/a": "1", "_x/a": "1"}, map[string]string{"abc": "x"})
	_, stderr := runDenied(t, opts, []string{"internal/p/testdata/x", "vendor/v", ".x", "_x"}, cli.ExitFailed, "--fetch-crashes")
	if strings.Contains(stderr, "skipped") {
		t.Errorf("stderr = %q, want no skip warning", stderr)
	}
}

// TestFuzzFetchUnreadableRoot covers AC-6.
func TestFuzzFetchUnreadableRoot(t *testing.T) {
	opts, _, _ := realCheckout(t, nil, map[string]string{"abc": "x"})
	_, stderr := runDenied(t, opts, []string{"."}, cli.ExitFailed, "--fetch-crashes")
	if !strings.Contains(stderr, "looking for FuzzParse in the checkout: ") {
		t.Errorf("stderr = %q, want the search to fail as before", stderr)
	}
	if strings.Contains(stderr, "skipped") {
		t.Errorf("stderr = %q, want no skip warning", stderr)
	}
}

// TestFuzzFetchSkipJSON covers AC-8: the JSON is the same with and without
// the unreadable folder, and the warning goes to stderr.
func TestFuzzFetchSkipJSON(t *testing.T) {
	crashesWith := func(denied []string) ([]jsonCrash, string) {
		files := map[string]string{}
		if denied != nil {
			files[secretFuzzGo] = parseFuzzGo
		}
		opts, _, _ := realCheckout(t, files, map[string]string{"abc": "x"})
		stdout, stderr := runDenied(t, opts, denied, cli.ExitFailed, "--json", "--fetch-crashes")
		var doc struct {
			Runs []map[string]json.RawMessage `json:"runs"`
		}
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil || len(doc.Runs) != 2 {
			t.Fatalf("stdout %q is not the JSON of 2 runs: %v", stdout, err)
		}
		crashes, _ := crashesOf(t, doc.Runs[0])
		return crashes, stderr
	}
	want, _ := crashesWith(nil)
	got, stderr := crashesWith([]string{"internal/secret"})
	if len(got) != 1 || got[0].FetchedTo == "" || !reflect.DeepEqual(got, want) {
		t.Errorf("fuzz_crashes = %+v, want %+v", got, want)
	}
	if strings.Count(stderr, skipWarning) != 1 {
		t.Errorf("stderr = %q, want the warning %q once", stderr, skipWarning)
	}
}
