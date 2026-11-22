package testlog_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/devvmhq/ciwatch/internal/testlog"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "logs", name)) //nolint:gosec // reads a fixture from testdata/ by a name the test supplies
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func parse(t *testing.T, log string) testlog.Result {
	t.Helper()
	res, err := testlog.Parse(strings.NewReader(log))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return res
}

var wantLinux = testlog.Failure{
	Package: "internal/vm",
	Name:    "TestAgentImportFailsWhole",
	Lines: []string{
		"    agentimport_test.go:957: changed: ",
		`    agentimport_test.go:963: changed true: the store has [".claude" ".claude/CLAUDE.md=another" ".claude/skills" ".claude/skills/a.md=a"], want [".claude" ".claude/CLAUDE.md=old" ".claude/skills" ".claude/skills/old.md=old"]`,
	},
}

var wantMacOS = testlog.Failure{
	Package: "internal/vmssh",
	Name:    "TestPipeSSHGrace",
	Lines: []string{
		"    relay_test.go:117: talking, half closed: the relay ended while the VM talked: EOF",
	},
}

func TestParseGotestsumFailures(t *testing.T) {
	tests := []struct {
		file string
		want testlog.Failure
	}{
		{"gotestsum-failures.log", wantLinux},
		{"gotestsum-failures-macos.log", wantMacOS},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			got := parse(t, fixture(t, tt.file)).Failures
			if want := []testlog.Failure{tt.want}; !reflect.DeepEqual(got, want) {
				t.Errorf("Failures = %#v, want %#v", got, want)
			}
		})
	}
}

