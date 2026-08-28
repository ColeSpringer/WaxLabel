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

// SkipsLeadingID3 reports false because a WavPack file begins with a wvpk block.
// The legacy ID3 a WavPack file can carry is a trailing ID3v1, not a front tag.
func (Codec) SkipsLeadingID3() bool { return false }

// Sniff matches the "wvpk" block marker at offset 0.
func (Codec) Sniff(header []byte) bool {
	return len(header) >= 4 && string(header[:4]) == blockMagic
}

// Parse reads metadata from src into a Media.
func (Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities reports WavPack's support, which is entirely APEv2's: the shared
// definition in internal/ape, so the three APE-backed codecs cannot drift. The
// trailing ID3v1 a file may carry is legacy - preserved, never written to - so it
// adds no capability.
func (Codec) Capabilities(_ *core.Media, _ core.WriteOptions) core.Capabilities {
	return ape.Capabilities(core.FormatWavPack, false)
}

// EssenceExtent returns the WavPack essence-digest inputs: a versioned extent name
// and the decoder-critical static configuration from the first block's flag word.
//
// It hashes the decoded fields rather than the raw flag word because that word also
// carries per-block state (the initial/final block markers and the decorrelation
// setup), which says nothing about the audio and would make two identical streams
// hash differently over how their blocks happen to be laid out.
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
	return "wavpack-v1", b[:]
}
