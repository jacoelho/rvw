//go:build darwin || linux

package subprocess

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

const descriptorLauncherArg0 = "rvw-internal-dirfd-launcher-7b671974"

const firstBorrowedFileDescriptor = 5

//nolint:gochecknoinits // The re-executed process must fchdir before application initialization.
func init() {
	if len(os.Args) == 0 || os.Args[0] != descriptorLauncherArg0 {
		return
	}
	if len(os.Args) < 3 {
		_, _ = fmt.Fprintln(os.Stderr, "rvw directory launcher: missing executable")
		os.Exit(126)
	}
	if err := syscall.Fchdir(4); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "rvw directory launcher: fchdir: %v\n", err)
		os.Exit(126)
	}
	if err := syscall.Close(4); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "rvw directory launcher: close directory: %v\n", err)
		os.Exit(126)
	}
	if err := os.Unsetenv("PWD"); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "rvw directory launcher: unset PWD: %v\n", err)
		os.Exit(126)
	}
	// The parent validated the resolved executable and every argument before re-exec.
	//nolint:gosec // G204 cannot follow validation across the launcher process boundary.
	if err := syscall.Exec(os.Args[1], os.Args[2:], os.Environ()); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "rvw directory launcher: exec: %v\n", err)
		os.Exit(126)
	}
}

func descriptorCommand(
	executable string,
	arguments []string,
) (string, []string, error) {
	launcher, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("locate directory launcher: %w", err)
	}
	launcherArguments := make([]string, 0, len(arguments)+2)
	launcherArguments = append(launcherArguments, descriptorLauncherArg0, executable)
	launcherArguments = append(launcherArguments, arguments...)
	return launcher, launcherArguments, nil
}

func borrowedFilePath(prefix string, index int) string {
	if index < 0 || index >= MaxBorrowedFiles {
		return ""
	}
	return prefix + strconv.Itoa(firstBorrowedFileDescriptor+index)
}
