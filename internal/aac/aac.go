// Package aac implements reading and writing raw-AAC (ADTS) metadata for the
// public waxlabel package. The codec itself is internal. A raw-AAC file is an
// optional front ID3v2 tag (via internal/id3, same store as MP3) followed by ADTS
// frames. The ID3v2 tag is the sole writable store; audio is copied verbatim.
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
// here (MP3 claims that header); the root parser peeks past ID3 via
// detectPastLeadingID3, where this recognizer wins for ID3-prefixed .aac.
func (Codec) Sniff(header []byte) bool {
	_, ok := decodeADTS(header)
	return ok
}

// Parse reads metadata from src into a Media.
func (c Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities reports AAC support. Tags and art live in the front ID3v2 tag
// (writable, version preserved). No secondary tag container.
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
		Representation: "ID3v2 CHAP/CTOC frames",
		Fidelity:       "start, end, and title stored; per-chapter language and hidden/disabled flags dropped",
		Constraints:    []string{"chapter start/end limited to a 32-bit millisecond field (~49.7 days)"},
		MaxItems:       255, // CTOC entry count is one byte
		ChapterLoss:    core.ChapterLossLangFlags,
	}
	// Numeric genre and v2.3 original-date reductions follow shared ID3 rules.
	perField := id3.PerFieldCapabilities(id3.WriteVersionFor(m, core.FormatAAC), opts.NumericGenre, true)
	// Front-tag padding is grow-only (ReuseOrTarget), same as MP3.
	return core.NewCapabilities(core.FormatAAC, false, fields, pictures, chapters, core.AccessPartial, perField).
		WithSyncedLyrics(id3.SyncedLyricsCapability()).
		WithFieldClassifier(id3.TransferClassifier)
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
	return "aac-adts-v1", cfg[:]
}
