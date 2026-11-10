package wavpack

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/internal/tail"
	"github.com/colespringer/waxlabel/waxerr"
)

// maxChannelBlocks caps the channel-count walk. At most two channels per block, so
// 32 channels need 16; the cap stops a corrupt stream with no final-block flag.
const maxChannelBlocks = 64

// parse reads geometry from the first block, tags from the APE tag that is the store, and legacy
// containers (leading ID3v2, trailing ID3v1) into the family view. The block chain says where the
// audio ends; the blocks up to there are copied verbatim on write.
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
	d.leadingID3, d.streamAt, warnings, err = id3.ReadFrontStray(src, size, limit, Codec{}, "WavPack")
	if err != nil {
		return nil, err
	}

	head, err := bits.ReadSlice(src, d.streamAt, min(size-d.streamAt, blockHeaderLen), limit)
	if fatal := bits.Fatal(err); fatal != nil {
		return nil, fatal
	}
	if err != nil {
		return nil, fmt.Errorf("%w: WavPack file shorter than a block header", waxerr.ErrInvalidData)
	}
	h, err := parseBlockHeader(head, size-d.streamAt)
	if err != nil {
		return nil, err
	}
	d.header = h

	// The tags start past the first block.
	var walk chain
	layout, err := tail.Scan(src, size, d.streamAt+h.totalLen(), limit, opts.Limits.MaxElements,
		func(src core.ReaderAtSized, boundary int64) (int64, error) {
			w, err := blockChainEnd(ctx, src, d.streamAt, boundary, limit, opts.Limits.MaxElements)
			walk = w
			return w.end, err
		})
	if err != nil {
		return nil, err
	}
	d.trailer = tail.Trailer{Layout: layout}
	warnings = append(warnings, d.trailer.Warnings("after the WavPack stream")...)

	media := &core.Media{Format: core.FormatWavPack, Native: d}
	projected, audioLen, unread := d.trailer.Project(media, d.leadingID3, d.streamAt, opts.Limits.MaxElements)
	d.leadingUnread = unread
	warnings = append(warnings, projected...)

	d.track, err = buildTrack(ctx, src, d, walk, audioLen, limit)
	if err != nil {
		return nil, err
	}
	media.Properties = core.Properties{Container: "WavPack", Tracks: []core.AudioTrack{d.track}}
	if walk.end >= 0 && !walk.audio && d.trailer.Start > d.streamAt {
		warnings = core.Warn(warnings, core.WarnNoAudioFrames,
			"no WavPack audio samples were found; the file may not be audio")
	}

	media.Warnings = warnings
	if err := core.SetIdentity(src, media); err != nil {
		return nil, err
	}
	core.SyncTextItems(media)
	return media, nil
}

// buildTrack: first-block geometry, optional ID_SAMPLE_RATE sub-block, channel sum
// over the first sample group (multichannel chains several blocks), the length a first block at
// index 0 states or else the one walk measured, and the bitrate of the audioLen essence bytes. A read
// over limit is refused, and one that fails with a device error is that error.
func buildTrack(ctx context.Context, src core.ReaderAtSized, d *doc, walk chain, audioLen, limit int64) (core.AudioTrack, error) {
	h := d.header
	t := core.AudioTrack{
		Codec:         "WavPack",
		SampleRate:    h.sampleRate(),
		Channels:      h.channels(),
		BitsPerSample: h.bitsPerSample(),
	}
	if h.float() {
		// Magnitude is the integer range samples came from, not storage; float is 32-bit.
		t.BitsPerSample = 32
	}
	switch {
	case h.blockIndex == 0 && h.statesTotal():
		t.TotalSamples = h.totalSamples
	case walk.end >= 0:
		t.TotalSamples = walk.samples
	}

	off := d.streamAt
	channels := 0
	for i := 0; i < maxChannelBlocks && off+blockHeaderLen <= d.trailer.Start; i++ {
		if ctx.Err() != nil {
			break
		}
		hb, err := bits.ReadSlice(src, off, blockHeaderLen, limit)
		if fatal := bits.Fatal(err); fatal != nil {
			return t, fatal
		}
		if err != nil {
			break
		}
		bh, err := parseBlockHeader(hb, d.trailer.Start-off)
		if err != nil || bh.blockIndex != h.blockIndex {
			break
		}
		if i == 0 && (t.SampleRate == 0 || bh.dsd()) {
			body, err := bits.ReadSlice(src, off+blockHeaderLen, min(bh.totalLen()-blockHeaderLen, maxSubBlockScan), limit)
			if fatal := bits.Fatal(err); fatal != nil {
				return t, fatal
			}
			if err == nil {
				rate, isDSD := subBlockRate(body)
				if rate > 0 {
					t.SampleRate = rate
				}
				if isDSD {
					// DSD: 1 bit/sample at the sub-block rate; PCM depth flags do not apply.
					t.Codec, t.BitsPerSample = "WavPack DSD", 1
				}
			}
		}
		channels += bh.channels()
		if bh.flags&flagFinalBlock != 0 {
			break
		}
		off += bh.totalLen()
	}
	if channels > 0 {
		t.Channels = channels
	}

	t.Duration = core.SamplesToDuration(t.TotalSamples, t.SampleRate)
	t.Bitrate = core.AverageBitrate(audioLen, t.Duration.Seconds())
	return t, nil
}
