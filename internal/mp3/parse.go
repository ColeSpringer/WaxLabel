package mp3

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/internal/tail"
	"github.com/colespringer/waxlabel/tag"
)

// scanWindow bounds the search past the stream start for the first MPEG frame
// and its VBR header.
const scanWindow = 64 << 10

// parse reads MP3 metadata: front ID3v2 (authoritative), audio geometry from the first frame, and
// the tail after the audio, whose tags are legacy (preserved, surfaced, warned).
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes
	d := &doc{}

	// Front ID3v2 tags, the one kept and its duplicates, and the stream behind them: where they end
	// when what opens there sniffs as MP3 (a frame header on its own), else at the first frame a
	// second one confirms in the resync window from there, else at the first frame [lenientStart]
	// accepts in the scanWindow bytes from their end, short of the tags at the end of the file; with
	// none, nowhere.
	front, warnings, err := id3.ReadFrontAll(src, size, limit, opts.Limits.MaxElements)
	if err != nil {
		return nil, err
	}
	d.id3, d.id3Off, d.id3Len, d.dups, d.frontEnd = front.Tag, front.Off, front.Len, front.Dups, front.End
	lenient := false // the start is the one lenientStart found
	at, stream, leading, err := id3.StreamBehindFront(src, d.frontEnd, limit, Codec{}, "MPEG frame", func(from int64) (at int64, ok bool, err error) {
		at, ok, err = lenientStart(src, from, limit, opts.Limits.MaxElements)
		lenient = ok
		return at, ok, err
	})
	if err != nil {
		return nil, err
	}
	if front.Tag.Unbounded() {
		// The tag declares more bytes than the file holds, so it reaches the stream, or the end of
		// the file with none: one unreadable region a rewrite replaces, not a gap.
		if !stream {
			at = size
		}
		d.id3Len, d.frontEnd, leading = at, at, nil
	}
	d.firstFrameOff = at

	// No tag starts inside the first frame.
	floor := d.firstFrameOff
	if stream {
		head, err := bits.ReadSlice(src, d.firstFrameOff, min(size-d.firstFrameOff, 4), limit)
		if err != nil {
			return nil, err
		}
		if h, ok := decodeHeader(head); ok {
			floor += max(int64(frameLength(h)), 4)
		}
	}

	// The first frame found before the tags gives the audio properties and, with the tags peeled,
	// where the audio ends. Asked again without the tags, the end sees the file as a strip leaves it,
	// so it reads its window from the source it is given; what the last asking found stands.
	var info mpegInfo
	var end measure
	found := false
	frameAt, mixed := int64(0), false            // where the first frame opens, and whether the frames after it vary in bitrate
	first, peeled := measure{end: -1}, int64(-1) // the first measure, over the file, and where its tags begin
	d.layout, err = tail.ScanLegacy(src, size, floor, limit, opts.Limits.MaxElements, func(in core.ReaderAtSized, boundary int64) (int64, error) {
		info, end, found, mixed = mpegInfo{}, measure{}, false, false
		if !stream {
			return -1, nil
		}
		window, err := bits.ReadSlice(in, d.firstFrameOff, min(boundary-d.firstFrameOff, scanWindow), limit)
		if err != nil {
			// The bytes exist; surface the failed read rather than report no audio.
			return 0, err
		}
		i, f, ok := parseMPEG(window)
		if !ok {
			return -1, nil
		}
		info, found = f, true
		frameAt, mixed = d.firstFrameOff+int64(i), mixedBitrates(window[i:], f)
		end, err = audioEnd(ctx, in, f, frameAt, boundary, limit)
		if peeled < 0 {
			first, peeled = end, boundary
		}
		return end.end, err
	})
	if err != nil {
		return nil, err
	}
	if lenient && !found {
		// The scan above, reading on to the tags it peels, finds no audio at the frame lenientStart
		// took, so that frame starts nothing and no gap sits before it; the tags stay as scanned.
		d.firstFrameOff, leading = d.frontEnd, nil
	}
	warnings = append(warnings, leading...)

	// The ID3v2 projection comes first, as the tail's legacy family entries sit behind its values,
	// flagged as conflicts when they disagree; what the tail cannot carry (a binary APE item, a
	// Lyrics3 block) is opaque, so the safe fix keeps it and dump surfaces it.
	media := &core.Media{Format: core.FormatMP3, Native: d, Tags: tag.NewTagSet()}
	var proj id3.Projection
	if d.id3 != nil {
		proj = id3.Project(d.id3)
		media.Tags, media.Pictures, media.Chapters = proj.Tags, proj.Pictures, proj.Chapters
		media.SyncedLyrics, media.Families = proj.SyncedLyrics, proj.Families
		media.Comments, media.Lyrics = proj.Comments, proj.Lyrics
	}
	ws, audioBytes := d.layout.Project(media, d.firstFrameOff, "after the MPEG stream")
	warnings = append(warnings, ws...)
	if found {
		if end.joined {
			// The stream goes on where the header's byte count ends, so the header counts only part of
			// it, and its frame count gives no duration either.
			info.vbrFrames = 0
		}
		if end.gap > 0 {
			warnings = core.Warn(warnings, core.WarnTrailingBytes, fmt.Sprintf(
				"%d byte(s) between the MPEG frames belong to no frame, a tag perhaps, and the frames go on past them, so they stay in the audio essence; preserved verbatim",
				end.gap))
		}
		warnings = id3.WarnJoinedTags(warnings, end.tags, "MPEG")
		d.firstHeader = info.header
		// A stream of mixed bitrates with no VBR header states its length nowhere, so its
		// frames are counted: the one case that reads the whole stream.
		var walked frameRun
		if info.vbrFrames == 0 && mixed {
			if walked, err = walkMPEGFrames(ctx, src, info, d.layout.Essence(frameAt), limit); err != nil {
				return nil, err
			}
		}
		d.track = buildTrack(info, audioBytes, walked)
		// A header's byte count past the tags by a frame or more is a truncation, and so is a byte
		// count or a frame that claims audio past a tag found before them, where the audio ends, unless
		// the stream goes on after the tag. Where frames follow the tag, the measure over the file
		// itself, the tag's bytes inside the stream, says how far a byte count runs past the file.
		// Without a byte count that covers a frame, a frame count shows one only by an average bitrate
		// below 8 kbps (incl. 0); CBR without a header or such a tag: undetectable here.
		tagged := d.layout.Start < peeled
		on := tagged && end.end > d.layout.Start
		past, n := end.past, end.past
		switch {
		case on && end.last >= d.layout.Start:
			past, n = first.past, first.past
		case tagged && end.end < 0:
			n = end.counted - d.layout.Start
		}
		switch {
		case past > 0:
			warnings = core.WarnTruncated(warnings, fmt.Sprintf("the VBR header, by %d byte(s),", n))
		case tagged && !on && first.claim > d.layout.Start:
			warnings = core.WarnTruncated(warnings, fmt.Sprintf("the last MPEG frame, by %d byte(s),", first.claim-d.layout.Start))
		case info.byteCount() == 0 && info.vbrFrames > 0 && d.track.Bitrate < 8000:
			warnings = core.Warn(warnings, core.WarnTruncatedAudio,
				"fewer audio frames than the Xing/Info header declares; file may be truncated")
		}
	} else if audioBytes > 0 {
		// Non-empty essence with no MPEG frame: not audio. Shared no-audio warning;
		// set/plan/verify refuse (distinct from empty-range no-audio).

		warnings = core.Warn(warnings, core.WarnNoAudioFrames,
			"no MPEG audio frames found; file may not be audio")
	}
	if d.track.Codec == "" {
		d.track.Codec = "MPEG audio"
	}

	if d.id3 != nil {
		core.OpenPastDurationEnds(media.Chapters, d.track.Duration)
		if proj.NumericGenre {
			warnings = core.Warn(warnings, core.WarnNumericGenre,
				"a numeric genre reference was resolved to a name")
		}
		warnings = append(warnings, proj.Warnings...)
		warnings = append(warnings, id3.EncoderNoise(d.id3)...)
	}

	media.Properties = core.Properties{Container: "MP3", Tracks: []core.AudioTrack{d.track}}
	media.Warnings = warnings
	if err := core.SetIdentity(src, media); err != nil {
		return nil, err
	}
	core.SyncTextItems(media)
	return media, nil
}

