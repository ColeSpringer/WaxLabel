package apen

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// headerWindow is the largest leading region parseHeader can need: a descriptor a
// writer padded past its documented length, plus the header that follows it. It
// bounds the single read the parse makes before the tail peel.
const headerWindow = 4096

// parse reads a Monkey's Audio file's metadata into a neutral Media: the audio
// geometry from the header, the canonical tags from the APEv2 tag, and the legacy
// ID3v1 (preserved, never authoritative) in the family view.
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes
	d := &doc{size: size}

	head, err := bits.ReadSlice(src, 0, min(size, headerWindow), limit)
	if err != nil {
		return nil, fmt.Errorf("%w: Monkey's Audio file shorter than its header", waxerr.ErrInvalidData)
	}
	h, err := parseHeader(head)
	if err != nil {
		return nil, err
	}
	d.header = h

	// A trailing tag can never begin before the end of the header region, which is
	// audio description; the floor keeps a crafted footer from swallowing it.
	trailer, warnings := ape.PeelTrailer(src, size, h.headerLen, limit, opts.Limits.MaxElements)
	d.trailer = trailer

	media := &core.Media{
		Format:     core.FormatMonkeysAudio,
		Native:     d,
		AudioStart: 0,
		AudioEnd:   d.trailer.Start,
	}
	proj := ape.Project(d.trailer.Tag)
	media.Tags = proj.Tags
	media.Families = proj.Families
	media.Pictures = proj.Pictures
	warnings = append(warnings, proj.Warnings...)
	warnings = append(warnings, ape.EncoderNoise(d.trailer.Items())...)
	warnings = append(warnings, ape.InvalidUTF8Warnings(d.trailer.Tag)...)
	warnings = append(warnings, ape.InvalidKeyWarnings(d.trailer.Tag)...)
	media.Families = append(media.Families, ape.LegacyFamilies(media.Tags, d.trailer.ID3v1)...)

	d.track = buildTrack(h, d.trailer.Start)
	media.Properties = core.Properties{Container: "Monkey's Audio", Tracks: []core.AudioTrack{d.track}}
	if d.track.TotalSamples == 0 && d.trailer.Start > 0 {
		warnings = core.Warn(warnings, core.WarnNoAudioFrames,
			"the Monkey's Audio header declares no frames; the file may not be audio")
	}

	media.Warnings = warnings
	media.Identity = core.Identity{Size: size}
	media.Identity.Fingerprint, media.Identity.HasFinger = core.Fingerprint(src, media, limit)
	return media, nil
}

// buildTrack derives the audio track from the decoded header. Every field is stated
// there, so unlike the other lossless containers there is nothing to walk for.
func buildTrack(h header, audioEnd int64) core.AudioTrack {
	t := core.AudioTrack{
		Codec:         "Monkey's Audio",
		SampleRate:    int(h.sampleRate),
		Channels:      int(h.channels),
		BitsPerSample: int(h.bitsPerSample),
		TotalSamples:  h.totalSamples(),
	}
	t.Duration = core.SamplesToDuration(t.TotalSamples, t.SampleRate)
	t.Bitrate = core.AverageBitrate(audioEnd, t.Duration.Seconds())
	return t
}
