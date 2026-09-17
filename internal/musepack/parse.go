package musepack

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// headerWindow bounds the leading read for the stream header. SV7 is 24 bytes;
// SV8 SH is small but seek-table/encoder-info may precede it.
const headerWindow = 8192

// parse reads geometry from the stream header, tags from APEv2, and legacy
// containers (leading ID3v2, trailing ID3v1) into the family view.
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes
	d := &doc{size: size}
	var warnings []core.Warning

	// Optional leading ID3v2 (some SV7 encoders); preserved, never authoritative.
	if hdr, err := bits.ReadSlice(src, 0, min(size, 10), limit); err == nil && len(hdr) == 10 {
		if n, ok := id3.TagSize(hdr); ok && n > 0 && n < size {
			d.leadingID3, err = bits.ReadSlice(src, 0, n, limit)
			if err != nil {
				return nil, err
			}
			d.streamAt = n
			warnings = core.Warn(warnings, core.WarnStrayLeadingID3,
				fmt.Sprintf("ID3v2 tag of %d bytes precedes the Musepack stream; preserved", n))
		}
	}

	head, err := bits.ReadSlice(src, d.streamAt, min(size-d.streamAt, headerWindow), limit)
	if err != nil {
		return nil, fmt.Errorf("%w: Musepack file shorter than its stream header", waxerr.ErrInvalidData)
	}
	h, err := parseHeader(head)
	if err != nil {
		return nil, err
	}
	d.header = h

	// Floor: trailing tag after the stream header.
	trailer, tailWarnings := ape.PeelTrailer(src, size, d.streamAt+h.headerLen, limit, opts.Limits.MaxElements)
	d.trailer = trailer
	warnings = append(warnings, tailWarnings...)

	// Chapter packets sit inside the stream, before the trailer.
	if d.chapterStore() {
		at, walkWarnings, err := chapterRun(ctx, src, d.streamAt, d.trailer.Start, limit, opts.Limits.MaxElements)
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, walkWarnings...)
		if at >= 0 {
			var chapterWarnings []core.Warning
			d.chapters, d.ctEnd, chapterWarnings = readChapters(src, at, d.trailer.Start, h.sampleRate, limit, opts.Limits.MaxElements)
			d.ctStart = at
			warnings = append(warnings, chapterWarnings...)
		}
	}

	media := &core.Media{
		Format:     core.FormatMusepack,
		Native:     d,
		AudioStart: d.streamAt,
		AudioEnd:   d.trailer.Start,
	}
	proj := ape.Project(d.trailer.Tag)
	media.Tags = proj.Tags
	media.Families = proj.Families
	media.Pictures = proj.Pictures
	media.Chapters = d.chapters
	warnings = append(warnings, proj.Warnings...)
	warnings = append(warnings, ape.EncoderNoise(d.trailer.Items())...)
	warnings = append(warnings, ape.InvalidUTF8Warnings(d.trailer.Tag)...)
	warnings = append(warnings, ape.InvalidKeyWarnings(d.trailer.Tag)...)
	media.Families = append(media.Families, ape.LegacyFamilies(media.Tags, d.trailer.ID3v1)...)
	fams, opaque := leadingID3Families(media.Tags, d.leadingID3, opts.Limits.MaxElements)
	media.Families = append(media.Families, fams...)
	media.LegacyOpaqueContent = opaque

	d.track = buildTrack(h, d.trailer.Start-d.streamAt)
	media.Properties = core.Properties{Container: "Musepack", Tracks: []core.AudioTrack{d.track}}
	if d.track.TotalSamples == 0 && d.trailer.Start > d.streamAt {
		warnings = core.Warn(warnings, core.WarnNoAudioFrames,
			"the Musepack header declares no samples; the file may not be audio")
	}

	media.Warnings = warnings
	media.Identity = core.Identity{Size: size}
	media.Identity.Fingerprint, media.Identity.HasFinger = core.Fingerprint(src, media, limit)
	return media, nil
}

// buildTrack from the decoded header.
func buildTrack(h header, audioLen int64) core.AudioTrack {
	// Raw name carries stream version; CanonicalCodec folds to "Musepack" and
	// keeps SV7/SV8 as profile detail (same pattern as MPEG layer spellings).
	t := core.AudioTrack{
		Codec:        fmt.Sprintf("Musepack SV%d", h.streamVersion),
		SampleRate:   h.sampleRate,
		Channels:     h.channels,
		TotalSamples: h.totalSamples,
	}
	t.Duration = core.SamplesToDuration(t.TotalSamples, t.SampleRate)
	t.Bitrate = core.AverageBitrate(audioLen, t.Duration.Seconds())
	return t
}

// leadingID3Families projects preserved leading ID3v2 via the shared ID3 helper
// (same as FLAC's stray container).
func leadingID3Families(auth tag.TagSet, leading []byte, maxElements int) ([]core.FamilyValue, bool) {
	return id3.LegacyV2Families(auth, leading, maxElements)
}
