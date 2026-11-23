// Package fuzztarget finds where a fuzz target is declared in a checkout.
package fuzztarget

import (
	"bufio"
	"io/fs"
	"regexp"
	"slices"
	"strings"
)

// Declaring returns the sorted slash paths of the _test.go files in fsys that
// declare func <target>(f *testing.F). It skips the directories go ignores:
// testdata, vendor, and names starting with "." or "_".
func Declaring(fsys fs.FS, target string) ([]string, error) {
	decl := regexp.MustCompile(`^func ` + regexp.QuoteMeta(target) + `\(\w+ \*testing\.F\)`)
	var found []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != "." && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, "_test.go") {
			return nil
		}
		f, err := fsys.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			if decl.MatchString(sc.Text()) {
				found = append(found, p)
				return nil
			}
		}
		return sc.Err()
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(found)
	return found, nil
}
