//go:build !linux

package atomicfile

// copyXattrs is a no-op off Linux.
func copyXattrs(string, string) {}
