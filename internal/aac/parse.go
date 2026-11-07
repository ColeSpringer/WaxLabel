package aac

import (
	"context"
	"errors"
	"fmt"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/internal/mpeg4audio"
	"github.com/colespringer/waxlabel/internal/tail"
	"github.com/colespringer/waxlabel/tag"
)

// parse reads raw-AAC (ADTS) into a Media: optional front ID3v2, geometry from the first ADTS
// header, and the tail after the frames, whose tags are legacy. The essence runs from the first
// frame to where the frames end.
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes
	d := &doc{}

	// Front ID3v2 tag, and the stream behind it: past chained tags or a gap, which detection looks
	// past too.
	tg, id3Len, warnings, err := id3.ReadFront(src, size, limit, opts.Limits.MaxElements)
	if err != nil {
		return nil, err
	}
	d.id3, d.id3Len = tg, id3Len
	at, _, ws, err := id3.StreamBehindFront(src, id3Len, limit, Codec{}, "ADTS frame", nil)
	if err != nil {
		return nil, err
	}
	d.firstFrameOff = at
	warnings = append(warnings, ws...)

	// First-frame header. Short/malformed leaves track zero (detection already
	// required a valid header for a real .aac). Read only when a full header fits.
	// No tag starts inside the first frame.
	floor := d.firstFrameOff
	headed := false
	if size-d.firstFrameOff >= int64(adtsHeaderSize) {
		head, err := bits.ReadSlice(src, d.firstFrameOff, int64(adtsHeaderSize), limit)
		if err != nil {
			// Bytes should exist; surface I/O rather than mislabel as no-audio below.
			return nil, err
		}
		if d.header, headed = decodeADTS(head); headed {
			floor += int64(d.header.frameLength)
		}
	}

	// The frames, walked up to the tags peeled from the end of the file, end the audio; a last frame
	// those tags or the end of the file cut short stays audio, so the audio then ends where they begin.
	// Asked again without the tags, the walk goes on from the last frame it counted, which a tag found
	// inside it cuts there: the bytes before that frame are the same with or without them. Joins past
	// ID3 tags count toward MaxElements over both walks.
	walk := adtsWalk{stop: d.firstFrameOff, last: -1}
	claim, peeled := int64(-1), int64(-1) // what the first walk's frames claim, and where its tags begin
	audioEnd := func(in core.ReaderAtSized, boundary int64) (int64, error) {
		if !headed {
			return -1, nil
		}
		if peeled >= 0 {
			walk.rewind() // its last frame may run into a tag the view leaves out
		}
		err := walk.walk(ctx, in, boundary, limit, opts.Limits.MaxElements, d.header)
		if peeled < 0 {
			claim, peeled = walk.stop, boundary
			if walk.cut {
				claim = walk.over
			}
		}
		if walk.cut {
			return boundary, err
		}
		return walk.stop, err
	}
	if d.layout, err = tail.ScanLegacy(src, size, floor, limit, opts.Limits.MaxElements, audioEnd); err != nil {
		return nil, err
	}

	// The ID3v2 projection comes first, as the tail's legacy family entries sit behind its values;
	// what the tail cannot carry is opaque.
	media := &core.Media{Format: core.FormatAAC, Native: d, Tags: tag.NewTagSet()}
	var proj id3.Projection
	if d.id3 != nil {
		proj = id3.Project(d.id3)
		media.Tags, media.Pictures, media.Chapters = proj.Tags, proj.Pictures, proj.Chapters
		media.SyncedLyrics, media.Families = proj.SyncedLyrics, proj.Families
	}
	ws, essence := d.layout.Project(media, d.firstFrameOff, "after the ADTS stream")
	warnings = id3.WarnJoinedTags(append(warnings, ws...), walk.tags, "ADTS")
	// Audio that ends before the tags at the end and before the first walk's claim ends at a tag found
	// inside a frame, which the tag cut unless the walk without the tag goes on after it.
	if d.layout.Start < peeled && claim > d.layout.Start && (walk.cut || walk.stop <= d.layout.Start) {
		warnings = core.WarnTruncated(warnings, fmt.Sprintf("the last ADTS frame, by %d byte(s),", claim-d.layout.Start))
	}
	if headed {
		sbr, ps, err := detectSBR(ctx, src, d.firstFrameOff, d.layout.Start, d.header, limit)
		if err != nil {
			return nil, err
		}
		d.track = buildTrack(d.header, walk.samples, walk.bytes, sbr, ps)
	}
	// Non-empty essence with zero TotalSamples: not ADTS audio. Shared no-audio
	// code (see internal/mp3/parse.go); dump/lint flag, set/plan/verify refuse.
	if d.track.TotalSamples == 0 && essence > 0 {
		warnings = core.Warn(warnings, core.WarnNoAudioFrames,
			"no ADTS audio frames found; file may not be audio")
	}
	if d.track.Codec == "" {
		d.track.Codec = "AAC"
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

	media.Properties = core.Properties{Container: "AAC (ADTS)", Tracks: []core.AudioTrack{d.track}}
	media.Warnings = warnings
	if err := core.SetIdentity(src, media); err != nil {
		return nil, err
	}
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
// sample count and byte span from [adtsWalk]. Duration and average
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

// adtsWalk is a walk of ADTS frames, kept so that a second walk goes on from the first's last frame.
type adtsWalk struct {
	samples uint64 // of the whole frames counted
	bytes   int64  // their bytes
	stop    int64  // where the walk stopped
	cut     bool   // the last walk stopped at a frame that runs past its end, as a cut stream's last does
	over    int64  // where that frame declares it ends
	joins   int    // times the stream went on past ID3 tags
	tags    int    // the tags it went on past, which stay in the stream

	last                  int64 // where the last frame counted starts, -1 for none
	lastSamples, lastSize int64 // what it added
}

// rewind takes back the last frame the walk counted, so that the next walk reads it again.
func (w *adtsWalk) rewind() {
	if w.last >= 0 {
		w.stop, w.samples, w.bytes, w.last = w.last, w.samples-uint64(w.lastSamples), w.bytes-w.lastSize, -1
	}
}

// walk goes on from w.stop through the ADTS frames before end of the stream whose first header is
// first. Each frame contributes samplesPerAACFrame*(rawBlocks+1). Reads headers only (advance by
// frame_length), a window at a time. A bad header stops the walk unless the stream goes on past ID3
// tags there ([joinedAt]), which stay in the stream: at most maxJoins times, the tags and the
// headers after them read from the window when it holds them. Trailing non-ADTS is excluded. Honors
// ctx per window and refuses a window over limit; reads as [bits.FillWithin] does, so a window past
// the end of the file ends the walk and a read that fails inside it is an error.
func (w *adtsWalk) walk(ctx context.Context, src core.ReaderAtSized, end, limit int64, maxJoins int, first adtsHeader) error {
	w.cut = false
	if end-w.stop < int64(adtsHeaderSize) {
		return nil
	}
	bufSize := min(end-w.stop, adtsScanChunk)
	if err := bits.CheckAlloc(bufSize, limit); err != nil {
		return err
	}
	buf := make([]byte, bufSize)
windows:
	for w.stop+int64(adtsHeaderSize) <= end {
		if e := ctx.Err(); e != nil {
			return e
		}
		off := w.stop
		n := min(end-off, int64(len(buf)))
		ok, err := bits.FillWithin(src, buf[:n], off)
		if err != nil || !ok {
			return err
		}
		window := buf[:n]
		p := 0
		for p+adtsHeaderSize <= len(window) {
			h, ok := decodeADTS(window[p:])
			if !ok {
				if maxJoins > 0 && w.joins >= maxJoins {
					w.stop = off + int64(p)
					return nil
				}
				in := &windowAt{b: window, off: off, src: src}
				at, tags, err := joinedAt(in, off+int64(p), end, first, limit)
				if in.missed {
					if p > 0 {
						break // refill at the bad header, so that the window holds what follows it
					}
					// Tags longer than a window: their headers are read from the file.
					in.fill, in.missed = true, false
					at, tags, err = joinedAt(in, off+int64(p), end, first, limit)
				}
				if err != nil {
					return err
				}
				if at < 0 {
					w.stop = off + int64(p) // stop at last good frame
					return nil
				}
				w.joins, w.tags = w.joins+1, w.tags+tags
				if at-off >= int64(len(window)) {
					w.stop = at
					continue windows
				}
				p = int(at - off)
				continue
			}
			if off+int64(p)+int64(h.frameLength) > end {
				w.stop, w.cut, w.over = off+int64(p), true, off+int64(p)+int64(h.frameLength) // truncated tail
				return nil
			}
			w.last, w.lastSamples, w.lastSize = off+int64(p), int64(samplesPerAACFrame*(h.rawBlocks+1)), int64(h.frameLength)
			w.samples += uint64(w.lastSamples)
			w.bytes += w.lastSize
			p += h.frameLength
		}
		if p == 0 {
			break
		}
		w.stop = off + int64(p)
	}
	return nil
}

// windowAt reads b, a window of the file src at off, and with fill set reads past it from src.
// Without fill, a read b cannot serve fails with errMissed and marks missed, so the caller can
// refill the window or read the file instead.
type windowAt struct {
	b      []byte
	off    int64
	src    core.ReaderAtSized
	fill   bool
	missed bool
}

// errMissed is a read past the window, which the window does not make.
var errMissed = errors.New("read past the ADTS window")

// Size is the file's, so a read past its end is absent and never made.
func (r *windowAt) Size() int64 { return r.src.Size() }

func (r *windowAt) ReadAt(p []byte, off int64) (int, error) {
	if i := off - r.off; i >= 0 && i <= int64(len(r.b)) && int64(len(p)) <= int64(len(r.b))-i {
		return copy(p, r.b[i:]), nil
	}
	if !r.fill {
		r.missed = true
		return 0, errMissed
	}
	return r.src.ReadAt(p, off)
}

// joinedAt is where first's stream goes on past the ID3 tags at off, as a stream joined there opens
// with them ([id3.SkipTags]), and how many tags it passes: two frames of first's config right after
// them, read by their headers. It is -1 when no tag sits at off or no such frames follow.
func joinedAt(src core.ReaderAtSized, off, end int64, first adtsHeader, limit int64) (int64, int, error) {
	at, tags, err := id3.SkipTags(src, off, end, limit)
	if err != nil || at == off {
		return -1, 0, err
	}
	h, ok, err := headerAt(src, at, end, limit)
	if err != nil || !ok || !sameConfig(first, h) {
		return -1, 0, err
	}
	n, ok, err := headerAt(src, at+int64(h.frameLength), end, limit)
	if err != nil || !ok || !sameConfig(first, n) {
		return -1, 0, err
	}
	return at, tags, nil
}

// headerAt decodes the ADTS header at off, before end, read as [bits.ReadWithin] reads: one past the
// end of the file holds no header, and a failed read or one over limit is an error.
func headerAt(src core.ReaderAtSized, off, end, limit int64) (adtsHeader, bool, error) {
	if end-off < int64(adtsHeaderSize) {
		return adtsHeader{}, false, nil
	}
	b, err := bits.ReadWithin(src, off, int64(adtsHeaderSize), limit)
	if err != nil || b == nil {
		return adtsHeader{}, false, err
	}
	h, ok := decodeADTS(b)
	return h, ok, nil
}
