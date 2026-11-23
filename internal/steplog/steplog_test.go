package steplog_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devvmhq/ciwatch/internal/steplog"
)

const job = "check (ubuntu-latest, America/Los_Angeles)"

// at is a time on 2026-11-20, as the Actions timestamps and the jobs JSON print it.
func at(clock string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, "2026-11-20T"+clock+"Z")
	if err != nil {
		panic(err)
	}
	return t
}

// ln is a line of gh run view --log-failed: gh labels every line
// UNKNOWN STEP, whatever step it came from.
func ln(clock, text string) string {
	return job + "\tUNKNOWN STEP\t2026-11-20T" + clock + "Z " + text + "\n"
}

// numbered is count lines "line 1" ... "line <count>", one per second from 10:00:00.
func numbered(count int) string {
	var b strings.Builder
	for i := 1; i <= count; i++ {
		b.WriteString(ln(fmt.Sprintf("10:%02d:00.0000000", i), fmt.Sprintf("line %d", i)))
	}
	return b.String()
}

func lastLines(from, to int) []string {
	var out []string
	for i := from; i <= to; i++ {
		out = append(out, fmt.Sprintf("line %d", i))
	}
	return out
}

// whole is the span of numbered logs of up to 59 lines.
var whole = steplog.Span{Name: "Run tests", Started: at("10:00:00"), Completed: at("10:59:00")}

