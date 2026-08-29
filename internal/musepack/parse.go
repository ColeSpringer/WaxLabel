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

// headerWindow bounds the leading region read to find the stream header. SV7's is 24
// bytes; SV8's SH packet sits at the front of the packet stream and is small, but a
// writer may put a seek-table or encoder-info packet ahead of it, so the window
// leaves room for those.
const headerWindow = 8192

// parse reads a Musepack file's metadata into a neutral Media: the audio geometry
// from the stream header, the canonical tags from the APEv2 tag, and the legacy
// containers (a leading ID3v2, a trailing ID3v1) in the family view.
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes
	d := &doc{size: size}
	var warnings []core.Warning

	// A leading ID3v2 tag some SV7 encoders wrote. It is preserved verbatim and never
	// authoritative: APEv2 is Musepack's native store.
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

	// A trailing tag can never begin before the end of the stream header.
	trailer, tailWarnings := ape.PeelTrailer(src, size, d.streamAt+h.headerLen, limit, opts.Limits.MaxElements)
	d.trailer = trailer
	warnings = append(warnings, tailWarnings...)

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

// buildTrack derives the audio track from the decoded header.
func buildTrack(h header, audioLen int64) core.AudioTrack {
	// The raw name carries the stream version; the central CanonicalCodec step folds it
	// to "Musepack" and keeps "Musepack SV7"/"SV8" as the profile detail, the same way
	// it handles the MPEG layer spellings.
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

// leadingID3Families projects the preserved leading ID3v2 tag some SV7 encoders wrote,
// through the shared ID3 helper so it cannot drift from FLAC's handling of the same
// stray container.
func leadingID3Families(auth tag.TagSet, leading []byte, maxElements int) ([]core.FamilyValue, bool) {
	return id3.LegacyV2Families(auth, leading, maxElements)
}
