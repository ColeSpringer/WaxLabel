package aac

import (
	"context"
	"io"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/internal/mpeg4audio"
	"github.com/colespringer/waxlabel/tag"
)

// parse reads raw-AAC (ADTS) into a Media: optional front ID3v2 and geometry from
// the first ADTS header. [id3Len, EOF) is the audio essence extent.
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes
	d := &doc{size: size}
	var warnings []core.Warning

	tg, id3Len, frontWs, err := id3.ReadFront(src, size, limit, opts.Limits.MaxElements)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, frontWs...)
	d.id3 = tg
	d.id3Len = id3Len
	d.audioStart = d.id3Len
	d.audioEnd = size
	if d.audioEnd < d.audioStart {
		d.audioEnd = d.audioStart
	}

	// First-frame header. Short/malformed leaves track zero (detection already
	// required a valid header for a real .aac). Read only when a full header fits.
	if avail := d.audioEnd - d.audioStart; avail >= int64(adtsHeaderSize) {
		head, err := bits.ReadSlice(src, d.audioStart, int64(adtsHeaderSize), limit)
		if err != nil {
			// Bytes should exist; surface I/O rather than mislabel as no-audio below.
			return nil, err
		}
		if h, ok := decodeADTS(head); ok {
			d.header = h
			samples, audioBytes, err := totalADTSSamples(ctx, src, d.audioStart, d.audioEnd)
			if err != nil {
				return nil, err
			}
			sbr, ps, err := detectSBR(ctx, src, d.audioStart, d.audioEnd, h, limit)
			if err != nil {
				return nil, err
			}
			d.track = buildTrack(h, samples, audioBytes, sbr, ps)
		}
	}
	// Non-empty essence with zero TotalSamples: not ADTS audio. Shared no-audio
	// code (see internal/mp3/parse.go); dump/lint flag, set/plan/verify refuse.
	if d.track.TotalSamples == 0 && d.audioEnd > d.audioStart {
		warnings = core.Warn(warnings, core.WarnNoAudioFrames,
			"no ADTS audio frames found; file may not be audio")
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
		media.Chapters = proj.Chapters
		core.OpenPastDurationEnds(media.Chapters, d.track.Duration)
		media.SyncedLyrics = proj.SyncedLyrics
		media.Families = proj.Families
		if proj.NumericGenre {
			warnings = core.Warn(warnings, core.WarnNumericGenre,
				"a numeric genre reference was resolved to a name")
		}
		warnings = append(warnings, proj.Warnings...)
		warnings = append(warnings, id3.EncoderNoise(d.id3)...)
	}

	media.Properties = core.Properties{Container: "AAC (ADTS)", Tracks: []core.AudioTrack{d.track}}
	media.Warnings = warnings
	media.Identity = core.Identity{Size: size}
	media.Identity.Fingerprint, media.Identity.HasFinger = core.Fingerprint(src, media, limit)
	return media, nil
}

// samplesPerAACFrame is PCM samples per AAC-LC raw data block. HE-AAC doubles
// play rate; [buildTrack] doubles the count when [detectSBR] finds SBR. An ADTS
// frame holds rawBlocks+1 such blocks.
const samplesPerAACFrame = 1024

// adtsScanChunk bounds each frame-walk read. Max ADTS frame is 8191 bytes; 64 KiB
// always holds one whole frame plus the next header. Buffer reused across reads.
const adtsScanChunk = 64 << 10

// buildTrack builds audio properties from the first frame's static config plus
// sample count and byte span from [totalADTSSamples]. Duration and average
// bitrate use that walked extent (accurate for VBR). Short streams yield zeros.
func buildTrack(h adtsHeader, totalSamples uint64, audioBytes int64, sbr, ps bool) core.AudioTrack {
	t := core.AudioTrack{
		Codec:      mpeg4audio.ObjectTypeName(h.objectType),
		SampleRate: h.sampleRate,
		Channels:   h.channels,
	}
	if sbr {
		t.SampleRate = 2 * h.sampleRate
		t.Codec = "HE-AAC"
		if ps {
			t.Codec = "HE-AAC v2"
			t.Channels = 2
		}
	}
	if h.sampleRate <= 0 || totalSamples == 0 {
		return t
	}
	// Frames count core samples; played stream has 2x at 2x rate (duration unchanged).
	if sbr {
		totalSamples *= 2
	}
	t.TotalSamples = totalSamples
	t.Duration = core.SamplesToDuration(totalSamples, t.SampleRate)
	t.Bitrate = core.AverageBitrate(audioBytes, t.Duration.Seconds())
	return t
}

// totalADTSSamples walks ADTS frames in [start, end), returning sample count and
// byte span of whole frames counted. Each frame contributes
// samplesPerAACFrame*(rawBlocks+1). Reads headers only (advance by frame_length).
// Trailing non-ADTS is excluded. Stops at first bad header or overrun. Honors ctx
// per window; propagates real read errors (not benign EOF).
func totalADTSSamples(ctx context.Context, src core.ReaderAtSized, start, end int64) (samples uint64, audioBytes int64, err error) {
	bufSize := end - start
	if bufSize > adtsScanChunk {
		bufSize = adtsScanChunk
	}
	buf := make([]byte, bufSize)
	for off := start; off+int64(adtsHeaderSize) <= end; {
		if e := ctx.Err(); e != nil {
			return 0, 0, e
		}
		n := end - off
		if n > int64(len(buf)) {
			n = int64(len(buf))
		}
		got, rerr := src.ReadAt(buf[:n], off)
		// Short read: source lost claimed bytes. Full read with EOF (got == n) is fine.
		if rerr != nil && rerr != io.EOF {
			return 0, 0, rerr
		}
		window := buf[:got]
		p := 0
		for p+adtsHeaderSize <= len(window) {
			h, ok := decodeADTS(window[p:])
			if !ok {
				return samples, audioBytes, nil // stop at last good frame
			}
			if off+int64(p)+int64(h.frameLength) > end {
				return samples, audioBytes, nil // truncated tail
			}
			samples += uint64(samplesPerAACFrame * (h.rawBlocks + 1))
			audioBytes += int64(h.frameLength)
			p += h.frameLength
			if p+adtsHeaderSize > len(window) {
				break // next header straddles: refill
			}
		}
		if p == 0 {
			break
		}
		off += int64(p)
		if rerr != nil {
			break // benign EOF after whole frames
		}
	}
	return samples, audioBytes, nil
}
