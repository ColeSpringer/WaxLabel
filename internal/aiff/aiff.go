// Package aiff implements reading and writing AIFF / AIFF-C metadata for the
// public waxlabel package. The codec itself is internal. An AIFF file is an IFF
// FORM ("AIFF" or "AIFC") with big-endian chunks: COMM (geometry; 80-bit sample
// rate), SSND (sample frames), plus metadata and ancillary chunks.
//
// Tags live in two places (like WAV; big-endian sizes and a different vocabulary):
//
//   - native text: NAME (title), AUTH (artist), "(c) " (copyright), ANNO
//     (comment, repeatable). Fixed vocabulary; what ffmpeg's AIFF muxer writes.
//   - embedded "ID3 " chunk: full ID3v2 (via internal/id3); only place for
//     pictures and the long tail. Also reads lowercase "id3 "; writer emits "ID3 ".
//
// Read precedence: ID3 wins when present; else native text. Both in the family
// view with conflicts flagged. Write: see write.go. Other chunks stay verbatim.
// Output >4 GiB returns an error.
//
// Reimplemented from the AIFF / AIFF-C and ID3 specs; reference implementations
// were consulted for design only.
package aiff

import (
	"context"
	"encoding/binary"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
)

// Codec implements core.Codec for AIFF / AIFF-C.
type Codec struct{}

// New returns an AIFF codec.
func New() Codec { return Codec{} }

func init() { core.Register(New()) }

func (Codec) Format() core.Format  { return core.FormatAIFF }
func (Codec) Extensions() []string { return []string{".aiff", ".aif", ".aifc", ".afc"} }

// SkipsLeadingID3 is false: AIFF/AIFC begins with FORM.
func (Codec) SkipsLeadingID3() bool { return false }

// Sniff matches FORM....AIFF or FORM....AIFC (distinct from RIFF/WAVE).
func (Codec) Sniff(header []byte) bool {
	return len(header) >= 12 &&
		string(header[0:4]) == "FORM" &&
		(string(header[8:12]) == "AIFF" || string(header[8:12]) == "AIFC")
}

// Parse reads metadata from src into a Media.
func (c Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities: tags/art via ID3 chunk (full); native text is lower-fidelity
// (fixed vocabulary, mostly single-valued).
func (Codec) Capabilities(m *core.Media, opts core.WriteOptions) core.Capabilities {
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
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "ID3v2 CHAP/CTOC frames (ID3 chunk)",
		Fidelity:       "start, end, and title stored; per-chapter language and hidden/disabled flags dropped",
		Constraints: []string{
			"chapters require an ID3 chunk; AIFF has no native chapter representation",
			"chapter start/end limited to a 32-bit millisecond field (~49.7 days)",
		},
		MaxItems:    255, // CTOC entry count is one byte
		ChapterLoss: core.ChapterLossLangFlags,
	}
	// No native genre; ID3 carries it. Shared numeric-genre / v2.3 date rules.
	perField := id3.PerFieldCapabilities(id3.WriteVersionFor(m, core.FormatAIFF), opts.NumericGenre, true)
	// No metadata padding. Synced lyrics need ID3 (edit may create the chunk).
	return core.NewCapabilities(core.FormatAIFF, false, fields, pictures, chapters, core.AccessNone, perField).
		WithSyncedLyrics(id3.SyncedLyricsCapability()).
		WithFieldClassifier(id3.TransferClassifier)
}

// ID3Tag returns the parsed ID3-chunk tag, or nil when absent.
func (d *doc) ID3Tag() *id3.Tag { return d.id3 }

// EssenceExtent: versioned name plus COMM config (channels, sample size, raw
// 80-bit rate, AIFF-C compression type) mixed ahead of SSND sample frames.
// Rate hashed as raw bytes. Extent is SSND frames excluding the 8-byte
// offset/blockSize header and declared "offset" alignment bytes.
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
	// v2 excludes SSND "offset" alignment (v1 hashed as audio).
	return "aiff-ssnd-v2", cfg
}
