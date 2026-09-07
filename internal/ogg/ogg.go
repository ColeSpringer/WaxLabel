package ogg

import (
	"bytes"
	"context"
	"encoding/binary"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/vorbis"
)

// Codec implements core.Codec for an Ogg-encapsulated codec. Three instances are
// registered - one each for Vorbis, Opus, and FLAC - sharing this implementation.
// They differ only in the format they claim and the detection signature; the parser
// identifies the actual codec from the stream, so editing or hashing a parsed
// document always routes back to the right instance via the recorded Format.
type Codec struct{ format core.Format }

// NewVorbis, NewOpus, and NewFLAC return the three Ogg codec instances.
func NewVorbis() Codec { return Codec{format: core.FormatOggVorbis} }
func NewOpus() Codec   { return Codec{format: core.FormatOggOpus} }
func NewFLAC() Codec   { return Codec{format: core.FormatOggFLAC} }

func init() {
	core.Register(NewVorbis())
	core.Register(NewOpus())
	core.Register(NewFLAC())
}

func (c Codec) Format() core.Format { return c.format }

// SkipsLeadingID3 reports false because Ogg streams begin with an OggS page.
func (Codec) SkipsLeadingID3() bool { return false }

// Extensions claims both .ogg and .oga for Vorbis and FLAC alike. RFC 5334 narrows
// .ogg to Vorbis and introduces .oga for other Ogg audio, but the reference flac tool
// wrote Ogg FLAC as .ogg for years and such files are still common - so claiming only
// .oga would make warnExtensionMismatch tell the user a legitimate .ogg write was a
// transcode. The extensions are genuinely shared; the resulting --format ambiguity is
// resolved by name, not by narrowing the claim.
func (c Codec) Extensions() []string {
	if c.format == core.FormatOggOpus {
		return []string{".opus"}
	}
	return []string{".ogg", ".oga"}
}

// Sniff matches an Ogg stream of this codec. All three start with the "OggS"
// capture pattern, so the codec is told apart by the identification header that
// begins the first page body ("\x01vorbis", "OpusHead", or "\x7FFLAC"). The
// detection window covers it: the id packet is small and alone on the first page,
// so its signature sits near the start of the file.
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

// Capabilities reports Ogg's support. Tags are Vorbis comments, losslessly
// writable. Art is METADATA_BLOCK_PICTURE for Vorbis and Opus, and a native FLAC
// PICTURE block for the FLAC mapping - the one place the three diverge. Chapters
// use the CHAPTERxxx comment convention, which stores start and title.
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
	// OggTags/OpusTags padding is round-tripped as-is; there is no padding control,
	// so AccessNone.
	caps := core.NewCapabilities(c.format, false, fields, pictures, chapters, core.AccessNone, nil).
		WithSyncedLyrics(vorbis.SyncedLyricsCapability()).
		WithFieldClassifier(vorbis.TransferClassifier)
	if c.format == core.FormatOggOpus {
		caps = caps.WithOutputGain(core.AccessFull)
	}
	return caps
}

// opusOutputGain returns the OpusHead's output gain (signed Q7.8 dB, little-endian at
// bytes 16:18), or 0 when the header is too short to hold one.
func opusOutputGain(head []byte) int {
	if len(head) < 18 {
		return 0
	}
	return int(int16(binary.LittleEndian.Uint16(head[16:18])))
}

// opusHeadWithGain returns a copy of the OpusHead with its output gain replaced, or an
// unchanged copy when the header is too short to hold one.
func opusHeadWithGain(head []byte, gain int) []byte {
	out := slices.Clone(head)
	if len(out) >= 18 {
		binary.LittleEndian.PutUint16(out[16:18], uint16(int16(gain)))
	}
	return out
}

// EssenceExtent returns the Ogg essence-digest inputs: a versioned extent name
// and the decoder-critical configuration mixed into the hash ahead of the audio
// packet payloads. For Opus that is the OpusHead packet with its output_gain masked
// (the gain is an editable playback control, not part of the encoded audio, so two
// copies differing only in gain dedup); for Vorbis it is the identification header plus
// the setup header (the codebooks), since identical packets decoded with different
// codebooks are not the same audio; for FLAC it is STREAMINFO.
func (c Codec) EssenceExtent(m *core.Media) (string, []byte) {
	d, ok := m.Native.(*doc)
	if !ok || d == nil {
		// No parsed document to read the mapping from: fall back to the format the codec
		// instance was registered for, which names the same extent.
		return extentForFormat(c.format), nil
	}
	switch d.kind {
	case kindOpus:
		return extentOpus, opusHeadWithGain(d.idPacket, 0)
	case kindFLAC:
		// STREAMINFO alone, not the whole identification packet: the packet also
		// carries the header-packet count, which a metadata rewrite legitimately
		// changes and which says nothing about the audio.
		return extentFLAC, slices.Clone(d.streamInfo())
	}
	return extentVorbis, slices.Concat(d.idPacket, d.setupPacket)
}

// The versioned essence-extent names, one per Ogg mapping. Named once so the two ways of
// reaching an extent - by codec format and by parsed stream kind - cannot disagree, and a
// version bump is a single edit.
const (
	extentVorbis = "ogg-vorbis-packets-v1"
	// v2 masks the OpusHead output gain, an editable playback control that says nothing
	// about the encoded audio. A v1 digest never compares equal to a v2 one.
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
