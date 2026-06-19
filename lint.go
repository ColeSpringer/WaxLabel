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

func (f Finding) String() string {
	if f.Key != "" {
		return fmt.Sprintf("[%s] %s: %s (%s)", f.Severity, f.Code, f.Message, f.Key)
	}
	return fmt.Sprintf("[%s] %s: %s", f.Severity, f.Code, f.Message)
}

// Lint inspects a document for issues a tagger would want to surface or fix:
// stale legacy containers, inherited encoder noise, conflicting family values,
// duplicate or invalid pictures, malformed dates, single-valued keys carrying
// several values, and custom (non-vocabulary) keys. It reads only the parsed
// document (no I/O) and never modifies it.
func (d *Document) Lint() []Finding {
	var out []Finding

	out = append(out, lintWarnings(d.media.Warnings)...)
	out = append(out, lintFamilies(d.media.Families)...)
	out = append(out, lintPictures(d.media.Pictures)...)
	out = append(out, lintDates(d.media.Tags)...)
	out = append(out, lintCardinality(d.media.Tags)...)
	out = append(out, lintCustomKeys(d.media.Tags)...)
	return out
}

// lintWarnings promotes the parse-time warnings that a tagger usually acts on.
func lintWarnings(ws []core.Warning) []Finding {
	var out []Finding
	for _, w := range ws {
		switch w.Code {
		case core.WarnStrayLeadingID3, core.WarnTrailingID3v1, core.WarnLegacyAPE:
			out = append(out, Finding{LintWarning, "stale-legacy-tag", w.Message, ""})
		case core.WarnInheritedEncoder:
			out = append(out, Finding{LintWarning, "encoder-noise", w.Message, ""})
		case core.WarnMultipleVorbisComment, core.WarnDuplicateTagBlock:
			out = append(out, Finding{LintError, "duplicate-tag-block", w.Message, ""})
		case core.WarnInvalidPicture:
			out = append(out, Finding{LintWarning, "invalid-picture", w.Message, ""})
		case core.WarnNoAudioFrames:
			out = append(out, Finding{LintError, "no-audio", w.Message, ""})
		case core.WarnTruncatedAudio:
			out = append(out, Finding{LintWarning, "truncated-audio", w.Message, ""})
		}
	}
	return out
}

// lintFamilies reports canonical keys whose source fields disagree (a value was
// not selected because multiple native fields supplied conflicting values).
func lintFamilies(fams []core.FamilyValue) []Finding {
	var out []Finding
	for _, f := range fams {
		if !f.Selected {
			out = append(out, Finding{
				LintWarning, "conflicting-families",
				"multiple source fields supplied conflicting values", f.Key,
			})
		}
	}
	return out
}

// lintPictures reports duplicate covers, redundant front covers, and the
// single-icon rule.
func lintPictures(pics []Picture) []Finding {
	var out []Finding
	seen := map[[32]byte]bool{}
	fronts := 0
	for _, p := range pics {
		// A picture the codec could not sniff is stored as application/octet-stream;
		// key on that MIME (not a re-sniff) so a cover a codec already recognized is
		// never false-flagged. Reported only - never auto-fixed - since a valid but
		// unsniffable cover (WebP/AVIF) degrades to exactly this, and dropping it
		// would be silent data loss.
		if p.MIME == "application/octet-stream" {
			out = append(out, Finding{LintWarning, "invalid-picture",
				fmt.Sprintf("%s picture is not a recognized image type (%s)", p.Type, p.MIME), ""})
		}
		h := p.Hash()
		if seen[h] {
			out = append(out, Finding{LintWarning, "duplicate-picture",
				fmt.Sprintf("identical %s picture appears more than once", p.Type), ""})
		}
		seen[h] = true
		if p.Type == core.PicFrontCover {
			fronts++
		}
	}
	if fronts > 1 {
		out = append(out, Finding{LintWarning, "multiple-front-covers",
			fmt.Sprintf("%d front-cover pictures", fronts), ""})
	}
	if icon, otherIcon := core.CountIcons(pics); icon > 1 || otherIcon > 1 {
		out = append(out, Finding{LintError, "duplicate-icon",
			"picture types 1/2 must be unique", ""})
	}
	return out
}

// lintDates reports date fields that are not ISO-8601 year, year-month, or full
// dates. It filters by [tag.IsDateKey] and validates with [tag.ValidPartialDate],
// the single date-key set and validator shared with the CLI's set-time
// malformed-value note, so the two cannot disagree - and it now covers
// AcquisitionDate alongside the recording/release/original dates.
func lintDates(ts tag.TagSet) []Finding {
	var out []Finding
	// Iterate the key names and Get only the date keys, rather than ranging All()
	// (which clones every key's value slice) - a 40-tag file then clones at most the
	// few date-key slices, not all 40.
	for _, k := range ts.Keys() {
		if !tag.IsDateKey(k) {
			continue
		}
		vals, _ := ts.Get(k)
		for _, v := range vals {
			if !tag.ValidPartialDate(v) {
				out = append(out, Finding{LintWarning, "malformed-date",
					fmt.Sprintf("%q is not YYYY, YYYY-MM, or YYYY-MM-DD", v), k})
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
