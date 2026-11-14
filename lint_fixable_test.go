package waxlabel

import (
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// The rule that says whether the lint fix ends a conflict, on family views the fixtures of
// tests/lint_test.go do not produce. An entry is one store's values for the key; unselected
// means in conflict.
func TestSafeFixSettles(t *testing.T) {
	entry := func(key tag.Key, family core.Family, selected, legacy bool, values ...string) core.FamilyValue {
		return core.FamilyValue{Key: key, Family: family, Values: values, Selected: selected, Legacy: legacy}
	}
	const stamp = "Lavf61.7.100"
	for name, c := range map[string]struct {
		fix  safeFix
		key  tag.Key
		fams []core.FamilyValue
		want bool
	}{
		"a second store's stamp no finding reports stays": {
			safeFix{}, tag.Encoder,
			[]core.FamilyValue{entry(tag.Encoder, core.FamilyMP4, true, false, "Clean"), entry(tag.Encoder, core.FamilyMP4, false, false, stamp)},
			false,
		},
		"a second store's stamp goes with the vendor string's": {
			safeFix{encoder: true}, tag.Encoder,
			[]core.FamilyValue{entry(tag.Encoder, core.FamilyID3v2, true, false, "Clean"), entry(tag.Encoder, core.FamilyRIFF, false, false, stamp)},
			true,
		},
		"a second store's other value stays": {
			safeFix{encoder: true}, tag.Encoder,
			[]core.FamilyValue{entry(tag.Encoder, core.FamilyID3v2, true, false, "Clean"), entry(tag.Encoder, core.FamilyRIFF, false, false, "Other")},
			false,
		},
		"a store at odds with itself stays, a stamp among its values or not": {
			safeFix{encoder: true}, tag.Encoder,
			[]core.FamilyValue{entry(tag.Encoder, core.FamilyVorbis, false, false, stamp, "Other")},
			false,
		},
		"a legacy stamp the fix keeps stays": {
			safeFix{encoder: true}, tag.Encoder,
			[]core.FamilyValue{entry(tag.Encoder, core.FamilyVorbis, true, false, "Clean"), entry(tag.Encoder, core.FamilyID3v2, false, true, stamp)},
			false,
		},
		"another key's value that reads like a stamp stays": {
			safeFix{encoder: true}, tag.Title,
			[]core.FamilyValue{entry(tag.Title, core.FamilyID3v2, true, false, "A"), entry(tag.Title, core.FamilyRIFF, false, false, stamp)},
			false,
		},
		"another key is not rewritten with ENCODER": {
			safeFix{encoder: true, stamped: true}, tag.Title,
			[]core.FamilyValue{entry(tag.Title, core.FamilyID3v2, true, false, "A"), entry(tag.Title, core.FamilyAPEv2, false, true, "B")},
			false,
		},
		"a kept legacy value the rewritten tag holds too agrees": {
			safeFix{encoder: true, stamped: true, left: []string{"Clean"}}, tag.Encoder,
			[]core.FamilyValue{entry(tag.Encoder, core.FamilyID3v2, false, false, stamp, "Clean"), entry(tag.Encoder, core.FamilyAPEv2, true, true, "clean")},
			true,
		},
		"a kept legacy value the rewritten tag lacks does not": {
			safeFix{encoder: true, stamped: true, left: []string{"Clean"}}, tag.Encoder,
			[]core.FamilyValue{entry(tag.Encoder, core.FamilyID3v2, true, false, stamp, "Clean"), entry(tag.Encoder, core.FamilyAPEv2, false, true, stamp)},
			false,
		},
		"a kept legacy value beside a tag left empty conflicts with nothing": {
			safeFix{encoder: true, stamped: true}, tag.Encoder,
			[]core.FamilyValue{entry(tag.Encoder, core.FamilyID3v2, true, false, stamp), entry(tag.Encoder, core.FamilyAPEv2, false, true, "Other")},
			true,
		},
		"a stripped legacy container takes its stamp with it": {
			safeFix{stripLegacy: true}, tag.Encoder,
			[]core.FamilyValue{entry(tag.Encoder, core.FamilyID3v2, true, false, "Clean"), entry(tag.Encoder, core.FamilyAPEv2, false, true, stamp)},
			true,
		},
		"a kept one does not": {
			safeFix{}, tag.Title,
			[]core.FamilyValue{entry(tag.Title, core.FamilyID3v2, true, false, "A"), entry(tag.Title, core.FamilyAPEv2, false, true, "B")},
			false,
		},
	} {
		if got := c.fix.settles(c.fams, c.key); got != c.want {
			t.Errorf("%s: settles = %v, want %v", name, got, c.want)
		}
	}
}

// The names the fix writes back with ENCODER are the file's own: not a legacy container's,
// which an edit leaves alone, not a targeted tag's, and no stamp.
func TestSafeFixKeepsTheFilesOwnEncoderNames(t *testing.T) {
	const stamp = "Lavf61.7.100"
	tags := tag.NewTagSet()
	tags.Set(tag.Encoder, stamp, "Read 1")
	doc := &Document{media: &core.Media{Tags: tags, Families: []core.FamilyValue{
		{Key: tag.Encoder, Family: core.FamilyID3v2, Values: []string{stamp, "Read 1"}, Selected: true},
		{Key: tag.Encoder, Family: core.FamilyRIFF, Values: []string{"Own 2", "read 1", "Lavc61.19.101"}},
		{Key: tag.Encoder, Family: core.FamilyMatroska, Values: []string{"A chapter's"}, Selected: true, Targeted: true},
		{Key: tag.Encoder, Family: core.FamilyAPEv2, Values: []string{"Legacy 3"}, Legacy: true},
		{Key: tag.Title, Family: core.FamilyRIFF, Values: []string{"No encoder"}},
	}}}
	fix := doc.safeFix([]Finding{{Code: "inherited-encoder"}}, nil)
	if want := []string{"Read 1", "Own 2"}; !fix.stamped || !slices.Equal(fix.left, want) {
		t.Errorf("stamped %v, left %q; want %q", fix.stamped, fix.left, want)
	}
	// With no stamp among the values ENCODER reads with, the key is not edited at all.
	tags.Set(tag.Encoder, "Read 1")
	if fix := doc.safeFix([]Finding{{Code: "inherited-encoder"}}, nil); fix.stamped || !slices.Equal(fix.left, []string{"Read 1"}) {
		t.Errorf("no stamp read: stamped %v, left %q", fix.stamped, fix.left)
	}
}

// A legacy container is redundant, and stripped, only while it holds no value of its own: a
// copy of what the tags hold, or a transcoder stamp, is none.
func TestLegacyValueOfItsOwn(t *testing.T) {
	const stamp = "Lavf61.7.100"
	legacy := func(key tag.Key, selected bool, values ...string) core.FamilyValue {
		return core.FamilyValue{Key: key, Family: core.FamilyAPEv2, Values: values, Selected: selected, Legacy: true}
	}
	for name, c := range map[string]struct {
		fams []core.FamilyValue
		want bool
	}{
		"a copy of the title":              {[]core.FamilyValue{legacy(tag.Title, true, "A")}, false},
		"another title":                    {[]core.FamilyValue{legacy(tag.Title, false, "B")}, true},
		"a clean encoder name":             {[]core.FamilyValue{legacy(tag.Encoder, false, "Clean")}, true},
		"another stamp":                    {[]core.FamilyValue{legacy(tag.Encoder, false, stamp)}, false},
		"a stamp under another key":        {[]core.FamilyValue{legacy(tag.Title, false, stamp)}, true},
		"the format's own second store":    {[]core.FamilyValue{{Key: tag.Title, Family: core.FamilyRIFF, Values: []string{"B"}}}, false},
		"a copy beside a value of its own": {[]core.FamilyValue{legacy(tag.Title, true, "A"), legacy(tag.Artist, false, "C")}, true},
	} {
		if got := legacyValueOfItsOwn(c.fams); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
