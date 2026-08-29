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
// Fragmented MP4 is readable but not writable: a file with a top-level moof parses
// normally (the initial movie box carries the tags) and reports ReadOnly, and only
// the rewrite is refused, with waxerr.ErrFragmented. A moov that declares mvex but
// carries no fragment is an ordinary progressive file and is written normally. A
// fragmented media segment - fragments with no moov at all - is rejected at parse.
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

func (Codec) Format() core.Format { return core.FormatMP4 }

// Extensions claims the MP4 family names. ".m4r" is Apple's ringtone extension - a
// plain AAC-in-MP4 file. ".mov" is QuickTime, the same box structure this codec
// already parses; claiming it means a --recursive walk descends into QuickTime files
// and rewrites their metadata, which is deliberate. Both are here so a recursive walk
// does not skip them and warnExtensionMismatch does not call a legitimate write a
// transcode.
func (Codec) Extensions() []string {
	return []string{".m4a", ".mp4", ".m4b", ".m4r", ".mov", ".alac"}
}

// SkipsLeadingID3 reports false because MP4 parsers expect an atom box at offset 0.
func (Codec) SkipsLeadingID3() bool { return false }

// Sniff matches an "....ftyp" header - the file-type atom that opens virtually
// every MP4/M4A file. The brand inside ftyp is not inspected here; an unsupported
// variant is detected in Parse, which rejects a movie-box-less fragmented segment
// and reads (but marks unwritable) a fragmented file.
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
//
// Support is per-file in one respect: a fragmented file (a top-level moof) reads
// normally but cannot be rewritten, so it reports ReadOnly. A format-level query
// (m == nil) reports writable, since the format at large is.
func (Codec) Capabilities(m *core.Media, opts core.WriteOptions) core.Capabilities {
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
		// Starts and titles write losslessly, and the QuickTime text track carries the final
		// chapter's explicit end. MP4 drops interior gapped ends, per-chapter language, and
		// hidden/disabled flags, but that loss depends on the chapter set: plain chapters
		// round-trip. Keep Write full and express the conditional loss through ChapterLoss and
		// edit warnings instead of AccessPartial.
		Read: core.AccessFull, Write: core.AccessFull,
		Representation:      "Nero chpl and a QuickTime chapter text track",
		Fidelity:            "chapter start, title, and the final chapter's end stored; interior gapped end times, per-chapter language, and hidden/disabled flags are dropped",
		MaxItems:            maxChplChapters,
		ChapterLoss:         core.ChapterLossInteriorEndsLangFlags,
		ChapterTitleByteMax: 255,
		Constraints: []string{
			"at most 255 chapters (8-bit chpl count)",
			"chapter titles are truncated to 255 bytes (8-bit chpl length prefix)",
			"both the chpl and the QuickTime chapter text track are written",
			"chapter start resolution is the movie timescale (typically 1 ms)",
		},
	}
	// Per-field value-drop predicates expose the values the iTunes atom encoders cannot
	// store: out-of-uint16 trkn/disk slots and invalid integer, BPM, or boolean atom values.
	// Transfer uses these predicates before applying fields so a dropped source value does
	// not overwrite a valid destination value.
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
	// trkn/disk slots store the number as a 16-bit integer, so a non-canonical form (a leading
	// zero or sign, "03"/"+3" stored as 3) is normalized on write. That normalization is
	// numerically lossless, so a copy grades it Carried, matching the diff command, which treats a
	// sign/leading-zero-only delta as no change (tag.NumericValuesEqual). Only a genuinely
	// unrepresentable slot (overflow, non-numeric, or a 0 that reads back absent) is a loss, so the
	// number fields carry the value-drop predicate without a reduction wrapper.
	add(tag.TrackNumber, core.WithValueDrop(fields, numberComponentDropped(tag.TrackNumber)))
	add(tag.TrackTotal, core.WithValueDrop(fields, slotValueDropped))
	add(tag.DiscNumber, core.WithValueDrop(fields, numberComponentDropped(tag.DiscNumber)))
	add(tag.DiscTotal, core.WithValueDrop(fields, slotValueDropped))
	add(tag.MediaType, core.WithValueDrop(fields, mediaTypeValueDropped))
	add(tag.Compilation, core.WithValueDrop(fields, compilationValueDropped))
	add(tag.ITunesAdvisory, core.WithValueDrop(fields, advisoryValueDropped))
	add(tag.Movement, core.WithValueDrop(fields, movementValueDropped))
	add(tag.MovementTotal, core.WithValueDrop(fields, movementTotalValueDropped))
	// BPM carries a reduction predicate beside the drop one: the tmpo atom rounds a valid
	// fraction to the nearest whole number, so a copy of "174.99" grades Lossy (the reader
	// gets 175) rather than a false clean carry. bpmValueCoerced is the same decision the
	// writer's coercion warning fires on, so the grade and the warning cannot drift.
	bpmField := fields
	bpmField.Fidelity = "stored as a whole number in the tmpo atom; a fractional value rounds to nearest"
	add(tag.BPM, core.WithValueDrop(core.WithValueReduction(bpmField, bpmValueReduced), bpmValueDropped))
	add(tag.ITunesGapless, core.WithValueDrop(fields, gaplessValueDropped))
	add(tag.ShowMovement, core.WithValueDrop(fields, showMovementValueDropped))
	// ReadOnly comes from the same predicate Plan refuses on, so the capability a caller
	// is shown matches what a write would actually do (the report==result invariant the
	// Codec contract states).
	//
	// Only ReadOnly is set: the field/picture/chapter levels keep describing the FORMAT
	// (and the editor's own gates key off them), and core.dispose short-circuits on
	// ReadOnly before consulting them, so transfer reporting is already correct. Dropping
	// them to AccessNone would be actively harmful - the editor refuses a chapter edit
	// with ErrUnsupportedTag and "chapters cannot be written to an MP4 file" before Plan
	// runs, pre-empting the precise refusal with a wrong sentinel and a false claim about
	// the format.
	// Keep the refusal itself, not just its existence: a fragmented file is
	// ErrFragmented (a distinct exit-code row) while an iloc/saio file is
	// ErrUnsupportedFormat, so a caller that declines before reaching Plan - the transfer
	// path - returns the same error a write would rather than flattening the two.
	var refusal error
	if m != nil {
		if d, ok := m.Native.(*doc); ok && d != nil {
			refusal = d.refuseWrite()
		}
	}
	// Padding is grow-only: a forced rewrite can reserve a region, but a fit-in-place
	// edit reuses the existing free space and cannot shrink it.
	return core.NewCapabilities(core.FormatMP4, refusal != nil, fields, pictures, chapters, core.AccessPartial, perField).
		WithFieldClassifier(transferClassifier).
		WithReadOnlyReason(refusal)
}

// transferClassifier grades the one field shape whose MP4 transfer fate the format-level
// capability cannot express: a structured single-atom key given more than one value stores
// only the first (the writer names the surplus in a value-dropped warning), so the copy must
// grade it Lossy rather than a clean carry. Every other field is left to the format-level
// grade.
func transferClassifier(key tag.Key, values []string, _ tag.TagSet) (core.Disposition, string, bool) {
	if structuredSingleAtomKeys[key] && len(values) > 1 {
		return core.Lossy, "this field is a single-value MP4 atom; only the first value is stored", true
	}
	return core.Carried, "", false
}

// bpmValueReduced adapts the writer's coercion decision to the capability layer's
// per-value reduction predicate.
func bpmValueReduced(v string) bool {
	_, coerced := bpmValueCoerced(v)
	return coerced
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
