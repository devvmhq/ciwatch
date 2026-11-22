// Package testlog parses the log of a failed CI job that ran go test or
// gotestsum and returns each failing test with the lines that follow it.
package testlog

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Failure is one failing test.
type Failure struct {
	Package string   // "" if the log never names it
	Name    string   // "TestBoot/arm64"
	Lines   []string // the lines after the FAIL line, prefixes removed

	// RunningTest is the test that was running when a "(package)" crash
	// happened during a verbose run; "" when unknown.
	RunningTest string
}

// Summary is gotestsum's DONE line.
type Summary struct{ Tests, Skipped, Failures, Errors int }

// Result is what Parse found in a log.
type Result struct {
	Failures []Failure // in log order
	Summary  *Summary  // the totals of every DONE line, since a job can run gotestsum more than once; nil when there's none
	Runs     []Summary // each DONE line, in log order
}

var (
	// gh's "job<TAB>step<TAB>" prefix is only taken off together with the
	// Actions timestamp, so a "FAIL<TAB>pkg<TAB>0.1s" line is left alone.
	prefixRE = regexp.MustCompile(`^(?:[^\t]*\t[^\t]*\t)?\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z ?`)
	// gh prints an escape byte as the text "^[".
	ansiRE     = regexp.MustCompile(`(?:\x1b|\^\[)\[[0-9;]*m`)
	gotestsumF = regexp.MustCompile(`^=== FAIL: (\S+) (\S*) ?\(`)
	goTestF    = regexp.MustCompile(`^\s*--- FAIL: (\S+) \(`)
	goTestDone = regexp.MustCompile(`^\s*--- (?:PASS|SKIP): (\S+) \(`)
	runRE      = regexp.MustCompile(`^\s*=== (?:RUN|NAME|CONT)\s+(\S+)`)
	// markerRE matches the lines that start or end a block without being part
	// of a failure's lines. A bare "--- " or "=== " is not one: a diff printed
	// by a test starts its lines with "--- Expected".
	markerRE  = regexp.MustCompile(`^(?:\s*--- (?:FAIL|PASS|SKIP): |\s*=== (?:RUN|PAUSE|CONT|NAME|FAIL|PASS|SKIP|Failed|Errors|Skipped)\b|##\[|DONE \d|exit status \d+$|ok  ?\t|\?   \t|FAIL\t|PASS$|FAIL$)`)
	exitRE    = regexp.MustCompile(`^exit status \d+$`)
	timeoutRE = regexp.MustCompile(`^panic: test timed out after `)
	// subResultRE matches a subtest's indented result line, which inside
	// gotestsum's block for a test is part of that test's lines.
	subResultRE = regexp.MustCompile(`^\s+--- (?:FAIL|PASS|SKIP): `)
	runningRE   = regexp.MustCompile(`^\s+(\S+) \([^)]*\)$`)
	// buildHeaderRE is the "# pkg [pkg.test]" line that starts a package's
	// compile errors; buildFileRE is an error line, which names its file.
	buildHeaderRE = regexp.MustCompile(`^# (\S+)(?: \[[^\]]*\])?$`)
	buildFileRE   = regexp.MustCompile(`^(\S+\.go):\d+`)
	// buildLineRE is what continues a block of compile errors: an error line,
	// an indented note, vet's output or the compiler's "too many errors".
	buildLineRE = regexp.MustCompile(`^(?:\S+\.go:\d+|\s|vet: |too many errors|# )`)
	pkgFailRE   = regexp.MustCompile(`^FAIL\t(\S+)`)
	doneRE      = regexp.MustCompile(`^DONE (?:\d+ runs?, )?(\d+) tests?(?:, (\d+) skipped)?(?:, (\d+) failures?)?(?:, (\d+) errors?)? in `)
)

// stripPrefix removes the gh job and step, the Actions timestamp and ANSI
// colour codes, in that order.
func stripPrefix(line string) string {
	line = strings.TrimSuffix(line, "\r")
	line = prefixRE.ReplaceAllString(line, "")
	return ansiRE.ReplaceAllString(line, "")
}

// buildBlock is one package's compile errors, from its "# pkg" line on.
type buildBlock struct {
	pkg     string // the import path in the "# pkg" line
	lines   []string
	claimed bool // given to a package's "FAIL<TAB>pkg" line
}

