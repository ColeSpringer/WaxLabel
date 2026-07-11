package mp3

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/tag"
)

// scanWindow bounds how far past the audio start we look for the first MPEG
// frame and its VBR header.
const scanWindow = 64 << 10

// parse reads an MP3 file's metadata into a neutral Media: the front ID3v2 tag
// (authoritative, writable), the audio geometry and properties, and any trailing
// legacy containers (preserved, surfaced, warned).
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes
	d := &doc{size: size}
	var warnings []core.Warning

	// Front ID3v2 tag.
	tg, id3Len, err := id3.ReadFront(src, size, limit, opts.Limits.MaxElements)
	if err != nil {
		return nil, err
	}
	d.id3 = tg
	d.id3Len = id3Len
	d.audioStart = d.id3Len

	// Trailing legacy containers, from the end inward: ID3v1 (last 128 bytes),
	// then an APEv2 tag ending just before it. The strict LooksLikeID3v1 gate can miss a
	// genuine ID3v1 whose year is non-standard (a legacy writer's "90s") or whose bytes carry
	// a control byte; when it does, tailEnd stays at size and the APE probe below starts inside
	// that undetected trailer, so a coexisting APEv2 tag (the rare [audio][APE][odd-year ID3v1]
	// layout) is also missed and folded into essence. Accepted as the cost of not false-flagging
	// random audio; the strict gate belongs on this sniff, unlike the WAV/AIFF path whose
	// trailing tag sits at a declared chunk boundary.
	tailEnd := size
	// Scan backward in 128-byte steps while each block is a valid ID3v1 trailer, so a
	// double-stacked (or deeper) ID3v1 run some re-tagging tools leave behind is captured as one
	// contiguous block and fully stripped by a single --legacy strip, rather than needing one run
	// per block. The strict LooksLikeID3v1 gate is kept per block, so audio bytes that happen to
	// land on a 128-byte boundary are never mistaken for a tag.
	id3v1Start := size
	for id3v1Start-128 >= d.audioStart {
		block, err := bits.ReadSlice(src, id3v1Start-128, 128, limit)
		if err != nil || !id3.LooksLikeID3v1(block) {
			break
		}
		id3v1Start -= 128
	}
	if id3v1Start < size {
		if run, err := bits.ReadSlice(src, id3v1Start, size-id3v1Start, limit); err == nil {
			d.id3v1 = run
			tailEnd = id3v1Start
			// Report the run length: a re-tagging tool can leave several stacked ID3v1 tags, and a
			// singular message would understate what was found and preserved (or stripped).
			msg := "legacy ID3v1 tag follows the audio; preserved"
			if blocks := (size - id3v1Start) / 128; blocks > 1 {
				msg = fmt.Sprintf("%d stacked legacy ID3v1 tags follow the audio; preserved", blocks)
			}
			warnings = core.Warn(warnings, core.WarnTrailingID3v1, msg)
		}
	}
	var apeTag *ape.Tag
	if at, ok, _ := ape.ParseAt(src, tailEnd, limit, opts.Limits.MaxElements); ok && at.Offset >= d.audioStart {
		if apeBytes, err := bits.ReadSlice(src, at.Offset, at.Size, limit); err == nil {
			d.ape = apeBytes
			d.apeOffset = at.Offset
			d.apeTag = at
			tailEnd = at.Offset
			apeTag = at
			warnings = core.Warn(warnings, core.WarnLegacyAPE,
				"APEv2 tag present; preserved")
		}
	}
	d.audioEnd = tailEnd
	if d.audioEnd < d.audioStart {
		d.audioEnd = d.audioStart
	}

	// Audio properties from the first MPEG frame (and its VBR header, for length).
	win := d.audioEnd - d.audioStart
	if win > scanWindow {
		win = scanWindow
	}
	if window, err := bits.ReadSlice(src, d.audioStart, win, limit); err == nil {
		if info, ok := parseMPEG(window); ok {
			d.firstHeader = info.header
			d.track = buildTrack(info, d.audioEnd-d.audioStart)
			// A Xing/Info header declares the encoder's frame count, from which
			// buildTrack derived the playable duration; the average bitrate it then
			// computes spreads the bytes actually present over that declared duration.
			// When frames are missing (a truncated download), that average collapses
			// far below the 8 kbps MPEG floor - a reliable, zero-I/O truncation signal.
			// An extreme truncation (a multi-minute declared duration with only the
			// ~48-byte header left) drives the integer average to 0, so the test is a
			// bare "< 8000" rather than "> 0 && < 8000": inside this block a frame was
			// found, so the bytes present are real, and a 0 here means truncation, not
			// "unknown". A CBR stream without a Xing count carries no declared length to
			// check against, so that case is undetectable here and is left unflagged
			// rather than risk a false positive on a valid file.
			if info.vbrFrames > 0 && d.track.Bitrate < 8000 {
				warnings = core.Warn(warnings, core.WarnTruncatedAudio,
					"fewer audio frames than the Xing/Info header declares; file may be truncated")
			}
		} else if d.audioEnd > d.audioStart {
			// A non-empty essence region that yields no MPEG frame is a.mp3 that is not
			// actually MPEG audio (text, a renamed file). Surface it under the shared
			// no-audio code so dump/lint flag it instead of accepting it silently. This is
			// distinct from the zero-essence no-audio in the root parse (which fires only
			// when the range is empty), so the two never double-warn. The parser leaves the
			// bytes intact (the file stays dumpable and usable as a copy source), but this
			// warning is the no-audio gate's signal: set/plan and verify now refuse the
			// file (ErrInvalidData, exit 4) rather than rewrite metadata around non-audio
			// bytes or hash them as essence.
			warnings = core.Warn(warnings, core.WarnNoAudioFrames,
				"no MPEG audio frames found; file may not be audio")
		}
	}
	if d.track.Codec == "" {
		d.track.Codec = "MPEG audio"
	}

	media := &core.Media{
		Format:     core.FormatMP3,
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
		media.SyncedLyrics = proj.SyncedLyrics
		media.Families = proj.Families
		if proj.NumericGenre {
			warnings = core.Warn(warnings, core.WarnNumericGenre,
				"a numeric genre reference was resolved to a name")
		}
		warnings = append(warnings, proj.Warnings...)
		warnings = append(warnings, id3.EncoderNoise(d.id3)...)
	}

	// Legacy family/source entries (ID3v1, APEv2): surfaced for the family view,
	// flagged as conflicts when they disagree with the authoritative ID3v2 value.
	media.Families = append(media.Families, legacyFamilies(media.Tags, d.id3v1, apeTag)...)

	// An APEv2 carrying a binary/cover/locator item (NonText) holds content Pairs()
	// skips, so it is not projected as a tag or family and a legacy strip cannot prove
	// it fully redundant. Mark it so the safe fix preserves it and dump surfaces it.
	// ID3v1 is pure text and never opaque.
	media.LegacyOpaqueContent = apeHasNonText(apeTag)

	media.Properties = core.Properties{Container: "MP3", Tracks: []core.AudioTrack{d.track}}
	media.Warnings = warnings
	media.Identity = core.Identity{Size: size}
	media.Identity.Fingerprint, media.Identity.HasFinger = core.Fingerprint(src, media, limit)
	return media, nil
}

