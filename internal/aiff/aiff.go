// Package aiff implements reading and writing AIFF / AIFF-C metadata. It is
// internal through v0.x (promoted to a public waxlabel/aiff only at v1.0). An
// AIFF file is an IFF container: a "FORM" header naming the form type ("AIFF" or
// the compressed "AIFC"), then big-endian-sized chunks — a "COMM" common chunk
// describing the audio (with the sample rate stored as an 80-bit extended
// float), an "SSND" chunk holding the sample frames, and any number of metadata
// and ancillary chunks.
//
// AIFF carries tags in two places, so the codec handles both — exactly as WAV
// does, the difference being big-endian sizes and a different chunk vocabulary:
//
//   - native text chunks — NAME (title), AUTH (artist), "(c) " (copyright), and
//     ANNO (comment/annotation, repeatable). A small fixed vocabulary of plain
//     character runs, one canonical key each. This is what ffmpeg's AIFF muxer
//     writes by default (NAME + ANNO) and reads back, hence the realistic
//     acquired-file case and the differential anchor.
//   - an embedded "ID3 " chunk — a full ID3v2 tag (decoded by internal/id3), the
//     only place AIFF can hold pictures and the MusicBrainz/Picard long tail. The
//     de-facto identifier is the uppercase "ID3 "; a lowercase "id3 " variant
//     some tools emit is also read. The writer emits "ID3 ".
//
// Precedence (read): the ID3 chunk is authoritative when present (it is the
// richer container and the deliberate-tagger signal); otherwise the native text
// chunks are. Both surface in the family view with conflicts flagged. Precedence
// (write): see write.go — by default both present containers are kept in sync,
// the native chunks are the home for a bare file, and pictures or any value the
// native vocabulary cannot represent force an "ID3 " chunk; nothing is ever lost.
// All other chunks are preserved verbatim. A >4 GiB output fails loudly.
//
// The codec is reimplemented from the AIFF / AIFF-C and ID3 specifications;
// reference implementations were consulted for design only.
package aiff

import (
	"context"
	"encoding/binary"

	"github.com/colespringer/waxlabel/internal/core"
)

// Codec implements core.Codec for AIFF / AIFF-C.
type Codec struct{}

// New returns an AIFF codec.
func New() Codec { return Codec{} }

func init() { core.Register(New()) }

func (Codec) Format() core.Format  { return core.FormatAIFF }
func (Codec) Extensions() []string { return []string{".aiff", ".aif", ".aifc", ".afc"} }

// Sniff matches a "FORM....AIFF" or "FORM....AIFC" header. The form type
// disambiguates AIFF from the other IFF/FORM families (which carry different
// type identifiers), so there is no collision with WAV's "RIFF/WAVE".
func (Codec) Sniff(header []byte) bool {
	return len(header) >= 12 &&
		string(header[0:4]) == "FORM" &&
		(string(header[8:12]) == "AIFF" || string(header[8:12]) == "AIFC")
}

// Parse reads metadata from src into a Media.
func (c Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities reports AIFF's support. Tags and art are fully writable through
// the embedded "ID3 " chunk; the native text chunks are also written but are a
// lower-fidelity store (a fixed vocabulary of single-valued character runs, save
// ANNO/Comment), so the generic-field capability notes both representations.
func (Codec) Capabilities(opts core.WriteOptions) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "ID3v2 (ID3 chunk) + native NAME/AUTH/(c)/ANNO",
		Fidelity:       "lossless via ID3; native chunks are single-valued, fixed-vocabulary",
		Constraints:    []string{"native text chunks cannot store multi-value (except ANNO) or unmapped keys; those use the ID3 chunk"},
	}
	pictures := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "APIC (ID3 chunk)", Fidelity: "lossless",
		Constraints: []string{"native AIFF chunks cannot hold pictures; an ID3 chunk is required"},
	}
	chapters := core.Capability{
		Read: core.AccessNone, Write: core.AccessNone,
		Representation: "not modeled",
	}
	return core.NewCapabilities(core.FormatAIFF, false, fields, pictures, chapters, nil)
}

// EssenceExtent returns the AIFF essence-digest inputs: a versioned extent name
// and the decoder-critical "COMM" configuration mixed in ahead of the audio —
// the channel count, sample size, the raw 80-bit sample rate, and (for AIFF-C)
// the compression type — so identical sample frames under a different channel
// layout, rate, or codec hash differently. The 80-bit rate is hashed as its raw
// bytes (not the decoded float) so the digest is exact. The hashed extent is the
// SSND sample-frame region (set as the media's [AudioStart, AudioEnd) range,
// which excludes SSND's 8-byte offset/blockSize sub-header).
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	var cfg []byte
	if d, ok := m.Native.(*doc); ok && d != nil {
		var n [4]byte
		binary.BigEndian.PutUint16(n[:2], d.comm.channels)
		cfg = append(cfg, n[:2]...)
		binary.BigEndian.PutUint16(n[:2], d.comm.sampleSize)
		cfg = append(cfg, n[:2]...)
		cfg = append(cfg, d.comm.rateBytes[:]...)
		cfg = append(cfg, d.comm.compType[:]...)
	}
	return "aiff-ssnd-v1", cfg
}
