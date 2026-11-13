package ogg

import (
	"bytes"
	"context"
	"encoding/binary"
	"slices"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/vorbis"
)

// Codec implements core.Codec for Ogg. Three instances (Vorbis, Opus, FLAC) share
// this code; they differ in claimed format and sniff signature. Parser picks the
// real codec; edits route via recorded Format.
type Codec struct{ format core.Format }

// NewVorbis, NewOpus, and NewFLAC return the three Ogg codecs.
func NewVorbis() Codec { return Codec{format: core.FormatOggVorbis} }
func NewOpus() Codec   { return Codec{format: core.FormatOggOpus} }
func NewFLAC() Codec   { return Codec{format: core.FormatOggFLAC} }

func init() {
	core.Register(NewVorbis())
	core.Register(NewOpus())
	core.Register(NewFLAC())
}

func (c Codec) Format() core.Format { return c.format }

// SkipsLeadingID3 is true: an Ogg stream is read behind a stray leading ID3v2 tag.
func (Codec) SkipsLeadingID3() bool { return true }

// Extensions: .ogg and .oga for Vorbis/FLAC. RFC 5334 prefers .oga for non-Vorbis,
// but flac long wrote Ogg FLAC as .ogg; claiming only .oga would false-flag those.
// Format ambiguity is resolved by name.
func (c Codec) Extensions() []string {
	if c.format == core.FormatOggOpus {
		return []string{".opus"}
	}
	return []string{".ogg", ".oga"}
}

// skeletonID opens the BOS packet of a Skeleton stream, which may come before the codec's.
var skeletonID = []byte("fishead\x00")

// Sniff: the id signature ("\x01vorbis", "OpusHead", or "\x7FFLAC") opens the packet that begins the
// stream on its first page, or on the next page when the first begins a Skeleton stream. Page headers
// are walked inside header; the signature anywhere else does not count.
func (c Codec) Sniff(header []byte) bool {
	sig := vorbisID
	switch c.format {
	case core.FormatOggOpus:
		sig = opusHead
	case core.FormatOggFLAC:
		sig = flacID
	}
	pkt, next, ok := bosPacket(header, 0)
	if ok && bytes.HasPrefix(pkt, skeletonID) {
		pkt, _, ok = bosPacket(header, next)
	}
	return ok && bytes.HasPrefix(pkt, sig)
}

// bosPacket returns the first packet of the beginning-of-stream page at off in b, cut at the end of
// b, and where the page ends; ok is false when b holds no such page header there.
func bosPacket(b []byte, off int) (pkt []byte, next int, ok bool) {
	if off+pageFixedHdr > len(b) || !bytes.Equal(b[off:off+4], oggMagic) || b[off+4] != 0 || b[off+5]&flagBOS == 0 {
		return nil, 0, false
	}
	body := off + pageFixedHdr + int(b[off+26])
	if body > len(b) {
		return nil, 0, false
	}
	pktLen, bodyLen, open := 0, 0, true
	for _, lacing := range b[off+pageFixedHdr : body] {
		bodyLen += int(lacing)
		if open {
			pktLen += int(lacing)
			open = lacing == maxSegments
		}
	}
	return b[body:min(body+pktLen, len(b))], body + bodyLen, true
}

// Resync finds the first BOS page in window whose checksum holds and that begins this codec's
// stream as Sniff reads it ([core.Resyncer]).
func (c Codec) Resync(window []byte) (int, bool) {
	w, _ := newWalker(core.BytesSource(window), int64(len(window)), 0)
	p, ok, _ := w.seek(0, 0, func(p rawPage) (bool, error) {
		return p.flags&flagBOS != 0 && c.Sniff(window[p.off:]), nil
	})
	return int(p.off), ok
}

// ChecksummedSync: Resync takes a page only when its checksum holds ([core.StartResyncer]).
func (Codec) ChecksummedSync() {}

func (c Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, c, src, opts)
}

