//go:build darwin

package source

import "os"

func repositoryDescriptorRealPath(file *os.File) (string, error) {
	return repositoryDescriptorPath(file, 0)
}
