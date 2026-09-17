package core

import (
	"fmt"
	"io"

	"github.com/colespringer/waxlabel/internal/bits"
)

// ReaderAtSized is io.ReaderAt plus Size. No shared seek offset.
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
	// off==len(b): zero-length read succeeds (os.File.ReadAt behavior).
	if off < 0 || off > int64(len(r.b)) {
		return 0, io.EOF
	}
	n := copy(p, r.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// Fingerprint hashes metadata regions around audio essence for change detection:
// [0, AudioStart) plus [AudioEnd, size). No essence: whole file. Multi-segment: gaps skipped.
func Fingerprint(src ReaderAtSized, m *Media, limit int64) ([32]byte, bool) {
	size := src.Size()
	if size <= 0 {
		return [32]byte{}, false
	}
	// No essence: hash whole file.
	if m.AudioStart <= 0 && m.AudioEnd <= 0 && len(m.AudioRanges) == 0 {
		all, err := bits.ReadSlice(src, 0, size, limit)
		if err != nil {
			return [32]byte{}, false
		}
		return bits.SHA256(all), true
	}
	var region []byte
	if m.AudioStart > 0 {
		head, err := bits.ReadSlice(src, 0, m.AudioStart, limit)
		if err != nil {
			return [32]byte{}, false
		}
		region = head
	}
	// Trailing metadata after essence.
	if m.AudioEnd > m.AudioStart && m.AudioEnd < size {
		tail, err := bits.ReadSlice(src, m.AudioEnd, size-m.AudioEnd, limit)
		if err != nil {
			return [32]byte{}, false
		}
		region = append(region, tail...)
	}
	if len(region) == 0 {
		return [32]byte{}, false
	}
	return bits.SHA256(region), true
}

// Identity fingerprints a source at parse time for save-back change detection.
type Identity struct {
	Path            string
	Size            int64
	ModTimeUnixNano int64  // 0 when unknown, unrepresentable, or exactly the epoch
	INode           uint64 // 0 when unavailable
	Device          uint64
	Fingerprint     [32]byte
	HasFinger       bool
}

// Matches is content check plus mtime for in-place save (SaveBack).
func (id Identity) Matches(other Identity) (bool, string) {
	if ok, why := id.MatchesContent(other); !ok {
		return false, why
	}
	if id.ModTimeUnixNano != 0 && other.ModTimeUnixNano != 0 && id.ModTimeUnixNano != other.ModTimeUnixNano {
		return false, "modification time changed"
	}
	return true, ""
}

// MatchesContent compares inode, size, and fingerprint; omits mtime (derived writes).
func (id Identity) MatchesContent(other Identity) (bool, string) {
	if id.INode != 0 && other.INode != 0 {
		if id.INode != other.INode || id.Device != other.Device {
			return false, "file inode changed"
		}
	}
	if id.Size != other.Size {
		return false, fmt.Sprintf("size changed (%d -> %d)", id.Size, other.Size)
	}
	if id.HasFinger && other.HasFinger && id.Fingerprint != other.Fingerprint {
		return false, "metadata fingerprint changed"
	}
	return true, ""
}
