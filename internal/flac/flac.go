package flac

import (
	"encoding/binary"

	"github.com/colespringer/waxlabel/internal/core"
)

// Codec implements [core.Codec] for FLAC.
type Codec struct{}

// New returns a FLAC codec.
func New() Codec { return Codec{} }

func init() { core.Register(New()) }

func (Codec) Format() core.Format { return core.FormatFLAC }

// SkipsLeadingID3 reports true because FLAC tolerates a stray leading ID3v2 tag.
func (Codec) SkipsLeadingID3() bool { return true }
func (Codec) Extensions() []string  { return []string{".flac"} }

// Sniff matches the "fLaC" stream marker at offset 0. A FLAC file with a stray leading
// ID3v2 tag has no "fLaC" there, so Sniff does not match it directly; it is recognized
// via DetectLeading, which peeks past the leading ID3 to the inner "fLaC" signature.
// The ID3 header is shared with MP3, so this method does not claim ID3 prefixes.
func (Codec) Sniff(header []byte) bool {
	return len(header) >= 4 && string(header[:4]) == string(flacMagic)
}

// Capabilities reports FLAC's support. FLAC stores tags as Vorbis comments and
// art as PICTURE blocks, both losslessly and fully writable. Chapters use the
// VorbisComment CHAPTERxxx convention (start and title); a CUESHEET block is preserved
// verbatim but not read as canonical chapters.
func (Codec) Capabilities(_ *core.Media, opts core.WriteOptions) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "Vorbis comment", Fidelity: "lossless",
	}
	pictures := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "FLAC PICTURE block", Fidelity: "lossless",
	}
	chapters := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "VorbisComment CHAPTERxxx",
		Fidelity:       "start and title stored",
		Constraints:    []string{"CHAPTERxxx stores start and title only; a CUESHEET is preserved opaque but not read"},
		ChapterLoss:    core.ChapterLossStartTitleOnly,
	}
	// FLAC rewrites its metadata block every edit, so it both grows and shrinks
	// padding: --padding and --no-padding fully apply.
	return core.NewCapabilities(core.FormatFLAC, false, fields, pictures, chapters, core.AccessFull, nil)
}

// EssenceExtent returns the FLAC essence-digest inputs: the versioned extent
// name and the decoder-critical STREAMINFO configuration (sample rate, channel
// count, bit depth, and block-size bounds) mixed into the hash ahead of the
// audio frames, so identical packets under different config hash differently.
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	t := m.Properties.First()
	var b [16]byte
	binary.BigEndian.PutUint32(b[0:4], uint32(t.SampleRate))
	binary.BigEndian.PutUint32(b[4:8], uint32(t.Channels))
	binary.BigEndian.PutUint32(b[8:12], uint32(t.BitsPerSample))
	b[12] = byte(t.MinBlockSize >> 8)
	b[13] = byte(t.MinBlockSize)
	b[14] = byte(t.MaxBlockSize >> 8)
	b[15] = byte(t.MaxBlockSize)
	return "flac-frames-v1", b[:]
}
