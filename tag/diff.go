package tag

import (
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/bits"
)

// NumericValuesEqual compares presentation equality for numeric/BPM/MP4-int keys
// (leading zeros; '+' on signed numerics; BPM via [BPMStoredWhole]). Compare-only.
func NumericValuesEqual(k Key, a, b []string) bool {
	if !IsNumericKey(k) && !IsMP4IntKey(k) && !IsBPMKey(k) {
		return slices.Equal(a, b)
	}
	if len(a) != len(b) {
		return false
	}
	canon := canonicalNumeric
	switch {
	case IsBPMKey(k):
		canon = canonicalBPMToken
	case IsMP4IntKey(k):
		canon = canonicalUnsignedToken
	}
	for i := range a {
		if !numericTokenEqual(a[i], b[i], canon) {
			return false
		}
	}
	return true
}

// numericTokenEqual compares one numeric value, splitting a slashed "n/total" pair (SplitNumberTotal)
// and comparing each side by its canonical form.
func numericTokenEqual(a, b string, canon func(string) string) bool {
	an, at := SplitNumberTotal(a)
	bn, bt := SplitNumberTotal(b)
	return canon(an) == canon(bn) && canon(at) == canon(bt)
}

// canonicalBPMToken folds a BPM token to the whole number tmpo stores, but only when storing
// does not round ([BPMStoredWhole]): "174.0", "0174", and "174.000000000000001" (ParseFloat
// collapses it to 174, so the atom stores it unwarned) all fold to "174". A rounded fraction,
// a signed value, or an invalid value compares verbatim (trimmed), so a warned rounding still
// reports and a value MP4 drops never equates to one it stores.
func canonicalBPMToken(s string) string {
	if stored, roundChanged, ok := BPMStoredWhole(s); ok && !roundChanged {
		return stored
	}
	return strings.TrimSpace(s)
}

// canonicalUnsignedToken is canonicalNumeric for the unsigned MP4-integer keys: leading zeros
// fold, a sign does not. Their atoms parse with ParseUint, which drops "+1"/"-1" rather than
// storing 1, so folding the sign would equate a dropped value with a stored one. A decimal
// needs no special case: canonicalNumeric leaves a non-integer verbatim, and the atoms drop it.
func canonicalUnsignedToken(s string) string {
	t := strings.TrimSpace(s)
	if len(t) > 0 && (t[0] == '+' || t[0] == '-') {
		return t
	}
	return canonicalNumeric(t)
}

// canonicalNumeric returns the canonical decimal form of a numeric token, dropping a leading '+'
// and leading zeros ("03" -> "3", "+3" -> "3", "-03" -> "-3", any all-zero form -> "0"). A token
// that is not a plain integer (empty, a sign with no digits, or any non-digit) is returned trimmed
// and verbatim, so a different or non-numeric value stays distinct. It works on the string, not a
// parsed int, so a value past the int range still canonicalizes its sign and leading zeros.
func canonicalNumeric(s string) string {
	s = strings.TrimSpace(s)
	digits := s
	neg := false
	if len(digits) > 0 && (digits[0] == '+' || digits[0] == '-') {
		neg = digits[0] == '-'
		digits = digits[1:]
	}
	if digits == "" || !isAllASCIIDigits(digits) {
		return s // not a plain integer: compare verbatim
	}
	// Drop leading zeros, keeping at least one digit.
	i := 0
	for i < len(digits)-1 && digits[i] == '0' {
		i++
	}
	digits = digits[i:]
	if digits == "0" {
		return "0" // an all-zero value is unsigned ("-0"/"+0"/"00" all fold to "0")
	}
	if neg {
		return "-" + digits
	}
	return digits
}

// isAllASCIIDigits reports whether s is non-empty and entirely ASCII digits.
func isAllASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ChangeKind names how one key differs between two tag sets.
type ChangeKind uint8

