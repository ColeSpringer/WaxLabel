package wavpack

import (
	"context"
	"encoding/binary"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
)

// Codec implements core.Codec for WavPack.
type Codec struct{}

// New returns a WavPack codec.
func New() Codec { return Codec{} }

func init() { core.Register(New()) }

func (Codec) Format() core.Format  { return core.FormatWavPack }
func (Codec) Extensions() []string { return []string{".wv"} }

// SkipsLeadingID3 is true: some taggers write a front ID3v2, preserved as legacy.
// core.DetectLeading peeks past it for "wvpk".
func (Codec) SkipsLeadingID3() bool { return true }

// Sniff matches "wvpk" at offset 0. Leading-ID3v2 files are recognized via DetectLeading instead.
func (Codec) Sniff(header []byte) bool {
	return len(header) >= 4 && string(header[:4]) == blockMagic
}

// Resync finds the first "wvpk" in window that opens a valid block header ([core.Resyncer]).
func (c Codec) Resync(window []byte) (int, bool) {
	for i := range window {
		if b := window[i:]; c.Sniff(b) {
			if _, err := parseBlockHeader(b, 0); err == nil {
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

// Capabilities is APEv2's shared definition. Trailing ID3v1 adds no capability.
func (Codec) Capabilities(_ *core.Media, _ core.WriteOptions) core.Capabilities {
	return ape.Capabilities(core.FormatWavPack, false)
}

// EssenceExtent returns wavpack-v2 and decoder-critical fields from the first
// block. Hashes decoded fields, not the raw flag word: that word also carries
// per-block state (initial/final markers, decorrelation) that would make identical
// streams hash differently by block layout.
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	var b [16]byte
	if d, ok := m.Native.(*doc); ok && d != nil {
		t := d.track
		binary.BigEndian.PutUint32(b[0:4], uint32(t.SampleRate))
		binary.BigEndian.PutUint32(b[4:8], uint32(t.Channels))
		binary.BigEndian.PutUint32(b[8:12], uint32(t.BitsPerSample))
		b[12] = byte(d.header.version >> 8)
		b[13] = byte(d.header.version)
		if d.header.hybrid() {
			b[14] = 1
		}
		if d.header.dsd() {
			b[15] = 1
		}
	}
	return "wavpack-v2", b[:]
}
