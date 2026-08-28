// Package apen implements reading and writing Monkey's Audio (.ape) metadata for
// the public waxlabel package. The codec itself is internal.
//
// A Monkey's Audio file is a "MAC " header followed by compressed frames, then an
// optional APEv2 tag and, after that, an optional legacy ID3v1 tag - the same
// trailing-store shape WavPack and Musepack use, shared through internal/ape. APEv2
// is the native, authoritative tag store; ID3v1 is preserved but never authoritative.
// The audio is copied verbatim on every write.
//
// The package is named for the container to keep it distinct from internal/ape, the
// APEv2 *tag* it happens to share a name and an extension with.
//
// It is reimplemented from the public Monkey's Audio header documentation;
// reference implementations were consulted for design only.
package apen

import (
	"context"
	"encoding/binary"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
)

// Codec implements core.Codec for Monkey's Audio.
type Codec struct{}

// New returns a Monkey's Audio codec.
func New() Codec { return Codec{} }

func init() { core.Register(New()) }

func (Codec) Format() core.Format  { return core.FormatMonkeysAudio }
func (Codec) Extensions() []string { return []string{".ape"} }

// SkipsLeadingID3 reports false because the file begins with the MAC marker. The
// legacy ID3 a Monkey's Audio file can carry is a trailing ID3v1, not a front tag.
func (Codec) SkipsLeadingID3() bool { return false }

// Sniff matches the "MAC " marker at offset 0. The trailing space is part of the
// marker, so this does not claim arbitrary files beginning "MAC".
func (Codec) Sniff(header []byte) bool {
	return len(header) >= 4 && string(header[:4]) == fileMagic
}

// Parse reads metadata from src into a Media.
func (Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities reports Monkey's Audio's support, which is entirely APEv2's: the
// shared definition in internal/ape, so the codecs backed by it cannot drift.
func (Codec) Capabilities(_ *core.Media, _ core.WriteOptions) core.Capabilities {
	return ape.Capabilities(core.FormatMonkeysAudio, false)
}

// EssenceExtent returns the Monkey's Audio essence-digest inputs: a versioned extent
// name and the decoder-critical configuration - the stream version, compression
// level, format flags, and audio geometry - mixed into the hash ahead of the frames.
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	var b [18]byte
	if d, ok := m.Native.(*doc); ok && d != nil {
		h := d.header
		binary.BigEndian.PutUint16(b[0:2], h.version)
		binary.BigEndian.PutUint16(b[2:4], h.compressionLevel)
		binary.BigEndian.PutUint16(b[4:6], h.formatFlags)
		binary.BigEndian.PutUint16(b[6:8], h.bitsPerSample)
		binary.BigEndian.PutUint16(b[8:10], h.channels)
		binary.BigEndian.PutUint32(b[10:14], h.sampleRate)
		// The whole frame size, not its high half: every documented value (9216, 73728,
		// 294912) fits in 32 bits, so shifting would collapse them all to zero.
		binary.BigEndian.PutUint32(b[14:18], h.blocksPerFrame)
	}
	return "monkeys-audio-v1", b[:]
}