// matches reports whether the block belongs to pkg: its "# pkg" line names it
// (or its external test package), or, when byFile is set and the header names
// no package ("command-line-arguments"), the directory of a file in its error
// lines is the package's path or the end of it.
func (b *buildBlock) matches(pkg string, byFile bool) bool {
	if !byFile {
		return b.pkg == pkg || b.pkg == pkg+"_test"
	}
	if b.pkg != "command-line-arguments" {
		return false
	}
	for _, l := range b.lines {
		m := buildFileRE.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		dir := path.Dir(path.Clean(m[1]))
		if dir != "." && dir != "/" && (pkg == dir || strings.HasSuffix(pkg, "/"+dir)) {
			return true
		}
	}
	return false
}

// found is a failure and which format reported it.
type found struct {
	Failure
	goTest bool // from a "--- FAIL:" line, not gotestsum's "=== FAIL:"
}

// Parse reads a log and returns its failing tests and gotestsum summary.
func Parse(r io.Reader) (Result, error) {
	var (
		res       Result
		all       []found
		cur       = -1  // index in all of the open failure
		panicked  bool  // the open failure has hit a panic, which spans blank lines
		pending   []int // failures waiting for a "FAIL<TAB>pkg" line
		out       string
		summaries []Summary     // every DONE line, in log order
		stray     []string      // lines outside any test since the last marker, where a crash with no "--- FAIL:" line lands
		crashed   bool          // a crash has been seen since the last "FAIL<TAB>pkg" line
		ran       bool          // a test has started or finished since the last "FAIL<TAB>pkg", "ok" or step line, so a bare "FAIL" is testing's own and not the run's closing one
		ended     bool          // testing's own "FAIL" or "PASS" line has been seen since the last "FAIL<TAB>pkg" line: the run finished, so what follows is not a crash
		timeout   []string      // lines of a "test timed out" panic being read; nil when none
		builds    []*buildBlock // compile errors, kept until a package's "FAIL<TAB>pkg" line claims them
		build     *buildBlock   // the block being read; nil when none
		crashTest string        // the -v test that was running when the package crashed
		timing    bool
		running   bool                    // inside the timeout's "running tests:" list
		names     []string                // the tests that list names
		output    = map[string][]string{} // go test -v output of the test named in out, and of tests before it
		br        = bufio.NewReader(r)
	)
	// endTimeout turns a finished "test timed out" panic into one failure per
	// running test. go test prints no "--- FAIL:" line for them.
	endTimeout := func() {
		if !timing {
			return
		}
		if len(names) == 0 {
			names = []string{"(timeout)"}
		}
		for _, n := range names {
			lines := append(append([]string(nil), output[n]...), timeout...)
			delete(output, n)
			all = append(all, found{Failure: Failure{Name: n, Lines: lines}, goTest: true})
			pending = append(pending, len(all)-1)
		}
		timing, running, timeout, names = false, false, nil, nil
	}
	// takeOut turns a -v test that never finished into crash lines: the package
	// ended while the test was still running.
	takeOut := func() {
		if out != "" {
			crashTest = out
			stray = append(stray, output[out]...)
			delete(output, out)
			out = ""
		}
	}
	hasText := func(lines []string) bool {
		for _, l := range lines {
			if strings.TrimSpace(l) != "" {
				return true
			}
		}
		return false
	}
	handle := func(line string) {
		line = stripPrefix(line)
		// A marker, gotestsum block or timeout ends the compile errors being read.
		if build != nil && (markerRE.MatchString(line) || gotestsumF.MatchString(line) || runRE.MatchString(line) || timeoutRE.MatchString(line) || doneRE.MatchString(line)) {
			build = nil
		}
		// Inside gotestsum's block for a test, the timeout panic is part of
		// that test's lines.
		inBlock := cur >= 0 && !all[cur].goTest
		if !inBlock && timeoutRE.MatchString(line) {
			endTimeout()
			timing, cur, out, stray = true, -1, "", nil
			timeout = []string{line}
			return
		}
		if timing && !gotestsumF.MatchString(line) && !pkgFailRE.MatchString(line) && !doneRE.MatchString(line) && !exitRE.MatchString(line) {
			if strings.TrimSpace(line) == "running tests:" {
				running = true
			} else if m := runningRE.FindStringSubmatch(line); m != nil && running {
				names = append(names, m[1])
			} else {
				running = false
			}
			timeout = append(timeout, line)
			return
		}
		endTimeout()
		if inBlock && subResultRE.MatchString(line) {
			all[cur].Lines = append(all[cur].Lines, line)
			return
		}
		if m := gotestsumF.FindStringSubmatch(line); m != nil {
			stray = nil
			name := m[2]
			if name == "" {
				name = "(package)" // gotestsum's line for a failure of the package itself
			}
			all = append(all, found{Failure: Failure{Package: m[1], Name: name}})
			cur, panicked, out = len(all)-1, false, ""
			return
		}
		if m := goTestF.FindStringSubmatch(line); m != nil {
			stray = nil
			ran = true
			f := Failure{Name: m[1], Lines: output[m[1]]}
			delete(output, m[1])
			all = append(all, found{Failure: f, goTest: true})
			cur, panicked, out = len(all)-1, false, ""
			pending = append(pending, cur)
			return
		}
		if m := pkgFailRE.FindStringSubmatch(line); m != nil {
			for _, i := range pending {
				all[i].Package = m[1]
			}
			// A package that failed with no "--- FAIL:" line of its own, or whose
			// output has a crash after its failures, crashed; the lines before
			// this one say how.
			takeOut()
			var claimed *buildBlock
			if strings.Contains(line, "[build failed]") || strings.Contains(line, "[setup failed]") {
				claimed = claimBuild(builds, m[1])
			}
			if claimed != nil {
				all = append(all, found{Failure: Failure{Package: m[1], Name: "(package)", Lines: claimed.lines}, goTest: true})
			} else if crashed || len(pending) == 0 || !ended {
				for len(stray) > 0 && strings.TrimSpace(stray[0]) == "" {
					stray = stray[1:]
				}
				if len(stray) > 0 {
					all = append(all, found{Failure: Failure{Package: m[1], Name: "(package)", Lines: stray, RunningTest: crashTest}, goTest: true})
				}
			}
			pending, cur, out, stray, crashed, ended, ran, crashTest = nil, -1, "", nil, false, false, false, ""
			return
		}
		if m := doneRE.FindStringSubmatch(line); m != nil {
			cur, out, stray = -1, "", nil
			if s, ok := summaryOf(m[1:]); ok {
				summaries = append(summaries, s)
			}
			return
		}
		if m := runRE.FindStringSubmatch(line); m != nil {
			cur, out, stray, ran = -1, m[1], nil, true
			return
		}
		if m := goTestDone.FindStringSubmatch(line); m != nil {
			delete(output, m[1])
			cur, out, stray, ran = -1, "", nil, true
			return
		}
		if markerRE.MatchString(line) {
			cur = -1
			// "exit status" ends a crash's lines; the "FAIL<TAB>pkg" line after it
			// is what collects them.
			if exitRE.MatchString(line) {
				takeOut()
				crashed = crashed || (!ended && hasText(stray))
			} else {
				if strings.HasPrefix(line, "ok") || strings.HasPrefix(line, "?") || strings.HasPrefix(line, "##[") {
					// A package that passed, or a new step, starts the next
					// package's run afresh.
					ended, crashed, ran = false, false, false
				} else {
					// The bare "FAIL" that closes a multi-package run comes after
					// the last package's line and must not mark the next run ended.
					ended = ended || (ran && (line == "FAIL" || line == "PASS"))
				}
				out, stray = "", nil
			}
			return
		}
		if out != "" {
			output[out] = append(output[out], line)
			return
		}
		// testing re-panics a test's own panic as "[recovered"; one without it
		// comes from a goroutine the test started and crashes the package, not
		// the test that was open.
		if cur >= 0 && all[cur].goTest && strings.HasPrefix(line, "panic: ") && !strings.Contains(line, "[recovered") {
			cur, panicked, crashed = -1, false, true
			stray = []string{line}
			return
		}
		// After "--- FAIL:" go test prints only the test's own output, which is
		// indented. An unindented line that isn't a panic (log.Fatal, os.Exit's
		// message) comes from the package dying, not from the test.
		if cur >= 0 && all[cur].goTest && !panicked && !strings.HasPrefix(all[cur].Name, "Example") && line != "" && line[0] != ' ' && line[0] != '\t' && !strings.HasPrefix(line, "panic: ") {
			cur = -1
		}
		if cur < 0 {
			// Only compiler output continues a block; anything else, such as a
			// panic from the next package, is not the build's.
			if build != nil && !buildLineRE.MatchString(line) {
				build = nil
			}
			if m := buildHeaderRE.FindStringSubmatch(line); m != nil {
				// vet's "# [pkg]" line goes on with the block of the "# pkg" before it.
				if strings.HasPrefix(m[1], "[") && build != nil {
					build.lines = append(build.lines, line)
					return
				}
				build = &buildBlock{pkg: strings.Trim(m[1], "[]"), lines: []string{line}}
				builds = append(builds, build)
				return
			}
			if build != nil {
				build.lines = append(build.lines, line)
				return
			}
			stray = append(stray, line)
			return
		}
		if line == "" {
			if !panicked {
				cur = -1
				return
			}
		} else if strings.HasPrefix(line, "panic: ") {
			panicked = true
		}
		all[cur].Lines = append(all[cur].Lines, line)
	}

	for {
		line, err := br.ReadString('\n')
		if line != "" {
			handle(strings.TrimSuffix(line, "\n"))
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Result{}, fmt.Errorf("read test log: %w", err)
		}
	}
	endTimeout()
	// Compile errors no package line claimed are kept, not dropped.
	var unclaimed []string
	for _, b := range builds {
		if !b.claimed {
			unclaimed = append(unclaimed, b.lines...)
		}
	}
	if len(unclaimed) > 0 {
		all = append(all, found{Failure: Failure{Name: "(build)", Lines: unclaimed}, goTest: true})
	}
	res.Failures = dedupe(all)
	res.Summary = total(summaries)
	res.Runs = summaries
	return res, nil
}

