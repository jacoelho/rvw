//go:build linux

package source

import (
	"fmt"
	"os"

	"github.com/jacoelho/rvw/internal/subprocess"
)

func repositoryDescriptorPath(file *os.File, index int) (string, error) {
	if file == nil {
		return "", fmt.Errorf("repository descriptor is nil")
	}
	path := subprocess.BorrowedFilePath(index)
	if path == "" {
		return "", fmt.Errorf("repository descriptor index %d is invalid", index)
	}
	return path, nil
}
