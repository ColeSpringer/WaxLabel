package mp3

import (
	"context"
	"encoding/binary"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// Codec implements core.Codec for MP3.
type Codec struct{}

// New returns an MP3 codec.
func New() Codec { return Codec{} }

func init() { core.Register(New()) }

func (Codec) Format() core.Format  { return core.FormatMP3 }
func (Codec) Extensions() []string { return []string{".mp3"} }

// Sniff matches a leading ID3v2 tag or a bare MPEG audio frame. An ID3v2 header
// is shared with other containers that may carry a stray leading ID3 (FLAC); the
// parser disambiguates by peeking past the tag, so claiming "ID3" here is safe.
func (Codec) Sniff(header []byte) bool {
	if len(header) >= 3 && header[0] == 'I' && header[1] == 'D' && header[2] == '3' {
		return true
	}
	if _, ok := decodeHeader(header); ok {
		return true
	}
	return false
}

// Parse reads metadata from src into a Media.
func (c Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities reports MP3's support. Tags and art are stored as ID3v2 frames,
// fully writable; the version is preserved on edit. Trailing ID3v1/APEv2 are
// preserved and surfaced but not the write target. The Media is bound (not version-
// blind) so the per-field ORIGINALDATE fidelity can reflect the file's actual ID3
// write version (C1).
func (Codec) Capabilities(m *core.Media, opts core.WriteOptions) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "ID3v2 frame", Fidelity: "lossless",
	}
	pictures := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "APIC frame", Fidelity: "lossless",
	}
	chapters := core.Capability{
		Read: core.AccessNone, Write: core.AccessNone,
		Representation: "CHAP preserved",
	}
	// In ID3v2.3, ORIGINALDATE is written to TORY, which holds the year only - so a
	// full YYYY-MM-DD original date is truncated to YYYY on write. Report ORIGINALDATE
	// as a partial (lossy) field on v2.3 so a transfer onto a v2.3 MP3 grades it Lossy
	// rather than claiming it carried unchanged (C1). v2.4 writes the full date to TDOR,
	// so it stays lossless and gets no override. RecordingDate (TYER+TDAT+TIME) and
	// ReleaseDate (TXXX:RELEASEDATE) keep their precision in v2.3, so only ORIGINALDATE
	// is downgraded.
	var perField map[tag.Key]core.Capability
	if id3WriteVersion(m) == 3 {
		perField = map[tag.Key]core.Capability{
			tag.OriginalDate: {
				Read: core.AccessFull, Write: core.AccessPartial,
				Representation: "ID3v2.3 TORY (year only)", Fidelity: "ID3v2.3 TORY stores the year only",
			},
		}
	}
	// ID3 front-tag padding is grow-only (ReuseOrTarget): a forced rewrite can grow
	// the region, but a fit-in-place edit reuses it and cannot shrink in place.
	return core.NewCapabilities(core.FormatMP3, false, fields, pictures, chapters, core.AccessPartial, perField)
}

// id3WriteVersion returns the ID3v2 minor version (3 or 4) the MP3 codec would write
// for media m: the parsed file's own tag version when present, else the format default
// (the file-less PlanTransfer path, where m is nil - a deliberate simulation choice,
// not a panic). Only the v2.3-vs-v2.4 distinction matters here (the date-frame split).
func id3WriteVersion(m *core.Media) byte {
	if m != nil {
		if d, ok := m.Native.(*doc); ok && d.id3 != nil {
			return d.id3.WriteVersion()
		}
	}
	return core.DefaultID3Version(core.FormatMP3)
}

// EssenceExtent returns the MP3 essence-digest inputs: a versioned extent name
// and the decoder-critical configuration mixed in ahead of the audio - the first
// frame header together with the decoded sample rate and channel count, so two
// streams with identical frame bytes but a different rate or channel layout hash
// differently.
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	var cfg [12]byte
	if d, ok := m.Native.(*doc); ok {
		copy(cfg[0:4], d.firstHeader[:])
		binary.BigEndian.PutUint32(cfg[4:8], uint32(d.track.SampleRate))
		binary.BigEndian.PutUint32(cfg[8:12], uint32(d.track.Channels))
	}
	return "mp3-frames-v1", cfg[:]
}