// claimBuild marks and returns the unclaimed block of compile errors that
// belongs to pkg: the one whose "# pkg" line names it, else the first whose
// error lines name a file in its directory. nil when none does.
func claimBuild(builds []*buildBlock, pkg string) *buildBlock {
	for _, byFile := range []bool{false, true} {
		for _, b := range builds {
			if !b.claimed && b.matches(pkg, byFile) {
				b.claimed = true
				return b
			}
		}
	}
	return nil
}

// summaryOf turns the captured counts of a DONE line into a Summary. An absent
// count is zero; a count too large for an int makes the line unusable.
func summaryOf(counts []string) (Summary, bool) {
	var n [4]int
	for i, c := range counts {
		if c == "" {
			continue
		}
		v, err := strconv.Atoi(c)
		if err != nil {
			return Summary{}, false
		}
		n[i] = v
	}
	return Summary{Tests: n[0], Skipped: n[1], Failures: n[2], Errors: n[3]}, true
}

// total adds up the summaries; nil when there are none.
func total(all []Summary) *Summary {
	if len(all) == 0 {
		return nil
	}
	var t Summary
	for _, s := range all {
		t.Tests += s.Tests
		t.Skipped += s.Skipped
		t.Failures += s.Failures
		t.Errors += s.Errors
	}
	return &t
}

