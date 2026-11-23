package fuzztarget_test

import (
	"slices"
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
			if !slices.Equal(got, tt.want) {
				t.Errorf("Declaring(%q) = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}
