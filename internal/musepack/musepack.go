// Package musepack implements reading and writing Musepack (.mpc) metadata for the
// public waxlabel package. The codec itself is internal.
//
// A Musepack file is a stream - SV7's fixed header and frames, or SV8's keyed packets
// - followed by an optional APEv2 tag and, after that, an optional legacy ID3v1 tag:
// the same trailing-store shape WavPack and Monkey's Audio use, shared through
// internal/ape. APEv2 is the native, authoritative tag store. Some SV7 encoders also
// wrote a leading ID3v2 tag; it is preserved verbatim and surfaced as legacy, never
// promoted into the canonical set.
//
// SV8 also carries chapters, as CT packets inside the stream. They are read where the
// reference decoder reads them and preserved through every rewrite, which copies the
// stream verbatim; they are not written, so the chapters capability is read-only.
//
// It is reimplemented from the published Musepack SV7 and SV8 stream documentation;
// reference implementations were consulted for design only.
package musepack

import (
	"context"
	"encoding/binary"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
)

// Codec implements core.Codec for Musepack.
type Codec struct{}

// New returns a Musepack codec.
func New() Codec { return Codec{} }

func init() { core.Register(New()) }

func (Codec) Format() core.Format { return core.FormatMusepack }

// Extensions claims ".mpc" and the historical ".mp+". ".mpp" is deliberately left
// out: it is more commonly a Microsoft Project file, and claiming it would put
// non-audio files into a --recursive walk's candidate set.
func (Codec) Extensions() []string { return []string{".mpc", ".mp+"} }

// SkipsLeadingID3 reports true because some SV7 encoders wrote a leading ID3v2 tag.
// core.DetectLeading routes such a file here by peeking past the tag to the inner
// "MP+" signature, exactly as it does for FLAC and raw AAC.
func (Codec) SkipsLeadingID3() bool { return true }

// Sniff matches either stream marker at offset 0: "MPCK" for SV8 or "MP+" for SV7.
// A file whose Musepack stream sits behind a leading ID3v2 tag has neither there, and
// is recognized through DetectLeading instead.
func (Codec) Sniff(header []byte) bool {
	if len(header) >= 4 && string(header[0:4]) == sv8Magic {
		return true
	}
	// SV7 needs the version byte too: "MP+" alone is three bytes and too weak a
	// signature to claim a file on.
	if len(header) >= 4 && string(header[0:3]) == sv7Magic {
		return header[3] == sv7Version || header[3] == sv7VersionAlt
	}
	return false
}

// Parse reads metadata from src into a Media.
func (Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities reports Musepack's support: APEv2's for fields and pictures, through the
// shared definition in internal/ape so the codecs backed by it cannot drift, plus the
// chapter store only this container has.
func (Codec) Capabilities(m *core.Media, _ core.WriteOptions) core.Capabilities {
	caps := ape.Capabilities(core.FormatMusepack, false)
	caps.Chapters = chapterCapability(m)
	return caps
}

// chapterCapability describes the SV8 chapter packets: read, and preserved by the
// verbatim stream copy, but never written. A file-less query answers for SV8, the
// version with the store; a parsed file answers from the predicate the parse read by,
// so an SV7 file, or one whose chapters cannot be placed, reports none.
func chapterCapability(m *core.Media) core.Capability {
	if m != nil {
		if d, ok := m.Native.(*doc); !ok || d == nil || !d.chapterStore() {
			return core.Capability{}
		}
	}
	return core.Capability{
		Read: core.AccessFull, Write: core.AccessNone,
		Representation: "SV8 chapter packets",
		Fidelity:       "read-only",
		Constraints: []string{
			"the chapter packets sit inside the stream, which a rewrite copies verbatim: chapters are read and preserved, and a chapter edit is refused",
			"SV7 streams have no chapter store",
		},
	}
}

// EssenceExtent returns the Musepack essence-digest inputs: a versioned extent name
// and the decoder-critical stream configuration mixed into the hash ahead of the
// audio.
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	var b [16]byte
	if d, ok := m.Native.(*doc); ok && d != nil {
		h := d.header
		b[0] = byte(h.streamVersion)
		b[1] = byte(h.channels)
		binary.BigEndian.PutUint32(b[2:6], uint32(h.sampleRate))
		// The whole 64-bit sample count, not its top half: no real file reaches 2^32
		// samples (about 27 hours at 44.1 kHz), so packing the high word alone would mix
		// a constant zero into every hash and leave the length out of the digest.
		binary.BigEndian.PutUint64(b[6:14], h.totalSamples)
	}
	return "musepack-v1", b[:]
}
