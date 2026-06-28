// Package wav implements reading and writing WAV (RIFF/WAVE) metadata for the
// public waxlabel package. The codec itself is internal. A WAV file is a RIFF
// container of chunks: a "fmt " chunk describing the audio, a "data" chunk
// holding the PCM essence, and any number of metadata and ancillary chunks
// (LIST/INFO tags, an embedded "id3 " ID3v2 tag, bext, iXML, cue, ...).
//
// WAV carries tags in two places, so the codec handles both:
//
//   - LIST/INFO - the RIFF-native tag block (a small fixed 4CC vocabulary, one
//     string each). It is what the ffmpeg family reads and writes, hence the
//     realistic acquired-file case, so it is a first-class read/write container.
//   - an embedded "id3 " chunk - a full ID3v2 tag (decoded by internal/id3),
//     the only place WAV can hold pictures and the MusicBrainz/Picard long tail.
//
// Precedence (read): the id3 chunk is authoritative when present (it is the
// richer container and the deliberate-tagger signal); otherwise LIST/INFO is.
// Both surface in the family view with conflicts flagged. Precedence (write):
// see write.go - by default both present containers are kept in sync, INFO is
// the home for a bare file, and pictures or any value INFO cannot represent
// force an id3 chunk; nothing is ever lost. All other chunks are preserved
// verbatim. RF64/BW64 (the >4 GiB extension) is out of scope and fails loudly.
//
// The codec is reimplemented from the RIFF/WAVE and ID3 specifications;
// reference implementations were consulted for design only.
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

// SkipsLeadingID3 reports false because WAV/RF64 files begin with a RIFF/RF64 header.
func (Codec) SkipsLeadingID3() bool { return false }

// Sniff matches a "RIFF....WAVE" header, plus the 64-bit RF64/BW64 variants, which also
// carry "WAVE" at offset 8. Detection is content-only, so matching them here is what
// routes such a file to Parse, where the out-of-scope 64-bit rejection can explain the
// limit instead of falling through to a generic "could not identify".
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

// Capabilities reports WAV's support. Tags and art are fully writable through
// the embedded id3 chunk; the RIFF-native LIST/INFO block is also written but is
// a lower-fidelity store (a fixed vocabulary of single-valued strings), so the
// generic-field capability notes both representations.
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
		Read: core.AccessNone, Write: core.AccessNone,
		Representation: "cue/adtl preserved",
	}
	// A WAV write may route genre through the id3 chunk when one is present or when an edit
	// forces one into existence. The capability is value-blind, so it conservatively reports
	// numeric GENRE as partial; edit warnings remain precise because they compare the written
	// result to the requested value. v2.3 original-date reductions follow the same shared
	// ID3 rules.
	perField := id3.PerFieldCapabilities(id3.WriteVersionFor(m, core.FormatWAV), opts.NumericGenre, true)
	// WAV has no metadata-padding concept, so the padding controls do not apply.
	return core.NewCapabilities(core.FormatWAV, false, fields, pictures, chapters, core.AccessNone, perField)
}

// ID3Tag returns the parsed id3-chunk tag, or nil when the file has none.
func (d *doc) ID3Tag() *id3.Tag { return d.id3 }

// EssenceExtent returns the WAV essence-digest inputs: a versioned extent name
// and the decoder-critical "fmt " configuration mixed in ahead of the audio -
// the sample format tag, channel count, sample rate, bit depth, and block
// alignment - so identical PCM bytes under a different channel layout or rate
// hash differently. The hashed extent itself is the data chunk's payload (set as
// the media's [AudioStart, AudioEnd) range).
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	var cfg [16]byte
	if d, ok := m.Native.(*doc); ok {
		binary.LittleEndian.PutUint16(cfg[0:2], d.fmtCfg.audioFormat)
		binary.LittleEndian.PutUint16(cfg[2:4], d.fmtCfg.channels)
		binary.LittleEndian.PutUint32(cfg[4:8], d.fmtCfg.sampleRate)
		binary.LittleEndian.PutUint16(cfg[8:10], d.fmtCfg.bitsPerSample)
		binary.LittleEndian.PutUint16(cfg[10:12], d.fmtCfg.blockAlign)
		// byteRate is largely redundant with the above for PCM but cheap to include
		// and distinguishes compressed RIFF payloads with the same surface geometry.
		// It is a uint32, so the full value is stored (not truncated to 16 bits).
		binary.LittleEndian.PutUint32(cfg[12:16], d.fmtCfg.byteRate)
	}
	return "wav-data-v1", cfg[:]
}
