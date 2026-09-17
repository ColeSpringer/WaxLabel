// Package apen implements Monkey's Audio (.ape) metadata for the public waxlabel
// package. The codec is internal.
//
// Layout: "MAC " header, compressed frames, optional APEv2, optional trailing ID3v1.
// Same trailing-store shape as WavPack/Musepack via internal/ape. APEv2 is authoritative;
// ID3v1 is preserved only. Audio is copied verbatim on write.
//
// Named for the container so it is distinct from internal/ape (the APEv2 tag).
// Reimplemented from the public Monkey's Audio header docs; reference code informed
// design only.
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

// SkipsLeadingID3 is false: the file starts with MAC. Legacy ID3 here is trailing ID3v1.
func (Codec) SkipsLeadingID3() bool { return false }

// Sniff matches "MAC " at offset 0 (trailing space is part of the marker).
func (Codec) Sniff(header []byte) bool {
	return len(header) >= 4 && string(header[:4]) == fileMagic
}

// Parse reads metadata from src into a Media.
func (Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities is APEv2's shared definition in internal/ape.
func (Codec) Capabilities(_ *core.Media, _ core.WriteOptions) core.Capabilities {
	return ape.Capabilities(core.FormatMonkeysAudio, false)
}

// EssenceExtent returns monkeys-audio-v1 and decoder-critical header fields
// (version, compression, flags, geometry) mixed ahead of the frames.
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
		// Whole frame size: documented values fit in 32 bits; high-half alone would be zero.
		binary.BigEndian.PutUint32(b[14:18], h.blocksPerFrame)
	}
	return "monkeys-audio-v1", b[:]
}