// dedupe keeps the first entry for each package and name, with the version
// that has the most lines, and drops the blank lines a panic leaves at the end.
// A go test "--- FAIL:" entry that repeats a gotestsum one is dropped: it has
// the same name under the full import path, where gotestsum prints the path
// relative to the module (a plain step in another module with a path ending the
// same way is taken for a repeat). Plain entries gotestsum didn't report, such as those
// of a separate go test step, are kept.
func dedupe(all []found) []Failure {
	var out []Failure
	at := map[[2]string]int{}
	for _, e := range all {
		if e.goTest && repeatsGotestsum(all, e) {
			continue
		}
		f := e.Failure
		for len(f.Lines) > 0 && strings.TrimSpace(f.Lines[len(f.Lines)-1]) == "" {
			f.Lines = f.Lines[:len(f.Lines)-1]
		}
		key := [2]string{f.Package, f.Name}
		i, seen := at[key]
		if !seen {
			at[key] = len(out)
			out = append(out, f)
		} else if len(f.Lines) > len(out[i].Lines) {
			out[i].Lines = f.Lines
		}
	}
	return out
}

// repeatsGotestsum reports whether a gotestsum entry in all is the same failure
// as the go test entry e.
// An entry with no package matches only when a single gotestsum package has
// that test name (counted by package, as gotestsum may print a block twice); otherwise it can't be told apart and is kept.
func repeatsGotestsum(all []found, e found) bool {
	same := map[string]bool{}
	for _, g := range all {
		if g.goTest || g.Name != e.Name {
			continue
		}
		if e.Package == "" {
			same[g.Package] = true
		} else if e.Package == g.Package || strings.HasSuffix(e.Package, "/"+g.Package) {
			return true
		}
	}
	return len(same) == 1
}
