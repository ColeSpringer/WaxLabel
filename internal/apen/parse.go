package apen

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/internal/tail"
	"github.com/colespringer/waxlabel/waxerr"
)

// headerWindow bounds the leading read; a header declared past it is read on its own.
const headerWindow = 4096

// parse reads geometry from the header, tags from the APE tag that is the store, and legacy
// containers (leading ID3v2, trailing ID3v1) into the family view. A 3.98+ descriptor says where the
// audio ends.
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes
	d := &doc{}
	var warnings []core.Warning

	// Optional leading ID3v2; preserved, never authoritative.
	var err error
	d.leadingID3, d.streamAt, warnings, err = id3.ReadFrontStray(src, size, limit, Codec{}, "Monkey's Audio")
	if err != nil {
		return nil, err
	}

	head, err := bits.ReadSlice(src, d.streamAt, min(size-d.streamAt, headerWindow), limit)
	if fatal := bits.Fatal(err); fatal != nil {
		return nil, fatal
	}
	if err != nil {
		return nil, fmt.Errorf("%w: Monkey's Audio file shorter than its header", waxerr.ErrInvalidData)
	}
	// Checked first, so a file refused here costs no read of its trailing tags.
	version, err := parseVersion(head)
	if err != nil {
		return nil, err
	}
	// The tags start past the layout's documented header length, and a declared header must end
	// before them: a rewrite replaces the tags, which would otherwise change the header.
	end := descriptorEnd(head, version, d.streamAt)
	layout, err := tail.Scan(src, size, d.streamAt+documentedHeaderLen(version), limit, opts.Limits.MaxElements,
		func(core.ReaderAtSized, int64) (int64, error) { return end, nil })
	if err != nil {
		return nil, err
	}
	d.trailer = tail.Trailer{Layout: layout}
	warnings = append(warnings, d.trailer.Warnings("after the Monkey's Audio stream")...)
	h, err := readHeader(src, head, version, d.streamAt, d.trailer.Start, size, limit)
	if err != nil {
		return nil, err
	}
	d.header = h

	media := &core.Media{Format: core.FormatMonkeysAudio, Native: d}
	projected, audioLen, unread := d.trailer.Project(media, d.leadingID3, d.streamAt, opts.Limits.MaxElements)
	d.leadingUnread = unread
	warnings = append(warnings, projected...)

	d.track = buildTrack(h, audioLen)
	media.Properties = core.Properties{Container: "Monkey's Audio", Tracks: []core.AudioTrack{d.track}}
	if d.track.TotalSamples == 0 && d.trailer.Start > d.streamAt {
		warnings = core.Warn(warnings, core.WarnNoAudioFrames,
			"the Monkey's Audio header declares no frames; the file may not be audio")
	}

	media.Warnings = warnings
	if err := core.SetIdentity(src, media); err != nil {
		return nil, err
	}
	core.SyncTextItems(media)
	return media, nil
}

// buildTrack takes geometry from the header (all fields are stated there) and the bitrate from the
// audioLen essence bytes.
func buildTrack(h header, audioLen int64) core.AudioTrack {
	t := core.AudioTrack{
		Codec:         "Monkey's Audio",
		SampleRate:    int(h.sampleRate),
		Channels:      int(h.channels),
		BitsPerSample: int(h.bitsPerSample),
		TotalSamples:  h.totalSamples(),
	}
	t.Duration = core.SamplesToDuration(t.TotalSamples, t.SampleRate)
	t.Bitrate = core.AverageBitrate(audioLen, t.Duration.Seconds())
	return t
}
