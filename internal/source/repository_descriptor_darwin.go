//go:build darwin

package source

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

func repositoryDescriptorPath(file *os.File, _ int) (string, error) {
	if file == nil {
		return "", fmt.Errorf("repository descriptor is nil")
	}
	buffer := make([]byte, unix.PathMax)
	//nolint:dogsled,gosec,staticcheck // x/sys/unix has no exported pointer-taking F_GETPATH wrapper; the kernel writes at most PathMax bytes to this live buffer.
	_, _, errno := unix.Syscall(
		unix.SYS_FCNTL,
		file.Fd(),
		uintptr(unix.F_GETPATH),
		uintptr(unsafe.Pointer(&buffer[0])),
	)
	runtime.KeepAlive(file)
	if errno != 0 {
		return "", fmt.Errorf("resolve repository descriptor path: %w", errno)
	}
	end := bytes.IndexByte(buffer, 0)
	if end <= 0 {
		return "", fmt.Errorf("repository descriptor path is empty or overlong")
	}
	return string(buffer[:end]), nil
}
