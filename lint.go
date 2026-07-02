package waxlabel

import (
	"fmt"

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
}

// String renders the finding as "[severity] code: message (key)". The severity and
// code are fixed library vocabulary; the message and key can be file-derived (the
// inherited-encoder message carries the raw inherited stamp; a custom-key finding
// carries the raw field name), so those two are run through [tag.SanitizeLine]
// individually - the finding prints as one list item, so a newline or tab is
// escaped too (it cannot forge a line), not just the terminal-hijack class. A
// library consumer that prints this without the CLI's output boundary is then safe.
// The malformed-date message is already %q-escaped inside the Message, which
// SanitizeLine leaves intact (no double-escape).
func (f Finding) String() string {
	msg := tag.SanitizeLine(f.Message)
	if f.Key != "" {
		return fmt.Sprintf("[%s] %s: %s (%s)", f.Severity, f.Code, msg, tag.SanitizeLine(string(f.Key)))
	}
	return fmt.Sprintf("[%s] %s: %s", f.Severity, f.Code, msg)
}

// Lint inspects a document for issues a tagger would want to surface or fix:
// stale legacy containers, inherited encoder noise, conflicting family values,
// duplicate or invalid pictures, malformed dates and numbers, single-valued keys
// carrying several values, and custom (non-vocabulary) keys. It reads only the
// parsed document (no I/O) and never modifies it.
func (d *Document) Lint() []Finding {
	if d.zero() {
		return nil
	}
	var out []Finding

	out = append(out, lintWarnings(d.media.Warnings)...)
	out = append(out, lintFamilies(d.media.Families)...)
	out = append(out, lintPictures(d.media.Pictures)...)
	out = append(out, lintValues(d.media.Tags)...)
	out = append(out, lintNegativeNumbers(d.media.Tags)...)
	out = append(out, lintCardinality(d.media.Tags)...)
	out = append(out, lintCustomKeys(d.media.Tags)...)
	return out
}

// lintWarnings promotes the parse-time warnings that a tagger usually acts on. Each
// promoted warning reuses w.Code.String() as its finding code, so a condition that
// both dump (which prints the warning code) and lint surface reads with the same code
// in each - no renamed alias to keep in sync (C1). Only the subset a tagger acts on is
// promoted (other parse warnings are informational); the per-condition severity is the
// only thing this assigns. The computed-only lint codes that dump never prints
// (malformed-date, single-valued-multi, custom-key, the picture checks) are added by
// the sibling lint* helpers, not here.
func lintWarnings(ws []core.Warning) []Finding {
	var out []Finding
	for _, w := range ws {
		switch w.Code {
		case core.WarnStrayLeadingID3, core.WarnTrailingID3v1, core.WarnLegacyAPE,
			core.WarnInheritedEncoder, core.WarnInvalidPicture, core.WarnTruncatedAudio,
			core.WarnInvalidTagKey:
			out = append(out, Finding{LintWarning, w.Code.String(), w.Message, ""})
		case core.WarnMultipleVorbisComment, core.WarnDuplicateTagBlock, core.WarnNoAudioFrames:
			out = append(out, Finding{LintError, w.Code.String(), w.Message, ""})
		case core.WarnNumericGenre:
			// Informational, like negative-numeric/custom-key: a numeric genre
			// reference resolved to a name, worth surfacing in lint (README promises
			// dump and lint both report it) but it does not flip the clean exit.
			out = append(out, Finding{LintInfo, w.Code.String(), w.Message, ""})
		}
	}
	return out
}

// lintFamilies reports canonical keys whose source fields disagree (a value was
// not selected because multiple native fields supplied conflicting values). A key
// is reported once even when several of its family entries are unselected: one
// conflict per key, so a consumer counting findings does not double-count a single
// disagreement (the parse warning already surfaces it once). The wording is the shared
// [core.ConflictingFamiliesMessage] - the same one the parser's conflicting-families
// warning uses - so dump and lint read identically; the key lives in the Finding.Key
// field (kept structured for JSON consumers, like the other key-specific findings), and
// Finding.String renders it as the " (KEY)" suffix the dump warning appends inline.
func lintFamilies(fams []core.FamilyValue) []Finding {
	var out []Finding
	seen := map[tag.Key]bool{}
	for _, f := range fams {
		if f.Selected || seen[f.Key] {
			continue
		}
		seen[f.Key] = true
		out = append(out, Finding{
			LintWarning, "conflicting-families",
			core.ConflictingFamiliesMessage(), f.Key,
		})
	}
	return out
}

// duplicatePictureMessage and multipleFrontCoversMessage are the shared human
// messages for the duplicate-picture and multiple-front-covers conditions, so the
// linter's whole-set finding (lintPictures) and the editor's edit-scoped plan warning
// (appendPictureWarnings) read identically - only their scope differs, not the
// wording, so a reword cannot make the two silently disagree on the same file.
func duplicatePictureMessage(t core.PictureType) string {
	return fmt.Sprintf("identical %s picture appears more than once", t)
}

