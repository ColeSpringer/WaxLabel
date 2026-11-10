package core

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/tag"
)

// TextItem is one COMMENT or LYRICS value with its qualifiers.
type TextItem struct{ Text, Description, Language string }

// TextItemLoss names the qualifiers a destination drops from COMMENT and LYRICS items. On
// those keys' field [Capability].
type TextItemLoss uint8

const (
	// TextLossDescriptionAndLanguage: text only. The zero value, so a capability that does not say
	// claims the loss.
	TextLossDescriptionAndLanguage TextItemLoss = iota
	// TextLossDescription: language preserved, description dropped (Matroska TagLanguage).
	TextLossDescription
	// TextLossNone: description and language preserved (ID3 COMM/USLT).
	TextLossNone
)

// TextItemsAs converts comment or lyrics items between this package's spelling and a public
// one of the same fields; nil for none.
func TextItemsAs[To, From ~struct{ Text, Description, Language string }](items []From) []To {
	if len(items) == 0 {
		return nil
	}
	out := make([]To, len(items))
	for i, it := range items {
		out[i] = To(it)
	}
	return out
}

// TextsOf returns the text of each item, in order.
func TextsOf(items []TextItem) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Text
	}
	return out
}

// TextItemLossReason words the qualifiers writing items under loss drops, "" when none is set:
// the transfer grade's reason and the edit warning's text.
func TextItemLossReason(items []TextItem, loss TextItemLoss) string {
	var desc, lang bool
	for _, it := range items {
		desc = desc || it.Description != "" && loss != TextLossNone
		lang = lang || it.Language != "" && loss == TextLossDescriptionAndLanguage
	}
	switch {
	case desc && lang:
		return "description and language not carried"
	case desc:
		return "description not carried"
	case lang:
		return "language not carried"
	}
	return ""
}

// QualifierStoredAs finds the first description or language of requested that stored, the items
// a write leaves, holds in another form, among the qualifiers loss keeps: a language cut to a
// fixed field, or one that is the format's undefined marker. A language's case is not compared.
func QualifierStoredAs(requested, stored []TextItem, loss TextItemLoss) (qualifier, want, got string, differs bool) {
	for i, at := range pairTextItems(stored, TextsOf(requested)) {
		if at < 0 || stored[at].Text != requested[i].Text {
			continue
		}
		r, s := requested[i], stored[at]
		if loss == TextLossNone && r.Description != "" && s.Description != r.Description {
			return "description", r.Description, s.Description, true
		}
		if loss != TextLossDescriptionAndLanguage && r.Language != "" && !strings.EqualFold(s.Language, r.Language) {
			return "language", r.Language, s.Language, true
		}
	}
	return "", "", "", false
}

// SyncTextItems aligns m.Comments and m.Lyrics with the COMMENT and LYRICS values
// ([AlignTextItems]).
func SyncTextItems(m *Media) {
	m.Comments = AlignTextItems(m.Comments, m.Tags, tag.Comment)
	m.Lyrics = AlignTextItems(m.Lyrics, m.Tags, tag.Lyrics)
}

// AlignTextItems returns one item per value of key in ts, in value order; the values stay
// authoritative. An item whose text is still a value keeps its qualifiers there, wherever the
// value moved; a value with no such item takes the qualifiers of the first item left over, as an
// edit that replaced that item's text; the rest get bare items.
func AlignTextItems(items []TextItem, ts tag.TagSet, key tag.Key) []TextItem {
	vals, _ := ts.Get(key)
	if len(vals) == 0 {
		return nil
	}
	out := make([]TextItem, len(vals))
	for i, from := range pairTextItems(items, vals) {
		if from >= 0 {
			out[i] = items[from]
		}
		out[i].Text = vals[i]
	}
	return out
}

