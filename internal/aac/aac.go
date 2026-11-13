// Package aac implements reading and writing raw-AAC (ADTS) metadata for the
// public waxlabel package. The codec itself is internal. A raw-AAC file is an
// optional front ID3v2 tag (via internal/id3, same store as MP3) followed by ADTS
// frames, then any trailing APEv2, Lyrics3 and ID3v1 tags, which are legacy. The
// ID3v2 tag is the sole writable store; audio is copied verbatim.
//
// The first ADTS header gives stream config (object type, sample rate, channels).
// ADTS has no frame-count header, so duration and average bitrate come from a
// bounded walk of frame headers (see parse.go).
//
// Reimplemented from the MPEG-2/4 AAC ADTS and ID3 specs; reference
// implementations were consulted for design only.
package aac

import (
	"context"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
)

// Codec implements core.Codec for raw AAC (ADTS).
type Codec struct{}

// New returns an AAC codec.
func New() Codec { return Codec{} }

func init() { core.Register(New()) }

func (Codec) Format() core.Format { return core.FormatAAC }

// Extensions claims ".adts" alongside ".aac" so recursive walks do not skip ADTS files.
func (Codec) Extensions() []string { return []string{".aac", ".adts"} }

// SkipsLeadingID3 reports true: raw AAC often has a leading ID3v2 tag.
func (Codec) SkipsLeadingID3() bool { return true }

// Sniff matches a valid ADTS frame header at offset 0. Leading ID3 is not sniffed
// here (MP3 claims that header); [core.DetectLeading] sniffs past it, where this
// recognizer wins for ID3-prefixed .aac, and resyncs past a gap with [Codec.Resync].
func (Codec) Sniff(header []byte) bool {
	_, ok := decodeADTS(header)
	return ok
}

// Resync finds the first ADTS frame in window followed, at its frame length and inside window, by a
// second header agreeing on object type, rate index and channel config ([core.Resyncer]).
func (Codec) Resync(window []byte) (int, bool) {
	for i := 0; i+adtsHeaderSize <= len(window); i++ {
		h, ok := decodeADTS(window[i:])
		if !ok || i+h.frameLength+adtsHeaderSize > len(window) {
			continue
		}
		if n, ok := decodeADTS(window[i+h.frameLength:]); ok && sameConfig(h, n) {
			return i, true
		}
	}
	return 0, false
}

// Parse reads metadata from src into a Media.
func (c Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities reports AAC support. Tags and art live in the front ID3v2 tag
// (writable, version preserved). Trailing tags are legacy only.
func (Codec) Capabilities(m *core.Media, opts core.WriteOptions) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "ID3v2 frame", Fidelity: "lossless",
	}
	pictures := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "APIC frame", Fidelity: "lossless",
	}
	chapters := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation:  "ID3v2 CHAP/CTOC frames",
		Fidelity:        "start, end, and title stored; per-chapter language and hidden/disabled flags dropped",
		Constraints:     []string{"chapter start/end limited to a 32-bit millisecond field (~49.7 days)"},
		MaxItems:        255, // CTOC entry count is one byte
		ChapterLoss:     core.ChapterLossLangFlags,
		ChapterTimeUnit: time.Millisecond, // CHAP counts milliseconds
		ChapterTimeMax:  id3.ChapterTimeMax,
	}
	// Numeric genre and v2.3 original-date reductions follow shared ID3 rules.
	version := id3.WriteVersionFor(m, core.FormatAAC)
	perField := id3.PerFieldCapabilities(version, opts.NumericGenre, true)
	// Front-tag padding reuses the region (ReuseOrTarget), same as MP3.
	return core.NewCapabilities(core.FormatAAC, false, fields, pictures, chapters, core.AccessPartial, perField).
		WithSyncedLyrics(id3.SyncedLyricsCapability()).
		WithTextQualifiers(core.TextLossNone, id3.DefaultLanguage).
		WithRenderedFields(id3.TransferRendering(version, id3.WriteOptsOf(opts)))
}

// ID3Tag returns the parsed front ID3 tag, or nil when absent.
func (d *doc) ID3Tag() *id3.Tag { return d.id3 }

// EssenceExtent returns the AAC essence-digest inputs: versioned extent name and
// decoded static config (object type, sampling-frequency index, channel config).
//
// Hashes decoded fields, not raw first-header bytes: bytes 3-5 carry per-frame
// frame_length, so otherwise-identical streams would diverge. Same idea as AIFF
// hashing COMM rate bytes rather than the decoded float.
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	var cfg [3]byte
	if d, ok := m.Native.(*doc); ok && d != nil {
		cfg[0] = byte(d.header.objectType)
		cfg[1] = byte(d.header.sfIndex)
		cfg[2] = byte(d.header.chanConfig)
	}
	return "aac-adts-v2", cfg[:]
}
