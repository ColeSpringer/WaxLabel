package waxlabel

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// LintSeverity grades a [Finding].
type LintSeverity uint8

const (
	// LintInfo notes something worth knowing but not wrong.
	LintInfo LintSeverity = iota
	// LintWarning flags a likely problem (stale legacy tags, encoder noise).
	LintWarning
	// LintError flags an invalid or contradictory state.
	LintError
)

func (s LintSeverity) String() string {
	switch s {
	case LintError:
		return "error"
	case LintWarning:
		return "warning"
	default:
		return "info"
	}
}

// Finding is one issue reported by [Document.Lint].
type Finding struct {
	Severity LintSeverity
	Code     string
	Message  string
	Key      tag.Key // the field involved, or "" if not field-specific
	// Fixable means the fix [Document.PlanLintFix] plans removes this finding, and the file
	// can be written.
	Fixable bool
}

// String renders "[severity] code: message (key)". Message and key are sanitized, a long key elided.
func (f Finding) String() string {
	msg := tag.SanitizeLine(f.Message)
	if f.Key != "" {
		return fmt.Sprintf("[%s] %s: %s (%s)", f.Severity, f.Code, msg, tag.SanitizeLine(tag.ElideKey(f.Key)))
	}
	return fmt.Sprintf("[%s] %s: %s", f.Severity, f.Code, msg)
}

// Lint reports metadata issues (legacy, encoder noise, conflicts, pictures,
// chapters, values, cardinality, custom keys). No I/O; does not modify.
func (d *Document) Lint() []Finding {
	if d.zero() {
		return nil
	}
	out, legacyOnly := d.findings()
	d.markFixable(out, d.safeFix(out, legacyOnly))
	return out
}

// findings is Lint before the fixable marks, with the legacy-only keys it computed on the way.
func (d *Document) findings() (out []Finding, legacyOnly []tag.Key) {
	out = append(out, lintWarnings(d.media.Warnings)...)
	out = append(out, lintFamilies(d.media.Families)...)
	legacyOnly = d.LegacyOnlyKeys()
	out = append(out, lintLegacyOnly(legacyOnly)...)
	out = append(out, lintOpaqueLegacy(d.media.LegacyOpaqueContent)...)
	out = append(out, lintPictures(core.ProjectPictures(d.media.Pictures))...)
	out = append(out, lintChapters(d.media.Chapters, d.media.Properties.Duration())...)
	out = append(out, lintValues(d.media.Tags)...)
	out = append(out, lintNegativeNumbers(d.media.Tags)...)
	out = append(out, lintCardinality(d.media)...)
	out = append(out, lintCustomKeys(d.media.Tags)...)
	return out, legacyOnly
}

// safeFix is what the lint fix does to a file. [Document.PlanLintFix] builds the edit from it
// and markFixable the marks, so the two cannot disagree.
type safeFix struct {
	// stripLegacy: the legacy containers hold no key, value or content of their own, and go.
	stripLegacy bool
	// encoder: a transcoder stamp was found; a vendor string or WAV ISFT item loses it.
	encoder bool
	// stamped: ENCODER held one too, and is written as left: its other values, then the
	// names other stores of the format's own hold.
	stamped bool
	left    []string
}

func (d *Document) safeFix(fs []Finding, legacyOnly []tag.Key) safeFix {
	var fix safeFix
	for _, f := range fs {
		switch f.Code {
		case "inherited-encoder":
			fix.encoder = true
		case "stray-leading-id3", "trailing-id3v1", "legacy-ape":
			fix.stripLegacy = len(legacyOnly) == 0 && !d.HasOpaqueLegacyContent() && !legacyValueOfItsOwn(d.media.Families)
		}
	}
	if fix.encoder {
		vals, _ := d.Get(tag.Encoder)
		for _, v := range vals {
			if core.IsTranscoderStamp(v) {
				fix.stamped = true
			} else {
				fix.left = append(fix.left, v)
			}
		}
	}
	if fix.stamped {
		// The edit reaches every store of the format's own, so a name one of them holds that
		// ENCODER does not read with (a WAV ISFT item under a stamped TSSE) is written too,
		// not cleared with the stamp.
		for _, f := range d.media.Families {
			if f.Key != tag.Encoder || f.Legacy || f.Targeted {
				continue
			}
			for _, v := range f.Values {
				held := slices.ContainsFunc(fix.left, func(l string) bool { return core.EqualFoldValue(l, v) })
				if !held && !core.IsTranscoderStamp(v) {
					fix.left = append(fix.left, v)
				}
			}
		}
	}
	return fix
}