// pairTextItems returns, for each value, the index of the item [AlignTextItems] gives it, or -1,
// in time linear in both: a file may hold a hundred thousand comments.
func pairTextItems(items []TextItem, vals []string) []int {
	from := make([]int, len(vals))
	aligned := len(items) == len(vals)
	for i := 0; aligned && i < len(vals); i++ {
		aligned = items[i].Text == vals[i]
	}
	if aligned {
		for i := range from {
			from[i] = i
		}
		return from
	}
	byText := make(map[string][]int, len(items)) // the items holding a text, first to last
	for j, it := range items {
		byText[it.Text] = append(byText[it.Text], j)
	}
	used := make([]bool, len(items))
	for i, v := range vals {
		from[i] = -1
		if q := byText[v]; len(q) > 0 {
			from[i], used[q[0]], byText[v] = q[0], true, q[1:]
		}
	}
	next := 0
	for i := range vals {
		if from[i] >= 0 {
			continue
		}
		for next < len(items) && used[next] {
			next++
		}
		if next < len(items) {
			from[i], used[next] = next, true
		}
	}
	return from
}

// WithoutLanguage returns items with the language of each that holds lang cleared: how a carry
// treats the language a source format's writer fills in.
func WithoutLanguage(items []TextItem, lang string) []TextItem {
	out := slices.Clone(items)
	for i := range out {
		if out[i].Language == lang {
			out[i].Language = ""
		}
	}
	return out
}

// CarryTextItems returns src's items as a carry onto a file holding dst writes them: each keeps
// what src's format stores of it (srcLoss says what it does not) and takes the rest from the dst
// item of the same text, so a source with no field for a description leaves the destination's.
func CarryTextItems(src, dst []TextItem, srcLoss TextItemLoss) []TextItem {
	byText := make(map[string][]int, len(dst)) // the dst items holding a text, first to last
	for j, it := range dst {
		byText[it.Text] = append(byText[it.Text], j)
	}
	out := make([]TextItem, len(src))
	for i, s := range src {
		out[i].Text = s.Text
		if q := byText[s.Text]; len(q) > 0 {
			out[i], byText[s.Text] = dst[q[0]], q[1:]
		}
		if srcLoss == TextLossNone {
			out[i].Description = s.Description
		}
		if srcLoss != TextLossDescriptionAndLanguage {
			out[i].Language = s.Language
		}
	}
	return out
}

// EqualTextItems reports whether a and b hold the same comment and lyrics items.
func EqualTextItems(a, b *Media) bool {
	return slices.Equal(a.Comments, b.Comments) && slices.Equal(a.Lyrics, b.Lyrics)
}

// QualifiersDistinct reports whether several items are told apart by description or language,
// as a lyric and its translations are: no two share both.
func QualifiersDistinct(items []TextItem) bool {
	seen := make(map[[2]string]bool, len(items))
	for _, it := range items {
		k := [2]string{it.Description, it.Language}
		if seen[k] {
			return false
		}
		seen[k] = true
	}
	return len(items) > 1
}

// SingleValuedMulti is [tag.Key.SingleValuedMulti] for a key holding count values in a file
// whose lyrics items are lyrics: lyrics a description or language tells apart are a text and its
// translations, not several values of a single-valued key. Lint, the plan warning and dump share it.
func SingleValuedMulti(k tag.Key, count int, lyrics []TextItem) bool {
	return k.SingleValuedMulti(count) && !(k == tag.Lyrics && QualifiersDistinct(lyrics))
}

// Kept names the qualifiers l leaves on an item, in a fixed order.
func (l TextItemLoss) Kept() []string {
	switch l {
	case TextLossNone:
		return []string{"description", "language"}
	case TextLossDescription:
		return []string{"language"}
	}
	return nil
}

// Both returns the loss of a pair of formats: a qualifier is kept only where l and o both keep it.
func (l TextItemLoss) Both(o TextItemLoss) TextItemLoss { return min(l, o) }

