package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/devvmhq/ciwatch/internal/cli"
)

func TestRunWithoutArgumentsPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run(t.Context(), nil, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUsage)
	}
	if !strings.HasPrefix(stderr.String(), "usage: ciwatch") {
		t.Errorf("stderr = %q, want the usage", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}
