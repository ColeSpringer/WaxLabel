// Package asf implements reading WMA/ASF metadata for the public waxlabel package.
// The codec itself is internal.
//
// An ASF file opens with a Header Object holding metadata: File Properties
// (duration, preroll), Stream Properties (WAVEFORMATEX), Content Description
// (five fixed text fields), Extended Content Description ("WM/*" descriptors,
// including cover art), Header Extension (Metadata / Metadata Library), and
// Marker Object (chapters).
//
// Read-only: writing ASF is a non-goal. Refusal lives on the native document so
// Capabilities and Plan share one predicate.
//
// WMA variants (v1, v2, Pro, Lossless, Voice) differ in decoder, not tag storage;
// all are read.
//
// Reimplemented from the ASF specification; reference implementations were
// consulted for design only.
package asf

import (
	"context"

	"github.com/colespringer/waxlabel/internal/core"
)

// codecName maps a WAVEFORMATEX format tag via the same table as the RIFF reader.
func codecName(tag uint16) string { return core.WaveFormatCodec(tag) }

// Codec implements core.Codec for WMA/ASF.
type Codec struct{}

// New returns a WMA codec.
func New() Codec { return Codec{} }

func init() { core.Register(New()) }

func (Codec) Format() core.Format { return core.FormatWMA }

// Extensions: ".wma" and ".asf" (generic container also used for audio-only).
func (Codec) Extensions() []string { return []string{".wma", ".asf"} }

// SkipsLeadingID3 is false: ASF starts with the Header Object GUID.
func (Codec) SkipsLeadingID3() bool { return false }

// Sniff matches the 16-byte Header Object GUID at offset 0.
func (Codec) Sniff(header []byte) bool {
	return len(header) >= 16 && guid(header[0:16]) == guidHeader
}

// Parse reads metadata from src into a Media.
func (Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities: full reads, no writes. ReadOnly and its reason come from
// refuseWrite (same as Plan). Field/picture/chapter levels still describe the
// format; core.dispose short-circuits on ReadOnly, and AccessNone would make the
// editor refuse with the wrong sentinel before Plan.
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
	// Carry the refusal error for callers that decline before Plan (transfer path).
	caps := core.NewCapabilities(core.FormatWMA, true, fields, pictures, chapters, core.AccessNone, nil)
	return caps.WithReadOnlyReason(refuseWrite())
}

// extentASF is the essence-extent name. v2 salts with WAVEFORMATEX as stored; v1
// packed fields, narrowed byte rate to 16 bits, and omitted block align.
const extentASF = "asf-packets-v2"

// EssenceExtent returns extent name and the first 16 WAVEFORMATEX bytes as stored
// (salt). Uses the fixed depth field, not WMA Lossless codec-extra corrections.
// No audio stream salts with zeros.
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	var w [16]byte
	if d, ok := m.Native.(*doc); ok && d != nil {
		w = d.waveFormat
	}
	return extentASF, w[:]
}