func TestParseGotestsumSummary(t *testing.T) {
	tests := []struct {
		file string
		want testlog.Summary
	}{
		{"gotestsum-failures.log", testlog.Summary{Tests: 4183, Skipped: 55, Failures: 1}},
		{"gotestsum-failures-macos.log", testlog.Summary{Tests: 3100, Skipped: 50, Failures: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			got := parse(t, fixture(t, tt.file)).Summary
			if got == nil || *got != tt.want {
				t.Errorf("Summary = %v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseSummaryForms(t *testing.T) {
	tests := []struct {
		line string
		want testlog.Summary
	}{
		{"DONE 30 tests in 2.511s", testlog.Summary{Tests: 30}},
		{"DONE 4211 tests, 55 skipped in 305.697s", testlog.Summary{Tests: 4211, Skipped: 55}},
		{"DONE 10 tests, 1 failure in 1.0s", testlog.Summary{Tests: 10, Failures: 1}},
		{"DONE 10 tests, 2 skipped, 2 failures in 1.0s", testlog.Summary{Tests: 10, Skipped: 2, Failures: 2}},
		{"DONE 1 tests, 0 failures in 0.1s", testlog.Summary{Tests: 1}},
		{"DONE 120 tests, 1 failure, 2 errors in 3.1s", testlog.Summary{Tests: 120, Failures: 1, Errors: 2}},
		{"DONE 7 tests, 3 skipped, 1 error in 3.1s", testlog.Summary{Tests: 7, Skipped: 3, Errors: 1}},
		{"DONE 2 runs, 4183 tests, 55 skipped, 1 failure in 263.8s", testlog.Summary{Tests: 4183, Skipped: 55, Failures: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := parse(t, tt.line+"\n").Summary
			if got == nil || *got != tt.want {
				t.Errorf("Summary = %v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseSummaryTooLarge(t *testing.T) {
	if got := parse(t, "DONE 99999999999999999999 tests in 1s\n").Summary; got != nil {
		t.Errorf("Summary = %+v, want nil", *got)
	}
}

func TestParseNoSummary(t *testing.T) {
	for _, log := range []string{
		"",
		"=== RUN   TestX\n--- PASS: TestX (0.00s)\nPASS\nok  \texample.com/x\t0.1s\n",
		fixture(t, "gotest-panic.log"),
	} {
		if got := parse(t, log).Summary; got != nil {
			t.Errorf("Summary = %+v, want nil", *got)
		}
	}
}

func TestParseDropsSkipsAndPasses(t *testing.T) {
	log := fixture(t, "gotestsum-pass.log")
	res := parse(t, log)
	if len(res.Failures) != 0 {
		t.Errorf("Failures = %#v, want none", res.Failures)
	}
	if res.Summary == nil || *res.Summary != (testlog.Summary{Tests: 4211, Skipped: 55}) {
		t.Errorf("Summary = %v, want 4211 tests, 55 skipped", res.Summary)
	}

	// A SKIP block's lines must not leak into a failure that follows it.
	mixed := fixture(t, "gotestsum-failures.log")
	for _, f := range parse(t, mixed).Failures {
		for _, l := range f.Lines {
			if strings.Contains(l, "differ only in case") || strings.Contains(l, "SKIP") {
				t.Errorf("failure %s has a skipped test's line: %q", f.Name, l)
			}
		}
	}
}

func TestParseGoTestVerbose(t *testing.T) {
	got := parse(t, fixture(t, "gotest-verbose-failures.log")).Failures
	var names []string
	for _, f := range got {
		if f.Package != "example.com/demo" {
			t.Errorf("%s: Package = %q, want example.com/demo", f.Name, f.Package)
		}
		names = append(names, f.Name)
	}
	want := []string{"TestParent", "TestParent/bad", "TestParent/bad/deep"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}

	// With -v, a test's messages come under its === RUN or === NAME line,
	// before the --- FAIL line.
	wantLines := map[string][]string{
		"TestParent":          {"    demo_test.go:11: parent also failed"},
		"TestParent/bad":      {"    demo_test.go:8: want 1, got 2"},
		"TestParent/bad/deep": {"    demo_test.go:9: deep broke"},
	}
	for _, f := range got {
		if !reflect.DeepEqual(f.Lines, wantLines[f.Name]) {
			t.Errorf("%s: Lines = %q, want %q", f.Name, f.Lines, wantLines[f.Name])
		}
	}

	// Without -v, go test prints the failure's lines right under the
	// --- FAIL line.
	plain := parse(t, fixture(t, "gotest-panic.log")).Failures
	if len(plain) == 0 || plain[0].Name != "TestBefore" || plain[0].Package != "example.com/p" ||
		!reflect.DeepEqual(plain[0].Lines, []string{"    p_test.go:5: failed before the panic"}) {
		t.Errorf("plain go test failures = %#v", plain)
	}
}

func TestParseSubtestsSeparate(t *testing.T) {
	got := parse(t, fixture(t, "gotest-verbose-failures.log")).Failures
	byName := map[string]testlog.Failure{}
	for _, f := range got {
		byName[f.Name] = f
	}
	for _, n := range []string{"TestParent", "TestParent/bad", "TestParent/bad/deep"} {
		if _, ok := byName[n]; !ok {
			t.Fatalf("missing failure %s in %#v", n, got)
		}
	}
	for _, l := range byName["TestParent"].Lines {
		if strings.Contains(l, "--- FAIL") || strings.Contains(l, "--- PASS") {
			t.Errorf("parent's lines include a subtest's block: %q", l)
		}
	}
	for _, f := range got {
		if len(f.Lines) > 0 && strings.Contains(strings.Join(f.Lines, "\n"), "TestSkipped") {
			t.Errorf("%s has the skipped test's lines: %q", f.Name, f.Lines)
		}
	}
}

func TestParsePanic(t *testing.T) {
	res := parse(t, fixture(t, "gotest-panic.log"))
	var boom *testlog.Failure
	for i := range res.Failures {
		if res.Failures[i].Name == "TestBoom" {
			boom = &res.Failures[i]
		}
	}
	if boom == nil {
		t.Fatalf("TestBoom not among %#v", res.Failures)
	}
	if boom.Package != "example.com/p" {
		t.Errorf("Package = %q, want example.com/p", boom.Package)
	}
	joined := strings.Join(boom.Lines, "\n")
	for _, want := range []string{
		"panic: assignment to entry in nil map",
		"goroutine 36 [running]:",
		"example.com/p.TestBoom(",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("TestBoom lines lack %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "FAIL\texample.com/p") {
		t.Errorf("TestBoom lines run into the package FAIL line:\n%s", joined)
	}
}

func TestParseDedupesRepeatedFailure(t *testing.T) {
	log := strings.Join([]string{
		"=== FAIL: internal/vm TestA (0.01s)",
		"    a_test.go:1: first",
		"",
		"=== FAIL: internal/vm TestB (0.01s)",
		"    b_test.go:2: only here",
		"",
		"=== Failed",
		"=== FAIL: internal/vm TestA (0.01s)",
		"    a_test.go:1: first",
		"    a_test.go:2: second, only in the repeat",
		"",
		"DONE 2 tests, 2 failures in 1s",
		"",
	}, "\n")
	got := parse(t, log).Failures
	want := []testlog.Failure{
		{Package: "internal/vm", Name: "TestA", Lines: []string{
			"    a_test.go:1: first",
			"    a_test.go:2: second, only in the repeat",
		}},
		{Package: "internal/vm", Name: "TestB", Lines: []string{"    b_test.go:2: only here"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Failures = %#v, want %#v", got, want)
	}
}

func TestParseStripsPrefixes(t *testing.T) {
	const (
		jobStep = "check (ubuntu-latest, UTC)\tRun mise run test\t"
		stamp   = "2026-11-20T08:52:20.9321737Z "
	)
	lines := []string{
		"=== %sFAIL%s: internal/vm TestA (0.01s)",
		"    a_test.go:1: boom",
		"",
		"DONE 5 tests, 1 skipped, 1 failure in 1.5s",
	}
	build := func(prefix, on, off string) string {
		var b strings.Builder
		for i, l := range lines {
			if i == 0 {
				l = strings.Replace(strings.Replace(l, "%s", on, 1), "%s", off, 1)
			}
			b.WriteString(prefix + l + "\n")
		}
		return b.String()
	}
	tests := []struct{ name, log string }{
		{"plain", build("", "", "")},
		{"timestamp", build(stamp, "", "")},
		{"job, step and timestamp", build(jobStep+stamp, "", "")},
		{"escape codes", build(jobStep+stamp, "\x1b[31m", "\x1b[0m")},
		{"gh's caret form of escape codes", build(jobStep+stamp, "^[[31m", "^[[0m")},
	}
	want := testlog.Result{
		Failures: []testlog.Failure{{Package: "internal/vm", Name: "TestA", Lines: []string{"    a_test.go:1: boom"}}},
		Summary:  &testlog.Summary{Tests: 5, Skipped: 1, Failures: 1},
		Runs:     []testlog.Summary{{Tests: 5, Skipped: 1, Failures: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parse(t, tt.log); !reflect.DeepEqual(got, want) {
				t.Errorf("Result = %#v (summary %+v), want %#v (summary %+v)", got, got.Summary, want, want.Summary)
			}
		})
	}
}

func TestParseStripsPrefixesOnLocalFixtures(t *testing.T) {
	// The local go test output has no gh prefix; add one the way gh does.
	const prefix = "check (ubuntu-latest, UTC)\tRun mise run test\t2026-11-20T08:52:20.9321737Z "
	plain := fixture(t, "gotest-panic.log")
	var b strings.Builder
	for _, l := range strings.SplitAfter(plain, "\n") {
		if l != "" {
			b.WriteString(prefix + l)
		}
	}
	if got, want := parse(t, b.String()), parse(t, plain); !reflect.DeepEqual(got, want) {
		t.Errorf("prefixed Result = %#v, want %#v", got, want)
	}
}

type failingReader struct {
	data string
	err  error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.data == "" {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestParseReturnsReadError(t *testing.T) {
	boom := errors.New("connection reset")
	r := &failingReader{data: "=== FAIL: internal/vm TestA (0.01s)\n    a_test.go:1: x\n", err: boom}
	if _, err := testlog.Parse(r); !errors.Is(err, boom) || !strings.Contains(err.Error(), "read test log") {
		t.Errorf("err = %v, want %v", err, boom)
	}
	if _, err := testlog.Parse(io.MultiReader(strings.NewReader("x\n"), &failingReader{err: boom})); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

func TestParseEdgeCases(t *testing.T) {
	long := strings.Repeat("x", 5<<20)
	tests := []struct {
		name string
		log  string
		want []testlog.Failure
	}{
		{"line longer than any scanner buffer", "=== FAIL: p TestA (0s)\n    " + long + "\n", []testlog.Failure{{Package: "p", Name: "TestA", Lines: []string{"    " + long}}}},
		{"CRLF line endings", "=== FAIL: p TestA (0s)\r\n    a_test.go:1: x\r\n\r\n", []testlog.Failure{{Package: "p", Name: "TestA", Lines: []string{"    a_test.go:1: x"}}}},
		{"no trailing newline", "=== FAIL: p TestA (0s)\n    a_test.go:1: x", []testlog.Failure{{Package: "p", Name: "TestA", Lines: []string{"    a_test.go:1: x"}}}},
		{"FAIL line with tabs is not mistaken for a prefix", "--- FAIL: TestA (0s)\n    a_test.go:1: x\nFAIL\tp\t0.1s\n", []testlog.Failure{{Package: "p", Name: "TestA", Lines: []string{"    a_test.go:1: x"}}}},
		{"error annotation ends a block", "=== FAIL: p TestA (0s)\n    a_test.go:1: x\n##[error]Process completed\n", []testlog.Failure{{Package: "p", Name: "TestA", Lines: []string{"    a_test.go:1: x"}}}},
		{"no failures", "ok  \tp\t0.1s\nPASS\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parse(t, tt.log).Failures; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Failures = %.300v, want %.300v", got, tt.want)
			}
		})
	}
}

func TestParseKeepsDiffAndBlankContinuation(t *testing.T) {
	tests := []struct {
		name string
		log  string
		want []string
	}{
		{
			"testify diff",
			"=== FAIL: p TestA (0s)\n    a_test.go:1: \n        \tDiff:\n        \t--- Expected\n        \t+++ Actual\n        \t@@ -1 +1 @@\n\nDONE 1 tests, 1 failure in 1s\n",
			[]string{"    a_test.go:1: ", "        \tDiff:", "        \t--- Expected", "        \t+++ Actual", "        \t@@ -1 +1 @@"},
		},
		{
			"blank line inside a message",
			"=== FAIL: p TestA (0s)\n    a_test.go:1: got:\n        x\n        \n        want:\n        y\n\nDONE 1 tests, 1 failure in 1s\n",
			[]string{"    a_test.go:1: got:", "        x", "        ", "        want:", "        y"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parse(t, tt.log).Failures
			if len(got) != 1 || !reflect.DeepEqual(got[0].Lines, tt.want) {
				t.Errorf("Failures = %#v, want one with Lines %q", got, tt.want)
			}
		})
	}
}

func TestParseGoTestThenGotestsumSummary(t *testing.T) {
	log := strings.Join([]string{
		"--- FAIL: TestA (0.01s)",
		"    a_test.go:1: first",
		"FAIL",
		"FAIL\tgithub.com/devvmhq/devvm/internal/vm\t0.5s",
		"FAIL",
		"",
		"=== Failed",
		"=== FAIL: internal/vm TestA (0.01s)",
		"    a_test.go:1: first",
		"",
		"DONE 2 tests, 1 failure in 1s",
		"",
	}, "\n")
	got := parse(t, log).Failures
	want := []testlog.Failure{{Package: "internal/vm", Name: "TestA", Lines: []string{"    a_test.go:1: first"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Failures = %#v, want %#v", got, want)
	}
}

func TestParseTimeout(t *testing.T) {
	timeout := []string{
		"panic: test timed out after 10m0s",
		"\trunning tests:",
		"\t\tTestSlow (10m0s)",
		"\t\tTestSlow/sub (9m0s)",
		"",
		"goroutine 1 [chan receive]:",
		"testing.(*T).Run(0x1)",
		"\t/usr/local/go/src/testing/testing.go:1 +0x1",
		"exit status 2",
		"FAIL\texample.com/p\t600.123s",
		"FAIL",
	}
	tests := []struct {
		name string
		log  string
	}{
		{"plain", strings.Join(timeout, "\n") + "\n"},
		{"verbose", "=== RUN   TestSlow\n    slow_test.go:3: before hang\n" + strings.Join(timeout, "\n") + "\n"},
		{"prefixed", "job\tstep\t2026-11-20T08:52:20.9321737Z " + strings.Join(timeout, "\njob\tstep\t2026-11-20T08:52:20.9321737Z ") + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parse(t, tt.log).Failures
			if len(got) != 2 || got[0].Name != "TestSlow" || got[1].Name != "TestSlow/sub" {
				t.Fatalf("Failures = %#v, want TestSlow and TestSlow/sub", got)
			}
			for _, f := range got {
				if len(f.Lines) == 0 {
					t.Fatalf("%s: no Lines", f.Name)
				}
				if f.Package != "example.com/p" {
					t.Errorf("%s: Package = %q", f.Name, f.Package)
				}
				const panicLine = "panic: test timed out after 10m0s"
				at := 0
				if tt.name == "verbose" && f.Name == "TestSlow" {
					// The test's own output comes before the panic.
					if f.Lines[0] != "    slow_test.go:3: before hang" {
						t.Errorf("%s: Lines = %q, want the test's own output first", f.Name, f.Lines)
					}
					at = 1
				}
				if len(f.Lines) <= at || f.Lines[at] != panicLine {
					t.Errorf("%s: Lines = %q, want the panic at %d", f.Name, f.Lines, at)
				}
				if last := f.Lines[len(f.Lines)-1]; strings.HasPrefix(last, "exit status") || strings.HasPrefix(last, "FAIL") {
					t.Errorf("%s: Lines end with %q", f.Name, last)
				}
			}
		})
	}
}

func TestParseTimeoutAtEOF(t *testing.T) {
	got := parse(t, "panic: test timed out after 1s\n\trunning tests:\n\t\tTestSlow (1s)\n").Failures
	if len(got) != 1 || got[0].Name != "TestSlow" {
		t.Errorf("Failures = %#v", got)
	}
}

func TestParsePanicExitStatus(t *testing.T) {
	got := parse(t, "--- FAIL: TestBoom (0s)\npanic: x [recovered]\n\ngoroutine 1 [running]:\nexit status 2\nFAIL\tp\t0.1s\n").Failures
	if len(got) != 1 || len(got[0].Lines) == 0 || got[0].Lines[len(got[0].Lines)-1] != "goroutine 1 [running]:" {
		t.Errorf("Failures = %#v", got)
	}
}

func TestParseTimeoutInsideGotestsumBlock(t *testing.T) {
	log := "=== FAIL: internal/vm TestSlow (unknown)\npanic: test timed out after 10m0s\n\trunning tests:\n\t\tTestSlow (10m0s)\n\ngoroutine 1 [chan receive]:\n\n=== FAIL: internal/vm TestOther (0.01s)\n    o_test.go:1: x\n\nDONE 2 tests, 2 failures in 1s\n"
	got := parse(t, log).Failures
	if len(got) != 2 || got[0].Name != "TestSlow" || got[1].Name != "TestOther" {
		t.Fatalf("Failures = %#v, want TestSlow and TestOther", got)
	}
	if len(got[0].Lines) == 0 || got[0].Lines[0] != "panic: test timed out after 10m0s" {
		t.Errorf("TestSlow Lines = %q", got[0].Lines)
	}
	if !reflect.DeepEqual(got[1].Lines, []string{"    o_test.go:1: x"}) {
		t.Errorf("TestOther Lines = %q", got[1].Lines)
	}
}

func TestParseTimeoutEndsAtGotestsumBlock(t *testing.T) {
	log := "panic: test timed out after 1s\n\trunning tests:\n\t\tTestSlow (1s)\n=== FAIL: p TestOther (0s)\n    o_test.go:1: x\n"
	got := parse(t, log).Failures
	var other *testlog.Failure
	for i := range got {
		if got[i].Name == "TestOther" {
			other = &got[i]
		}
	}
	if other == nil || !reflect.DeepEqual(other.Lines, []string{"    o_test.go:1: x"}) {
		t.Errorf("Failures = %#v", got)
	}
}

func TestParsePackageLevelFailure(t *testing.T) {
	log := "=== FAIL: internal/vm  (0.00s)\npanic: init failed\n\ngoroutine 1 [running]:\n\nDONE 0 tests, 1 failure in 1s\n"
	got := parse(t, log).Failures
	want := []testlog.Failure{{Package: "internal/vm", Name: "(package)", Lines: []string{"panic: init failed", "", "goroutine 1 [running]:"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Failures = %#v, want %#v", got, want)
	}
}

func TestParseIndentedFailInsideGotestsumBlock(t *testing.T) {
	log := "=== FAIL: p TestA (0.00s)\n" +
		"    a_test.go:1: x\n" +
		"    --- FAIL: TestA/sub (0.00s)\n" +
		"        a_test.go:2: y\n" +
		"panic: boom\n" +
		"\n" +
		"goroutine 1 [running]:\n" +
		"\n" +
		"DONE 2 tests, 1 failure in 1s\n"
	got := parse(t, log).Failures
	want := []testlog.Failure{{Package: "p", Name: "TestA", Lines: []string{
		"    a_test.go:1: x",
		"    --- FAIL: TestA/sub (0.00s)",
		"        a_test.go:2: y",
		"panic: boom",
		"",
		"goroutine 1 [running]:",
	}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Failures = %#v, want %#v", got, want)
	}
}

func TestParseCrashWithoutFailLine(t *testing.T) {
	log := "--- FAIL: TestX (0.00s)\n" +
		"    x_test.go:1: bad\n" +
		"FAIL\n" +
		"FAIL\tq\t0.1s\n" +
		"panic: boom\n" +
		"goroutine 7 [running]:\n" +
		"main.go:1\n" +
		"exit status 2\n" +
		"FAIL\tp\t0.1s\n"
	got := parse(t, log).Failures
	want := []testlog.Failure{
		{Package: "q", Name: "TestX", Lines: []string{"    x_test.go:1: bad"}},
		{Package: "p", Name: "(package)", Lines: []string{"panic: boom", "goroutine 7 [running]:", "main.go:1"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Failures = %#v, want %#v", got, want)
	}
}

func TestParseTwoGotestsumRuns(t *testing.T) {
	got := parse(t, fixture(t, "gotestsum-two-runs.log")).Summary
	// 4211 tests and 55 skipped from the unit run, 30 tests from the e2e run.
	if want := (testlog.Summary{Tests: 4241, Skipped: 55}); got == nil || *got != want {
		t.Errorf("Summary = %v, want %+v", got, want)
	}
	runs := parse(t, fixture(t, "gotestsum-two-runs.log")).Runs
	if want := []testlog.Summary{{Tests: 4211, Skipped: 55}, {Tests: 30}}; !reflect.DeepEqual(runs, want) {
		t.Errorf("Runs = %+v, want %+v", runs, want)
	}
}

func TestParseKeepsPlainFailuresBesideGotestsum(t *testing.T) {
	log := "=== FAIL: p TestA (0.00s)\n" +
		"    a_test.go:1: x\n" +
		"\n" +
		"DONE 1 tests, 1 failure in 1s\n" +
		"--- FAIL: TestB (0.00s)\n" +
		"    b_test.go:1: y\n" +
		"FAIL\n" +
		"FAIL\tq\t0.1s\n"
	got := parse(t, log).Failures
	want := []testlog.Failure{
		{Package: "p", Name: "TestA", Lines: []string{"    a_test.go:1: x"}},
		{Package: "q", Name: "TestB", Lines: []string{"    b_test.go:1: y"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Failures = %#v, want %#v", got, want)
	}
}

func TestParseIndentedPassInsideGotestsumBlock(t *testing.T) {
	log := "=== FAIL: p TestA (0.00s)\n" +
		"    --- PASS: TestA/ok (0.00s)\n" +
		"    a_test.go:1: x\n" +
		"\n" +
		"DONE 2 tests, 1 failure in 1s\n"
	got := parse(t, log).Failures
	want := []testlog.Failure{{Package: "p", Name: "TestA", Lines: []string{"    --- PASS: TestA/ok (0.00s)", "    a_test.go:1: x"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Failures = %#v, want %#v", got, want)
	}
}

func TestParseDropsPlainRepeatOfGotestsumFailure(t *testing.T) {
	log := "=== FAIL: internal/vm TestA (0.00s)\n" +
		"    a_test.go:1: x\n" +
		"\n" +
		"DONE 1 tests, 1 failure in 1s\n" +
		"--- FAIL: TestA (0.00s)\n" +
		"    a_test.go:1: x\n" +
		"FAIL\n" +
		"FAIL\texample.com/m/internal/vm\t0.1s\n"
	got := parse(t, log).Failures
	want := []testlog.Failure{{Package: "internal/vm", Name: "TestA", Lines: []string{"    a_test.go:1: x"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Failures = %#v, want %#v", got, want)
	}
}

func TestParseCrashes(t *testing.T) {
	crash := []string{"panic: boom", "", "goroutine 7 [running]:", "main.go:1"}
	passed := "=== RUN   TestOK\n--- PASS: TestOK (0.00s)\nPASS\nok  \ta\t0.1s\n"
	for _, c := range []struct {
		name string
		log  string
		want []testlog.Failure
	}{
		{
			"verbose, no failing test", "=== RUN   TestX\n" + strings.Join(crash, "\n") + "\nexit status 2\nFAIL\tp\t0.1s\n",
			[]testlog.Failure{{Package: "p", Name: "(package)", Lines: crash}},
		},
		{
			"after a failing test of the package", "--- FAIL: TestA (0.00s)\n    a_test.go:1: x\n" + strings.Join(crash, "\n") + "\nexit status 2\nFAIL\tp\t0.1s\n",
			[]testlog.Failure{
				{Package: "p", Name: "TestA", Lines: []string{"    a_test.go:1: x"}},
				{Package: "p", Name: "(package)", Lines: crash},
			},
		},
		{
			"verbose, after a failing test of the package", "=== RUN   TestA\n    a_test.go:1: x\n--- FAIL: TestA (0.00s)\n=== RUN   TestB\n" + strings.Join(crash, "\n") + "\nexit status 2\nFAIL\tp\t0.1s\n",
			[]testlog.Failure{
				{Package: "p", Name: "TestA", Lines: []string{"    a_test.go:1: x"}},
				{Package: "p", Name: "(package)", Lines: crash},
			},
		},
		{
			"log.Fatal after a failing test, package list", "--- FAIL: TestA (0.00s)\n    a_test.go:1: x\n2026/11/22 10:00:00 db gone\nFAIL\tp\t0.1s\n",
			[]testlog.Failure{
				{Package: "p", Name: "TestA", Lines: []string{"    a_test.go:1: x"}},
				{Package: "p", Name: "(package)", Lines: []string{"2026/11/22 10:00:00 db gone"}},
			},
		},
		{
			"log.Fatal after a failing test, exit status", "--- FAIL: TestA (0.00s)\n    a_test.go:1: x\n2026/11/22 10:00:00 db gone\nexit status 1\nFAIL\tp\t0.1s\n",
			[]testlog.Failure{
				{Package: "p", Name: "TestA", Lines: []string{"    a_test.go:1: x"}},
				{Package: "p", Name: "(package)", Lines: []string{"2026/11/22 10:00:00 db gone"}},
			},
		},
		{
			"TestMain output after the end of a normal run", "--- FAIL: TestA (0.00s)\n    a_test.go:1: x\nFAIL\ncleanup done\nexit status 1\nFAIL\tp\t0.1s\n",
			[]testlog.Failure{{Package: "p", Name: "TestA", Lines: []string{"    a_test.go:1: x"}}},
		},
		{
			"log noise before a timeout", "2026/11/22 10:00:00 setup\npanic: test timed out after 1s\n\trunning tests:\n\t\tTestSlow (1s)\n\nexit status 2\nFAIL\tp\t1s\n",
			[]testlog.Failure{{Package: "p", Name: "TestSlow", Lines: []string{"panic: test timed out after 1s", "\trunning tests:", "\t\tTestSlow (1s)"}}},
		},
		{
			"verbose, after a passing package, panic after a failing test", passed + "=== RUN   TestA\n    a_test.go:1: x\n--- FAIL: TestA (0.00s)\n=== RUN   TestB\n" + strings.Join(crash, "\n") + "\nexit status 2\nFAIL\tp\t0.1s\n",
			[]testlog.Failure{
				{Package: "p", Name: "TestA", Lines: []string{"    a_test.go:1: x"}},
				{Package: "p", Name: "(package)", Lines: crash},
			},
		},
		{
			"after a passing package, log.Fatal after a failing test", passed + "--- FAIL: TestA (0.00s)\n    a_test.go:1: x\n2026/11/22 10:00:00 db gone\nexit status 1\nFAIL\tp\t0.1s\n",
			[]testlog.Failure{
				{Package: "p", Name: "TestA", Lines: []string{"    a_test.go:1: x"}},
				{Package: "p", Name: "(package)", Lines: []string{"2026/11/22 10:00:00 db gone"}},
			},
		},
		{
			"after a package with no test files", "?   \tq\t[no test files]\npanic: boom\nexit status 2\nFAIL\tp\t0.1s\n",
			[]testlog.Failure{{Package: "p", Name: "(package)", Lines: []string{"panic: boom"}}},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := parse(t, c.log).Failures; !reflect.DeepEqual(got, c.want) {
				t.Errorf("Failures = %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestParseKeepsPlainFailureOfAmbiguousPackage(t *testing.T) {
	// Two packages fail TestA under gotestsum, and a plain go test step's
	// TestA never got its FAIL line, so it can't be said to repeat either.
	log := "=== FAIL: p TestA (0.00s)\n    a_test.go:1: x\n\n" +
		"=== FAIL: r TestA (0.00s)\n    a_test.go:1: x\n\n" +
		"DONE 2 tests, 2 failures in 1s\n" +
		"--- FAIL: TestA (0.00s)\n    a_test.go:2: y\n"
	got := parse(t, log).Failures
	if len(got) != 3 || !reflect.DeepEqual(got[2], testlog.Failure{Name: "TestA", Lines: []string{"    a_test.go:2: y"}}) {
		t.Errorf("Failures = %#v, want the plain TestA kept as a third", got)
	}
}

func TestParseFailingExampleKeepsGotWant(t *testing.T) {
	lines := []string{"got:", "hello", "want:", "bye"}
	want := []testlog.Failure{{Package: "p", Name: "ExampleHello", Lines: lines}}
	for name, log := range map[string]string{
		"plain":   "--- FAIL: ExampleHello (0.00s)\n" + strings.Join(lines, "\n") + "\nFAIL\nFAIL\tp\t0.1s\n",
		"verbose": "=== RUN   ExampleHello\n--- FAIL: ExampleHello (0.00s)\n" + strings.Join(lines, "\n") + "\nFAIL\nFAIL\tp\t0.1s\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got := parse(t, log).Failures; !reflect.DeepEqual(got, want) {
				t.Errorf("Failures = %#v, want %#v", got, want)
			}
		})
	}
}

func TestParseRepeatedGotestsumBlocksDropPlainRepeat(t *testing.T) {
	log := "=== FAIL: p TestA (0.00s)\n    a_test.go:1: x\n\n" +
		"=== Failed\n" +
		"=== FAIL: p TestA (0.00s)\n    a_test.go:1: x\n\n" +
		"DONE 1 tests, 1 failure in 1s\n" +
		"--- FAIL: TestA (0.00s)\n    a_test.go:1: x\n"
	got := parse(t, log).Failures
	want := []testlog.Failure{{Package: "p", Name: "TestA", Lines: []string{"    a_test.go:1: x"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Failures = %#v, want %#v", got, want)
	}
}

func TestParseCrashAfterTrailingFailOfEarlierRun(t *testing.T) {
	log := "--- FAIL: TestA (0.00s)\n    a_test.go:1: x\nFAIL\nFAIL\tq\t0.1s\nFAIL\n" +
		"--- FAIL: TestB (0.00s)\n    b_test.go:1: y\n2026/11/22 10:00:00 db gone\nexit status 1\nFAIL\tp\t0.1s\n"
	got := parse(t, log).Failures
	want := []testlog.Failure{
		{Package: "q", Name: "TestA", Lines: []string{"    a_test.go:1: x"}},
		{Package: "p", Name: "TestB", Lines: []string{"    b_test.go:1: y"}},
		{Package: "p", Name: "(package)", Lines: []string{"2026/11/22 10:00:00 db gone"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Failures = %#v, want %#v", got, want)
	}
}
