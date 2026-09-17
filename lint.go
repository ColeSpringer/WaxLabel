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
	// Fixable means [Document.PlanLintFix] acts on this finding.
	Fixable bool
}

// String renders "[severity] code: message (key)". Message and key are sanitized.
func (f Finding) String() string {
	msg := tag.SanitizeLine(f.Message)
	if f.Key != "" {
		return fmt.Sprintf("[%s] %s: %s (%s)", f.Severity, f.Code, msg, tag.SanitizeLine(string(f.Key)))
	}
	return fmt.Sprintf("[%s] %s: %s", f.Severity, f.Code, msg)
}

// Lint reports metadata issues (legacy, encoder noise, conflicts, pictures,
// chapters, values, cardinality, custom keys). No I/O; does not modify.
func (d *Document) Lint() []Finding {
	if d.zero() {
		return nil
	}
	var out []Finding

	out = append(out, lintWarnings(d.media.Warnings)...)
	out = append(out, lintFamilies(d.media.Families)...)
	legacyOnly := d.LegacyOnlyKeys()
	out = append(out, lintLegacyOnly(legacyOnly)...)
	out = append(out, lintOpaqueLegacy(d.media.LegacyOpaqueContent)...)
	out = append(out, lintPictures(core.ProjectPictures(d.media.Pictures))...)
	out = append(out, lintChapters(d.media.Chapters, d.media.Properties.Duration())...)
	out = append(out, lintValues(d.media.Tags)...)
	out = append(out, lintNegativeNumbers(d.media.Tags)...)
	out = append(out, lintCardinality(d.media.Tags)...)
	out = append(out, lintCustomKeys(d.media.Tags)...)
	d.markFixable(out, legacyOnly)
	return out
}

// markFixable sets Fixable from the gates PlanLintFix applies, so the marker and the fix
// cannot disagree. legacyOnly is the key list [Document.Lint] already computed.
func (d *Document) markFixable(fs []Finding, legacyOnly []tag.Key) {
	legacyRedundant := len(legacyOnly) == 0 && !d.HasOpaqueLegacyContent()
	encoderReachable := d.encoderStampReachable()
	for i := range fs {
		switch fs[i].Code {
		case "inherited-encoder":
			fs[i].Fixable = encoderReachable
		case "stray-leading-id3", "trailing-id3v1", "legacy-ape":
			fs[i].Fixable = legacyRedundant
		}
	}
}

// encoderStampReachable is true when PlanLintFix can reach a stamp: ENCODER or
// vendor/ISFT via WithStripEncoderStamp. ENCODEDBY-only stamps are unreachable.
func (d *Document) encoderStampReachable() bool {
	return hasTranscoderStamp(d, tag.Encoder) || !hasTranscoderStamp(d, tag.EncodedBy)
}

// hasTranscoderStamp reports whether any value under key is an inherited transcoder stamp,
// by the same test the linter and the fix use.
func hasTranscoderStamp(d *Document, key tag.Key) bool {
	vals, ok := d.Get(key)
	if !ok {
		return false
	}
	return slices.ContainsFunc(vals, core.IsTranscoderStamp)
}

// lintWarnings promotes actionable parse warnings (same code strings as dump).
func lintWarnings(ws []core.Warning) []Finding {
	var out []Finding
	for _, w := range ws {
		switch w.Code {
		case core.WarnStrayLeadingID3, core.WarnTrailingID3v1, core.WarnLegacyAPE,
			core.WarnInheritedEncoder, core.WarnInvalidPicture, core.WarnTruncatedAudio,
			core.WarnInvalidTagKey, core.WarnChainedStream, core.WarnTrailingBytes,
			core.WarnOversizedChunk, core.WarnMalformedTagEntry:
			out = append(out, Finding{Severity: LintWarning, Code: w.Code.String(), Message: w.Message})
		case core.WarnMultipleVorbisComment, core.WarnDuplicateTagBlock, core.WarnNoAudioFrames:
			out = append(out, Finding{Severity: LintError, Code: w.Code.String(), Message: w.Message})
		case core.WarnNumericGenre, core.WarnUnknownChunkSize:
			// Info only (README: dump+lint report; must not fail clean exit / piped WAV).
			out = append(out, Finding{Severity: LintInfo, Code: w.Code.String(), Message: w.Message})
		}
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
		Message: "a legacy container holds non-tag content (picture, chapter, or binary item) not shown; see dump --native"}}
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

// distinctSortedRoles returns the distinct picture types among pics whose bytes hash to one of
// the given per-index hashes equal to h, sorted, so a duplicate-picture message names every role
// the identical bytes appear under in a stable, iteration-order-independent way. hashes[i] is the
// precomputed hash of pics[i] (a site may only hash a length-matching subset; an index absent
// from hashes is skipped).
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
	// Precompute every picture's hash once, so a duplicate finding can name the whole set of
	// roles the identical bytes appear under (distinctSortedRoles) rather than a single
	// occurrence's role - keeping the message identical to the editor's edit-scope warning.
	hashes := make(map[int][32]byte, len(pics))
	for i, p := range pics {
		hashes[i] = p.Hash()
	}
	seen := map[[32]byte]bool{}
	fronts := 0
	for i, p := range pics {
		// A picture the codec could not sniff is stored as the unrecognized-image MIME;
		// key on that (not a re-sniff) so a cover a codec already recognized is never
		// false-flagged. Reported only - never auto-fixed - since a valid cover in an
		// image format the sniff does not know degrades to exactly this, and dropping
		// it would be silent data loss.
		if p.Unrecognized() {
			out = append(out, Finding{Severity: LintWarning, Code: "invalid-picture",
				Message: fmt.Sprintf("%s picture is not a recognized image type (%s)", p.Type, p.MIME)})
		}
		if reason, bad := core.NonConformingIcon(p); bad {
			// The code comes from the warning's own String, not a literal: the edit-time
			// warning and this finding are documented to report the same condition under the
			// same code, and a hand-written copy would let a rename split them silently.
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
	// LintError, not the LintWarning non-conforming-icon gets: two type-1 pictures make the
	// frame set ambiguous and unrepairable without choosing one, while an oversized icon is
	// unambiguous and every reader renders it. Do not "fix" the asymmetry.
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
			// Trim the value the same way set does before it validates, so lint and set cannot
			// disagree on a whitespace-only or space-padded trimmable value. A whitespace-only
			// numeric ("   ") trims to empty and is skipped as the benign empty-value case set
			// writes; a space-padded number (" 3 ") validates on its trimmed form. TrimTokenValue
			// early-returns for a non-trimmable key, so those validators see the value unchanged.
			v = tag.TrimTokenValue(k, v)
			if v != "" && !val.Valid(k, v) {
				detail, _ := val.Details(k, v)
				out = append(out, Finding{Severity: LintWarning, Code: val.LintCode,
					Message: fmt.Sprintf("%q %s", v, detail), Key: k})
			}
		}
	}
	return out
}

// lintNegativeNumbers reports numeric fields with negative values, such as a negative
// track number or play count. These values parse and round-trip, but they are usually
// mistakes. This mirrors the set-time advisory using the same predicate and stays
// LintInfo, like custom-key, so it does not change the clean/non-clean exit boundary.
// Present-but-empty values are skipped.
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
					Message: fmt.Sprintf("%q is negative (numbering is normally non-negative)", v), Key: k})
			}
		}
	}
	return out
}

// lintCardinality reports single-valued known keys that hold multiple values.
func lintCardinality(ts tag.TagSet) []Finding {
	var out []Finding
	for k, vals := range ts.All() {
		if k.SingleValuedMulti(len(vals)) {
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