// lenientStart finds the first frame the parser's own scan accepts, one whose next frame agrees, a
// free-format one, or one whose next frame would lie past the scanned bytes: at most scanWindow of
// them from from, none at or past where the tags at the end of the file begin, as a tail scan
// floored at from finds them with no audio end. So no frame is looked for inside those tags.
func lenientStart(src core.ReaderAtSized, from, limit int64, maxElements int) (int64, bool, error) {
	l, err := tail.ScanLegacy(src, src.Size(), from, limit, maxElements, nil)
	if err != nil {
		return 0, false, err
	}
	window, err := bits.ReadSlice(src, from, min(l.Start-from, scanWindow), limit)
	if err != nil {
		return 0, false, err
	}
	i, _, ok := firstFrame(window, confirmNextFrame)
	return from + int64(i), ok, nil
}

// buildTrack assembles the audio properties. The length is the frame count's less the encoder's
// delay and padding: the count a VBR header states, else walked's, the frames of a stream of mixed
// bitrates without one, less the header's own frame; with neither, the (constant) frame bitrate's.
// The average bitrate is over the frames as encoded, delay and padding included.
func buildTrack(info mpegInfo, audioBytes int64, walked frameRun) core.AudioTrack {
	t := core.AudioTrack{Codec: info.codec, SampleRate: info.sampleRate, Channels: info.channels}
	frames := uint64(info.vbrFrames)
	if frames == 0 && walked.frames > 0 {
		frames, audioBytes = walked.frames, walked.bytes
		if info.vbrHeader {
			frames, audioBytes = frames-1, audioBytes-int64(frameLength(info))
		}
	}
	switch encoded := frames * uint64(info.samplesPerFrame); {
	case encoded > 0 && info.sampleRate > 0:
		t.TotalSamples = encoded
		if trim := uint64(info.encDelay + info.encPadding); trim < encoded {
			t.TotalSamples -= trim
		}
		t.Duration = core.SamplesToDuration(t.TotalSamples, info.sampleRate)
		t.Bitrate = core.AverageBitrate(audioBytes, core.SamplesToDuration(encoded, info.sampleRate).Seconds())
	case info.frameBitrate > 0:
		t.Bitrate = info.frameBitrate
		secs := float64(audioBytes) * 8 / float64(info.frameBitrate)
		if secs > 0 && secs < float64(math.MaxInt64)/float64(time.Second) {
			t.Duration = time.Duration(secs * float64(time.Second))
			if info.sampleRate > 0 {
				t.TotalSamples = uint64(secs * float64(info.sampleRate))
			}
		}
	}
	return t
}
