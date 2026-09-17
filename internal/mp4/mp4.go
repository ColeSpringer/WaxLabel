// Package mp4 implements reading and writing MP4 / iTunes (M4A) metadata for the
// public waxlabel package. The codec itself is internal. Tags live at
// moov.udta.meta.ilst; media in mdat with offsets in stco/co64.
//
// Preservation-first: rewrites ilst, reusing neighbouring free padding when possible.
// Growth past padding shifts stco/co64 and patches moov/udta/meta sizes; mdat is copied
// verbatim.
//
// Chapters: Nero chpl and QuickTime text track, projected together; an edit rewrites
// both (chpl plus a new text track via tref "chap", samples in an end-of-file mdat).
//
// Fragmented MP4 (top-level moof) is readable but ReadOnly; rewrite refused with
// waxerr.ErrFragmented. mvex without fragments is progressive and writable. Segments
// with no moov are rejected at parse.
//
// Reimplemented from ISO/IEC 14496-12 and iTunes metadata conventions; reference
// implementations were consulted for design only.
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

// Extensions: .m4r (ringtone AAC-in-MP4), .mov (QuickTime, same boxes). Included so
// recursive walks do not skip them and warnExtensionMismatch does not treat a write
// as a transcode.
func (Codec) Extensions() []string {
	return []string{".m4a", ".mp4", ".m4b", ".m4r", ".mov", ".alac"}
}

// SkipsLeadingID3 is false: MP4 expects an atom at offset 0.
func (Codec) SkipsLeadingID3() bool { return false }

// Sniff matches ....ftyp. Brand is not inspected here; Parse rejects moov-less
// fragments and marks fragmented files unwritable.
//
// Steps over a leading free/skip/wide within the 64-byte [core.DetectLeading]
// window. A box that does not fit hides ftyp; each step advances by at least a
// header so the walk terminates.
func (Codec) Sniff(header []byte) bool {
	for off := 0; off+8 <= len(header); {
		switch string(header[off+4 : off+8]) {
		case "ftyp":
			return true
		case "free", "skip", "wide":
			// A size below 8 is the "0" (extends to EOF) or "1" (64-bit size follows)
			// form, neither of which names a next box inside this window. Read as int64 so
			// a hostile size cannot wrap the offset on a 32-bit platform.
			size := int64(binary.BigEndian.Uint32(header[off : off+4]))
			if size < 8 || int64(off)+size > int64(len(header)) {
				return false
			}
			off += int(size)
		default:
			return false
		}
	}
	return false
}

// Parse reads metadata from src into a Media.
func (c Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities: ilst tags/art fully writable; chapters from chpl and QT text track
// (edit rewrites both). Numeric gnre is read but rewritten as text genre.
// Fragmented files (top-level moof) report ReadOnly; m == nil reports writable.
func (Codec) Capabilities(m *core.Media, opts core.WriteOptions) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "iTunes ilst atoms, an mdta keys store, or moov.udta text atoms", Fidelity: "lossless",
		Constraints: []string{
			"the long tail is stored as com.apple.iTunes freeform atoms",
			"values go to one store per file: the ilst when the file has one, else its moov.udta text atoms",
		},
	}
	pictures := core.Capability{
		// Write Full: image bytes lossless; role/description loss is per-picture
		// (plan warning), not AccessPartial.
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
		// Starts/titles lossless; QT track carries final chapter end. Interior ends /
		// lang / flags via ChapterLoss, not AccessPartial.
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
	// Value-drop predicates for unstorable iTunes atom values. Lazy add preserves Genre.
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
	// trkn/disk: 16-bit; non-canonical forms normalize (Carried). Unrepresentable slots drop.
	add(tag.TrackNumber, core.WithValueDrop(fields, numberComponentDropped(tag.TrackNumber)))
	add(tag.TrackTotal, core.WithValueDrop(fields, slotValueDropped))
	add(tag.DiscNumber, core.WithValueDrop(fields, numberComponentDropped(tag.DiscNumber)))
	add(tag.DiscTotal, core.WithValueDrop(fields, slotValueDropped))
	add(tag.MediaType, core.WithValueDrop(fields, mediaTypeValueDropped))
	add(tag.Compilation, core.WithValueDrop(fields, compilationValueDropped))
	add(tag.ITunesAdvisory, core.WithValueDrop(fields, advisoryValueDropped))
	add(tag.Movement, core.WithValueDrop(fields, movementValueDropped))
	add(tag.MovementTotal, core.WithValueDrop(fields, movementTotalValueDropped))
	// BPM: tmpo rounds fractions; reduction predicate matches writer coercion warning.
	bpmField := fields
	bpmField.Fidelity = "stored as a whole number in the tmpo atom; a fractional value rounds to nearest"
	add(tag.BPM, core.WithValueDrop(core.WithValueReduction(bpmField, bpmValueReduced), bpmValueDropped))
	add(tag.ITunesGapless, core.WithValueDrop(fields, gaplessValueDropped))
	add(tag.ShowMovement, core.WithValueDrop(fields, showMovementValueDropped))
	// ReadOnly from the same refuseWrite Plan uses (capability matches write outcome).
	var refusal error
	if m != nil {
		if d, ok := m.Native.(*doc); ok && d != nil {
			refusal = d.refuseWrite()
		}
	}
	// Padding is grow-only (ReuseOrTarget).
	return core.NewCapabilities(core.FormatMP4, refusal != nil, fields, pictures, chapters, core.AccessPartial, perField).
		WithFieldClassifier(transferClassifier).
		WithReadOnlyReason(refusal)
}

// transferClassifier: multi-value on a single-atom key stores only the first (Lossy).
func transferClassifier(key tag.Key, values []string, _ tag.TagSet) (core.Disposition, string, bool) {
	if structuredSingleAtomKeys[key] && len(values) > 1 {
		return core.Lossy, "this field is a single-value MP4 atom; only the first value is stored", true
	}
	return core.Carried, "", false
}

// bpmValueReduced adapts writer coercion to the capability reduction predicate.
func bpmValueReduced(v string) bool {
	_, coerced := bpmValueCoerced(v)
	return coerced
}

// EssenceExtent: versioned name plus sample-entry config (codec fourcc, channels,
// sample size, rate) mixed ahead of mdat payload range(s).
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	var cfg [12]byte
	if d, ok := m.Native.(*doc); ok {
		copy(cfg[0:4], d.cfg.codec[:])
		binary.BigEndian.PutUint16(cfg[4:6], d.cfg.channels)
		binary.BigEndian.PutUint16(cfg[6:8], d.cfg.sampleSize)
		binary.BigEndian.PutUint32(cfg[8:12], d.cfg.sampleRate)
	}
	// v3: essenceMdats trims each mdat to its first non-chapter chunk.
	return "mp4-mdat-v3", cfg[:]
}
