// Package mp4 implements reading and writing MP4 / iTunes (M4A) metadata. It is
// internal through v0.x (promoted to a public waxlabel/mp4 only at v1.0). An MP4
// file is a tree of atoms (boxes); tags live in an iTunes-style list at
// moov.udta.meta.ilst, and the audio media lives in one or more mdat atoms whose
// byte offsets are recorded in per-track stco/co64 chunk-offset tables.
//
// The codec is preservation-first: it rewrites only the ilst tag list, reusing a
// neighbouring free padding atom so the media usually does not move at all. When
// the tag list must grow beyond the available padding, every track's stco/co64
// offset table is shifted so the media stays playable, and the enclosing
// moov/udta/meta atom sizes are patched - no atom is reordered and the mdat bytes
// are copied verbatim.
//
// Chapters are read from both the Nero list (moov.udta.chpl) and a QuickTime
// chapter text track, projected into one model, and a chapter edit rewrites both
// representations: the chpl and a freshly built QuickTime chapter text track
// (referenced from the audio track via a tref "chap", its samples in an mdat
// appended at end-of-file) so the edit is visible to iTunes and Apple Books.
//
// Out of scope in v1 (rejected loudly): fragmented MP4 (a top-level moof, or a
// moov declaring movie fragments via mvex).
//
// The codec is reimplemented from ISO/IEC 14496-12 and the iTunes metadata
// conventions; reference implementations were consulted for design only.
package mp4

import (
	"context"
	"encoding/binary"

	"github.com/colespringer/waxlabel/internal/core"
)

// Codec implements core.Codec for MP4.
type Codec struct{}

// New returns an MP4 codec.
func New() Codec { return Codec{} }

func init() { core.Register(New()) }

func (Codec) Format() core.Format  { return core.FormatMP4 }
func (Codec) Extensions() []string { return []string{".m4a", ".mp4", ".m4b", ".alac"} }

// Sniff matches an "....ftyp" header - the file-type atom that opens virtually
// every MP4/M4A file. The brand inside ftyp is not inspected here; a fragmented
// or otherwise unsupported variant is detected and rejected in Parse.
func (Codec) Sniff(header []byte) bool {
	return len(header) >= 8 && string(header[4:8]) == "ftyp"
}

// Parse reads metadata from src into a Media.
func (c Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities reports MP4's support. Tags and art are stored as ilst atoms,
// fully writable; chapters are read from both the Nero chpl and a QuickTime
// chapter text track, and a chapter edit rewrites both representations. The
// numeric "gnre" genre is read but always rewritten as the text genre.
func (Codec) Capabilities(_ *core.Media, opts core.WriteOptions) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "iTunes ilst atom (text / freeform ----)", Fidelity: "lossless",
		Constraints: []string{"the long tail is stored as com.apple.iTunes freeform atoms"},
	}
	pictures := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "covr atom (JPEG/PNG)", Fidelity: "lossless",
	}
	chapters := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "Nero chpl and a QuickTime chapter text track",
		MaxItems:       maxChplChapters,
		Constraints: []string{
			"at most 255 chapters (8-bit chpl count)",
			"both the chpl and the QuickTime chapter text track are written",
			"chapter start resolution is the movie timescale (typically 1 ms)",
		},
	}
	return core.NewCapabilities(core.FormatMP4, false, fields, pictures, chapters, nil)
}

// EssenceExtent returns the MP4 essence-digest inputs: a versioned extent name
// and the decoder-critical sample-entry configuration mixed in ahead of the
// media - the codec four-cc plus the channel count, sample size, and sample rate
// - so identical mdat bytes under a different codec or geometry hash differently.
// The hashed extent itself is the mdat payload range(s).
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	var cfg [12]byte
	if d, ok := m.Native.(*doc); ok {
		copy(cfg[0:4], d.cfg.codec[:])
		binary.BigEndian.PutUint16(cfg[4:6], d.cfg.channels)
		binary.BigEndian.PutUint16(cfg[6:8], d.cfg.sampleSize)
		binary.BigEndian.PutUint32(cfg[8:12], d.cfg.sampleRate)
	}
	return "mp4-mdat-v1", cfg[:]
}
