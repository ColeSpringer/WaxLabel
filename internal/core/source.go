package core

import (
	"fmt"
	"io"
)

// ReaderAtSized is WaxLabel's internal source contract: random access plus a
// known size, and crucially no shared seek offset. A Document never holds one
// (it is detached); codecs receive one only for the duration of a parse or a
// write.
type ReaderAtSized interface {
	io.ReaderAt
	Size() int64
}

// bytesReaderAt adapts a byte slice to ReaderAtSized.
type bytesReaderAt struct {
	b []byte
}

// BytesSource returns a ReaderAtSized backed by b. b must not be mutated for
// the lifetime of the source.
func BytesSource(b []byte) ReaderAtSized { return bytesReaderAt{b: b} }

func (r bytesReaderAt) Size() int64 { return int64(len(r.b)) }

func (r bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	// off == len(b) is allowed: it yields an empty source slice, so a
	// zero-length read there succeeds with (0, nil) rather than EOF — matching
	// os.File.ReadAt, and avoiding a spurious error for empty reads at the end.
	if off < 0 || off > int64(len(r.b)) {
		return 0, io.EOF
	}
	n := copy(p, r.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// Identity is a strong fingerprint of a source file, recorded at parse so a
// later save-back can detect that the file changed underneath us. Path, size,
// and mtime alone are too weak — and weaker still once mtime is not preserved
// — so a small structural fingerprint (a hash of the metadata region) is
// included.
type Identity struct {
	Path            string
	Size            int64
	ModTimeUnixNano int64
	INode           uint64 // 0 when unavailable
	Device          uint64
	Fingerprint     [32]byte
	HasFinger       bool
}

// Matches reports whether other is the same source this identity was recorded
// from, and a reason string when it is not. It is the single change-detection
// rule, checking inode/device, size, modification time, and (when both sides
// have one) the structural fingerprint.
func (id Identity) Matches(other Identity) (bool, string) {
	if id.INode != 0 && other.INode != 0 {
		if id.INode != other.INode || id.Device != other.Device {
			return false, "file inode changed"
		}
	}
	if id.Size != other.Size {
		return false, fmt.Sprintf("size changed (%d -> %d)", id.Size, other.Size)
	}
	if id.ModTimeUnixNano != 0 && other.ModTimeUnixNano != 0 && id.ModTimeUnixNano != other.ModTimeUnixNano {
		return false, "modification time changed"
	}
	if id.HasFinger && other.HasFinger && id.Fingerprint != other.Fingerprint {
		return false, "metadata fingerprint changed"
	}
	return true, ""
}
