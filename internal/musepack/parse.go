package musepack

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/internal/tail"
	"github.com/colespringer/waxlabel/waxerr"
)

// headerWindow bounds the leading read for the stream header. SV7 is 24 bytes;
// SV8 SH is small but seek-table/encoder-info may precede it.
const headerWindow = 8192

// parse reads geometry from the stream header, tags from the APE tag that is the store, and
// legacy containers (leading ID3v2, trailing ID3v1) into the family view. An SV8 stream ends with
// its SE packet; an SV7 stream says nowhere where its audio ends.
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes
	d := &doc{}
	var warnings []core.Warning

	// Optional leading ID3v2 (some SV7 encoders); preserved, never authoritative.
	var err error
	d.leadingID3, d.streamAt, warnings, err = id3.ReadFrontStray(src, size, limit, Codec{}, "Musepack")
	if err != nil {
		return nil, err
	}

	head, err := bits.ReadSlice(src, d.streamAt, min(size-d.streamAt, headerWindow), limit)
	if fatal := bits.Fatal(err); fatal != nil {
		return nil, fatal
	}
	if err != nil {
		return nil, fmt.Errorf("%w: Musepack file shorter than its stream header", waxerr.ErrInvalidData)
	}
	h, err := parseHeader(head)
	if err != nil {
		return nil, err
	}
	d.header = h

	// The tags start past the stream header. An SV8 stream's packet walk places its end and its
	// chapters.
	var end tail.AudioEnd
	packets := walk{end: -1, chapters: -1, capAt: -1}
	if h.streamVersion == 8 {
		end = func(src core.ReaderAtSized, boundary int64) (int64, error) {
			w, err := walkPackets(ctx, src, d.streamAt, boundary, limit, opts.Limits.MaxElements)
			packets = w
			return w.end, err
		}
	}
	layout, err := tail.Scan(src, size, d.streamAt+h.headerLen, limit, opts.Limits.MaxElements, end)
	if err != nil {
		return nil, err
	}
	d.trailer = tail.Trailer{Layout: layout}
	warnings = append(warnings, d.trailer.Warnings("after the Musepack stream")...)

	// Chapter packets sit inside the stream, before the trailer; a cap the walk meets past it, in a
	// tag found there, leaves none unread.
	if d.chapterStore() {
		if packets.capAt >= 0 && packets.capAt < d.trailer.Start {
			warnings = core.Warn(warnings, core.WarnElementCap,
				"the stream has more packets than the element limit allows; no chapters are read")
		}
		if at := packets.chapters; at >= 0 && at <= d.trailer.Start {
			var chapterWarnings []core.Warning
			d.chapters, d.ctEnd, chapterWarnings, err = readChapters(src, at, d.trailer.Start, h.sampleRate, limit, opts.Limits.MaxElements)
			if err != nil {
				return nil, err
			}
			d.ctStart = at
			warnings = append(warnings, chapterWarnings...)
		}
	}

	media := &core.Media{Format: core.FormatMusepack, Native: d, Chapters: d.chapters}
	projected, audioLen := d.trailer.Project(media, d.leadingID3, d.streamAt, opts.Limits.MaxElements)
	warnings = append(warnings, projected...)

	d.track = buildTrack(h, audioLen)
	media.Properties = core.Properties{Container: "Musepack", Tracks: []core.AudioTrack{d.track}}
	if d.track.TotalSamples == 0 && d.trailer.Start > d.streamAt {
		warnings = core.Warn(warnings, core.WarnNoAudioFrames,
			"the Musepack header declares no samples; the file may not be audio")
	}

	media.Warnings = warnings
	if err := core.SetIdentity(src, media); err != nil {
		return nil, err
	}
	return media, nil
}

// buildTrack from the decoded header, the bitrate from the audioLen essence bytes.
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
