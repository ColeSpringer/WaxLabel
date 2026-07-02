// Package mp4 implements reading and writing MP4 / iTunes (M4A) metadata for
// the public waxlabel package. The codec itself is internal. An MP4 file is a
// tree of atoms (boxes); tags live in an iTunes-style list at
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
// Fragmented MP4 is not writable: a top-level moof, or a moov declaring movie
// fragments via mvex, is rejected during parse.
//
// The codec is reimplemented from ISO/IEC 14496-12 and the iTunes metadata
// conventions; reference implementations were consulted for design only.
package mp4

import (
	"context"
	"encoding/binary"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// Codec implements core.Codec for MP4.
type Codec struct{}

// New returns an MP4 codec.
func New() Codec { return Codec{} }

func init() { core.Register(New()) }

func (Codec) Format() core.Format  { return core.FormatMP4 }
func (Codec) Extensions() []string { return []string{".m4a", ".mp4", ".m4b", ".alac"} }

// SkipsLeadingID3 reports false because MP4 parsers expect an atom box at offset 0.
func (Codec) SkipsLeadingID3() bool { return false }

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
		// Write is Full: the image set carries losslessly (byte-for-byte). The covr atom
		// drops a picture's role and description, but that loss is per-picture (a plain
		// front cover round-trips), so it is surfaced precisely by the plan's
		// picture-metadata-dropped warning rather than the coarse, count-based transfer
		// level - which, as AccessPartial, would mislabel even a lossless front-cover copy
		// as lossy. The Fidelity/Constraints below still document the limitation in caps.
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "covr atom (JPEG/PNG/BMP)", Fidelity: "image bytes lossless; role and description not stored",
		Constraints: []string{"covers store image data only - picture role and description are dropped (read back as front cover)"},
		PictureLoss: core.PictureLossRoleAndDescription,
		// A covr atom can only label JPEG/PNG/BMP, so the transfer layer drops other cover
		// formats per-image. Clone the package var: Capabilities is publicly exported, so
		// handing out the backing array would let a caller mutate the write-time allowlist.
		PictureMIMEs: slices.Clone(coverMIMEs),
	}
	chapters := core.Capability{
		// Starts and titles write losslessly. MP4 drops gapped ends, per-chapter
		// language, and hidden/disabled flags, but that loss depends on the chapter set:
		// plain chapters round-trip. Keep Write full and express the conditional loss
		// through ChapterLoss and edit warnings instead of AccessPartial.
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "Nero chpl and a QuickTime chapter text track",
		Fidelity:       "chapter start and title stored; gapped end times, per-chapter language, and hidden/disabled flags are dropped",
		MaxItems:       maxChplChapters,
		ChapterLoss:    core.ChapterLossStartTitleOnly,
		Constraints: []string{
			"at most 255 chapters (8-bit chpl count)",
			"both the chpl and the QuickTime chapter text track are written",
			"chapter start resolution is the movie timescale (typically 1 ms)",
		},
	}
	// Per-field value-drop predicates expose the values the iTunes atom encoders cannot
	// store: out-of-uint16 trkn/disk slots and invalid stik/cpil values. Transfer uses these
	// predicates before applying fields so a dropped source value does not overwrite a valid
	// destination value.
	//
	// Under --numeric-genre, recognized genres are written as numeric "gnre" atoms and
	// re-read as canonical ID3 genre names; the capability is value-blind, so it reports
	// GENRE as partial. The lazy add inits the map on first use and preserves the Genre
	// entry rather than overwriting it.
	var perField map[tag.Key]core.Capability
	add := func(k tag.Key, c core.Capability) {
		if perField == nil {
			perField = map[tag.Key]core.Capability{}
		}
		perField[k] = c
	}
	if opts.NumericGenre {
		add(tag.Genre, core.NumericGenreCapability("numeric gnre atom"))
	}
	add(tag.TrackNumber, core.WithValueDrop(fields, numberComponentDropped))
	add(tag.TrackTotal, core.WithValueDrop(fields, slotValueDropped))
	add(tag.DiscNumber, core.WithValueDrop(fields, numberComponentDropped))
	add(tag.DiscTotal, core.WithValueDrop(fields, slotValueDropped))
	add(tag.MediaType, core.WithValueDrop(fields, mediaTypeValueDropped))
	add(tag.Compilation, core.WithValueDrop(fields, compilationValueDropped))
	// Padding is grow-only: a forced rewrite can reserve a region, but a fit-in-place
	// edit reuses the existing free space and cannot shrink it.
	return core.NewCapabilities(core.FormatMP4, false, fields, pictures, chapters, core.AccessPartial, perField)
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
	// v3 changed the hashed byte set again. essenceMdats now trims each mdat to its first
	// non-chapter chunk, which excludes front-loaded QuickTime chapter text in common M4B
	// files as well as chapter-only mdats.
	return "mp4-mdat-v3", cfg[:]
}
