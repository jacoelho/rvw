//go:build e2e && linux

package e2e_test

import (
	"context"
	"os/exec"
)

func newScriptCommand(ctx context.Context, shell string) *exec.Cmd {
	return exec.CommandContext(
		ctx,
		systemScript,
		"-q",
		"-c",
		"exec sh -c "+shellQuote(shell),
		"/dev/null",
	)
}

func stableTerminalState(value string) string {
	return value
}
