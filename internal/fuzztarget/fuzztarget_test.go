package fuzztarget_test

import (
	"bufio"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/devvmhq/ciwatch/internal/fuzztarget"
)

func file(body string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(body)} }

const parseFuzz = "package p\n\nimport \"testing\"\n\nfunc FuzzParse(f *testing.F) {\n\tf.Add(\"x\")\n}\n"

func TestDeclaring(t *testing.T) {
	tests := []struct {
		name   string
		fsys   fstest.MapFS
		target string
		want   []string
	}{
		{ // AC-3
			name:   "one declaring file is found",
			fsys:   fstest.MapFS{"internal/p/p_test.go": file(parseFuzz), "internal/p/p.go": file("package p\n")},
			target: "FuzzParse",
			want:   []string{"internal/p/p_test.go"},
		},
		{ // AC-3: the parameter may have any name
			name:   "any parameter name",
			fsys:   fstest.MapFS{"p_test.go": file("func FuzzParse(ff *testing.F) {\n}\n")},
			target: "FuzzParse",
			want:   []string{"p_test.go"},
		},
		{ // AC-4
			name:   "none is found",
			fsys:   fstest.MapFS{"p_test.go": file("package p\n\nfunc TestParse(t *testing.T) {}\n")},
			target: "FuzzParse",
			want:   nil,
		},
		{ // AC-4: sorted slash paths
			name: "two are found, sorted",
			fsys: fstest.MapFS{
				"b/b_test.go": file(parseFuzz),
				"a_test.go":   file(parseFuzz),
			},
			target: "FuzzParse",
			want:   []string{"a_test.go", "b/b_test.go"},
		},
		{ // AC-4
			name: "testdata and vendor are ignored",
			fsys: fstest.MapFS{
				"testdata/x_test.go":        file(parseFuzz),
				"a/testdata/x_test.go":      file(parseFuzz),
				"vendor/v/v_test.go":        file(parseFuzz),
				"p/p_test.go":               file(parseFuzz),
				"p/vendor/q/q_test.go":      file(parseFuzz),
				"p/testdata/fuzz/z_test.go": file(parseFuzz),
			},
			target: "FuzzParse",
			want:   []string{"p/p_test.go"},
		},
		{ // AC-4: go ignores directories starting with . or _
			name: "dot and underscore directories are ignored",
			fsys: fstest.MapFS{
				".git/x_test.go":      file(parseFuzz),
				"_tmp/x_test.go":      file(parseFuzz),
				"a/.hidden/y_test.go": file(parseFuzz),
				"a/_old/z_test.go":    file(parseFuzz),
			},
			target: "FuzzParse",
			want:   nil,
		},
		{ // AC-4
			name:   "a longer name does not match",
			fsys:   fstest.MapFS{"p_test.go": file("func FuzzParseX(f *testing.F) {\n}\n")},
			target: "FuzzParse",
			want:   nil,
		},
		{ // AC-4
			name:   "a non-test .go file does not match",
			fsys:   fstest.MapFS{"p.go": file(parseFuzz)},
			target: "FuzzParse",
			want:   nil,
		},
		{ // AC-4: the declaration starts a line
			name:   "a mention in a comment or an indented line does not match",
			fsys:   fstest.MapFS{"p_test.go": file("// func FuzzParse(f *testing.F)\n\tfunc FuzzParse(f *testing.F) {\n")},
			target: "FuzzParse",
			want:   nil,
		},
		{ // AC-4: the target is not a regexp
			name:   "the target is taken literally",
			fsys:   fstest.MapFS{"p_test.go": file("func FuzzParse(f *testing.F) {\n}\n")},
			target: "Fuzz.*",
			want:   nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := fuzztarget.Declaring(tt.fsys, tt.target)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.Files, tt.want) {
				t.Errorf("Declaring(%q).Files = %q, want %q", tt.target, got.Files, tt.want)
			}
			if len(got.Skipped) != 0 {
				t.Errorf("Declaring(%q).Skipped = %v, want none", tt.target, got.Skipped)
			}
		})
	}
}

// denyFS refuses to open the chosen paths with a permission error. It
// implements only Open, so fs.WalkDir lists directories through Open too.
type denyFS struct {
	fs.FS
	denied map[string]error
}

func (d denyFS) Open(name string) (fs.File, error) {
	if err, ok := d.denied[name]; ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return d.FS.Open(name)
}