// TextItemChanges reports the description and language changes of the key's items a and b both
// hold ([AlignTextItems] pairs them), one per item and qualifier, under the synthetic keys
// "<KEY> description" and "<KEY> language". A qualifier loss drops is not compared, nor, with
// unsetLanguageMatches, a language either side leaves unset (ID3 fills in eng for it).
func TextItemChanges(key tag.Key, a, b []TextItem, loss TextItemLoss, unsetLanguageMatches bool) []tag.Change {
	var out []tag.Change
	note := func(qualifier, from, to string) {
		if from != to {
			out = append(out, tag.Change{Key: key + " " + tag.Key(qualifier), Kind: tag.ChangeChanged,
				Old: []string{from}, New: []string{to}, Quote: true})
		}
	}
	for i, from := range pairTextItems(a, TextsOf(b)) {
		if from < 0 {
			continue
		}
		if loss == TextLossNone {
			note("description", a[from].Description, b[i].Description)
		}
		la, lb := a[from].Language, b[i].Language
		if loss != TextLossDescriptionAndLanguage && !(unsetLanguageMatches && (la == "" || lb == "")) {
			note("language", la, lb)
		}
	}
	return out
}

// LowerASCII folds A-Z to lowercase and leaves every other byte alone: the form a language code
// is modeled in, whatever case a file holds.
func LowerASCII(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

// SanitizeUTF8 replaces invalid UTF-8 with U+FFFD. No-op on valid input.
func SanitizeUTF8(s string) string {
	return strings.ToValidUTF8(s, string(utf8.RuneError))
}

// IsTranscoderStamp reports Lavf/Lavc/libav* encoder stamps from transcodes.
func IsTranscoderStamp(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "lavf") || strings.Contains(s, "libavformat") ||
		strings.Contains(s, "lavc") || strings.Contains(s, "libavcodec")
}

// IndefiniteArticle returns "a" or "an" before a format name. MP3/MP4 use "an".
func IndefiniteArticle(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "a"
	}
	// MP* names read "em-pee" (vowel sound).
	if len(name) >= 2 && (name[0] == 'M' || name[0] == 'm') && (name[1] == 'P' || name[1] == 'p') {
		return "an"
	}
	switch name[0] {
	case 'a', 'e', 'i', 'o', 'u', 'A', 'E', 'I', 'O', 'U':
		return "an"
	}
	return "a"
}

// Fold delegates to [tag.Fold] for shared case/space-insensitive compare.
func Fold(s string) string { return tag.Fold(s) }

// ContainsFold reports whether vals holds value under fold compare.
func ContainsFold(vals []string, value string) bool {
	for _, v := range vals {
		if EqualFoldValue(v, value) {
			return true
		}
	}
	return false
}

// EqualFoldValue compares values case-insensitively with trimmed space.
func EqualFoldValue(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// FoldValueKey is the index key for [EqualFoldValue]. Used by [FamilySelector].
func FoldValueKey(s string) string {
	var b strings.Builder
	s = strings.TrimSpace(s)
	b.Grow(len(s))
	for _, r := range s {
		lo := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < lo {
				lo = f
			}
		}
		b.WriteRune(lo)
	}
	return b.String()
}

// FamilySelected reports whether a secondary value matches any authoritative value for key.
func FamilySelected(auth tag.TagSet, key tag.Key, value string) bool {
	if !auth.Has(key) {
		return true
	}
	// AnyValue avoids cloning the full value list per native item.
	return auth.AnyValue(key, func(v string) bool { return EqualFoldValue(v, value) })
}

// FamilySelector caches [FamilySelected] per key via [FoldValueKey] index.
func FamilySelector(auth tag.TagSet) func(tag.Key, string) bool {
	index := map[tag.Key]map[string]bool{}
	return func(key tag.Key, value string) bool {
		if !auth.Has(key) {
			return true
		}
		keys, built := index[key]
		if !built {
			keys = map[string]bool{}
			auth.AnyValue(key, func(v string) bool {
				keys[FoldValueKey(v)] = true
				return false
			})
			index[key] = keys
		}
		return keys[FoldValueKey(value)]
	}
}
