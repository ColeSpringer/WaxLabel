package wavpack

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// maxChannelBlocks caps the channel-count walk. At most two channels per block, so
// 32 channels need 16; the cap stops a corrupt stream with no final-block flag.
const maxChannelBlocks = 64

// parse reads geometry from the first block, tags from APEv2, and legacy ID3v1
// into the family view. Peel is end-first (ID3v1 after APEv2). Bytes before the
// trailer are audio and are copied verbatim on write.
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes
	d := &doc{size: size}
	var warnings []core.Warning

	head, err := bits.ReadSlice(src, 0, min(size, blockHeaderLen), limit)
	if err != nil {
		return nil, fmt.Errorf("%w: WavPack file shorter than a block header", waxerr.ErrInvalidData)
	}
	h, err := parseBlockHeader(head, size)
	if err != nil {
		return nil, err
	}
	d.header = h

	// Floor: a trailing tag must start after the first block.
	trailer, tailWarnings := ape.PeelTrailer(src, size, h.totalLen(), limit, opts.Limits.MaxElements)
	d.trailer = trailer
	warnings = append(warnings, tailWarnings...)

	media := &core.Media{
		Format:     core.FormatWavPack,
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
	// ID3v1 is family-only; never promoted into the canonical set.
	media.Families = append(media.Families, ape.LegacyFamilies(media.Tags, d.trailer.ID3v1)...)

	d.track = buildTrack(ctx, src, d, limit)
	media.Properties = core.Properties{Container: "WavPack", Tracks: []core.AudioTrack{d.track}}
	if d.track.TotalSamples == 0 && d.trailer.Start > 0 {
		warnings = core.Warn(warnings, core.WarnNoAudioFrames,
			"no WavPack audio samples were found; the file may not be audio")
	}

	media.Warnings = warnings
	media.Identity = core.Identity{Size: size}
	media.Identity.Fingerprint, media.Identity.HasFinger = core.Fingerprint(src, media, limit)
	return media, nil
}

// buildTrack: first-block geometry, optional ID_SAMPLE_RATE sub-block, channel sum
// over the first sample group (multichannel chains several blocks).
func buildTrack(ctx context.Context, src core.ReaderAtSized, d *doc, limit int64) core.AudioTrack {
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
	if h.totalSamples32 != totalSamplesUnknown {
		t.TotalSamples = h.totalSamples
	}

	off := int64(0)
	channels := 0
	for i := 0; i < maxChannelBlocks && off+blockHeaderLen <= d.trailer.Start; i++ {
		if ctx.Err() != nil {
			break
		}
		hb, err := bits.ReadSlice(src, off, blockHeaderLen, limit)
		if err != nil {
			break
		}
		bh, err := parseBlockHeader(hb, d.trailer.Start-off)
		if err != nil || bh.blockIndex != 0 {
			break
		}
		if i == 0 && (t.SampleRate == 0 || bh.dsd()) {
			body, err := bits.ReadSlice(src, off+blockHeaderLen, min(bh.totalLen()-blockHeaderLen, maxSubBlockScan), limit)
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
	t.Bitrate = core.AverageBitrate(d.trailer.Start, t.Duration.Seconds())
	return t
}