const (
	// ChangeUnknown is the zero value, so a never-set ChangeKind is detectably invalid.
	ChangeUnknown ChangeKind = iota
	// ChangeAdded marks a key present only in the edited set.
	ChangeAdded
	// ChangeRemoved marks a key present only in the base set.
	ChangeRemoved
	// ChangeChanged marks a key present in both but with different values.
	ChangeChanged
)

// String renders the kind as the diff(1)-style word used in both textual and
// machine-readable output ("added", "removed", "changed").
func (k ChangeKind) String() string {
	switch k {
	case ChangeAdded:
		return "added"
	case ChangeRemoved:
		return "removed"
	case ChangeChanged:
		return "changed"
	default:
		return "unknown"
	}
}

// Change is one key's difference between a base and an edited [TagSet]. Old holds
// the base values (set for a removed or changed key); New holds the edited values
// (set for an added or changed key).
//
// Count is set only for the synthetic picture/chapter set-count changes (the
// lowercase "pictures"/"chapters" pseudo-keys a write plan emits). It carries the
// count as a number, so a machine consumer need not parse the Old/New strings,
// which exist for the text render. It is zero for an ordinary tag-key change.
//
// Quote is set for the synthetic "<KEY> description" and "<KEY> language" changes of a comment
// or lyrics item: their values, which may be empty, render quoted.
type Change struct {
	Key   Key
	Kind  ChangeKind
	Old   []string
	New   []string
	Count int
	Quote bool
}

// Diff reports the per-key delta from base to edited. Removed and changed keys
// come first in base's order, then added keys in edited's order, so the result is
// stable and minimal. Values compare order-significantly, the same equality a
// codec uses to detect an edit, so a key present in both with identical values
// yields no Change. The CLI's diff command and the write-plan change preview
// share it.
func Diff(base, edited TagSet) []Change {
	var out []Change
	// Read the unexported fields directly: Keys/Get clone defensively, and only the
	// values kept in a Change need detached copies. Unchanged keys allocate nothing.
	for _, k := range base.order {
		ov := base.values[k]
		if nv, ok := edited.values[k]; ok {
			if !slices.Equal(ov, nv) {
				out = append(out, Change{Key: k, Kind: ChangeChanged, Old: slices.Clone(ov), New: slices.Clone(nv)})
			}
		} else {
			out = append(out, Change{Key: k, Kind: ChangeRemoved, Old: slices.Clone(ov)})
		}
	}
	for _, k := range edited.order {
		if _, ok := base.values[k]; !ok {
			out = append(out, Change{Key: k, Kind: ChangeAdded, New: slices.Clone(edited.values[k])})
		}
	}
	return out
}

// String renders one change as a diff-style line: "- KEY: old" for a removed
// key, "+ KEY: new" for an added one, "~ KEY: old -> new" for a changed one.
// Multiple values join with " | " as [ElideValues] joins them; a key with no
// values reads "(present, no value)". A long key is elided ([ElideKey]), and key
// and values pass through [SanitizeLine], so an embedded newline or tab cannot
// forge a row; read Key/Old/New for the exact bytes. No indent, no trailing
// newline. The plan/diff previews and the CLI share it.
func (c Change) String() string {
	key := SanitizeLine(ElideKey(c.Key))
	join := joinChangeValues
	if c.Quote {
		join = joinQuotedValues
	}
	switch c.Kind {
	case ChangeRemoved:
		return "- " + key + ": " + join(c.Old)
	case ChangeAdded:
		return "+ " + key + ": " + join(c.New)
	case ChangeChanged:
		return "~ " + key + ": " + join(c.Old) + " -> " + join(c.New)
	default:
		return ""
	}
}

// joinQuotedValues is joinChangeValues with each value in double quotes, so an empty one shows.
func joinQuotedValues(vals []string) string { return joinValues(vals, " | ", `"`) }

// joinChangeValues renders a key's values for a change line: "(present, no value)"
// for none, otherwise as [ElideValues] joins them with " | ". The exact values are in
// [Change.Old]/[Change.New].
func joinChangeValues(vals []string) string {
	if len(vals) == 0 {
		return "(present, no value)"
	}
	return joinValues(vals, " | ", "")
}

