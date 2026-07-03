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
	// zero-length read there succeeds with (0, nil) rather than EOF - matching
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

// Fingerprint hashes a file's metadata regions - the bytes around the audio
// essence - for the structural source fingerprint used in change detection. It
// hashes the header before the essence ([0, AudioStart)) plus the tail after it
// ([AudioEnd, size)), so a trailing ID3v1, a WAV INFO/id3 chunk written after the
// data chunk, or an MP4 moov that follows the (last) mdat are all covered. A file
// with no identified essence is hashed whole. For a multi-segment essence (Ogg
// page bodies, multiple mdat) the gaps *between* segments are not hashed - Ogg
// keeps its tags up front, and an MP4 moov sandwiched between two mdats is a rare
// shape - so the head/tail backstop is weaker there but size/mtime/inode still
// guard. ok is false when there is nothing to hash or the region cannot be read,
// in which case the caller falls back to size/mtime/inode.
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
	// Trailing metadata after the last essence byte (any tags that follow the
	// audio), for both contiguous and multi-segment essences.
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

// Identity is a strong fingerprint of a source file, recorded at parse so a
// later save-back can detect that the file changed underneath us. Path, size,
// and mtime alone are too weak - and weaker still once mtime is not preserved
// - so a small structural fingerprint (a hash of the metadata region) is
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
// from, and a reason string when it is not. It is the change-detection rule for a
// conservative in-place save (SaveBack, or a SaveAsFile whose target resolves to the
// source): the full content check plus the modification time. A derived write - to
// another path or a streaming writer - uses [Identity.MatchesContent] instead, which
// omits the mtime so a benign touch during a long parse->write window does not spuriously
// block a write whose byte offsets are still valid.
func (id Identity) Matches(other Identity) (bool, string) {
	if ok, why := id.MatchesContent(other); !ok {
		return false, why
	}
	if id.ModTimeUnixNano != 0 && other.ModTimeUnixNano != 0 && id.ModTimeUnixNano != other.ModTimeUnixNano {
		return false, "modification time changed"
	}
	return true, ""
}

// MatchesContent reports whether other has the same byte content as this identity -
// inode/device, size, and (when both sides have one) the structural fingerprint - but
// NOT the modification time. A moved audio region always changes the size and/or the
// fingerprint, so mtime says nothing about whether the recorded byte offsets are still
// valid; a derived write skips it to avoid a false positive from an mtime-only touch.
// [Identity.Matches] layers the mtime check on top for the in-place case.
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