// legacyValueOfItsOwn reports a legacy container holding, under a key the tags hold too, a
// value they do not. A transcoder stamp is no such value: the fix exists to remove those.
func legacyValueOfItsOwn(fams []core.FamilyValue) bool {
	return slices.ContainsFunc(fams, func(f core.FamilyValue) bool {
		return f.Legacy && !f.Selected && !(f.Key == tag.Encoder && !slices.ContainsFunc(f.Values, isNoStamp))
	})
}

// markFixable sets Fixable on the findings fix removes. On a file no edit can be saved to (a
// read-only format or stream, no audio) that is none.
func (d *Document) markFixable(fs []Finding, fix safeFix) {
	writable := !d.Capabilities().ReadOnly && !hasNoAudioWarning(d.media)
	for i := range fs {
		switch fs[i].Code {
		case "inherited-encoder":
			// lintWarnings marked the stamps the fix reaches.
			fs[i].Fixable = fs[i].Fixable && writable
		case "stray-leading-id3", "trailing-id3v1", "legacy-ape":
			fs[i].Fixable = writable && fix.stripLegacy
		case "conflicting-families":
			fs[i].Fixable = writable && fix.settles(d.media.Families, fs[i].Key)
		case "single-valued-multi":
			// ENCODER is written less its stamps; a legacy container's values are no tags.
			fs[i].Fixable = writable && fs[i].Key == tag.Encoder && fix.stamped && len(fix.left) <= 1
		}
	}
}

// settles reports whether the fix leaves no entry of key in conflict.
func (fix safeFix) settles(fams []core.FamilyValue, key tag.Key) bool {
	// The edit of ENCODER is written to every store of the format's own.
	rewritten := key == tag.Encoder && fix.stamped
	written := tag.NewTagSet()
	if len(fix.left) > 0 {
		written.Set(tag.Encoder, fix.left...)
	}
	for _, f := range fams {
		switch {
		case f.Key != key, rewritten && !f.Legacy, f.Legacy && fix.stripLegacy:
		case rewritten:
			// A legacy container the fix keeps is judged against the values written, as a
			// parse judges it: with none written it conflicts with nothing.
			if !core.FamilySelected(written, key, f.Values[0]) {
				return false
			}
		case key == tag.Encoder && fix.encoder && !f.Legacy && !slices.ContainsFunc(f.Values, isNoStamp):
			// A stamp outside the tags that read as ENCODER (a WAV ISFT item under another
			// TSSE) goes with no edit of the key.
		case !f.Selected:
			return false
		}
	}
	return true
}

func isNoStamp(v string) bool { return !core.IsTranscoderStamp(v) }

// lintWarnings promotes actionable parse warnings (same code strings as dump). A finding's
// message is the warning's display text, a key it names held to the length a key is shown at.
func lintWarnings(ws []core.Warning) []Finding {
	var out []Finding
	for _, w := range ws {
		f := Finding{Code: w.Code.String(), Message: w.Text()}
		switch w.Code {
		case core.WarnInheritedEncoder:
			// The fix reaches a stamp under ENCODER and one outside the tags (a vendor string,
			// a WAV ISFT item), which carries no key. One under ENCODEDBY it leaves.
			f.Severity, f.Fixable = LintWarning, len(w.Keys) == 0 || slices.Contains(w.Keys, tag.Encoder)
		case core.WarnStrayLeadingID3, core.WarnTrailingID3v1, core.WarnLegacyAPE,
			core.WarnInvalidPicture, core.WarnTruncatedAudio,
			core.WarnInvalidTagKey, core.WarnChainedStream, core.WarnTrailingBytes,
			core.WarnOversizedChunk, core.WarnMalformedTagEntry, core.WarnTrailingLyrics3,
			core.WarnLeadingBytes, core.WarnPageGap, core.WarnInvalidText:
			f.Severity = LintWarning
		case core.WarnMultipleVorbisComment, core.WarnDuplicateTagBlock, core.WarnNoAudioFrames:
			f.Severity = LintError
		case core.WarnNumericGenre, core.WarnUnknownChunkSize, core.WarnMultiplexedStream:
			// Info only (README: dump+lint report; must not fail clean exit / piped WAV).
			f.Severity = LintInfo
		default:
			continue
		}
		out = append(out, f)
	}
	return out
}

