// Package apen implements Monkey's Audio (.ape) metadata for the public waxlabel
// package. The codec is internal.
//
// Layout: optional leading ID3v2, "MAC " header, compressed frames, then the tail internal/tail
// reads, the same trailing store as WavPack/Musepack: APE tags, Lyrics3 blocks, ID3v1 tags and
// junk. The APE tag nearest the end of the file is authoritative; the ID3 containers and Lyrics3
// blocks are preserved as legacy only. Audio is copied verbatim on write.
//
// Named for the container so it is distinct from internal/ape (the APEv2 tag).
// Reimplemented from the public Monkey's Audio header docs; reference code informed
// design only.
package apen

import (
	"context"
	"encoding/binary"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/tail"
)

// Codec implements core.Codec for Monkey's Audio.
type Codec struct{}

// New returns a Monkey's Audio codec.
func New() Codec { return Codec{} }

func init() { core.Register(New()) }

func (Codec) Format() core.Format  { return core.FormatMonkeysAudio }
func (Codec) Extensions() []string { return []string{".ape"} }

// SkipsLeadingID3 is true: some taggers write a front ID3v2, preserved as legacy.
// core.DetectLeading peeks past it for "MAC ".
func (Codec) SkipsLeadingID3() bool { return true }

// Sniff matches "MAC " at offset 0 (trailing space is part of the marker). Leading-ID3v2 files are
// recognized via DetectLeading instead.
func (Codec) Sniff(header []byte) bool {
	return len(header) >= 4 && string(header[:4]) == fileMagic
}

// Resync finds the first "MAC " in window followed by a supported version ([core.Resyncer]).
func (c Codec) Resync(window []byte) (int, bool) {
	for i := range window {
		if b := window[i:]; c.Sniff(b) {
			if _, err := parseVersion(b); err == nil {
				return i, true
			}
		}
	}
	return 0, false
}

// Parse reads metadata from src into a Media.
func (Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities is APEv2's shared definition in internal/ape.
func (Codec) Capabilities(m *core.Media, _ core.WriteOptions) core.Capabilities {
	return tail.Capabilities(core.FormatMonkeysAudio, m)
}

// EssenceExtent returns monkeys-audio-v2 and decoder-critical header fields
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
	return "monkeys-audio-v2", b[:]
}
