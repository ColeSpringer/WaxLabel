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

// parse reads MP3 metadata: front ID3v2 (authoritative), audio geometry, trailing
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
	tg, id3Len, frontWs, err := id3.ReadFront(src, size, limit, opts.Limits.MaxElements)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, frontWs...)
	d.id3 = tg
	d.id3Len = id3Len
	d.audioStart = d.id3Len

	// Trailing legacy inward: ID3v1 then APEv2 before it. Strict LooksLikeID3v1 can miss
	// odd-year/control-byte trailers (and coexisting APE); accepted vs false-flagging audio.

	tailEnd := size
	// Scan back in 128-byte steps for stacked ID3v1 runs (one --legacy strip). Strict
	// gate per block so audio on a 128-byte boundary is not mistaken for a tag.

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
			// Xing frame count → duration → average bitrate. Truncation: average falls
			// below 8 kbps (incl. 0). CBR without Xing count: undetectable here.

			if info.vbrFrames > 0 && d.track.Bitrate < 8000 {
				warnings = core.Warn(warnings, core.WarnTruncatedAudio,
					"fewer audio frames than the Xing/Info header declares; file may be truncated")
			}
		} else if d.audioEnd > d.audioStart {
			// Non-empty essence with no MPEG frame: not audio. Shared no-audio warning;
			// set/plan/verify refuse (distinct from empty-range no-audio).

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
		if it.NonText() {
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
