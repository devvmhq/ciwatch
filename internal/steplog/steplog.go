// Package steplog finds the end of each failed step in the log of a job, for
// any language or tool the step ran.
//
// gh run view --log-failed labels every line UNKNOWN STEP and prints the whole
// job, so the lines of a step are found by the time the step ran, which the
// jobs JSON gives.
package steplog

import (
	"bufio"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
)

// Tail is how many of a step's last lines Parse and Whole keep.
const Tail = 40

// Step is a failed step and the end of its log.
type Step struct {
	Name    string
	Lines   []string // the last Tail lines, without gh's prefix, the timestamp and colour codes
	Omitted int      // how many lines came before them
}

// Span is a failed step and when it ran, as the jobs JSON gives it, in whole
// seconds.
type Span struct {
	Name      string
	Started   time.Time
	Completed time.Time
}

// colour matches an ANSI colour code, as an escape or as gh prints it in text.
var colour = regexp.MustCompile(`(?:\x1b|\^\[)\[[0-9;]*m`)

// bom is the byte order mark that starts the first line of a log.
var bom = string(rune(0xFEFF))

// errorMark starts the line that ends a failed step.
const errorMark = "##[error]"

// line splits a log line into its text, without the prefix before the second
// tab, the timestamp, a trailing \r and colour codes, and the timestamp. A line
// without a timestamp is kept as it is, and ok is false.
func line(raw string) (text string, ts time.Time, ok bool) {
	raw = strings.TrimSuffix(strings.TrimSuffix(raw, "\n"), "\r")
	parts := strings.SplitN(raw, "\t", 3)
	if len(parts) < 3 {
		return colour.ReplaceAllString(raw, ""), time.Time{}, false
	}
	rest := strings.TrimPrefix(parts[2], bom)
	stamp, after, _ := strings.Cut(rest, " ")
	ts, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return colour.ReplaceAllString(rest, ""), time.Time{}, false
	}
	return colour.ReplaceAllString(after, ""), ts, true
}

// tail keeps the last Tail lines of a stream of lines.
type tail struct {
	lines []string
	n     int // how many lines were added
}

func (t *tail) add(l string) {
	t.lines = append(t.lines, l)
	t.n++
	if len(t.lines) >= 2*Tail {
		t.lines = append(t.lines[:0], t.lines[len(t.lines)-Tail:]...)
	}
}

func (t tail) step(name string) Step {
	last := t.lines[max(0, len(t.lines)-Tail):]
	return Step{Name: name, Lines: append([]string(nil), last...), Omitted: t.n - len(last)}
}

// groupMark starts the line GitHub prints at the start of a step.
const groupMark = "##[group]"

// state is what Parse knows of one span.
type state struct {
	all, atError tail
	hasError     bool
	in           bool // the last line with a timestamp was in the window
	settled      bool // the start second is over, or a ##[group] line ended it
}

// take adds a line of the window.
func (s *state) take(text string, stamped bool) {
	s.all.add(text)
	if stamped && strings.HasPrefix(text, errorMark) {
		s.hasError = true
		s.atError = tail{append([]string(nil), s.all.lines...), s.all.n}
	}
}

// dropStart forgets the lines taken so far, which came from the start second
// and were the previous step's, and ends the start second.
func (s *state) dropStart() {
	*s = state{in: s.in, settled: true}
}

// each calls f with every line of r, however long.
func each(r io.Reader, f func(string)) error {
	br := bufio.NewReader(r)
	for {
		s, err := br.ReadString('\n')
		if s != "" {
			f(s)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Parse reads the log of a job and returns, for each span with lines in its
// window, the last Tail of them, in the order of spans. The window is from the
// span's start up to a second after its completion, and ends at the last
// ##[error] line in it when there is one. A line without a timestamp belongs
// to the line before it. A span with no lines has no step.
//
// The jobs JSON has whole seconds, so the previous step's lines from the
// span's start second would fall in the window. GitHub starts each step with a
// ##[group] line, so a ##[group] line in the start second drops the lines
// taken before it (the previous step's); without one, they stay.
func Parse(r io.Reader, spans []Span) ([]Step, error) {
	st := make([]state, len(spans))
	err := each(r, func(raw string) {
		text, ts, stamped := line(raw)
		for i, sp := range spans {
			s := &st[i]
			if stamped {
				s.in = !ts.Before(sp.Started) && ts.Before(sp.Completed.Add(time.Second))
				if !ts.Before(sp.Started.Add(time.Second)) {
					s.settled = true
				}
			}
			if !s.in {
				continue
			}
			if !s.settled && stamped && strings.HasPrefix(text, groupMark) {
				s.dropStart()
			}
			s.take(text, stamped)
		}
	})
	if err != nil {
		return nil, err
	}
	var steps []Step
	for i, sp := range spans {
		t := st[i].all
		if st[i].hasError {
			t = st[i].atError
		}
		if t.n > 0 {
			steps = append(steps, t.step(sp.Name))
		}
	}
	return steps, nil
}

// Whole returns the last Tail lines of the whole log, cleaned like Parse's. Its
// Name is empty.
func Whole(r io.Reader) (Step, error) {
	var t tail
	if err := each(r, func(raw string) {
		text, _, _ := line(raw)
		t.add(text)
	}); err != nil {
		return Step{}, err
	}
	if t.n == 0 {
		return Step{}, nil
	}
	return t.step(""), nil
}
