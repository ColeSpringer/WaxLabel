// Package mp4 implements reading and writing MP4 / iTunes (M4A) metadata for the
// public waxlabel package. The codec itself is internal. Tags live in the ilst of
// moov.udta.meta, or of a meta directly under moov, keyed by atom name or through an mdta
// keys index, and in moov.udta text atoms; media in mdat with offsets in stco/co64.
//
// Preservation-first: rewrites ilst, reusing neighbouring free padding when possible.
// Growth past padding shifts stco/co64 and patches moov/udta/meta sizes; mdat is copied
// verbatim. An item keeps the encoding the file gave it while its value stands.
//
// The audio essence is the sound tracks' sample chunks; without readable sample tables,
// the mdats.
//
// Chapters: Nero chpl and QuickTime text track, projected together; an edit rewrites
// both (chpl plus the text track a tref "chap" entry names, samples in an mdat behind the
// last atom). A video track named there holds thumbnails and stays.
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
	"time"

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
// Steps over a leading free/skip/wide within the [core.SniffWindow]. A box that
// does not fit hides ftyp; each step advances by at least a header so the walk
// terminates.
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
// (edit rewrites both). Numeric gnre reads; a write keeps it only for an unchanged
// value or with NumericGenre, else it becomes a text genre.
// Fragmented files (top-level moof) report ReadOnly; m == nil reports writable.
func (Codec) Capabilities(m *core.Media, opts core.WriteOptions) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "iTunes ilst atoms or an mdta keys store, under moov.udta.meta or moov.meta, or moov.udta text atoms", Fidelity: "lossless",
		Constraints: []string{
			"the long tail is stored as com.apple.iTunes freeform atoms",
			"values go to one store per file: the ilst when the file has one, moov.udta.meta's ahead of moov.meta's, else its moov.udta text atoms",
			"moov.udta text atoms and a second ilst follow each write for the keys they hold; a text atom drops a value it cannot hold, and a key the second ilst alone holds stays there",
			"a key that several ilst items hold reads from the first; the others stay until the key is edited",
			"a new mdta entry takes ffmpeg's name for the key, and a track or disc number shares one entry with its total",
		},
	}
	pictures := core.Capability{
		// Write Full: image bytes lossless; role/description loss is per-picture
		// (plan warning), not AccessPartial.
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "covr atom, or the artwork key of an mdta keys store (JPEG/PNG/BMP)", Fidelity: "image bytes lossless; role and description not stored",
		Constraints: []string{"covers store image data only - picture role and description are dropped (read back as front cover)"},
		PictureLoss: core.PictureLossRoleAndDescription,
		// A covr atom can only label JPEG/PNG/BMP, so the transfer layer drops other cover
		// formats per-image. Clone the package var so a caller cannot mutate the allowlist.
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
		ChapterTimeUnit:     time.Second / chplStartUnit,
		Constraints: []string{
			"at most 255 chapters (8-bit chpl count)",
			"chapter titles are truncated to 255 bytes (8-bit chpl length prefix)",
			"both the chpl and the QuickTime chapter text track are written",
			"the chapter track is the text or subtitle track a tref chap entry names; a video track named there holds thumbnails and is kept",
			"a new chapter track is referenced from every sound track and every video track that holds no thumbnails",
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
	var parsed []item
	ilst := true
	if m != nil {
		if d, ok := m.Native.(*doc); ok && d != nil {
			refusal, parsed = d.refuseWrite(), d.items
			// A keys store, the file's own or a second one beside its ilst, holds what an
			// iTunes ilst has no form for: the plan's own result grades those.
			ilst = !mdtaStore(d) && d.second == nil
		}
	}
	// The limit checkBuiltItems holds every written item to.
	limit, store := itemSizeLimit(parsed, opts.Limits.MaxAllocBytes), "an MP4 metadata item holds under the allocation limit"
	pictures = core.WithPictureSizeLimit(pictures, func(p core.Picture) string {
		return core.SizeLimitReason("the picture", int64(len(p.Data)), limit, store)
	})
	// An edit that fits reuses the ilst and free region in place unless the policy reuses none.
	caps := core.NewCapabilities(core.FormatMP4, refusal != nil, fields, pictures, chapters, core.AccessPartial, perField).
		WithReadOnlyReason(refusal).
		WithValueSizeLimit(limit, store)
	if ilst {
		return caps.WithRenderedFields(ilstRendering(opts.NumericGenre))
	}
	return caps.WithFieldClassifier(singleAtomClassifier)
}

// singleAtomReason: a single-atom key stores only the first of several values.
const singleAtomReason = "this field is a single-value MP4 atom; only the first value is stored"

// singleAtomClassifier: multi-value on a single-atom key stores only the first (Lossy).
func singleAtomClassifier(key tag.Key, values []string, _ *core.Media) (core.Disposition, string, bool) {
	if structuredSingleAtomKeys[key] && len(values) > 1 {
		return core.Lossy, singleAtomReason, true
	}
	return core.Carried, "", false
}

// ilstRendering grades the fields of a file whose tags go to an iTunes ilst, or of the format
// with no file, by what the item builder stores of them ([core.RenderedFields]): the one value
// of a single-atom key, a number an integer atom rounds, a custom key whose freeform name reads
// back as another key, an empty value an atom has no form for.
func ilstRendering(numericGenre bool) core.RenderedFields {
	return core.RenderedFields{
		Render: func(_ *core.Media, fields tag.TagSet) *core.Media {
			tags, _, _, _ := project(&doc{items: buildItems(fields, nil, nil, numericGenre)})
			read := &core.Media{Format: core.FormatMP4, Tags: tags}
			core.SyncTextItems(read)
			return read
		},
		Reason: func(k tag.Key, d core.Disposition, fields tag.TagSet) string {
			vals, _ := fields.Get(k)
			switch {
			case d == core.Dropped && !slices.ContainsFunc(vals, func(v string) bool { return v != "" }):
				return "the MP4 atom for this key has no form for an empty value"
			case d == core.Lossy && structuredSingleAtomKeys[k] && len(vals) > 1:
				return singleAtomReason
			}
			return ""
		},
	}
}

// bpmValueReduced adapts writer coercion to the capability reduction predicate.
func bpmValueReduced(v string) bool {
	_, coerced := bpmValueCoerced(v)
	return coerced
}

// EssenceExtent: versioned name plus sample-entry config (codec fourcc, channels,
// sample size, rate) mixed ahead of the essence ranges.
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	var cfg [12]byte
	// v3: essenceMdats, each mdat from its first non-chapter chunk, where no sample table
	// gives the sound tracks' own chunks.
	label := "mp4-mdat-v3"
	if d, ok := m.Native.(*doc); ok {
		copy(cfg[0:4], d.cfg.codec[:])
		binary.BigEndian.PutUint16(cfg[4:6], d.cfg.channels)
		binary.BigEndian.PutUint16(cfg[6:8], d.cfg.sampleSize)
		binary.BigEndian.PutUint32(cfg[8:12], d.cfg.sampleRate)
		if d.sampleExtent {
			// v4: the sound tracks' sample chunks, no video and no chapter samples.
			label = "mp4-samples-v4"
		}
	}
	return label, cfg[:]
}