// Capabilities: Vorbis comments; art is METADATA_BLOCK_PICTURE or FLAC PICTURE;
// chapters are CHAPTERxxx (start+title). A file Plan refuses to rewrite is read-only.
func (c Codec) Capabilities(m *core.Media, opts core.WriteOptions) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "Vorbis comment", Fidelity: "lossless",
		Constraints: []string{vorbis.CommentCountConstraint()},
	}
	pictureRep := "METADATA_BLOCK_PICTURE"
	if c.format == core.FormatOggFLAC {
		pictureRep = "FLAC PICTURE block"
	}
	pictures := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: pictureRep, Fidelity: "lossless",
		PictureGeometry: true,
	}
	chapters := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation:  "VorbisComment CHAPTERxxx",
		Fidelity:        "start and title stored",
		MaxItems:        vorbis.MaxChapters, // CHAPTERxxx is a 3-digit namespace
		Constraints:     []string{"CHAPTERxxx stores start and title only (no end time, language, or flags)"},
		ChapterLoss:     core.ChapterLossStartTitleOnly,
		ChapterTimeUnit: time.Millisecond, // CHAPTERxxx=HH:MM:SS.mmm
		ChapterTimeMax:  vorbis.MaxChapterTime,
	}
	var refusal error
	// A header packet is written up to the allocation limit, or the largest the file holds.
	var d *doc
	if m != nil {
		d, _ = m.Native.(*doc)
	}
	limit, store := d.writeAllocLimit(opts), "an Ogg header packet holds under the allocation limit"
	if d != nil {
		refusal = d.refuseWrite()
	}
	if c.format == core.FormatOggFLAC {
		// Each picture is a metadata block in a packet of its own: both limits hold.
		packetLimit, packetStore := limit, store
		pictures = core.WithPictureSizeLimit(pictures, func(p core.Picture) string {
			if r := vorbis.PictureBlockTooLarge(p); r != "" {
				return r
			}
			return core.SizeLimitReason("the picture", vorbis.PictureBlockLen(p), packetLimit, packetStore)
		})
		if limit > vorbis.MaxBlockBody {
			limit, store = vorbis.MaxBlockBody, vorbis.BlockStore
		}
	} else {
		pictures = core.WithPictureSizeLimit(pictures, func(p core.Picture) string {
			return core.SizeLimitReason("the picture", vorbis.PictureCommentLen(p), limit, store)
		})
	}
	// Comment padding round-tripped as-is; no padding control.
	caps := core.NewCapabilities(c.format, refusal != nil, fields, pictures, chapters, core.AccessNone, nil).
		WithSyncedLyrics(vorbis.SyncedLyricsCapability()).
		WithFieldClassifier(vorbis.TransferClassifier).
		WithReadOnlyReason(refusal).
		WithValueSizeLimit(limit, store)
	if c.format == core.FormatOggOpus {
		caps = caps.WithOutputGain(core.AccessFull)
	}
	return caps
}

// opusOutputGain reads OpusHead output gain (signed Q7.8 at 16:18), or 0 if too short.
func opusOutputGain(head []byte) int {
	if len(head) < 18 {
		return 0
	}
	return int(int16(binary.LittleEndian.Uint16(head[16:18])))
}

// opusHeadWithGain copies OpusHead with gain set (no-op copy if head too short).
func opusHeadWithGain(head []byte, gain int) []byte {
	out := slices.Clone(head)
	if len(out) >= 18 {
		binary.LittleEndian.PutUint16(out[16:18], uint16(int16(gain)))
	}
	return out
}

// EssenceExtent: versioned name plus decoder-critical config ahead of audio packets.
// Opus: OpusHead with output_gain masked. Vorbis: id+setup. FLAC: STREAMINFO.
func (c Codec) EssenceExtent(m *core.Media) (string, []byte) {
	d, ok := m.Native.(*doc)
	if !ok || d == nil {
		// No native doc: use registered format's extent name.
		return extentForFormat(c.format), nil
	}
	switch d.kind {
	case kindOpus:
		return extentOpus, opusHeadWithGain(d.idPacket, 0)
	case kindFLAC:
		// STREAMINFO only; id packet also has header-packet count (edit-mutable).
		return extentFLAC, slices.Clone(d.streamInfo())
	}
	return extentVorbis, slices.Concat(d.idPacket, d.setupPacket)
}

// Essence-extent names per mapping (shared by format fallback and parsed kind).
const (
	extentVorbis = "ogg-vorbis-packets-v1"
	// v2 masks output gain (playback control, not encoded audio).
	extentOpus = "ogg-opus-packets-v2"
	extentFLAC = "ogg-flac-frames-v1"
)

func extentForFormat(f core.Format) string {
	switch f {
	case core.FormatOggOpus:
		return extentOpus
	case core.FormatOggFLAC:
		return extentFLAC
	}
	return extentVorbis
}
