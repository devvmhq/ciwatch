// Package fuzztarget finds where a fuzz target is declared in a checkout.
package fuzztarget

import (
	"bufio"
	"errors"
	"io/fs"
	"regexp"
	"slices"
	"strings"
)

// Skipped is a folder or _test.go file the search wasn't allowed to read.
type Skipped struct {
	Path string // slash path relative to the checkout root
	Err  error
}

// Result is what Declaring found.
type Result struct {
	Files   []string  // sorted slash paths of the declaring files
	Skipped []Skipped // unreadable paths the search carried on past, in walk order
}

// Declaring returns the sorted slash paths of the _test.go files in fsys that
// declare func <target>(f *testing.F). It skips the directories go ignores:
// testdata, vendor, and names starting with "." or "_". A folder or file
// below the root that it isn't permitted to read is skipped and recorded in
// Result.Skipped; any other error, and an unreadable root, is returned. A line
// over 1 MiB in a _test.go file fails the search with an error naming the file.
func Declaring(fsys fs.FS, target string) (Result, error) {
	decl := regexp.MustCompile(`^func ` + regexp.QuoteMeta(target) + `\(\w+ \*testing\.F\)`)
	var res Result
	skip := func(p string, err error) bool {
		if p == "." || !errors.Is(err, fs.ErrPermission) {
			return false
		}
		res.Skipped = append(res.Skipped, Skipped{Path: p, Err: err})
		return true
	}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if skip(p, err) {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
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
			if skip(p, err) {
				return nil
			}
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			if decl.MatchString(sc.Text()) {
				res.Files = append(res.Files, p)
				return nil
			}
		}
		if err := sc.Err(); err != nil && !skip(p, err) {
			if errors.Is(err, bufio.ErrTooLong) {
				return &fs.PathError{Op: "read", Path: p, Err: err}
			}
			return err
		}
		return nil
	})
	if err != nil {
		// Keep what was skipped before the failure, so the caller can still say so.
		return Result{Skipped: res.Skipped}, err
	}
	slices.Sort(res.Files)
	return res, nil
}