func multipleFrontCoversMessage(fronts int) string {
	return fmt.Sprintf("%d front-cover pictures", fronts)
}

// lintPictures reports duplicate covers, redundant front covers, and the
// single-icon rule.
func lintPictures(pics []Picture) []Finding {
	var out []Finding
	seen := map[[32]byte]bool{}
	fronts := 0
	for _, p := range pics {
		// A picture the codec could not sniff is stored as the unrecognized-image MIME;
		// key on that (not a re-sniff) so a cover a codec already recognized is never
		// false-flagged. Reported only - never auto-fixed - since a valid but
		// unsniffable cover (WebP/AVIF) degrades to exactly this, and dropping it
		// would be silent data loss.
		if p.Unrecognized() {
			out = append(out, Finding{LintWarning, "invalid-picture",
				fmt.Sprintf("%s picture is not a recognized image type (%s)", p.Type, p.MIME), ""})
		}
		h := p.Hash()
		if seen[h] {
			out = append(out, Finding{LintWarning, "duplicate-picture", duplicatePictureMessage(p.Type), ""})
		}
		seen[h] = true
		if p.Type == core.PicFrontCover {
			fronts++
		}
	}
	if fronts > 1 {
		out = append(out, Finding{LintWarning, "multiple-front-covers", multipleFrontCoversMessage(fronts), ""})
	}
	if icon, otherIcon := core.CountIcons(pics); icon > 1 || otherIcon > 1 {
		out = append(out, Finding{LintError, "duplicate-icon",
			"picture types 1/2 must be unique", ""})
	}
	return out
}

// lintValues reports tag values that violate their key's typed contract, driven by
// the shared [tag.ValidatorFor] registry so the linter and the CLI's set-time note
// ([noteMalformedValue]) apply exactly the same rule per category - numeric, date,
// boolean, MEDIATYPE (a non-negative int), and ReplayGain (a decimal/dB). This is the
// single source the "lint and set agree" contract needs: it folds in the former
// lintDates/lintNumbers and closes the gap where COMPILATION was set-validated but not
// lint-validated, and MEDIATYPE/REPLAYGAIN at neither. A present-but-empty value is
// skipped (set blesses it as the benign "empty value" advisory and writes it, so lint
// must agree); RATING is uncovered (free-form across formats). Each finding is a
// LintWarning, so a file with e.g. TRACKNUMBER=abc flips to a non-zero lint exit (a
// deliberate expansion of lint coverage, V3). Iterating the key names and Get-ing only
// the keys with a contract (mirroring the prior helpers) clones at most those few value
// slices, not the whole set.
func lintValues(ts tag.TagSet) []Finding {
	var out []Finding
	for _, k := range ts.Keys() {
		val, ok := tag.ValidatorFor(k)
		if !ok {
			continue
		}
		vals, _ := ts.Get(k)
		for _, v := range vals {
			if v != "" && !val.Valid(k, v) {
				out = append(out, Finding{LintWarning, val.LintCode,
					fmt.Sprintf("%q %s", v, val.LintDetail), k})
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
				out = append(out, Finding{LintInfo, "negative-numeric",
					fmt.Sprintf("%q is negative (numbering is normally non-negative)", v), k})
			}
		}
	}
	return out
}

// lintCardinality reports known keys that canonically hold a single value but carry
// more than one - e.g. a transcoded file projecting ENCODER to a muxer value plus a
// codec value across two Matroska scopes. The typed accessor would silently read
// only the first, so surfacing the duplication keeps that lossiness visible. A
// multi-valued key (artist, genre, ...) is exempt, and so is a custom (unknown)
// key: it has no typed accessor, so its values are read back in full via
// TagSet.Get, and it is already reported by the custom-key rule. Flagging it here
// would be a false positive (multiple values in a custom field are legitimate).
func lintCardinality(ts tag.TagSet) []Finding {
	var out []Finding
	for k, vals := range ts.All() {
		if k.SingleValuedMulti(len(vals)) {
			out = append(out, Finding{LintWarning, "single-valued-multi",
				fmt.Sprintf("single-valued key holds %d values", len(vals)), k})
		}
	}
	return out
}

// lintCustomKeys reports keys outside the published canonical vocabulary. A custom
// field round-trips faithfully, so this is informational, never a warning: it
// never flips a clean file to a non-zero exit, it just tells a tagger which fields
// are non-standard.
func lintCustomKeys(ts tag.TagSet) []Finding {
	var out []Finding
	for _, k := range ts.Keys() {
		if !k.Known() {
			out = append(out, Finding{LintInfo, "custom-key", "custom field, not a known key", k})
		}
	}
	return out
}
