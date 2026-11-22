// Package cli is ciwatch's command line: it reads the arguments, runs the
// command and returns the process's exit code.
package cli

import (
	"context"
	"fmt"
	"io"
)

// Exit codes.
const (
	ExitOK    = 0
	ExitUsage = 2
)

const usage = `usage: ciwatch <commit>

Watches every GitHub Actions run for a commit until it finishes.
`

// Run runs ciwatch with args (without the program name), writing its output to
// stdout and its errors to stderr, and returns the exit code.
func Run(_ context.Context, args []string, _, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return ExitUsage
	}
	_, _ = fmt.Fprintln(stderr, "ciwatch: not implemented yet")
	return ExitUsage
}