// lintChapters reports duplicate starts and starts past duration
// ([core.ChaptersPastDuration], [core.DuplicateChapterStarts]; same as editor).
func lintChapters(chapters []core.Chapter, duration time.Duration) []Finding {
	var out []Finding
	for _, c := range core.ChaptersPastDuration(chapters, duration) {
		out = append(out, Finding{Severity: LintWarning, Code: core.WarnChapterPastDuration.String(),
			Message: core.ChapterPastDurationMessage(c.Start, duration)})
	}
	for _, start := range core.DuplicateChapterStarts(chapters) {
		out = append(out, Finding{Severity: LintWarning, Code: core.WarnDuplicateChapter.String(),
			Message: core.DuplicateChapterMessage(start)})
	}
	return out
}

// lintFamilies reports one finding per key with unselected conflicting family values.
func lintFamilies(fams []core.FamilyValue) []Finding {
	var out []Finding
	seen := map[tag.Key]bool{}
	for _, f := range fams {
		if f.Selected || seen[f.Key] {
			continue
		}
		seen[f.Key] = true
		out = append(out, Finding{
			Severity: LintWarning, Code: "conflicting-families",
			Message: core.ConflictingFamiliesMessage(), Key: f.Key,
		})
	}
	return out
}

// lintLegacyOnly reports keys present only in a legacy container (info; explains --fix keep).
func lintLegacyOnly(keys []tag.Key) []Finding {
	if len(keys) == 0 {
		return nil
	}
	return []Finding{{Severity: LintInfo, Code: "legacy-only-tags",
		Message: fmt.Sprintf("%d tag(s) present only in a legacy container; see dump --native", len(keys))}}
}

// lintOpaqueLegacy reports non-tag legacy content that keeps the container on --fix.
func lintOpaqueLegacy(opaque bool) []Finding {
	if !opaque {
		return nil
	}
	return []Finding{{Severity: LintInfo, Code: "legacy-opaque-content",
		Message: "a legacy container holds non-tag content (picture, chapter, binary item, Lyrics3 block, leading bytes, or unreadable bytes) not shown; see dump --native"}}
}

// Shared wording for duplicate-picture / multiple-front-covers (lint and editor).
func duplicatePictureMessage(roles []core.PictureType) string {
	// Name by sorted roles present, not first/second occurrence.
	if len(roles) <= 1 {
		var t core.PictureType
		if len(roles) == 1 {
			t = roles[0]
		}
		return fmt.Sprintf("identical %s picture appears more than once", t)
	}
	names := make([]string, len(roles))
	for i, r := range roles {
		names[i] = r.String()
	}
	return fmt.Sprintf("identical picture appears more than once (roles: %s)", strings.Join(names, ", "))
}

// distinctSortedRoles returns the sorted distinct picture types of the pics whose
// hash equals h, so a duplicate-picture message names every role the identical
// bytes appear under. hashes[i] is the precomputed hash of pics[i]; an index absent
// from hashes is skipped (a site may hash only a length-matching subset).
func distinctSortedRoles(pics []Picture, hashes map[int][32]byte, h [32]byte) []core.PictureType {
	var roles []core.PictureType
	for i := range pics {
		if hashes[i] == h && !slices.Contains(roles, pics[i].Type) {
			roles = append(roles, pics[i].Type)
		}
	}
	slices.Sort(roles)
	return roles
}

func multipleFrontCoversMessage(fronts int) string {
	return fmt.Sprintf("%d front-cover pictures", fronts)
}

