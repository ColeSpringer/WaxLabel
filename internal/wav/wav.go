// Package wav implements reading and writing WAV (RIFF/WAVE) metadata for the
// public waxlabel package. The codec itself is internal. A WAV file is RIFF
// chunks: "fmt " (audio), "data" (PCM), plus metadata (LIST/INFO, embedded
// "id3 ", bext, iXML, cue, ...).
//
// Tags live in two places:
//
//   - LIST/INFO: RIFF-native fixed 4CC vocabulary (what ffmpeg reads/writes).
//   - embedded "id3 " chunk: full ID3v2 (via internal/id3); only place for
//     pictures and the long tail.
//
// Read precedence: id3 wins when present; else LIST/INFO. Both in the family
// view with conflicts flagged. Write: see write.go. Other chunks stay verbatim.
//
// RF64/BW64 (EBU Tech 3306): sizes that do not fit 32-bit read as 0xFFFFFFFF;
// real values come from leading "ds64", regenerated on write. Form is preserved
// (RF64 is never rewritten as plain RIFF).
//
// Reimplemented from the RIFF/WAVE and ID3 specs; reference implementations
// were consulted for design only.
package wav

import (
	"context"
	"encoding/binary"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
)

// Codec implements core.Codec for WAV.
type Codec struct{}

// New returns a WAV codec.
func New() Codec { return Codec{} }

func init() { core.Register(New()) }

func (Codec) Format() core.Format  { return core.FormatWAV }
func (Codec) Extensions() []string { return []string{".wav", ".wave"} }

// SkipsLeadingID3 is false: WAV/RF64 begins with RIFF/RF64.
func (Codec) SkipsLeadingID3() bool { return false }

// Sniff matches RIFF/RF64/BW64 with WAVE at offset 8.
func (Codec) Sniff(header []byte) bool {
	if len(header) < 12 || string(header[8:12]) != "WAVE" {
		return false
	}
	switch string(header[0:4]) {
	case "RIFF", "RF64", "BW64":
		return true
	}
	return false
}

// Parse reads metadata from src into a Media.
func (c Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities: tags/art via id3 chunk (full); LIST/INFO is lower-fidelity
// (fixed vocabulary, single-valued).
func (Codec) Capabilities(m *core.Media, opts core.WriteOptions) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "ID3v2 (id3 chunk) + RIFF LIST/INFO", Fidelity: "lossless via id3; INFO is single-valued, fixed-vocabulary",
		Constraints: []string{"LIST/INFO cannot store multi-value or unmapped keys; those use the id3 chunk"},
	}
	pictures := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "APIC (id3 chunk)", Fidelity: "lossless",
		Constraints: []string{"LIST/INFO cannot hold pictures; an id3 chunk is required"},
	}
	chapters := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "ID3v2 CHAP/CTOC frames (id3 chunk)",
		Fidelity:       "start, end, and title stored; per-chapter language and hidden/disabled flags dropped",
		Constraints: []string{
			"chapters require an id3 chunk; native cue/adtl chapters are preserved opaque but not read",
			"chapter start/end limited to a 32-bit millisecond field (~49.7 days)",
		},
		MaxItems:    255, // CTOC entry count is one byte
		ChapterLoss: core.ChapterLossLangFlags,
	}
	// Genre may route through id3; capability is value-blind so numeric GENRE is
	// partial. Shared v2.3 original-date rules.
	perField := id3.PerFieldCapabilities(id3.WriteVersionFor(m, core.FormatWAV), opts.NumericGenre, true)
	// No metadata padding. Synced lyrics need id3 (edit may create the chunk).
	return core.NewCapabilities(core.FormatWAV, false, fields, pictures, chapters, core.AccessNone, perField).
		WithSyncedLyrics(id3.SyncedLyricsCapability()).
		WithFieldClassifier(id3.TransferClassifier)
}

// ID3Tag returns the parsed id3-chunk tag, or nil when absent.
func (d *doc) ID3Tag() *id3.Tag { return d.id3 }

// EssenceExtent: versioned name plus fmt config (format tag, channels, rate,
// bit depth, block align, byte rate) mixed ahead of the data payload.
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	var cfg [16]byte
	if d, ok := m.Native.(*doc); ok {
		binary.LittleEndian.PutUint16(cfg[0:2], d.fmtCfg.audioFormat)
		binary.LittleEndian.PutUint16(cfg[2:4], d.fmtCfg.channels)
		binary.LittleEndian.PutUint32(cfg[4:8], d.fmtCfg.sampleRate)
		binary.LittleEndian.PutUint16(cfg[8:10], d.fmtCfg.bitsPerSample)
		binary.LittleEndian.PutUint16(cfg[10:12], d.fmtCfg.blockAlign)
		// byteRate distinguishes compressed RIFF with the same surface geometry.
		binary.LittleEndian.PutUint32(cfg[12:16], d.fmtCfg.byteRate)
	}
	return "wav-data-v1", cfg[:]
}
