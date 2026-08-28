package wavpack

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// maxChannelBlocks bounds the block-group walk that counts channels. A WavPack
// sample group carries at most two channels per block, so even a 32-channel stream
// needs 16; the cap only stops a corrupt stream whose final-block flag never
// arrives from walking the whole file.
const maxChannelBlocks = 64

// parse reads a WavPack file's metadata into a neutral Media: the audio geometry
// from the first block header, the canonical tags from the APEv2 tag, and the
// legacy ID3v1 (preserved, never authoritative) in the family view.
//
// The tail is peeled the way MP3's is: an ID3v1 tag sits after the APEv2 tag when
// both are present, so the peel runs end-first. Everything before what it finds is
// audio and is copied verbatim on every write.
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

	// A trailing tag can never begin before the end of the first block, which is always
	// audio; the floor keeps a crafted footer from swallowing the header just parsed.
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
	// ID3v1 is legacy here exactly as it is in MP3: surfaced in the family view so a
	// value living only there is visible, never promoted into the canonical set.
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

// buildTrack derives the audio track from the first block header, resolving a
// non-standard sample rate from the first block's metadata sub-blocks and summing
// the channel count across the first sample group (multichannel WavPack chains
// several two-channel blocks per group).
func buildTrack(ctx context.Context, src core.ReaderAtSized, d *doc, limit int64) core.AudioTrack {
	h := d.header
	t := core.AudioTrack{
		Codec:         "WavPack",
		SampleRate:    h.sampleRate(),
		Channels:      h.channels(),
		BitsPerSample: h.bitsPerSample(),
	}
	if h.float() {
		// A float stream's magnitude field describes the integer range the samples were
		// derived from, not the storage; report the real 32-bit float width.
		t.BitsPerSample = 32
	}
	if h.totalSamples32 != totalSamplesUnknown {
		t.TotalSamples = h.totalSamples
	}

	// Walk the first sample group: sum its blocks' channels, and read the first
	// block's sub-blocks when the rate index says the rate is non-standard.
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
					// DSD is one bit per sample at a rate the sub-block carries, so the
					// PCM-style depth the flag word describes does not apply.
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
