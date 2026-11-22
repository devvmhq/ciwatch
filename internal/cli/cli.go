// Package cli is ciwatch's command line: it reads the arguments, runs the
// command and returns the process's exit code.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/devvmhq/ciwatch/internal/gh"
)

// Exit codes.
const (
	ExitOK        = 0
	ExitFailed    = 1
	ExitUsage     = 2
	ExitCancelled = 3
	ExitUnchecked = 4
)

const usage = `usage: ciwatch [--json] <commit>

Watches every GitHub Actions run for a commit until it finishes.
`

// App is ciwatch with its dependencies.
type App struct {
	GH gh.Runner
}

// New returns an App that runs the real gh and git.
func New() App {
	return App{GH: gh.Runner{Exec: gh.Exec}}
}

// Run runs ciwatch with args (without the program name), writing its output to
// stdout and its errors to stderr, and returns the exit code.
func (a App) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	commit, asJSON, err := parseArgs(args)
	if err != nil {
		if !errors.Is(err, errUsage) && !errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", err)
		}
		_, _ = fmt.Fprint(stderr, usage)
		return ExitUsage
	}

	sha, err := a.GH.ResolveCommit(ctx, commit)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", err)
		if errors.Is(err, gh.ErrBadRevision) {
			return ExitUsage
		}
		return ExitUnchecked
	}
	runs, err := a.GH.RunList(ctx, sha)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", err)
		return ExitUnchecked
	}
	if len(runs) == 0 {
		_, _ = fmt.Fprintf(stderr, "ciwatch: no runs found for %s\n", sha)
		return ExitUnchecked
	}

	if asJSON {
		err = writeJSON(stdout, sha, runs)
	} else {
		err = writeTable(stdout, runs)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ciwatch: %v\n", err)
		return ExitUnchecked
	}

	code, pending := exitCode(runs)
	if pending > 0 {
		_, _ = fmt.Fprintf(stderr, "ciwatch: %d runs still in progress\n", pending)
	}
	if code == ExitOK && len(runs) >= gh.RunListLimit {
		_, _ = fmt.Fprintf(stderr, "ciwatch: gh listed %d runs, the most it returns, so there may be more; can't vouch for a pass\n", len(runs))
		return ExitUnchecked
	}
	return code
}

var errUsage = errors.New("usage")

// parseArgs accepts --json and one commit, in either order.
func parseArgs(args []string) (commit string, asJSON bool, err error) {
	fs := flag.NewFlagSet("ciwatch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&asJSON, "json", false, "")
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return "", false, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		if args[0] == "--" {
			positional = append(positional, args[1:]...)
			break
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
	if len(positional) != 1 {
		return "", false, errUsage
	}
	return positional[0], asJSON, nil
}

// exitCode sums up runs as an exit code. pending is the number of runs that
// haven't finished, which only matters when the code is ExitUnchecked.
func exitCode(runs []gh.Run) (code, pending int) {
	var failed, cancelled bool
	for _, r := range runs {
		if r.Status != "completed" {
			pending++
			continue
		}
		switch r.Conclusion {
		case "success", "neutral", "skipped":
		case "cancelled":
			cancelled = true
		default: // failure, timed_out, stale, and whatever GitHub adds later
			failed = true
		}
	}
	switch {
	case failed:
		return ExitFailed, pending
	case cancelled:
		return ExitCancelled, pending
	case pending > 0:
		return ExitUnchecked, pending
	}
	return ExitOK, 0
}

func writeTable(w io.Writer, runs []gh.Run) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "WORKFLOW\tSTATUS\tCONCLUSION")
	for _, r := range runs {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Workflow, r.Status, r.Conclusion)
	}
	return tw.Flush()
}

type jsonRun struct {
	ID         int64  `json:"id"`
	Workflow   string `json:"workflow"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
}

func writeJSON(w io.Writer, sha string, runs []gh.Run) error {
	out := struct {
		SHA  string    `json:"sha"`
		Runs []jsonRun `json:"runs"`
	}{SHA: sha, Runs: make([]jsonRun, len(runs))}
	for i, r := range runs {
		out.Runs[i] = jsonRun(r)
	}
	return json.NewEncoder(w).Encode(out)
}
