package tail

import (
	"fmt"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/tag"
)

// Segments copies the tail a writer keeps after the audio, in file order: the junk always, the
// tags unless strip. Adjacent regions copy as one.
func Segments(l Layout, strip bool) []bits.Segment {
	var segs []bits.Segment
	for _, r := range l.Regions {
		if strip && r.Kind != KindJunk {
			continue
		}
		if n := len(segs); n > 0 && segs[n-1].Off+segs[n-1].Len == r.Off {
			segs[n-1].Len += r.Len
		} else {
			segs = append(segs, bits.Copy(r.Off, r.Len))
		}
	}
	return segs
}

// Operations names what a write that copies l with [Segments] does with each kind of tag in it, every
// one legacy: "trailing <kind> preservation", or with strip "trailing <kind> strip", and how many there
// are when more than one, stacked for ID3v1.
func Operations(l Layout, strip bool) []string {
	verb := "preservation"
	if strip {
		verb = "strip"
	}
	var ops []string
	for _, k := range []Kind{KindAPEv2, KindLyrics3v1, KindLyrics3v2, KindID3v1} {
		n := 0
		for _, r := range l.Regions {
			if r.Kind == k {
				n += max(r.Blocks, 1)
			}
		}
		switch {
		case n == 1:
			ops = append(ops, fmt.Sprintf("trailing %s %s", k, verb))
		case k == KindID3v1 && n > 1:
			ops = append(ops, fmt.Sprintf("trailing %s %s (%d stacked tags)", k, verb, n))
		case n > 1:
			ops = append(ops, fmt.Sprintf("trailing %s %s (%d tags)", k, verb, n))
		}
	}
	return ops
}

// Project fills m from l as the tail of a stream starting at start whose every tag is legacy: the
// essence ([Layout.SetEssence]), the family view behind m.Tags, which is set first, and the opaque
// flag. It returns the tail's warnings, [Warnings] with subject then legacy-ape for an APE tag, and
// how many bytes the essence covers, the figure an average bitrate counts.
func (l Layout) Project(m *core.Media, start int64, subject string) ([]core.Warning, int64) {
	audioLen := l.SetEssence(m, start)
	return l.ProjectLegacy(m, subject), audioLen
}

// ProjectLegacy is [Layout.Project] without the essence, for a stream that places its own: the
// family view, the opaque flag and the warnings.
func (l Layout) ProjectLegacy(m *core.Media, subject string) []core.Warning {
	m.Families = append(m.Families, Families(m.Tags, l)...)
	m.LegacyOpaqueContent = l.Opaque()
	ws := Warnings(l, subject)
	if l.Tag() != nil {
		ws = core.Warn(ws, core.WarnLegacyAPE, "APEv2 tag present; preserved")
	}
	return ws
}

// Entries lists the regions of l for the native view in file order, every tag in it legacy, and the
// junk a known end finds the stream going on with across a tag as audioKind, the codec's audio.
func (l Layout) Entries(audioKind string) []core.NativeEntry {
	out := make([]core.NativeEntry, 0, len(l.Regions))
	for _, r := range l.Regions {
		if r.Kind == KindJunk && r.audio && l.EndKnown {
			out = append(out, core.NativeEntry{Kind: audioKind, Size: int(r.Len), Note: "the stream going on across a tag, kept in the essence"})
			continue
		}
		out = append(out, r.entry())
	}
	return out
}

// entry describes r for the native view, a tag as legacy.
func (r Region) entry() core.NativeEntry {
	switch r.Kind {
	case KindAPEv2:
		return core.NativeEntry{Kind: fmt.Sprintf("APEv%d", r.Tag.Version/1000), Size: int(r.Len),
			Note: fmt.Sprintf("%d items, legacy, preserved", len(r.Tag.Items))}
	case KindID3v1:
		note := "legacy, preserved"
		if r.Blocks > 1 {
			note = fmt.Sprintf("%d stacked tags, legacy, preserved", r.Blocks)
		}
		return core.NativeEntry{Kind: "ID3v1", Size: int(r.Len), Note: note}
	case KindLyrics3v1, KindLyrics3v2:
		return core.NativeEntry{Kind: r.Kind.String(), Size: int(r.Len), Note: "legacy, preserved, not read"}
	default:
		return core.NativeEntry{Kind: "trailing bytes", Size: int(r.Len), Note: "preserved"}
	}
}

