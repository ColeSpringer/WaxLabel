package waxlabel

import (
	"fmt"
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
}

func (f Finding) String() string {
	if f.Key != "" {
		return fmt.Sprintf("[%s] %s: %s (%s)", f.Severity, f.Code, f.Message, f.Key)
	}
	return fmt.Sprintf("[%s] %s: %s", f.Severity, f.Code, f.Message)
}

// Lint inspects a document for issues a tagger would want to surface or fix:
// stale legacy containers, inherited encoder noise, conflicting family values,
// duplicate or invalid pictures, and malformed dates. It reads only the parsed
// document (no I/O) and never modifies it.
func (d *Document) Lint() []Finding {
	var out []Finding

	out = append(out, lintWarnings(d.media.Warnings)...)
	out = append(out, lintFamilies(d.media.Families)...)
	out = append(out, lintPictures(d.media.Pictures)...)
	out = append(out, lintDates(d.media.Tags)...)
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
// dates.
func lintDates(ts tag.TagSet) []Finding {
	var out []Finding
	for _, k := range []tag.Key{tag.RecordingDate, tag.ReleaseDate, tag.OriginalDate} {
		vals, ok := ts.Get(k)
		if !ok {
			continue
		}
		for _, v := range vals {
			if !validPartialDate(v) {
				out = append(out, Finding{LintWarning, "malformed-date",
					fmt.Sprintf("%q is not YYYY, YYYY-MM, or YYYY-MM-DD", v), k})
			}
		}
	}
	return out
}

// validPartialDate accepts the ISO-8601 reduced precisions YYYY, YYYY-MM, and
// YYYY-MM-DD. It uses time.Parse so the calendar is checked properly - month
// range, days per month, and leap years - rejecting e.g. 2021-02-31. The exact
// length match enforces zero-padded canonical form (rejecting "2021-6-1").
func validPartialDate(s string) bool {
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if len(s) == len(layout) {
			if _, err := time.Parse(layout, s); err == nil {
				return true
			}
		}
	}
	return false
}
