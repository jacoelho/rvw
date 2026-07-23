//go:build linux

package source

import (
	"fmt"
	"os"
	"path/filepath"
)

func repositoryDescriptorRealPath(file *os.File) (string, error) {
	if file == nil {
		return "", fmt.Errorf("repository descriptor is nil")
	}
	path, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
	if err != nil {
		return "", fmt.Errorf("resolve repository descriptor path: %w", err)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("repository descriptor path is not absolute")
	}
	return filepath.Clean(path), nil
}
