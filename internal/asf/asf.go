// Package asf implements reading WMA/ASF metadata for the public waxlabel package.
// The codec itself is internal.
//
// An ASF file opens with a Header Object holding every metadata object: File
// Properties (duration and preroll), Stream Properties (the WAVEFORMATEX describing
// the audio), Content Description (five fixed text fields), Extended Content
// Description (the open-ended "WM/*" descriptor list, where cover art also lives),
// and a Header Extension nesting the Metadata and Metadata Library objects.
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
	"encoding/binary"

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

// Capabilities reports WMA's support: full reads, no writes. ReadOnly is derived
// from the same refuseWrite the Plan path calls, so the two cannot disagree.
//
// Only ReadOnly is set. The field, picture, and chapter levels keep describing what
// the FORMAT holds, because core.dispose short-circuits on ReadOnly before consulting
// them and the editor's own gates key off them: dropping them to AccessNone would make
// the editor refuse a picture edit with a wrong sentinel before Plan could give the
// precise refusal.
func (Codec) Capabilities(m *core.Media, _ core.WriteOptions) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessNone,
		Representation: "ASF descriptor", Fidelity: "read-only",
	}
	pictures := core.Capability{
		Read: core.AccessFull, Write: core.AccessNone,
		Representation: "WM/Picture descriptor", Fidelity: "read-only",
	}
	readOnly := true
	if m != nil {
		if d, ok := m.Native.(*doc); ok && d != nil {
			readOnly = d.refuseWrite() != nil
		}
	}
	return core.NewCapabilities(core.FormatWMA, readOnly, fields, pictures, core.Capability{}, core.AccessNone, nil)
}

// EssenceExtent returns the ASF essence-digest inputs: a versioned extent name and
// the decoder-critical stream configuration from the WAVEFORMATEX.
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	var b [12]byte
	if d, ok := m.Native.(*doc); ok && d != nil {
		binary.BigEndian.PutUint16(b[0:2], d.formatTag)
		binary.BigEndian.PutUint16(b[2:4], uint16(d.channels))
		binary.BigEndian.PutUint32(b[4:8], uint32(d.sampleRate))
		binary.BigEndian.PutUint16(b[8:10], uint16(d.bitsPerSample))
		binary.BigEndian.PutUint16(b[10:12], uint16(d.byteRate))
	}
	return "asf-packets-v1", b[:]
}
