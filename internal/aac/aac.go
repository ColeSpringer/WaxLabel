// Package aac implements reading and writing raw-AAC (ADTS) metadata for the
// public waxlabel package. The codec itself is internal. A raw-AAC file is an
// optional front ID3v2 tag (decoded by internal/id3, the same authoritative
// container MP3 uses) followed by a bare sequence of ADTS frames, with no MPEG
// framing layer and no trailing legacy containers. The ID3v2 tag is the sole
// writable store; the audio is copied verbatim.
//
// The first ADTS frame header gives the stream configuration (object type, sample
// rate, channels). ADTS carries no frame-count header, so an accurate duration and
// average bitrate come from a bounded walk of the frame headers - advancing by each
// frame_length to sum the sample count, reading only headers and never the essence
// payloads (see parse.go).
//
// The codec is reimplemented from the MPEG-2/4 AAC ADTS and ID3 specifications;
// reference implementations were consulted for design only.
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

func (Codec) Format() core.Format  { return core.FormatAAC }
func (Codec) Extensions() []string { return []string{".aac"} }

// Sniff matches a raw ADTS stream by a valid ADTS frame header at the start. A
// front ID3v2 tag is intentionally not sniffed here: that header is claimed by
// MP3, and the root parser disambiguates a leading ID3 by peeking past the tag
// (detectPastLeadingID3), where this codec's ADTS recognizer then wins for an
// ID3-prefixed.aac. Sniffing ID3 here too would just create a redundant tie.
func (Codec) Sniff(header []byte) bool {
	_, ok := decodeADTS(header)
	return ok
}

// Parse reads metadata from src into a Media.
func (c Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities reports AAC's support. Tags and art live in the front ID3v2 tag
// and are fully writable, identical to MP3's ID3-backed story; the version is
// preserved on edit. AAC has no secondary tag container, so there are no legacy
// conflicts to surface.
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
		Read: core.AccessNone, Write: core.AccessNone,
	}
	// AAC's front ID3 tag is the only tag store, so numeric genre and v2.3 original-date
	// reductions follow the shared ID3 capability rules.
	perField := id3.PerFieldCapabilities(id3.WriteVersionFor(m, core.FormatAAC), opts.NumericGenre, true)
	// ID3 front-tag padding is grow-only (ReuseOrTarget), identical to MP3: a forced
	// rewrite can grow the region, but a fit-in-place edit cannot shrink it.
	return core.NewCapabilities(core.FormatAAC, false, fields, pictures, chapters, core.AccessPartial, perField)
}

// ID3Tag returns the parsed front ID3 tag, or nil when the file has none.
func (d *doc) ID3Tag() *id3.Tag { return d.id3 }

// EssenceExtent returns the AAC essence-digest inputs: a versioned extent name
// and the decoded static stream configuration - object type, sampling-frequency
// index, and channel configuration - mixed into the hash ahead of the audio.
//
// It hashes the decoded static fields, not the raw first-header bytes, on
// purpose: bytes 3-5 of an ADTS header carry the per-frame frame_length, so two
// otherwise-identical streams whose first frame happens to differ in length
// would hash differently if the raw header were used. The static config bits are
// exact and need no decode (the same principle as AIFF hashing the COMM rate
// bytes rather than the decoded float).
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	var cfg [3]byte
	if d, ok := m.Native.(*doc); ok && d != nil {
		cfg[0] = byte(d.header.objectType)
		cfg[1] = byte(d.header.sfIndex)
		cfg[2] = byte(d.header.chanConfig)
	}
	return "aac-adts-v1", cfg[:]
}
