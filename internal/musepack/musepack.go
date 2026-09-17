// Package musepack implements Musepack (.mpc) metadata for the public waxlabel
// package. The codec is internal.
//
// Layout: SV7 fixed header/frames or SV8 keyed packets, optional APEv2, optional
// trailing ID3v1 (same trailing store as WavPack/Monkey's Audio via internal/ape).
// APEv2 is authoritative. Some SV7 files also have a leading ID3v2: preserved as
// legacy, never canonical.
//
// SV8 CT chapter packets are read where the reference decoder looks and preserved
// by the verbatim stream copy; not written (chapters are read-only).
//
// Reimplemented from published SV7/SV8 docs; reference code informed design only.
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

// Extensions: ".mpc" and historical ".mp+". Not ".mpp" (often MS Project).
func (Codec) Extensions() []string { return []string{".mpc", ".mp+"} }

// SkipsLeadingID3 is true: some SV7 encoders wrote a front ID3v2.
// core.DetectLeading peeks past it for "MP+" (same as FLAC/raw AAC).
func (Codec) SkipsLeadingID3() bool { return true }

// Sniff matches "MPCK" (SV8) or "MP+"+version (SV7) at offset 0.
// Leading-ID3v2 files are recognized via DetectLeading instead.
func (Codec) Sniff(header []byte) bool {
	if len(header) >= 4 && string(header[0:4]) == sv8Magic {
		return true
	}
	// SV7 needs the version byte; "MP+" alone is too weak.
	if len(header) >= 4 && string(header[0:3]) == sv7Magic {
		return header[3] == sv7Version || header[3] == sv7VersionAlt
	}
	return false
}

// Parse reads metadata from src into a Media.
func (Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities: shared APEv2 fields/pictures, plus this container's chapter store.
func (Codec) Capabilities(m *core.Media, _ core.WriteOptions) core.Capabilities {
	caps := ape.Capabilities(core.FormatMusepack, false)
	caps.Chapters = chapterCapability(m)
	return caps
}

// chapterCapability: SV8 CT packets are read and preserved, never written.
// Nil m assumes SV8; a parsed file uses [doc.chapterStore].
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

// EssenceExtent returns musepack-v1 and decoder-critical stream config.
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	var b [16]byte
	if d, ok := m.Native.(*doc); ok && d != nil {
		h := d.header
		b[0] = byte(h.streamVersion)
		b[1] = byte(h.channels)
		binary.BigEndian.PutUint32(b[2:6], uint32(h.sampleRate))
		// Full 64-bit sample count: high half alone is zero for any real file.
		binary.BigEndian.PutUint64(b[6:14], h.totalSamples)
	}
	return "musepack-v1", b[:]
}
