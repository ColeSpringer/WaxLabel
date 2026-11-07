package core

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/colespringer/waxlabel/waxerr"
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

// SetIdentity records m's identity as a parse of src ends: the size of src and the fingerprint of
// its metadata regions ([ReadFingerprint]). A read of them that fails is returned with no
// fingerprint, so the parse fails closed: a device error keeps its cause, and a source that serves
// fewer bytes than its size is invalid data.
func SetIdentity(src ReaderAtSized, m *Media) error {
	m.Identity = Identity{Size: src.Size()}
	var err error
	m.Identity.Fingerprint, m.Identity.HasFinger, err = ReadFingerprint(src, m)
	return err
}

// ReadFingerprint hashes metadata regions around audio essence for change detection:
// [0, AudioStart) plus [AudioEnd, size), within size. No essence: whole file. Multi-segment: gaps
// skipped. It streams, so region size is unbounded. ok is false with a nil error when there is
// nothing to hash. A source that serves fewer bytes than its size inside a region is
// waxerr.ErrInvalidData with io.ErrUnexpectedEOF; a read that fails otherwise is the error.
func ReadFingerprint(src ReaderAtSized, m *Media) (sum [32]byte, ok bool, err error) {
	size := src.Size()
	if size <= 0 {
		return sum, false, nil
	}
	var regions [][2]int64
	if m.AudioStart <= 0 && m.AudioEnd <= 0 && len(m.AudioRanges) == 0 {
		regions = append(regions, [2]int64{0, size})
	} else {
		if m.AudioStart > 0 {
			regions = append(regions, [2]int64{0, min(m.AudioStart, size)})
		}
		if m.AudioEnd > m.AudioStart && m.AudioEnd < size {
			regions = append(regions, [2]int64{m.AudioEnd, size})
		}
	}
	if len(regions) == 0 {
		return sum, false, nil
	}
	// Large reads: a region with no audio inside it can be the whole file, read three times a save.
	var longest int64
	for _, r := range regions {
		longest = max(longest, r[1]-r[0])
	}
	buf := make([]byte, min(longest, 1<<20))
	h := sha256.New()
	for _, r := range regions {
		n, err := io.CopyBuffer(h, io.NewSectionReader(src, r[0], r[1]-r[0]), buf)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return sum, false, err
		}
		if n != r[1]-r[0] {
			return sum, false, fmt.Errorf("%w: read %d at %d: %w", waxerr.ErrInvalidData, r[1]-r[0], r[0], io.ErrUnexpectedEOF)
		}
	}
	return [32]byte(h.Sum(nil)), true, nil
}

// Identity fingerprints a source at parse time for save-back change detection.
type Identity struct {
	Path               string
	Size               int64
	ModTimeUnixNano    int64  // 0 when unknown, unrepresentable, or exactly the epoch
	ChangeTimeUnixNano int64  // 0 when unavailable (Windows)
	INode              uint64 // 0 when unavailable
	Device             uint64
	Fingerprint        [32]byte
	HasFinger          bool

	// file is the stat [FileIdentity] read where the stat carries no inode (Windows); os.SameFile
	// compares it there (volume serial plus file index).
	file os.FileInfo
}

// FileIdentity is the identity of the file info describes, recorded under path.
func FileIdentity(path string, info os.FileInfo) Identity {
	ino, dev, ctime := sysStat(info)
	id := Identity{
		Path:               path,
		Size:               info.Size(),
		ModTimeUnixNano:    UnixNanoOrZero(info.ModTime()),
		ChangeTimeUnixNano: ctime,
		INode:              ino,
		Device:             dev,
	}
	if ino == 0 && dev == 0 {
		// Windows reads the file index on the first comparison, not at stat; pin it to this stat.
		os.SameFile(info, info)
		id.file = info
	}
	return id
}

var (
	minRepresentableTime = time.Unix(0, math.MinInt64)
	maxRepresentableTime = time.Unix(0, math.MaxInt64)
)

// UnixNanoOrZero returns Unix nanos, or 0 outside the representable range / as unknown.
func UnixNanoOrZero(t time.Time) int64 {
	if t.Before(minRepresentableTime) || t.After(maxRepresentableTime) {
		return 0
	}
	return t.UnixNano()
}

// Matches is content check plus mtime and change time for in-place save (SaveBack).
func (id Identity) Matches(other Identity) (bool, string) {
	if ok, why := id.MatchesContent(other); !ok {
		return false, why
	}
	if id.ModTimeUnixNano != 0 && other.ModTimeUnixNano != 0 && id.ModTimeUnixNano != other.ModTimeUnixNano {
		return false, "modification time changed"
	}
	if id.ChangeTimeUnixNano != 0 && other.ChangeTimeUnixNano != 0 && id.ChangeTimeUnixNano != other.ChangeTimeUnixNano {
		return false, "change time changed"
	}
	return true, ""
}

// MatchesContent compares file identity, size, and fingerprint; omits mtime and change time
// (derived writes).
func (id Identity) MatchesContent(other Identity) (bool, string) {
	if !id.sameFile(other) {
		return false, "file inode changed"
	}
	if id.Size != other.Size {
		return false, fmt.Sprintf("size changed (%d -> %d)", id.Size, other.Size)
	}
	if id.HasFinger && other.HasFinger && id.Fingerprint != other.Fingerprint {
		return false, "metadata fingerprint changed"
	}
	return true, ""
}

// sameFile compares file identity by os.SameFile when both sides were stat'ed, else by inode.
func (id Identity) sameFile(other Identity) bool {
	if id.file != nil && other.file != nil {
		return os.SameFile(id.file, other.file)
	}
	if id.INode != 0 && other.INode != 0 {
		return id.INode == other.INode && id.Device == other.Device
	}
	return true
}
