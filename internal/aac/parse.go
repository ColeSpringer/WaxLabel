package aac

import (
	"context"
	"math"
	"time"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/tag"
)

// parse reads a raw-AAC (ADTS) file's metadata into a neutral Media: the
// optional front ID3v2 tag (authoritative, writable) and the audio
// geometry/properties from the first ADTS frame header. The whole region after
// the tag, [id3Len, EOF), is the single audio essence extent.
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes
	d := &doc{size: size}
	var warnings []core.Warning

	// Optional front ID3v2 tag (same authoritative container as MP3).
	tg, id3Len, err := id3.ReadFront(src, size, limit)
	if err != nil {
		return nil, err
	}
	d.id3 = tg
	d.id3Len = id3Len
	d.audioStart = d.id3Len
	d.audioEnd = size
	if d.audioEnd < d.audioStart {
		d.audioEnd = d.audioStart
	}

	// Audio properties from the first ADTS frame header. A stream too short to
	// hold a fixed header, or a malformed one, leaves the track config zero;
	// detection already required a valid header for a real .aac, so this is only
	// defensive. Read only when a full header is present - a shorter slice can
	// never decode, so there is nothing to gain from reading it.
	if avail := d.audioEnd - d.audioStart; avail >= int64(adtsHeaderSize) {
		if head, err := bits.ReadSlice(src, d.audioStart, int64(adtsHeaderSize), limit); err == nil {
			if h, ok := decodeADTS(head); ok {
				d.header = h
				d.track = buildTrack(h, avail)
			}
		}
	}
	if d.track.Codec == "" {
		d.track.Codec = "AAC"
	}

	media := &core.Media{
		Format:     core.FormatAAC,
		Native:     d,
		AudioStart: d.audioStart,
		AudioEnd:   d.audioEnd,
	}
	media.Tags = tag.NewTagSet()
	if d.id3 != nil {
		proj := id3.Project(d.id3)
		media.Tags = proj.Tags
		media.Pictures = proj.Pictures
		media.Families = proj.Families
		if proj.NumericGenre {
			warnings = core.Warn(warnings, core.WarnNumericGenre,
				"a numeric genre reference was resolved to a name")
		}
		warnings = append(warnings, id3.EncoderNoise(d.id3)...)
	}

	media.Properties = core.Properties{Container: "AAC (ADTS)", Tracks: []core.AudioTrack{d.track}}
	media.Warnings = warnings
	media.Identity = core.Identity{Size: size}
	media.Identity.Fingerprint, media.Identity.HasFinger = core.Fingerprint(src, media, limit)
	return media, nil
}

// samplesPerAACFrame is the PCM sample count an AAC-LC frame decodes to. SBR
// (HE-AAC) doubles the output rate, but the raw frame still carries this many
// samples at the signaled rate, which is what the cheap estimate uses.
const samplesPerAACFrame = 1024

// buildTrack assembles the audio properties from the first ADTS frame. The
// duration is a deliberate cheap estimate - the stream size divided by the first
// frame's bitrate - not an O(frames) walk of every ADTS frame_length, which is
// exactly the per-frame essence read a metadata read must avoid. It is therefore
// approximate for variable-bitrate streams (the first frame may not be
// representative), which is an accepted trade-off, not a bug.
func buildTrack(h adtsHeader, audioBytes int64) core.AudioTrack {
	t := core.AudioTrack{
		Codec:      aotName(h.objectType),
		SampleRate: h.sampleRate,
		Channels:   h.channels,
	}
	if h.sampleRate <= 0 || h.frameLength <= 0 || audioBytes <= 0 {
		return t
	}
	// First-frame bitrate: frameLength bytes carry samplesPerAACFrame samples, so
	// bitrate = frameLength*8 / (samplesPerAACFrame / sampleRate).
	t.Bitrate = int(math.Round(float64(h.frameLength) * 8 * float64(h.sampleRate) / float64(samplesPerAACFrame)))
	if t.Bitrate <= 0 {
		return t
	}
	secs := float64(audioBytes) * 8 / float64(t.Bitrate)
	if secs > 0 && secs < float64(math.MaxInt64)/float64(time.Second) {
		t.Duration = time.Duration(secs * float64(time.Second))
		t.TotalSamples = uint64(secs * float64(h.sampleRate))
	}
	return t
}