// TestDeclaringLongLine: a line over 1 MiB fails the search with an error
// that names the file (AC-3); a declaration before it is still found (AC-4).
func TestDeclaringLongLine(t *testing.T) {
	long := strings.Repeat("x", 1<<20+1)
	t.Run("AC-3 error names the file", func(t *testing.T) {
		fsys := fstest.MapFS{"internal/q/q_test.go": file("package q\n" + long + "\n")}
		_, err := fuzztarget.Declaring(fsys, "FuzzParse")
		var pe *fs.PathError
		if !errors.As(err, &pe) || pe.Op != "read" || pe.Path != "internal/q/q_test.go" {
			t.Fatalf("err = %#v, want a *fs.PathError with Op read and Path internal/q/q_test.go", err)
		}
		if !errors.Is(err, bufio.ErrTooLong) {
			t.Errorf("err = %v, want it to wrap bufio.ErrTooLong", err)
		}
	})
	t.Run("AC-4 declaration before the long line", func(t *testing.T) {
		fsys := fstest.MapFS{"internal/q/q_test.go": file(parseFuzz + long + "\n")}
		got, err := fuzztarget.Declaring(fsys, "FuzzParse")
		if err != nil {
			t.Fatalf("Declaring: %v", err)
		}
		if want := []string{"internal/q/q_test.go"}; !slices.Equal(got.Files, want) {
			t.Errorf("Files = %v, want %v", got.Files, want)
		}
	})
}

// TestDeclaringKeepsSkippedOnError: what was skipped before a fatal error is
// still reported with it.
func TestDeclaringKeepsSkippedOnError(t *testing.T) {
	fsys := denyFS{
		fstest.MapFS{"a/a_test.go": file(parseFuzz), "b/b_test.go": file(parseFuzz)},
		map[string]error{"a": fs.ErrPermission, "b": fs.ErrInvalid},
	}
	got, err := fuzztarget.Declaring(fsys, "FuzzParse")
	if err == nil {
		t.Fatalf("Declaring = %+v, want an error", got)
	}
	if len(got.Skipped) != 1 || got.Skipped[0].Path != "a" {
		t.Errorf("Skipped = %v, want [a]", got.Skipped)
	}
}

func TestDeclaringSkipsUnreadable(t *testing.T) {
	denied := func(paths ...string) map[string]error {
		m := map[string]error{}
		for _, p := range paths {
			m[p] = fs.ErrPermission
		}
		return m
	}
	tests := []struct {
		name        string
		fsys        fstest.MapFS
		denied      map[string]error
		wantFiles   []string
		wantSkipped []string
		wantErr     bool
	}{
		{ // AC-1
			name: "an unreadable folder is skipped and a declaration elsewhere is found",
			fsys: fstest.MapFS{
				"internal/p/p_test.go":      file(parseFuzz),
				"internal/secret/s_test.go": file(parseFuzz),
			},
			denied:      denied("internal/secret"),
			wantFiles:   []string{"internal/p/p_test.go"},
			wantSkipped: []string{"internal/secret"},
		},
		{ // AC-2
			name:        "the only declaring file is in an unreadable folder",
			fsys:        fstest.MapFS{"internal/secret/s_test.go": file(parseFuzz)},
			denied:      denied("internal/secret"),
			wantSkipped: []string{"internal/secret"},
		},
		{ // AC-3
			name: "an unreadable _test.go file is skipped",
			fsys: fstest.MapFS{
				"internal/p/p_test.go": file(parseFuzz),
				"internal/q/q_test.go": file(parseFuzz),
			},
			denied:      denied("internal/q/q_test.go"),
			wantFiles:   []string{"internal/p/p_test.go"},
			wantSkipped: []string{"internal/q/q_test.go"},
		},
		{ // AC-5
			name: "an unreadable folder go ignores is never entered",
			fsys: fstest.MapFS{
				"p_test.go":      file(parseFuzz),
				"testdata/x/a":   file(""),
				"vendor/x/a":     file(""),
				".x/a":           file(""),
				"_x/a":           file(""),
				"p/testdata/x/a": file(""),
			},
			denied:    denied("testdata/x", "vendor/x", ".x", "_x", "p/testdata/x"),
			wantFiles: []string{"p_test.go"},
		},
		{ // AC-6
			name:    "an unreadable root is an error",
			fsys:    fstest.MapFS{"p_test.go": file(parseFuzz)},
			denied:  denied("."),
			wantErr: true,
		},
		{ // non-goal boundary: only permission errors are skipped
			name:    "another error in a folder stops the search",
			fsys:    fstest.MapFS{"a/p_test.go": file(parseFuzz)},
			denied:  map[string]error{"a": fs.ErrInvalid},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := fuzztarget.Declaring(denyFS{tt.fsys, tt.denied}, "FuzzParse")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Declaring = %+v, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.Files, tt.wantFiles) {
				t.Errorf("Files = %q, want %q", got.Files, tt.wantFiles)
			}
			var paths []string
			for _, s := range got.Skipped {
				paths = append(paths, s.Path)
				if !errors.Is(s.Err, fs.ErrPermission) {
					t.Errorf("Skipped %q has Err %v, want a permission error", s.Path, s.Err)
				}
			}
			if !slices.Equal(paths, tt.wantSkipped) {
				t.Errorf("Skipped paths = %q, want %q", paths, tt.wantSkipped)
			}
		})
	}
}
