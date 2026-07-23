//go:build linux

package subprocess

// BorrowedFilePath returns the target-visible path for BorrowedFiles[index],
// or an empty string when index is outside the request bound.
func BorrowedFilePath(index int) string {
	return borrowedFilePath("/proc/self/fd/", index)
}
