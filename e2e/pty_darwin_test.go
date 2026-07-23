//go:build e2e && darwin

package e2e_test

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
)

func newScriptCommand(ctx context.Context, shell string) *exec.Cmd {
	return exec.CommandContext(ctx, systemScript, "-q", "/dev/null", systemShell, "-c", shell)
}

func stableTerminalState(value string) string {
	fields := strings.Split(value, ":")
	for index, field := range fields {
		const prefix = "lflag="
		if !strings.HasPrefix(field, prefix) {
			continue
		}
		flags, err := strconv.ParseUint(strings.TrimPrefix(field, prefix), 16, 64)
		if err == nil {
			// PENDIN is transient kernel input state, not an application-owned mode.
			fields[index] = prefix + strconv.FormatUint(flags&^0x20000000, 16)
		}
	}
	return strings.Join(fields, ":")
}