// lintPictures reports duplicate covers, redundant front covers, and the
// single-icon rule.
func lintPictures(pics []Picture) []Finding {
	var out []Finding
	// Hash every picture once so a duplicate finding can name every role the identical
	// bytes appear under (distinctSortedRoles), matching the editor's warning.
	hashes := make(map[int][32]byte, len(pics))
	for i, p := range pics {
		hashes[i] = p.Hash()
	}
	seen := map[[32]byte]bool{}
	fronts := 0
	for i, p := range pics {
		// A picture the codec could not sniff carries the unrecognized-image MIME; key
		// on that, not a re-sniff. Never auto-fixed: a valid cover in an image format
		// the sniff does not know looks the same, and dropping it would lose data.
		if p.Unrecognized() {
			out = append(out, Finding{Severity: LintWarning, Code: "invalid-picture",
				Message: fmt.Sprintf("%s picture is not a recognized image type (%s)", p.Type, p.MIME)})
		}
		if reason, bad := core.NonConformingIcon(p); bad {
			// The code comes from the warning's String, not a literal, so the edit-time
			// warning and this finding cannot diverge.
			out = append(out, Finding{Severity: LintWarning, Code: core.WarnNonConformingIcon.String(), Message: reason})
		}
		h := hashes[i]
		if seen[h] {
			out = append(out, Finding{Severity: LintWarning, Code: "duplicate-picture", Message: duplicatePictureMessage(distinctSortedRoles(pics, hashes, h))})
		}
		seen[h] = true
		if p.Type == core.PicFrontCover {
			fronts++
		}
	}
	if fronts > 1 {
		out = append(out, Finding{Severity: LintWarning, Code: "multiple-front-covers", Message: multipleFrontCoversMessage(fronts)})
	}
	// LintError, unlike non-conforming-icon: two type-1 pictures are ambiguous and
	// unrepairable without choosing one, while an oversized icon still renders.
	if icon, otherIcon := core.CountIcons(pics); icon > 1 || otherIcon > 1 {
		out = append(out, Finding{Severity: LintError, Code: "duplicate-icon",
			Message: "picture types 1/2 must be unique"})
	}
	return out
}

// lintValues reports values that fail [tag.ValidatorFor] contracts. Same rules
// as set-time validation. Skips present-but-empty.
func lintValues(ts tag.TagSet) []Finding {
	var out []Finding
	for _, k := range ts.Keys() {
		val, ok := tag.ValidatorFor(k)
		if !ok {
			continue
		}
		vals, _ := ts.Get(k)
		for _, v := range vals {
			// Trim as set does before it validates, so lint and set agree: a
			// whitespace-only numeric ("   ") trims to empty and is skipped, and " 3 "
			// validates on its trimmed form. Non-trimmable keys are unchanged.
			v = tag.TrimTokenValue(k, v)
			if v != "" && !val.Valid(k, v) {
				detail, _ := val.Details(k, v)
				out = append(out, Finding{Severity: LintWarning, Code: val.LintCode,
					Message: fmt.Sprintf("%q %s", tag.ElideSnippet(v), detail), Key: k})
			}
		}
	}
	return out
}

// lintNegativeNumbers reports negative numeric values (track number, play count).
// They round-trip but are usually mistakes. Same predicate as the set-time
// advisory; LintInfo, like custom-key, so it does not change the clean exit
// boundary. Empty values are skipped.
func lintNegativeNumbers(ts tag.TagSet) []Finding {
	var out []Finding
	for _, k := range ts.Keys() {
		if !tag.IsNumericKey(k) {
			continue
		}
		vals, _ := ts.Get(k)
		for _, v := range vals {
			if v != "" && tag.NegativeNumericValue(k, v) {
				out = append(out, Finding{Severity: LintInfo, Code: "negative-numeric",
					Message: fmt.Sprintf("%q is negative (numbering is normally non-negative)", tag.ElideSnippet(v)), Key: k})
			}
		}
	}
	return out
}

// lintCardinality reports single-valued known keys that hold multiple values
// ([core.SingleValuedMulti]).
func lintCardinality(m *core.Media) []Finding {
	var out []Finding
	for k, vals := range m.Tags.All() {
		if core.SingleValuedMulti(k, len(vals), m.Lyrics) {
			out = append(out, Finding{Severity: LintWarning, Code: "single-valued-multi",
				Message: fmt.Sprintf("single-valued key holds %d values", len(vals)), Key: k})
		}
	}
	return out
}

// lintCustomKeys reports unknown keys (info only). R128_* exempt (RFC 7845).
func lintCustomKeys(ts tag.TagSet) []Finding {
	var out []Finding
	for _, k := range ts.Keys() {
		if !k.Known() && !tag.IsR128GainKey(k) {
			out = append(out, Finding{Severity: LintInfo, Code: "custom-key", Message: "custom field, not a known key", Key: k})
		}
	}
	return out
}
