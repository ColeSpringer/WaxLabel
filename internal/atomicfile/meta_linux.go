package atomicfile

import (
	"bytes"
	"strings"
	"syscall"
)

const accessACL = "system.posix_acl_access"

// copyXattrs copies from's user.* attributes and POSIX access ACL to to. Best-effort: the
// filesystem may hold none, and other namespaces (security.*, trusted.*) are the system's.
func copyXattrs(from, to string) {
	list, err := xattrRead(func(b []byte) (int, error) { return syscall.Listxattr(from, b) })
	if err != nil {
		return
	}
	hasACL := false
	for name := range bytes.SplitSeq(list, []byte{0}) {
		n := string(name)
		if !strings.HasPrefix(n, "user.") && n != accessACL {
			continue
		}
		val, err := xattrRead(func(b []byte) (int, error) { return syscall.Getxattr(from, n, b) })
		if err != nil {
			continue
		}
		hasACL = hasACL || n == accessACL
		_ = syscall.Setxattr(to, n, val, 0)
	}
	if !hasACL {
		// A default ACL on the directory gave the new file one that from does not have.
		_ = syscall.Removexattr(to, accessACL)
	}
}

// xattrRead sizes a buffer for read, retrying when the attribute grows in between.
func xattrRead(read func([]byte) (int, error)) ([]byte, error) {
	for range 4 {
		n, err := read(nil)
		if err != nil || n == 0 {
			return nil, err
		}
		buf := make([]byte, n)
		n, err = read(buf)
		switch {
		case err == nil:
			return buf[:n], nil
		case err != syscall.ERANGE: // the attribute went away or the file changed under the read
			return nil, err
		}
	}
	return nil, syscall.ERANGE
}