// ElideValues renders values for one line of human output: each elided ([ElideValue]) and
// escaped ([SanitizeLine]), joined with sep. Once the values shown pass the length one value
// is shown at, the rest are counted in their place ("…[+998 more]"), so a key with a thousand
// values leaves the line readable.
func ElideValues(vals []string, sep string) string { return joinValues(vals, sep, "") }

func joinValues(vals []string, sep, quote string) string {
	var b strings.Builder
	for i, v := range vals {
		if i > 0 {
			b.WriteString(sep)
		}
		if b.Len() > maxDisplayValueBytes {
			b.WriteString("…[+")
			b.WriteString(strconv.Itoa(len(vals) - i))
			b.WriteString(" more]")
			break
		}
		b.WriteString(quote)
		b.WriteString(SanitizeLine(ElideValue(v)))
		b.WriteString(quote)
	}
	return b.String()
}

// maxDisplayValueBytes bounds how much of one tag value a human-facing renderer
// prints. A longer value is elided to a prefix plus a length hint, so a pathological
// value (a 100k-character comment) cannot flood the terminal. Normal values, long
// lyrics and comments included, print whole. The structured accessors
// ([Change.Old]/[Change.New], [TagSet]) and --json keep the exact bytes.
const maxDisplayValueBytes = 4096

// ElideValue returns v shortened for human display when it exceeds
// maxDisplayValueBytes: the first maxDisplayValueBytes bytes (trimmed back to a
// UTF-8 rune boundary), an ellipsis, and a hint naming the elided remainder, e.g.
// "…[+94.0 KiB]". A value within the limit is returned unchanged. The change-line
// formatter ([Change.String]) and the CLI's tag/dump renderers share it. It runs
// before sanitizing, so the hint and boundary come from the real value and the
// caller's [SanitizeLine]/[SanitizeText] then escapes the result.
func ElideValue(v string) string { return ElideValueAt(v, maxDisplayValueBytes) }

// maxSnippetBytes bounds a key, and text a message quotes, in human output. Every vocabulary
// key and tagger spelling is far shorter; a file can still hold a name of any length.
const maxSnippetBytes = 96

// ElideSnippet is [ElideValue] at the length a key or a quoted piece of text is shown at.
func ElideSnippet(s string) string { return ElideValueAt(s, maxSnippetBytes) }

// ElideKey is [ElideSnippet] for a key. Like [ElideValue] it runs before sanitizing; --json
// and the structured accessors keep the key whole.
func ElideKey(k Key) string { return ElideSnippet(string(k)) }

// ElideValueAt is [ElideValue] with a caller-chosen threshold, for text held to a tighter
// bound than a displayed tag value, such as a warning quoting a file-derived key or entry.
// Sharing the cut keeps one back-off rule and one hint spelling.
//
// When the prefix backs all the way off (max lands inside a run of continuation bytes, as
// in a binary blob) it keeps the raw cut instead of eliding to nothing; the caller's
// [SanitizeLine] then escapes the invalid byte.
func ElideValueAt(v string, max int) string {
	if max < 1 || len(v) <= max {
		return v
	}
	keep := max
	for keep > 0 && !utf8.RuneStart(v[keep]) {
		keep-- // back up off a UTF-8 continuation byte so the prefix ends on a rune
	}
	if keep == 0 {
		keep = max
	}
	return v[:keep] + "…[+" + bits.HumanBytes(int64(len(v)-keep)) + "]"
}

// SanitizeText escapes control/non-printable runes as \xNN (or \uXXXX, \UXXXXXXXX past
// U+FFFF, for the invisible and reordering ones). Keeps \t and \n. Rune-aware so multi-byte
// UTF-8 is intact. Escaping the result again changes nothing. Single-line fields use
// [SanitizeLine].
func SanitizeText(s string) string { return sanitize(s, controlRune) }

