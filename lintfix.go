package waxlabel

import (
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// LintFix is the safe remediation from lint findings: tag patch and write
// options that clear what can be cleared without data loss. Only the encoder
// stamp and provably-redundant legacy containers are touched.
type LintFix struct {
	Patch   tag.TagPatch
	Options []WriteOption
}

// PlanLintFix maps lint findings to safe remediation:
//
//   - inherited-encoder: remove transcoder-stamp values from ENCODER
//     ([tag.Encoder]), clearing only when every value is a stamp; neutralize
//     vendor string / WAV ISFT via [WithStripEncoderStamp].
//   - stray-leading-id3 / trailing-id3v1 / legacy-ape: strip legacy containers
//     ([WithLegacyPolicy] [LegacyStrip]) only when fully redundant with the
//     canonical set ([Finding.Fixable]).
//
// Encoder remediation is not gated on Fixable: the option is always safe and
// reaches a vendor string no document-level predicate sees. Legacy strip is
// gated on Fixable (unique legacy tags or opaque legacy content keep the
// container). Explicit [LegacyStrip] still strips unconditionally with a
// warning; this plan never trips that warning.
//
// No other findings are acted on. Derived from the parsed document only.
func (d *Document) PlanLintFix() LintFix {
	var fix LintFix
	encoderCleared, legacyStripped := false, false
	for _, f := range d.Lint() {
		switch f.Code {
		case "inherited-encoder":
			if !encoderCleared {
				// Keep clean ENCODER values; clear when all are stamps. Always
				// attach WithStripEncoderStamp for vendor/ISFT.
				if v, ok := d.Get(tag.Encoder); ok {
					clean := make([]string, 0, len(v))
					for _, s := range v {
						if !core.IsTranscoderStamp(s) {
							clean = append(clean, s)
						}
					}
					if len(clean) < len(v) { // at least one stamp value to remove
						if len(clean) == 0 {
							fix.Patch.Clear(tag.Encoder)
						} else {
							fix.Patch.Set(tag.Encoder, clean...)
						}
					}
				}
				fix.Options = append(fix.Options, WithStripEncoderStamp())
				encoderCleared = true
			}
		case "stray-leading-id3", "trailing-id3v1", "legacy-ape":
			if !legacyStripped && f.Fixable {
				fix.Options = append(fix.Options, WithLegacyPolicy(LegacyStrip))
				legacyStripped = true
			}
		}
	}
	return fix
}