// Families projects the tags of l into legacy family entries, none promoted into auth: every
// ID3v1 tag of each run and the text items of each APE tag, in file order. Lyrics3 is never
// projected. Every region counts as legacy; a codec that reads an APE tag as its own store
// passes l.Without(KindAPEv2).
func Families(auth tag.TagSet, l Layout) []core.FamilyValue {
	var out []core.FamilyValue
	for _, r := range l.Regions {
		switch r.Kind {
		case KindID3v1:
			out = append(out, id3.LegacyV1Families(auth, r.Raw)...)
		case KindAPEv2:
			var pairs []core.Contribution
			for _, p := range r.Tag.Pairs() {
				pairs = append(pairs, core.Contribution{Key: p.Key, Value: p.Value})
			}
			out = append(out, core.LegacyFamilies(auth, core.FamilyAPEv2, pairs)...)
		}
	}
	return out
}

// Warnings reports l: its ID3v1 tags with their count, in one warning however many runs hold them,
// each Lyrics3 block, the junk (subject places it, as [core.WarnTrailing] takes it, unless it stays
// in the essence, because the audio end is unknown or the stream goes on with it across a tag, when
// the APE tag before or after it places it), more than one APE tag, and a footer whose tag would
// start below the floor. Whether an APE tag is legacy is the codec's to report.
func Warnings(l Layout, subject string) []core.Warning {
	var ws []core.Warning
	if m := l.misplaced; m.Len > 0 {
		ws = core.Warn(ws, core.WarnMalformedTagEntry, fmt.Sprintf(
			"an APE tag footer ending at offset %d places its tag at offset %d, inside the stream header or first audio unit; the tag is not read",
			m.Off+m.Len, m.Off))
	}
	apes, tagged, v1Warned := 0, false, false
	for _, r := range l.Regions {
		switch r.Kind {
		case KindID3v1:
			if !v1Warned {
				ws = core.Warn(ws, core.WarnTrailingID3v1, l.id3v1Message())
				v1Warned = true
			}
		case KindLyrics3v1, KindLyrics3v2:
			ws = core.Warn(ws, core.WarnTrailingLyrics3,
				fmt.Sprintf("%s block of %d bytes follows the audio; preserved, not read", r.Kind, r.Len))
		case KindJunk:
			// Junk the essence keeps sits among the APE tags the stretch holds: after one, or before
			// the first.
			where := "before an APE tag"
			if tagged {
				where = "after an APE tag"
			}
			switch {
			case !l.EndKnown:
				ws = core.WarnTrailing(ws, r.Len, where,
					"belong to no tag and stay in the audio essence, since the audio end is unknown")
			case r.audio:
				ws = core.WarnTrailing(ws, r.Len, where, "are audio the stream goes on with across the tag, kept in the audio essence")
			default:
				ws = core.WarnTrailing(ws, r.Len, subject, "belong to no tag")
			}
		case KindAPEv2:
			apes++
		}
		tagged = tagged || r.Kind != KindJunk
	}
	if apes > 1 {
		ws = core.Warn(ws, core.WarnDuplicateTagBlock,
			fmt.Sprintf("%d APE tags follow the audio; APE readers see only the one nearest the end of the file", apes))
	}
	return ws
}

// id3v1Message words the one warning for the ID3v1 tags of l: a lone tag, a stacked run, or the tags
// of runs that other regions keep apart, with how many places hold them.
func (l Layout) id3v1Message() string {
	tags, runs := 0, 0
	for _, r := range l.Regions {
		if r.Kind == KindID3v1 {
			tags, runs = tags+r.Blocks, runs+1
		}
	}
	switch {
	case runs > 1:
		return fmt.Sprintf("%d legacy ID3v1 tags follow the audio in %d places; preserved", tags, runs)
	case tags > 1:
		return fmt.Sprintf("%d stacked legacy ID3v1 tags follow the audio; preserved", tags)
	}
	return "legacy ID3v1 tag follows the audio; preserved"
}
