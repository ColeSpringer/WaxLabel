package apen

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// headerWindow bounds the leading read; a header declared past it is read on its own.
const headerWindow = 4096

// parse reads geometry from the header, tags from APEv2, and legacy ID3v1 into
// the family view.
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
	// Checked first, so a file refused here costs no read of its tags.
	version, err := parseVersion(head)
	if err != nil {
		return nil, err
	}
	// The tags are peeled above the layout's documented header length, and a declared header must
	// end before them: a rewrite replaces the tags, which would otherwise change the header.
	trailer, warnings := ape.PeelTrailer(src, size, documentedHeaderLen(version), limit, opts.Limits.MaxElements)
	h, err := readHeader(src, head, version, trailer.Start, size, limit)
	if err != nil {
		return nil, err
	}
	d.header = h
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
	media.Identity.Fingerprint, media.Identity.HasFinger = core.Fingerprint(src, media)
	return media, nil
}

// buildTrack takes geometry from the header (all fields are stated there).
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
