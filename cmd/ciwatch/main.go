// Command ciwatch watches every GitHub Actions run for a commit until it
// finishes, and shows what failed.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/devvmhq/ciwatch/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
