// Package testlog parses the log of a failed CI job that ran go test or
// gotestsum and returns each failing test with the lines that follow it.
package testlog

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Failure is one failing test.
type Failure struct {
	Package string   // "" if the log never names it
	Name    string   // "TestBoot/arm64"
	Lines   []string // the lines after the FAIL line, prefixes removed
}

// Summary is gotestsum's DONE line.
type Summary struct{ Tests, Skipped, Failures, Errors int }

// Result is what Parse found in a log.
type Result struct {
	Failures []Failure // in log order
	Summary  *Summary  // nil when there's no DONE line
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
	markerRE  = regexp.MustCompile(`^(?:\s*--- (?:FAIL|PASS|SKIP): |\s*=== (?:RUN|PAUSE|CONT|NAME|FAIL|PASS|SKIP|Failed|Errors|Skipped)\b|##\[|DONE \d|exit status \d+$|ok  ?\t|FAIL\t|PASS$|FAIL$)`)
	exitRE    = regexp.MustCompile(`^exit status \d+$`)
	timeoutRE = regexp.MustCompile(`^panic: test timed out after `)
	runningRE = regexp.MustCompile(`^\s+(\S+) \([^)]*\)$`)
	pkgFailRE = regexp.MustCompile(`^FAIL\t(\S+)`)
	doneRE    = regexp.MustCompile(`^DONE (?:\d+ runs?, )?(\d+) tests?(?:, (\d+) skipped)?(?:, (\d+) failures?)?(?:, (\d+) errors?)? in `)
)

// stripPrefix removes the gh job and step, the Actions timestamp and ANSI
// colour codes, in that order.
func stripPrefix(line string) string {
	line = strings.TrimSuffix(line, "\r")
	line = prefixRE.ReplaceAllString(line, "")
	return ansiRE.ReplaceAllString(line, "")
}

// found is a failure and which format reported it.
type found struct {
	Failure
	goTest bool // from a "--- FAIL:" line, not gotestsum's "=== FAIL:"
}

// Parse reads a log and returns its failing tests and gotestsum summary.
func Parse(r io.Reader) (Result, error) {
	var (
		res      Result
		all      []found
		cur      = -1  // index in all of the open failure
		panicked bool  // the open failure has hit a panic, which spans blank lines
		pending  []int // failures waiting for a "FAIL<TAB>pkg" line
		out      string
		timeout  []string // lines of a "test timed out" panic being read; nil when none
		timing   bool
		running  bool                    // inside the timeout's "running tests:" list
		names    []string                // the tests that list names
		output   = map[string][]string{} // go test -v output of the test named in out, and of tests before it
		br       = bufio.NewReader(r)
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
	handle := func(line string) {
		line = stripPrefix(line)
		// Inside gotestsum's block for a test, the timeout panic is part of
		// that test's lines.
		inBlock := cur >= 0 && !all[cur].goTest
		if !inBlock && timeoutRE.MatchString(line) {
			endTimeout()
			timing, cur, out = true, -1, ""
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
		if m := gotestsumF.FindStringSubmatch(line); m != nil {
			name := m[2]
			if name == "" {
				name = "(package)" // gotestsum's line for a failure of the package itself
			}
			all = append(all, found{Failure: Failure{Package: m[1], Name: name}})
			cur, panicked, out = len(all)-1, false, ""
			return
		}
		if m := goTestF.FindStringSubmatch(line); m != nil {
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
			pending, cur, out = nil, -1, ""
			return
		}
		if m := doneRE.FindStringSubmatch(line); m != nil {
			cur, out = -1, ""
			if s, ok := summaryOf(m[1:]); ok {
				res.Summary = &s
			}
			return
		}
		if m := runRE.FindStringSubmatch(line); m != nil {
			cur, out = -1, m[1]
			return
		}
		if m := goTestDone.FindStringSubmatch(line); m != nil {
			delete(output, m[1])
			cur, out = -1, ""
			return
		}
		if markerRE.MatchString(line) {
			cur, out = -1, ""
			return
		}
		if out != "" {
			output[out] = append(output[out], line)
			return
		}
		if cur < 0 {
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
	res.Failures = dedupe(all)
	return res, nil
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

// dedupe keeps the first entry for each package and name, with the version
// that has the most lines, and drops the blank lines a panic leaves at the end.
// When gotestsum reported failures, go test's own "--- FAIL:" entries are
// dropped: they repeat gotestsum's, under the full import path where gotestsum
// prints the path relative to the module.
func dedupe(all []found) []Failure {
	gotestsum := false
	for _, f := range all {
		gotestsum = gotestsum || !f.goTest
	}
	var out []Failure
	at := map[[2]string]int{}
	for _, e := range all {
		if gotestsum && e.goTest {
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