// TestParse covers AC-1, AC-2 and AC-7.
func TestParse(t *testing.T) {
	tests := []struct {
		name  string
		log   string
		spans []steplog.Span
		want  []steplog.Step
	}{
		{
			name: "AC-1 keeps the lines from the step's start up to a second after its completion",
			log: ln("10:00:04.9999999", "before") + ln("10:00:05.0000000", "first") + ln("10:00:09.5000000", "last") +
				ln("10:00:09.9999999", "still in the last second") + ln("10:00:10.0000000", "after"),
			spans: []steplog.Span{{Name: "Run tests", Started: at("10:00:05"), Completed: at("10:00:09")}},
			want:  []steplog.Step{{Name: "Run tests", Lines: []string{"first", "last", "still in the last second"}}},
		},
		{
			name: "AC-1 removes the job and step prefix, the timestamp, a trailing carriage return and colour codes",
			log: job + "\tUNKNOWN STEP\t2026-11-20T10:00:05.0000000Z ^[[31mFAIL^[[0m one\r\n" +
				ln("10:00:06.0000000", "\x1b[1;31mFAIL\x1b[0m two"),
			spans: []steplog.Span{{Name: "Run tests", Started: at("10:00:05"), Completed: at("10:00:07")}},
			want:  []steplog.Step{{Name: "Run tests", Lines: []string{"FAIL one", "FAIL two"}}},
		},
		{
			name: "AC-1 keeps indentation after the timestamp",
			log:  ln("10:00:05.0000000", "    agentimport_test.go:957: changed: "),
			spans: []steplog.Span{
				{Name: "Run tests", Started: at("10:00:05"), Completed: at("10:00:05")},
			},
			want: []steplog.Step{{Name: "Run tests", Lines: []string{"    agentimport_test.go:957: changed: "}}},
		},
		{
			name: "AC-1 a line without a timestamp belongs to the line before it",
			log: ln("10:00:04.0000000", "before") + "continues before\n" +
				ln("10:00:05.0000000", "in") + "continues in\n" + ln("10:00:06.0000000", "end") + "no tabs at all\n" +
				ln("10:00:30.0000000", "after") + "continues after\n",
			spans: []steplog.Span{{Name: "Run tests", Started: at("10:00:05"), Completed: at("10:00:06")}},
			want:  []steplog.Step{{Name: "Run tests", Lines: []string{"in", "continues in", "end", "no tabs at all"}}},
		},
		{
			name: "AC-1 ends at the last ##[error] line in the window",
			log: ln("10:00:05.0000000", "go test") + ln("10:00:06.0000000", "##[error]one") + ln("10:00:07.0000000", "more") +
				ln("10:00:08.0000000", "##[error]Process completed with exit code 1.") + ln("10:00:08.5000000", "Post job cleanup."),
			spans: []steplog.Span{{Name: "Run tests", Started: at("10:00:05"), Completed: at("10:00:08")}},
			want:  []steplog.Step{{Name: "Run tests", Lines: []string{"go test", "##[error]one", "more", "##[error]Process completed with exit code 1."}}},
		},
		{
			name: "AC-1 an ##[error] line after the window does not extend it",
			log: ln("10:00:05.0000000", "go test") + ln("10:00:06.0000000", "output") +
				ln("10:00:20.0000000", "##[error]another step's"),
			spans: []steplog.Span{{Name: "Run tests", Started: at("10:00:05"), Completed: at("10:00:06")}},
			want:  []steplog.Step{{Name: "Run tests", Lines: []string{"go test", "output"}}},
		},
		{
			name: "AC-1 each failed step gets its own window, in the order given",
			log: ln("10:00:01.0000000", "setup") + ln("10:00:05.0000000", "lint failed") + ln("10:00:06.0000000", "##[error]lint") +
				ln("10:00:10.0000000", "test failed") + ln("10:00:11.0000000", "##[error]test"),
			spans: []steplog.Span{
				{Name: "Lint", Started: at("10:00:05"), Completed: at("10:00:06")},
				{Name: "Test", Started: at("10:00:10"), Completed: at("10:00:11")},
			},
			want: []steplog.Step{
				{Name: "Lint", Lines: []string{"lint failed", "##[error]lint"}},
				{Name: "Test", Lines: []string{"test failed", "##[error]test"}},
			},
		},
		{
			name:  "AC-2 more than 40 lines keeps the last 40 and counts the rest",
			log:   numbered(45),
			spans: []steplog.Span{whole},
			want:  []steplog.Step{{Name: "Run tests", Lines: lastLines(6, 45), Omitted: 5}},
		},
		{
			name:  "AC-2 exactly 40 lines leaves none out",
			log:   numbered(40),
			spans: []steplog.Span{whole},
			want:  []steplog.Step{{Name: "Run tests", Lines: lastLines(1, 40), Omitted: 0}},
		},
		{
			name:  "AC-2 fewer than 40 lines leaves none out",
			log:   numbered(3),
			spans: []steplog.Span{whole},
			want:  []steplog.Step{{Name: "Run tests", Lines: lastLines(1, 3), Omitted: 0}},
		},
		{
			name:  "AC-2 blank lines count toward the 40",
			log:   numbered(30) + strings.Repeat(ln("10:31:00.0000000", ""), 12),
			spans: []steplog.Span{whole},
			want: []steplog.Step{{
				Name:    "Run tests",
				Lines:   append(lastLines(3, 30), make([]string, 12)...),
				Omitted: 2,
			}},
		},
		{
			name:  "AC-7 an empty log has no steps",
			log:   "",
			spans: []steplog.Span{whole},
			want:  nil,
		},
		{
			name:  "AC-7 a failed step with no line in its window is left out",
			log:   ln("10:00:01.0000000", "setup") + ln("10:00:30.0000000", "cleanup"),
			spans: []steplog.Span{{Name: "Run tests", Started: at("10:00:05"), Completed: at("10:00:06")}},
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := steplog.Parse(strings.NewReader(tt.log), tt.spans)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestParseRecordedDevvmLog covers AC-3 at the parser: the recorded log of the
// ubuntu job of devvm's CI run 44705166834, whose "Run mise run test" step
// ran from 08:47:47 to 08:52:20.
func TestParseRecordedDevvmLog(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "testdata", "logs", "devvm-44705166834.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	got, err := steplog.Parse(f, []steplog.Span{{Name: "Run mise run test", Started: at("08:47:47"), Completed: at("08:52:20")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "Run mise run test" {
		t.Fatalf("Parse = %+v, want the one step Run mise run test", got)
	}
	lines := got[0].Lines
	if len(lines) != steplog.Tail || got[0].Omitted == 0 {
		t.Errorf("got %d lines and %d omitted, want %d lines and some omitted", len(lines), got[0].Omitted, steplog.Tail)
	}
	if last := lines[len(lines)-1]; last != "##[error]Process completed with exit code 1." {
		t.Errorf("last line = %q, want the ##[error] line", last)
	}
	text := strings.Join(lines, "\n")
	for _, s := range []string{"agentimport_test.go:957: changed:", "agentimport_test.go:963: changed true: the store has", "DONE 4183 tests, 55 skipped, 1 failure in 263.817s"} {
		if !strings.Contains(text, s) {
			t.Errorf("lines = %q, want them to contain %q", text, s)
		}
	}
	for _, s := range []string{"Post job cleanup", "Cleaning up orphan processes", "UNKNOWN STEP", "2026-11-20T", "^[["} {
		if strings.Contains(text, s) {
			t.Errorf("lines = %q, want them to leave out %q", text, s)
		}
	}
}

// TestWhole covers AC-8: the last 40 lines of a whole log, cleaned.
func TestWhole(t *testing.T) {
	tests := []struct {
		name string
		log  string
		want steplog.Step
	}{
		{"45 lines", numbered(45), steplog.Step{Lines: lastLines(6, 45), Omitted: 5}},
		{"exactly 40 lines", numbered(40), steplog.Step{Lines: lastLines(1, 40)}},
		{"cleaned", ln("10:00:00.0000000", "^[[31mred^[[0m") + "no tabs\n", steplog.Step{Lines: []string{"red", "no tabs"}}},
		{"empty", "", steplog.Step{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := steplog.Whole(strings.NewReader(tt.log))
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Lines) == 0 && len(tt.want.Lines) == 0 {
				if got.Omitted != 0 {
					t.Errorf("Whole = %+v, want nothing", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Whole = %+v, want %+v", got, tt.want)
			}
		})
	}
}
