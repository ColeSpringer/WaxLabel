package ogg

import (
	"bytes"
	"context"
	"encoding/binary"
	"slices"

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

// SkipsLeadingID3 is false: streams start with OggS.
func (Codec) SkipsLeadingID3() bool { return false }

// Extensions: .ogg and .oga for Vorbis/FLAC. RFC 5334 prefers .oga for non-Vorbis,
// but flac long wrote Ogg FLAC as .ogg; claiming only .oga would false-flag those.
// Format ambiguity is resolved by name.

func (c Codec) Extensions() []string {
	if c.format == core.FormatOggOpus {
		return []string{".opus"}
	}
	return []string{".ogg", ".oga"}
}

// Sniff: OggS plus id signature ("\x01vorbis", "OpusHead", or "\x7FFLAC") near start
// (id packet is alone on page 0).

func (c Codec) Sniff(header []byte) bool {
	if !bytes.HasPrefix(header, oggMagic) {
		return false
	}
	switch c.format {
	case core.FormatOggOpus:
		return bytes.Contains(header, opusHead)
	case core.FormatOggFLAC:
		return bytes.Contains(header, flacID)
	}
	return bytes.Contains(header, vorbisID)
}

func (c Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities: Vorbis comments; art is METADATA_BLOCK_PICTURE or FLAC PICTURE;
// chapters are CHAPTERxxx (start+title).

func (c Codec) Capabilities(_ *core.Media, opts core.WriteOptions) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "Vorbis comment", Fidelity: "lossless",
	}
	pictureRep := "METADATA_BLOCK_PICTURE"
	if c.format == core.FormatOggFLAC {
		pictureRep = "FLAC PICTURE block"
	}
	pictures := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: pictureRep, Fidelity: "lossless",
	}
	chapters := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "VorbisComment CHAPTERxxx",
		Fidelity:       "start and title stored",
		MaxItems:       vorbis.MaxChapters, // CHAPTERxxx is a 3-digit namespace
		Constraints:    []string{"CHAPTERxxx stores start and title only (no end time, language, or flags)"},
		ChapterLoss:    core.ChapterLossStartTitleOnly,
	}
	// Comment padding round-tripped as-is; no padding control.

	caps := core.NewCapabilities(c.format, false, fields, pictures, chapters, core.AccessNone, nil).
		WithSyncedLyrics(vorbis.SyncedLyricsCapability()).
		WithFieldClassifier(vorbis.TransferClassifier)
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