// buildTrack assembles the audio properties, computing an accurate duration from
// a VBR frame count when present, else from the (constant) frame bitrate.
func buildTrack(info mpegInfo, audioBytes int64) core.AudioTrack {
	t := core.AudioTrack{Codec: info.codec, SampleRate: info.sampleRate, Channels: info.channels}
	switch {
	case info.vbrFrames > 0 && info.sampleRate > 0:
		t.TotalSamples = uint64(info.vbrFrames) * uint64(info.samplesPerFrame)
		t.Duration = core.SamplesToDuration(t.TotalSamples, info.sampleRate)
		t.Bitrate = core.AverageBitrate(audioBytes, t.Duration.Seconds())
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

// apeHasNonText reports whether an APEv2 tag carries a binary/cover/locator item (NonText),
// which Pairs() skips, so it is neither a projected tag nor a family and a legacy strip cannot
// prove it redundant. Shared by parse and the post-write result builder so the two agree on when
// an APEv2 counts as opaque legacy content. A nil tag is never opaque.
func apeHasNonText(t *ape.Tag) bool {
	if t == nil {
		return false
	}
	for _, it := range t.Items {
		if it.NonText {
			return true
		}
	}
	return false
}

// legacyFamilies builds family/source entries for the trailing ID3v1 and APEv2
// containers. Each entry is marked unselected (a conflict) when its value
// disagrees with the authoritative ID3v2 value for the same key.
func legacyFamilies(auth tag.TagSet, id3v1 []byte, apeTag *ape.Tag) []core.FamilyValue {
	var out []core.FamilyValue
	add := func(key tag.Key, value string, fam core.Family) {
		out = append(out, core.FamilyValue{
			Key: key, Family: fam, Scope: core.ScopeTrack,
			Values: []string{value}, Selected: core.FamilySelected(auth, key, value), Legacy: true,
		})
	}
	if v1, ok := id3.ParseV1(id3v1); ok {
		for _, p := range v1.Pairs() {
			add(p.Key, p.Value, core.FamilyID3v1)
		}
	}
	if apeTag != nil {
		for _, p := range apeTag.Pairs() {
			add(p.Key, p.Value, core.FamilyAPEv2)
		}
	}
	return out
}
