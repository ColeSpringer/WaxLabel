package ogg

import (
	"bytes"
	"context"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
)

// Codec implements core.Codec for an Ogg-encapsulated codec. Two instances are
// registered - one for Vorbis, one for Opus - sharing this implementation. They
// differ only in the format they claim and the detection signature; the parser
// identifies the actual codec from the stream, so editing or hashing a parsed
// document always routes back to the right instance via the recorded Format.
type Codec struct{ format core.Format }

// NewVorbis and NewOpus return the two Ogg codec instances.
func NewVorbis() Codec { return Codec{format: core.FormatOggVorbis} }
func NewOpus() Codec   { return Codec{format: core.FormatOggOpus} }

func init() {
	core.Register(NewVorbis())
	core.Register(NewOpus())
}

func (c Codec) Format() core.Format { return c.format }

func (c Codec) Extensions() []string {
	if c.format == core.FormatOggOpus {
		return []string{".opus"}
	}
	return []string{".ogg", ".oga"}
}

// Sniff matches an Ogg stream of this codec. Both start with the "OggS" capture
// pattern, so the codec is told apart by the identification header that begins
// the first page body ("\x01vorbis" or "OpusHead"). The detection window covers
// it: the id packet is small and alone on the first page, so its signature sits
// near the start of the file.
func (c Codec) Sniff(header []byte) bool {
	if !bytes.HasPrefix(header, oggMagic) {
		return false
	}
	if c.format == core.FormatOggOpus {
		return bytes.Contains(header, opusHead)
	}
	return bytes.Contains(header, vorbisID)
}

func (c Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities reports Ogg's support. Tags are Vorbis comments and art is
// METADATA_BLOCK_PICTURE, both losslessly writable. Chapters are not modeled.
func (c Codec) Capabilities(_ *core.Media, opts core.WriteOptions) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "Vorbis comment", Fidelity: "lossless",
	}
	pictures := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "METADATA_BLOCK_PICTURE", Fidelity: "lossless",
	}
	chapters := core.Capability{
		Read: core.AccessNone, Write: core.AccessNone, Representation: "unsupported",
	}
	return core.NewCapabilities(c.format, false, fields, pictures, chapters, nil)
}

// EssenceExtent returns the Ogg essence-digest inputs: a versioned extent name
// and the decoder-critical configuration mixed into the hash ahead of the audio
// packet payloads. For Opus that is the OpusHead packet (channel mapping,
// pre-skip, and the R128 output_gain); for Vorbis it is the identification
// header plus the setup header (the codebooks), since identical packets decoded
// with different codebooks are not the same audio.
func (c Codec) EssenceExtent(m *core.Media) (string, []byte) {
	d, ok := m.Native.(*doc)
	if !ok || d == nil {
		if c.format == core.FormatOggOpus {
			return "ogg-opus-packets-v1", nil
		}
		return "ogg-vorbis-packets-v1", nil
	}
	if d.kind == kindOpus {
		return "ogg-opus-packets-v1", slices.Clone(d.idPacket)
	}
	return "ogg-vorbis-packets-v1", slices.Concat(d.idPacket, d.setupPacket)
}
