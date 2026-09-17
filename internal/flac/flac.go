package flac

import (
	"encoding/binary"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/vorbis"
)

// Codec implements [core.Codec] for FLAC.
type Codec struct{}

// New returns a FLAC codec.
func New() Codec { return Codec{} }

func init() { core.Register(New()) }

func (Codec) Format() core.Format { return core.FormatFLAC }

// SkipsLeadingID3 is true: FLAC tolerates a stray leading ID3v2 tag.
func (Codec) SkipsLeadingID3() bool { return true }
func (Codec) Extensions() []string  { return []string{".flac"} }

// Sniff matches "fLaC" at offset 0. A leading ID3v2 has no "fLaC" there; DetectLeading
// peeks past ID3 to the inner marker. Does not claim ID3 prefixes (shared with MP3).
func (Codec) Sniff(header []byte) bool {
	return len(header) >= 4 && string(header[:4]) == string(flacMagic)
}

// Capabilities: tags as Vorbis comments, art as PICTURE blocks, both fully writable.
// Chapters use CHAPTERxxx (start+title); CUESHEET is preserved opaque, not projected.
func (Codec) Capabilities(_ *core.Media, opts core.WriteOptions) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "Vorbis comment", Fidelity: "lossless",
	}
	pictures := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "FLAC PICTURE block", Fidelity: "lossless",
	}
	chapters := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "VorbisComment CHAPTERxxx",
		Fidelity:       "start and title stored",
		MaxItems:       vorbis.MaxChapters, // CHAPTERxxx is a 3-digit namespace
		Constraints:    []string{"CHAPTERxxx stores start and title only; a CUESHEET is preserved opaque but not read"},
		ChapterLoss:    core.ChapterLossStartTitleOnly,
	}
	// Metadata rewrite every edit: padding both grows and shrinks.
	return core.NewCapabilities(core.FormatFLAC, false, fields, pictures, chapters, core.AccessFull, nil).
		WithSyncedLyrics(vorbis.SyncedLyricsCapability()).
		WithFieldClassifier(vorbis.TransferClassifier)
}

// EssenceExtent: versioned name plus STREAMINFO config mixed ahead of audio frames.
// v2 excludes trailing junk found by the frame-tail walk (v1 hashed it as audio);
// AudioDigest requires a new name for that refinement. Search is bounded by
// min(4 MiB, alloc limit), so exclusion is deterministic for a given file/config.
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	t := m.Properties.First()
	var b [16]byte
	binary.BigEndian.PutUint32(b[0:4], uint32(t.SampleRate))
	binary.BigEndian.PutUint32(b[4:8], uint32(t.Channels))
	binary.BigEndian.PutUint32(b[8:12], uint32(t.BitsPerSample))
	b[12] = byte(t.MinBlockSize >> 8)
	b[13] = byte(t.MinBlockSize)
	b[14] = byte(t.MaxBlockSize >> 8)
	b[15] = byte(t.MaxBlockSize)
	return "flac-frames-v2", b[:]
}
