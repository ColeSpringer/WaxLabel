package mp3

import (
	"context"
	"encoding/binary"

	"github.com/colespringer/waxlabel/internal/core"
)

// Codec implements core.Codec for MP3.
type Codec struct{}

// New returns an MP3 codec.
func New() Codec { return Codec{} }

func init() { core.Register(New()) }

func (Codec) Format() core.Format  { return core.FormatMP3 }
func (Codec) Extensions() []string { return []string{".mp3"} }

// Sniff matches a leading ID3v2 tag or a bare MPEG audio frame. An ID3v2 header
// is shared with other containers that may carry a stray leading ID3 (FLAC); the
// parser disambiguates by peeking past the tag, so claiming "ID3" here is safe.
func (Codec) Sniff(header []byte) bool {
	if len(header) >= 3 && header[0] == 'I' && header[1] == 'D' && header[2] == '3' {
		return true
	}
	if _, ok := decodeHeader(header); ok {
		return true
	}
	return false
}

// Parse reads metadata from src into a Media.
func (c Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities reports MP3's support. Tags and art are stored as ID3v2 frames,
// fully writable; the version is preserved on edit. Trailing ID3v1/APEv2 are
// preserved and surfaced but not the write target.
func (Codec) Capabilities(opts core.WriteOptions) core.Capabilities {
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
		Representation: "CHAP (not modeled)",
	}
	return core.NewCapabilities(core.FormatMP3, false, fields, pictures, chapters, nil)
}

// EssenceExtent returns the MP3 essence-digest inputs: a versioned extent name
// and the decoder-critical configuration mixed in ahead of the audio - the first
// frame header together with the decoded sample rate and channel count, so two
// streams with identical frame bytes but a different rate or channel layout hash
// differently.
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	var cfg [12]byte
	if d, ok := m.Native.(*doc); ok {
		copy(cfg[0:4], d.firstHeader[:])
		binary.BigEndian.PutUint32(cfg[4:8], uint32(d.track.SampleRate))
		binary.BigEndian.PutUint32(cfg[8:12], uint32(d.track.Channels))
	}
	return "mp3-frames-v1", cfg[:]
}
