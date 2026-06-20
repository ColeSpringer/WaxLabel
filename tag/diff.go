package tag

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// ChangeKind names how one key differs between two tag sets.
type ChangeKind uint8

const (
	// ChangeUnknown is the zero value, so a never-set ChangeKind is detectably
	// invalid rather than silently reading as a real kind.
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

// Change is one key's difference between a base and an edited [TagSet]: the key,
// how it changed, and the relevant values. Old holds the base values (set for a
// removed or changed key); New holds the edited values (set for an added or
// changed key).
type Change struct {
	Key  Key
	Kind ChangeKind
	Old  []string
	New  []string
}

// Diff reports the per-key delta from base to edited: keys dropped (removed),
// keys whose values changed, then keys introduced (added). Removed and changed
// keys come first in base's order, added keys last in edited's order, so the
// result is stable and minimal-change. Values are compared order-significantly
// (the same equality a codec uses to detect an edit), so a key present in both
// with identical values yields no Change.
//
// It is the single tag-diff primitive shared by the CLI's diff command and the
// write-plan change preview, so the two cannot drift.
func Diff(base, edited TagSet) []Change {
	var out []Change
	// Read the unexported fields directly rather than through Keys/Get: those
	// clone defensively for external callers, but here (inside the package) we
	// need clones only for the values we actually keep in a Change, which must be
	// detached copies the caller can hold. Unchanged keys allocate nothing.
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

// String renders one change as a single diff-style line: "- KEY: old" for a
// removed key, "+ KEY: new" for an added one, and "~ KEY: old -> new" for a
// changed one. Multiple values are joined with " | " (so a value containing a
// comma is not misread as two), and a key present with no values reads as
// "(present, no value)". The key and every value are run through [SanitizeLine] -
// the row is a single line, so an embedded newline or tab is escaped (it can
// neither forge a row nor break the layout), not just the terminal-hijack class -
// even though both originate in an untrusted file (a custom Vorbis/MP4 field name
// bypasses key validation on parse); a caller that needs the exact bytes reads
// Key/Old/New directly.
//
// The line carries no indent and no trailing newline, so the caller controls
// layout. It is the single change-line formatter shared by the library's
// plan/diff previews and the CLI, so their formatting cannot drift.
func (c Change) String() string {
	key := SanitizeLine(string(c.Key))
	switch c.Kind {
	case ChangeRemoved:
		return "- " + key + ": " + joinChangeValues(c.Old)
	case ChangeAdded:
		return "+ " + key + ": " + joinChangeValues(c.New)
	case ChangeChanged:
		return "~ " + key + ": " + joinChangeValues(c.Old) + " -> " + joinChangeValues(c.New)
	default:
		return ""
	}
}

// joinChangeValues renders a key's values for a change line: the empty case as
// "(present, no value)", otherwise each value escaped for a single-line row via
// [SanitizeLine] and joined with " | ".
func joinChangeValues(vals []string) string {
	if len(vals) == 0 {
		return "(present, no value)"
	}
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = SanitizeLine(v)
	}
	return strings.Join(out, " | ")
}

// SanitizeText returns s with control and non-printable bytes rendered as
// visible \xNN escapes, so an untrusted tag value cannot inject terminal escape
// sequences (ESC/CSI), carriage returns, or bells into human-facing text output.
// It is rune-aware: it decodes UTF-8 and escapes only genuine control
// codepoints, so multi-byte text (accented Latin, CJK, emoji) survives intact
// even though its continuation bytes fall in the 0x80-0x9F range a naive
// byte-level scan would corrupt.
//
// Kept verbatim: the horizontal tab and the newline (the multi-line value
// renderer relies on \n, and a tab is benign alignment). Escaped: the C0
// controls (0x00-0x1F except \t/\n), DEL (0x7F), the C1 controls (0x80-0x9F),
// and any byte that is not valid UTF-8 (escaped one byte at a time). Every other
// rune passes through unchanged.
//
// It keeps '\n'/'\t', so it backs the multi-line value display (the dump value
// renderer, which owns the line break) and the CLI's sanitizing output boundary.
// Single-line fields use [SanitizeLine] instead (it also escapes '\n'/'\t'); both
// share one escape core ([sanitize]), so their escaping cannot drift. The
// structured accessors ([TagSet], [Change.Old]/[Change.New]) and --json still
// carry the exact bytes for scripts.
func SanitizeText(s string) string { return sanitize(s, controlRune) }

// SanitizeLine is [SanitizeText] that additionally escapes the horizontal tab and
// the newline, for a single-line field - a tag key, a picture type or MIME, a
// chapter title, a native block label, a file path - where an embedded newline
// would forge a fake line in a listing (output spoofing) or a tab would break
// column alignment. Its output is printable ASCII, so it composes with the CLI's
// sanitizing output boundary and any surrounding per-field escape with no
// double-escaping.
//
// Multi-line tag values keep [SanitizeText] (applied by the value renderer, which
// owns the line break), so their genuine newlines survive as real breaks.
func SanitizeLine(s string) string { return sanitize(s, lineControlRune) }

// sanitize returns s with every rune isControl reports as control rendered as a
// visible \xNN escape. It is rune-aware - it decodes UTF-8 and escapes only
// genuine control codepoints, so multi-byte text survives - and escapes any byte
// that is not valid UTF-8 one at a time. isControl must match only codepoints
// <= U+00FF, so the escaped value fits one byte; both controlRune and
// lineControlRune do. It backs both [SanitizeText] and [SanitizeLine] so their
// escaping cannot drift.
func sanitize(s string, isControl func(rune) bool) string {
	// Fast path: a clean, valid-UTF-8 value (the common case) is returned
	// unchanged with no allocation.
	if utf8.ValidString(s) && strings.IndexFunc(s, isControl) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// An invalid UTF-8 byte: escape it on its own so the result stays valid
			// printable text rather than emitting a replacement character.
			writeHexEscape(&b, s[i])
			i++
			continue
		}
		if isControl(r) {
			// isControl only matches codepoints <= U+00FF, so the value fits one byte.
			writeHexEscape(&b, byte(r))
		} else {
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

const hexDigits = "0123456789abcdef"

// writeHexEscape writes c as a two-digit \xNN escape. It avoids fmt's reflection
// and per-call allocation, since this runs once for every offending byte on the
// (already cold) escape path.
func writeHexEscape(b *strings.Builder, c byte) {
	b.WriteString(`\x`)
	b.WriteByte(hexDigits[c>>4])
	b.WriteByte(hexDigits[c&0x0f])
}

// controlRune reports whether r is a control codepoint SanitizeText escapes: a
// C0 control other than tab/newline, DEL, or a C1 control. Tab and newline are
// kept (the multi-line value renderer relies on the newline).
func controlRune(r rune) bool {
	return r != '\t' && r != '\n' && isControlByte(r)
}

// isControlByte reports whether r is a non-printable control codepoint - a C0
// control, DEL, or a C1 control - independent of the tab/newline policy. It is the
// shared core of both escaping predicates and matches only codepoints <= U+009F,
// so an escaped value fits one byte.
func isControlByte(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// lineControlRune reports whether r is a control codepoint [SanitizeLine] escapes:
// every control byte, including the tab and newline a single-line field must not
// carry.
func lineControlRune(r rune) bool {
	return isControlByte(r)
}
