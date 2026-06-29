package aac

import (
	"context"
	"io"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/tag"
)

// parse reads a raw-AAC (ADTS) file's metadata into a neutral Media: the
// optional front ID3v2 tag (authoritative, writable) and the audio
// geometry/properties from the first ADTS frame header. The whole region after
// the tag, [id3Len, EOF), is the single audio essence extent.
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes
	d := &doc{size: size}
	var warnings []core.Warning

	// Optional front ID3v2 tag (same authoritative container as MP3).
	tg, id3Len, err := id3.ReadFront(src, size, limit, opts.Limits.MaxElements)
	if err != nil {
		return nil, err
	}
	d.id3 = tg
	d.id3Len = id3Len
	d.audioStart = d.id3Len
	d.audioEnd = size
	if d.audioEnd < d.audioStart {
		d.audioEnd = d.audioStart
	}

	// Audio properties from the first ADTS frame header. A stream too short to
	// hold a fixed header, or a malformed one, leaves the track config zero;
	// detection already required a valid header for a real.aac, so this is only
	// defensive. Read only when a full header is present - a shorter slice can
	// never decode, so there is nothing to gain from reading it.
	if avail := d.audioEnd - d.audioStart; avail >= int64(adtsHeaderSize) {
		head, err := bits.ReadSlice(src, d.audioStart, int64(adtsHeaderSize), limit)
		if err != nil {
			// avail >= adtsHeaderSize means the bytes exist, so a read failure here is a
			// genuine I/O fault - a faulting source, a concurrent truncate, or MaxAllocBytes
			// below the header size - not a malformed or absent header. Surface it (as
			// totalADTSSamples does for its reads) rather than swallowing it and letting the
			// no-audio gate below mislabel an I/O error as "file may not be audio".
			return nil, err
		}
		if h, ok := decodeADTS(head); ok {
			d.header = h
			samples, audioBytes, err := totalADTSSamples(ctx, src, d.audioStart, d.audioEnd)
			if err != nil {
				return nil, err
			}
			d.track = buildTrack(h, samples, audioBytes)
		}
	}
	// A non-empty essence region that decodes no whole ADTS frame is a.aac that is
	// not actually ADTS audio (random bytes, zeros, a renamed file). buildTrack
	// leaves TotalSamples at zero on every no-frame path - too short for a header, a
	// ReadSlice failure, a header that does not decode, or a valid-looking header
	// that yields zero complete frames - so this one check covers them all. Surface
	// it under the shared no-audio code (matching MP3 at internal/mp3/parse.go) so
	// dump/lint flag it and set/plan/verify refuse it (ErrInvalidData, exit 4)
	// rather than hash non-audio bytes as essence. A valid stream always has
	// TotalSamples > 0, so there are no false positives.
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

// samplesPerAACFrame is the PCM sample count one AAC-LC raw data block decodes to.
// SBR (HE-AAC) doubles the output rate, but the raw block still carries this many
// samples at the signaled rate. An ADTS frame holds rawBlocks+1 such blocks.
const samplesPerAACFrame = 1024

// adtsScanChunk bounds each read of the frame walk. ADTS frames are at most 8191
// bytes, so a 64 KiB window always holds at least one whole frame plus the next
// header; the buffer is reused across reads, so the walk allocates it once (not per
// frame).
const adtsScanChunk = 64 << 10

// buildTrack assembles the audio properties from the first ADTS frame's static
// config plus the true sample count and byte span from [totalADTSSamples]. The
// duration is the counted samples over the sample rate, and the average bitrate is
// audioBytes (the walked frames' span, not the raw region to EOF) over that
// duration - so both use one consistent extent and the result is accurate on a
// variable-bitrate stream, where the former first-frame estimate (one frame's size
// taken as representative) was off by tens of percent. ADTS carries no frame-count
// header, so this accuracy needs the O(frames) header walk; there is no Xing-style
// shortcut. A stream too short to hold one whole frame yields zero samples and so a
// zero duration/bitrate (the honest answer for an unplayable fragment).
func buildTrack(h adtsHeader, totalSamples uint64, audioBytes int64) core.AudioTrack {
	t := core.AudioTrack{
		Codec:      aotName(h.objectType),
		SampleRate: h.sampleRate,
		Channels:   h.channels,
	}
	if h.sampleRate <= 0 || totalSamples == 0 {
		return t
	}
	t.TotalSamples = totalSamples
	t.Duration = core.SamplesToDuration(totalSamples, h.sampleRate)
	t.Bitrate = core.AverageBitrate(audioBytes, t.Duration.Seconds())
	return t
}

// totalADTSSamples walks the ADTS frames in [start, end), returning both the true
// sample count and the byte span of the frames it counted - the basis for an
// accurate duration and an average bitrate over the *same* extent, since a VBR ADTS
// stream has no frame-count header to read. Each frame contributes
// samplesPerAACFrame*(rawBlocks+1) samples (AAC-LC's 1024 per raw data block, 1..4
// blocks per frame); AAC-LD's 512-sample frames are an unhandled rarity and would
// read about twice as long. It reads only frame headers - advancing by frame_length
// skips every payload - in windows reused across iterations, so it never reads
// essence twice and allocates nothing per frame.
//
// The returned audioBytes counts only the whole frames walked, so a trailing
// non-ADTS region (none is expected in raw ADTS, but a stray tail would otherwise
// inflate the bitrate) is excluded and the duration/bitrate use one consistent
// extent. The walk stops at the first header that does not decode or a frame that
// would run past end (a truncated tail), counting only whole frames. Because it is
// O(frames) it honors ctx cancellation once per window (a cancelled walk fails the
// parse rather than returning a short count), and it propagates a genuine read error
// rather than swallowing it as a benign EOF - matching the per-loop ctx/error
// handling the other codecs use.
func totalADTSSamples(ctx context.Context, src core.ReaderAtSized, start, end int64) (samples uint64, audioBytes int64, err error) {
	// Size the scratch window to the audio extent, capped at adtsScanChunk: a short
	// clip needs no full 64 KiB buffer, and the cap never falls below the 8191-byte
	// max frame (extents below it are read whole, so no frame straddles a refill).
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
		// ReadAt returns a non-nil error whenever got < len(p), and may return io.EOF
		// even on a full read that ends exactly at EOF. A short read here means the
		// source no longer holds the bytes the audio region claimed (a concurrent
		// truncate) and a non-EOF error is a genuine I/O failure: surface it rather
		// than report a silently short sample count. A full read that merely touched
		// EOF (got == n) is benign and stops the walk below.
		if rerr != nil && rerr != io.EOF {
			return 0, 0, rerr
		}
		window := buf[:got]
		p := 0
		for p+adtsHeaderSize <= len(window) {
			h, ok := decodeADTS(window[p:])
			if !ok {
				return samples, audioBytes, nil // corrupt/foreign bytes: stop at the last good frame
			}
			if off+int64(p)+int64(h.frameLength) > end {
				return samples, audioBytes, nil // frame runs past the audio region: truncated tail
			}
			samples += uint64(samplesPerAACFrame * (h.rawBlocks + 1))
			audioBytes += int64(h.frameLength)
			p += h.frameLength
			if p+adtsHeaderSize > len(window) {
				break // next header straddles the window (or the body did): refill from off+p
			}
		}
		if p == 0 {
			break // no whole frame fit the window / nothing read: do not spin
		}
		off += int64(p)
		if rerr != nil {
			break // benign EOF reached after consuming the whole frames already read
		}
	}
	return samples, audioBytes, nil
}
