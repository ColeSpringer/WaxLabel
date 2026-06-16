package core

import (
	"fmt"
	"io"

	"github.com/colespringer/waxlabel/internal/bits"
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

// Fingerprint hashes a file's metadata regions — everything outside the audio
// essence — for the structural source fingerprint used in change detection. For
// a contiguous-essence format that is the header before the audio plus any tail
// after it (so a trailing ID3v1, or a WAV INFO/id3 chunk written after the data
// chunk, is covered); for an interleaved-essence format (Ogg, AudioRanges set)
// it is just the header, where the tags live, since the rest is audio. A file
// with no identified essence is hashed whole. ok is false when there is nothing
// to hash or the region cannot be read, in which case the caller falls back to
// size/mtime/inode.
//
// Both parse and save-back call this with the same media extents, so the
// parse-time fingerprint and the recomputed one cover identical byte ranges and
// cannot disagree spuriously.
func Fingerprint(src ReaderAtSized, m *Media, limit int64) ([32]byte, bool) {
	size := src.Size()
	if size <= 0 {
		return [32]byte{}, false
	}
	// No identified essence (e.g. a data-less WAV): the whole file is metadata.
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
	// Trailing metadata after a single contiguous extent. Interleaved-essence
	// formats keep their tags up front, so they hash only the head.
	if len(m.AudioRanges) == 0 && m.AudioEnd > m.AudioStart && m.AudioEnd < size {
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
