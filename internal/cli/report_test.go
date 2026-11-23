package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/devvmhq/ciwatch/internal/cli"
)

// goldenJSON runs ciwatch --json HEAD on the run-list-failure.json fixture.
func goldenJSON(t *testing.T) (stdout string, code int) {
	t.Helper()
	app, _ := fake(t, "run-list-failure.json", nil, nil)
	var out, errOut bytes.Buffer
	code = app.Run(t.Context(), []string{"--json", "HEAD"}, &out, &errOut)
	return out.String(), code
}

// TestJSONSchemaVersion covers AC-1.
func TestJSONSchemaVersion(t *testing.T) {
	stdout, code := goldenJSON(t)
	if code != cli.ExitFailed {
		t.Errorf("exit code = %d, want %d", code, cli.ExitFailed)
	}
	if !strings.HasPrefix(stdout, `{"schema":1,`) {
		t.Errorf("stdout starts %.40q, want {\"schema\":1,", stdout)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &top); err != nil {
		t.Fatalf("stdout is not one JSON document: %v", err)
	}
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"runs", "schema", "sha"}; !slices.Equal(keys, want) {
		t.Errorf("top-level keys = %q, want %q", keys, want)
	}
	if got := string(top["schema"]); got != "1" {
		t.Errorf("schema = %s, want 1", got)
	}
}

// docTable returns the cells of each row of the first table under the
// "## <heading>" heading of docs/json.md, without the header and separator.
func docTable(t *testing.T, heading string) [][]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "json.md"))
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]string
	in := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "## ") {
			in = strings.TrimSpace(strings.TrimPrefix(line, "## ")) == heading
			continue
		}
		if !in || !strings.HasPrefix(line, "|") {
			continue
		}
		var cells []string
		for _, c := range strings.Split(strings.Trim(line, "|"), "|") {
			cells = append(cells, strings.TrimSpace(c))
		}
		if strings.Trim(cells[0], "-: ") == "" {
			continue // the separator
		}
		rows = append(rows, cells)
	}
	if len(rows) < 2 {
		t.Fatalf("docs/json.md has no %q table", heading)
	}
	return rows[1:] // the header
}

// walkFields adds the path and doc type of every JSON field of struct type st.
func walkFields(prefix string, st reflect.Type, out map[string]string) {
	for f := range st.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		path := prefix + name
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch {
		case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.String:
			out[path] = "array of strings"
		case ft.Kind() == reflect.Slice:
			et := ft.Elem()
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			out[path] = "array of objects"
			walkFields(path+"[].", et, out)
		case ft.Kind() == reflect.Struct:
			out[path] = "object"
			walkFields(path+".", ft, out)
		case ft.Kind() == reflect.String:
			out[path] = "string"
		case ft.Kind() == reflect.Bool:
			out[path] = "boolean"
		case ft.Kind() >= reflect.Int && ft.Kind() <= reflect.Int64:
			out[path] = "integer"
		default:
			out[path] = ft.Kind().String()
		}
	}
}

// TestJSONFieldsDocumented covers AC-2.
func TestJSONFieldsDocumented(t *testing.T) {
	have := map[string]string{}
	walkFields("", reflect.TypeFor[cli.Report](), have)

	doc := map[string]string{}
	for _, cells := range docTable(t, "Fields") {
		if len(cells) < 3 {
			t.Errorf("Fields row %q needs a path, a type and when it is present", cells)
			continue
		}
		path := strings.Trim(cells[0], "`")
		if cells[2] == "" {
			t.Errorf("field %q says nothing about when it is present", path)
		}
		doc[path] = cells[1]
	}
	for path, typ := range have {
		d, ok := doc[path]
		switch {
		case !ok:
			t.Errorf("field %q (%s) is not in docs/json.md", path, typ)
		case d != typ:
			t.Errorf("field %q is %q in docs/json.md, but %q in cli.Report", path, d, typ)
		}
	}
	for path := range doc {
		if _, ok := have[path]; !ok {
			t.Errorf("docs/json.md lists %q, which cli.Report does not have", path)
		}
	}
}

// TestJSONGolden covers AC-3.
func TestJSONGolden(t *testing.T) {
	got, _ := goldenJSON(t)
	want, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "run-list-failure.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got == string(want) {
		return
	}
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	ctx := func(s string) string { return s[max(0, i-80):min(len(s), i+80)] }
	t.Errorf("output differs from the golden file at offset %d:\n got: %q\nwant: %q\nwhole output:\n%s", i, ctx(got), ctx(string(want)), got)
}

// TestTableFromReport covers AC-4.
func TestTableFromReport(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "run-list-failure.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report cli.Report
	if err := json.Unmarshal(golden, &report); err != nil {
		t.Fatal(err)
	}
	var again bytes.Buffer
	if err := report.WriteJSON(&again); err != nil {
		t.Fatal(err)
	}
	if again.String() != string(golden) {
		t.Errorf("encoding the decoded golden file gives different bytes:\n%s", again.String())
	}

	var table bytes.Buffer
	if err := report.WriteTable(&table, testLines); err != nil {
		t.Fatal(err)
	}
	if len(report.Runs) != 2 || len(report.Runs[0].Jobs) != 43 || len(report.Runs[1].Jobs) != 5 {
		t.Fatalf("golden report has %d runs, want 2 with 43 and 5 jobs", len(report.Runs))
	}
	var want []row
	for _, r := range report.Runs {
		want = append(want, runRow(r.Workflow, r.Status, r.Conclusion))
		for _, j := range r.Jobs {
			want = append(want, row{fields: append(strings.Fields(j.Name), nonEmpty(j.Status, j.Conclusion)...), job: true})
		}
	}
	checkTable(t, table.String(), want)
	block := "\nFuzzPromisorHostSite (Fuzz, run 44729305066):\n  (no failed step in the log)\n"
	if !strings.HasSuffix(table.String(), block) || strings.Count(table.String(), "\n\n") != 1 {
		t.Errorf("table = %q, want it to end with the one block %q", table.String(), block)
	}

	app, _ := fake(t, "run-list-failure.json", nil, nil)
	var out, errOut bytes.Buffer
	app.Run(t.Context(), []string{"HEAD"}, &out, &errOut)
	if out.String() != table.String() {
		t.Errorf("ciwatch HEAD prints\n%q\nbut Report.WriteTable prints\n%q", out.String(), table.String())
	}
}

// TestExitCodesDocumented covers AC-5.
func TestExitCodesDocumented(t *testing.T) {
	want := map[int]string{
		cli.ExitOK: "ExitOK", cli.ExitFailed: "ExitFailed", cli.ExitUsage: "ExitUsage",
		cli.ExitCancelled: "ExitCancelled", cli.ExitUnchecked: "ExitUnchecked",
	}
	got := map[int]bool{}
	for _, cells := range docTable(t, "Exit codes") {
		n, err := strconv.Atoi(strings.Trim(cells[0], "` "))
		if err != nil {
			t.Errorf("exit code row %q does not start with a number", cells)
			continue
		}
		if len(cells) < 2 || cells[1] == "" {
			t.Errorf("exit code %d has no meaning", n)
		}
		if _, ok := want[n]; !ok {
			t.Errorf("docs/json.md lists exit code %d, which has no constant", n)
		}
		got[n] = true
	}
	for n, name := range want {
		if !got[n] {
			t.Errorf("docs/json.md does not list exit code %d (%s)", n, name)
		}
	}
}
