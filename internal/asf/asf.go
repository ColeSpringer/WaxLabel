// Package asf implements reading WMA/ASF metadata for the public waxlabel package.
// The codec itself is internal.
//
// An ASF file opens with a Header Object holding every metadata object: File
// Properties (duration and preroll), Stream Properties (the WAVEFORMATEX describing
// the audio), Content Description (five fixed text fields), Extended Content
// Description (the open-ended "WM/*" descriptor list, where cover art also lives),
// a Header Extension nesting the Metadata and Metadata Library objects, and a Marker
// Object whose named markers are the chapters a WMA audiobook carries.
//
// It is read-only. Writing ASF is an explicit non-goal - a WMA file is only ever a
// source here - and the refusal lives on the native document so the capability a
// caller is shown and the outcome of an actual write come from one predicate.
//
// WMA is a family: v1, v2, Pro, Lossless, and Voice differ in the decoder they need,
// not in how the container stores tags. All of them are read; refusing a variant by
// name would be an encoder's concern, not a metadata reader's.
//
// It is reimplemented from the published ASF specification; reference
// implementations were consulted for design only.
package asf

import (
	"context"

	"github.com/colespringer/waxlabel/internal/core"
)

// codecName maps a WAVEFORMATEX format tag to a codec name, through the same table the
// RIFF reader uses: an ASF Stream Properties object and a WAV "fmt " chunk describe
// their audio with the identical structure, so one tag must not name two codecs.
//
// WMA is a family, not one codec, and its members differ only in the decoder they need;
// the tags are container-level either way, so all of them are read. Refusing a variant
// by name would be an encoder's concern, not a metadata reader's.
func codecName(tag uint16) string { return core.WaveFormatCodec(tag) }

// Codec implements core.Codec for WMA/ASF.
type Codec struct{}

// New returns a WMA codec.
func New() Codec { return Codec{} }

func init() { core.Register(New()) }

func (Codec) Format() core.Format { return core.FormatWMA }

// Extensions claims both spellings: ".wma" for an audio file and ".asf" for the
// generic container, which audio-only files also use.
func (Codec) Extensions() []string { return []string{".wma", ".asf"} }

// SkipsLeadingID3 reports false because an ASF file begins with the Header Object GUID.
func (Codec) SkipsLeadingID3() bool { return false }

// Sniff matches the 16-byte Header Object GUID at offset 0.
func (Codec) Sniff(header []byte) bool {
	return len(header) >= 16 && guid(header[0:16]) == guidHeader
}

// Parse reads metadata from src into a Media.
func (Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities reports WMA's support: full reads, no writes. ReadOnly and the reason it
// carries both come from the same refuseWrite the Plan path calls, so the three cannot
// disagree.
//
// Only ReadOnly is set. The field, picture, and chapter levels keep describing what
// the FORMAT holds, because core.dispose short-circuits on ReadOnly before consulting
// them and the editor's own gates key off them: dropping them to AccessNone would make
// the editor refuse a picture edit with a wrong sentinel before Plan could give the
// precise refusal.
func (Codec) Capabilities(_ *core.Media, _ core.WriteOptions) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessNone,
		Representation: "ASF descriptor", Fidelity: "read-only",
	}
	pictures := core.Capability{
		Read: core.AccessFull, Write: core.AccessNone,
		Representation: "WM/Picture descriptor", Fidelity: "read-only",
	}
	chapters := core.Capability{
		Read: core.AccessFull, Write: core.AccessNone,
		Representation: "Marker Object", Fidelity: "read-only",
	}
	// Keep the refusal itself, not just its existence: a caller that declines before
	// reaching Plan (the transfer path) then returns this exact error rather than
	// synthesizing one, so copy and set fail a WMA destination the same way.
	caps := core.NewCapabilities(core.FormatWMA, true, fields, pictures, chapters, core.AccessNone, nil)
	return caps.WithReadOnlyReason(refuseWrite())
}

// extentASF is the versioned essence-extent name. v2 salts the digest with the WAVEFORMATEX
// as stored; v1 packed the fields one by one, narrowed the byte rate to 16 bits, so two
// streams whose rates differed by a multiple of 65536 salted alike, and left the block
// align out. A v1 digest never compares equal to a v2 one.
const extentASF = "asf-packets-v2"

// EssenceExtent returns the ASF essence-digest inputs: the versioned extent name and the
// decoder-critical stream configuration, the first 16 bytes of the WAVEFORMATEX exactly
// as the file stores them (format tag, channels, sample rate, byte rate, block align,
// wBitsPerSample). The fixed field stands for the depth, not the WMA Lossless value the
// codec extra bytes correct it to: the salt describes the stored structure, and the extra
// bytes follow from the same encoder settings that shape the packets. A file with no audio
// stream salts with zeros.
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	var w [16]byte
	if d, ok := m.Native.(*doc); ok && d != nil {
		w = d.waveFormat
	}
	return extentASF, w[:]
}
