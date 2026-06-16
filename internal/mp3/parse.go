package mp3

import (
	"context"
	"math"
	"strings"
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
	if hdr, err := bits.ReadSlice(src, 0, 10, limit); err == nil {
		if total, ok := id3.TagSize(hdr); ok && total <= size {
			tagBytes, err := bits.ReadSlice(src, 0, total, limit)
			if err != nil {
				return nil, err
			}
			tg, err := id3.ParseTag(tagBytes)
			if err != nil {
				return nil, err
			}
			d.id3 = tg
			d.id3Len = total
		}
	}
	d.audioStart = d.id3Len

	// Trailing legacy containers, from the end inward: ID3v1 (last 128 bytes),
	// then an APEv2 tag ending just before it.
	tailEnd := size
	if size-128 >= d.audioStart {
		if tail, err := bits.ReadSlice(src, size-128, 128, limit); err == nil && string(tail[:3]) == "TAG" {
			d.id3v1 = tail
			tailEnd = size - 128
			warnings = core.Warn(warnings, core.WarnTrailingID3v1,
				"legacy ID3v1 tag follows the audio; preserved")
		}
	}
	var apeTag *ape.Tag
	if at, ok, _ := ape.ParseAt(src, tailEnd, limit); ok && at.Offset >= d.audioStart {
		if apeBytes, err := bits.ReadSlice(src, at.Offset, at.Size, limit); err == nil {
			d.ape = apeBytes
			d.apeOffset = at.Offset
			d.apeTag = at
			tailEnd = at.Offset
			apeTag = at
			warnings = core.Warn(warnings, core.WarnLegacyAPE,
				"APEv2 tag present alongside ID3; preserved")
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
		media.Families = proj.Families
		if proj.NumericGenre {
			warnings = core.Warn(warnings, core.WarnNumericGenre,
				"a numeric genre reference was resolved to a name")
		}
		warnings = append(warnings, encoderNoise(d.id3)...)
	}

	// Legacy family/source entries (ID3v1, APEv2): surfaced for the family view,
	// flagged as conflicts when they disagree with the authoritative ID3v2 value.
	media.Families = append(media.Families, legacyFamilies(media.Tags, d.id3v1, apeTag)...)

	media.Properties = core.Properties{Container: "MP3", Tracks: []core.AudioTrack{d.track}}
	media.Warnings = warnings
	media.Identity = core.Identity{
		Size:        size,
		Fingerprint: bits.SHA256(bits.PrefixOrNil(src, d.audioStart, limit)),
		HasFinger:   true,
	}
	return media, nil
}

// buildTrack assembles the audio properties, computing an accurate duration from
// a VBR frame count when present, else from the (constant) frame bitrate.
func buildTrack(info mpegInfo, audioBytes int64) core.AudioTrack {
	t := core.AudioTrack{Codec: info.codec, SampleRate: info.sampleRate, Channels: info.channels}
	switch {
	case info.vbrFrames > 0 && info.sampleRate > 0:
		t.TotalSamples = uint64(info.vbrFrames) * uint64(info.samplesPerFrame)
		t.Duration = samplesToDuration(t.TotalSamples, info.sampleRate)
		if t.Duration > 0 {
			t.Bitrate = int(float64(audioBytes) * 8 / t.Duration.Seconds())
		}
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

// samplesToDuration converts a sample count at rate into a duration, guarding the
// int64-nanosecond range against a pathological count.
func samplesToDuration(samples uint64, rate int) time.Duration {
	if rate <= 0 {
		return 0
	}
	ns := float64(samples) / float64(rate) * float64(time.Second)
	if ns < 0 || ns >= math.MaxInt64 {
		return 0
	}
	return time.Duration(ns)
}

// legacyFamilies builds family/source entries for the trailing ID3v1 and APEv2
// containers. Each entry is marked unselected (a conflict) when its value
// disagrees with the authoritative ID3v2 value for the same key.
func legacyFamilies(auth tag.TagSet, id3v1 []byte, apeTag *ape.Tag) []core.FamilyValue {
	var out []core.FamilyValue
	add := func(key tag.Key, value string, fam core.Family) {
		// A legacy value conflicts only when the key is present in the
		// authoritative ID3v2 set and none of its values match — comparing against
		// just the first value would falsely flag a multi-value field (e.g. ID3v2
		// ARTIST=[A,B] vs an ID3v1 artist of "B").
		selected := true
		if avs, ok := auth.Get(key); ok && !containsFold(avs, value) {
			selected = false
		}
		out = append(out, core.FamilyValue{
			Key: key, Family: fam, Scope: core.ScopeTrack,
			Values: []string{value}, Selected: selected,
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

// containsFold reports whether vals holds value, comparing case- and
// space-insensitively.
func containsFold(vals []string, value string) bool {
	value = strings.TrimSpace(value)
	for _, v := range vals {
		if strings.EqualFold(strings.TrimSpace(v), value) {
			return true
		}
	}
	return false
}

// encoderNoise flags an inherited transcoder stamp in the TSSE/TENC frames
// (ffmpeg writes "Lavf..." there), the typical signature of an acquired file.
func encoderNoise(t *id3.Tag) []core.Warning {
	var ws []core.Warning
	for _, f := range t.Frames() {
		if f.ID != "TSSE" && f.ID != "TENC" {
			continue
		}
		for _, v := range id3.DecodeText(f) {
			low := strings.ToLower(v)
			if strings.Contains(low, "lavf") || strings.Contains(low, "libavformat") {
				ws = core.Warn(ws, core.WarnInheritedEncoder, "inherited encoder stamp: "+v)
			}
		}
	}
	return ws
}