// SanitizeLine is [SanitizeText] that also escapes \t and \n (listing/spoof safety).
func SanitizeLine(s string) string { return sanitize(s, lineControlRune) }

// sanitize is the shared escape core for [SanitizeText] and [SanitizeLine].
func sanitize(s string, isControl func(rune) bool) string {
	// Fast path: a clean, valid-UTF-8 value (the common case) is returned
	// unchanged with no allocation.
	if utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool { return isControl(r) || formatControlRune(r) }) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// An invalid UTF-8 byte is escaped on its own, not emitted as a replacement character.
			writeHexEscape(&b, s[i])
			i++
			continue
		}
		if isControl(r) {
			// isControl only matches codepoints <= U+00FF, so the value fits one byte.
			writeHexEscape(&b, byte(r))
		} else if formatControlRune(r) {
			writeUnicodeEscape(&b, r)
		} else {
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

const hexDigits = "0123456789abcdef"

// writeHexEscape writes c as a two-digit \xNN escape. It avoids fmt's reflection
// and allocation, since it runs once per offending byte.
func writeHexEscape(b *strings.Builder, c byte) {
	b.WriteString(`\x`)
	b.WriteByte(hexDigits[c>>4])
	b.WriteByte(hexDigits[c&0x0f])
}

// formatControlRune reports a rune that reorders or breaks text without being a C0/C1 byte, or
// that shows as nothing, so a value can differ from what it reads as: the bidirectional
// controls, the zero width space, word joiner and byte order mark, the line and paragraph
// separators, the invisible operators, deprecated format characters, interlinear annotation
// marks and tag characters, the soft hyphen, combining grapheme joiner, Mongolian vowel
// separator and Khmer inherent vowels, and the Hangul fillers. The zero width joiner and
// non-joiner and the variation selectors stay: emoji sequences and Indic text need them.
func formatControlRune(r rune) bool {
	switch {
	case r == 0x00AD, r == 0x034F, r == 0x061C, r == 0x115F, r == 0x1160, r == 0x17B4, r == 0x17B5, r == 0x180E:
		return true
	case r == 0x200B, r == 0x200E, r == 0x200F, r == 0x2028, r == 0x2029, r >= 0x202A && r <= 0x202E:
		return true
	case r >= 0x2060 && r <= 0x206F, r == 0x3164, r == 0xFEFF, r == 0xFFA0, r >= 0xFFF0 && r <= 0xFFFB:
		return true
	case r == 0xE0001, r >= 0xE0020 && r <= 0xE007F:
		return true
	}
	return false
}

// writeUnicodeEscape writes r as a \uXXXX escape, or \UXXXXXXXX past U+FFFF, for a format
// control whose codepoint does not fit \xNN. Like [writeHexEscape] it avoids fmt.
func writeUnicodeEscape(b *strings.Builder, r rune) {
	digits := 4
	if r > 0xFFFF {
		digits = 8
		b.WriteString(`\U`)
	} else {
		b.WriteString(`\u`)
	}
	for shift := 4 * (digits - 1); shift >= 0; shift -= 4 {
		b.WriteByte(hexDigits[r>>shift&0x0f])
	}
}

// controlRune reports whether r is a control codepoint SanitizeText escapes: a
// C0 control other than tab/newline, DEL, or a C1 control. Tab and newline are
// kept (the multi-line value renderer relies on the newline).
func controlRune(r rune) bool {
	return r != '\t' && r != '\n' && isControlByte(r)
}

// isControlByte reports whether r is a C0 control, DEL, or a C1 control, regardless
// of the tab/newline policy. Both escaping predicates share it. It matches only
// codepoints <= U+009F, so an escaped value fits one byte.
func isControlByte(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// lineControlRune reports whether r is a control codepoint [SanitizeLine] escapes:
// every control byte, including tab and newline.
func lineControlRune(r rune) bool {
	return isControlByte(r)
}
