package waxlabel

import (
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// LintFix is the safe, non-destructive remediation derived from a document's
// lint findings: the tag patch and write options that, applied together and
// saved, clear what can be safely cleared. It is deliberately conservative -
// only the encoder stamp and provably-redundant legacy containers are touched -
// so applying it can never lose data a user might want to keep.
type LintFix struct {
	Patch   tag.TagPatch
	Options []WriteOption
}

// PlanLintFix maps a document's lint findings to the safe remediation. Two
// finding classes are auto-fixed, both non-destructive:
//
//   - inherited-encoder: clear the ENCODER software stamp ([tag.Encoder]);
//   - stray-leading-id3 / trailing-id3v1 / legacy-ape: strip the legacy
//     ID3v1/APEv2/stray-ID3 containers ([WithLegacyPolicy] [LegacyStrip]), but only
//     when WaxLabel can prove them fully redundant with the canonical set.
//
// The legacy strip is [LegacyStrip], which is all-or-nothing (it strips every legacy
// container). So it is skipped when any legacy container holds unique data the strip
// would destroy - a tag present only in a legacy container ([Document.LegacyOnlyKeys])
// or non-tag content the projection does not fold in ([Document.HasOpaqueLegacyContent]:
// an APEv2 binary item, a leading ID3v2's pictures/chapters/lyrics, or an unreadable
// container). A mixed file (one redundant container plus one carrying unique data)
// conservatively keeps both; the pre-existing legacy warning still fires so lint exits
// non-zero, and the legacy-only-tags info explains why the container was preserved. An
// explicit [WithLegacyPolicy] [LegacyStrip] still strips unconditionally.
//
// The finding codes are the canonical parse-warning codes (the same ones dump
// prints), so this keys off exactly what lint reports - no private alias to keep in
// step with the linter. No other finding is acted on: dropping an
// unsniffable-but-valid cover would be silent data loss, a malformed date cannot be
// guessed, conflicting families have no winner, and missing audio cannot be
// synthesized. The encoder fix clears the canonical ENCODER key and, via
// [WithStripEncoderStamp], also handles native stamps the canonical key cannot reach:
// the WAV ISFT INFO item and the FLAC/Ogg/Opus comment-header vendor string. Vendor fields
// are mandatory, so they are rewritten to a neutral value instead of removed. This plan is
// derived from the parsed document only; the saved file's next lint is the final result.
func (d *Document) PlanLintFix() LintFix {
	var fix LintFix
	encoderCleared, legacyStripped := false, false
	// Auto-strip a legacy container only when it is provably, fully redundant with the
	// canonical set. Computed once (the primitives would otherwise run per matching finding).
	legacyLoses := len(d.LegacyOnlyKeys()) > 0 || d.HasOpaqueLegacyContent()
	for _, f := range d.Lint() {
		switch f.Code {
		case "inherited-encoder":
			if !encoderCleared {
				// Remove only the transcoder-stamp values from a (possibly multi-valued) ENCODER,
				// preserving any clean user-set value. The inherited-encoder finding also fires on a
				// bare Vorbis vendor string or WAV ISFT with no ENCODER tag at all, so an
				// unconditional Clear would destroy a clean ENCODER as collateral - but checking
				// only the FIRST value would equally miss a stamp in a later value (EncoderNoise
				// flags any stamped ENCODER comment) or clear a clean earlier value. So filter:
				// clear when every value is a stamp, set the survivors when only some are, and leave
				// a stamp-free ENCODER untouched. IsTranscoderStamp reuses the linter's own noise
				// test (matches Lavf/libavformat, not Lavc), so a "Lavc.. libvorbis" ENCODER is
				// preserved while a "Lavf.." one is removed - the filter can never disagree with the
				// finding. WithStripEncoderStamp stays OUTSIDE this gate: it neutralizes the vendor
				// string and ISFT (neither a canonical ENCODER tag), which must still be remediated
				// when only they carry the stamp. Do not fold them.
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
			if !legacyStripped && !legacyLoses {
				fix.Options = append(fix.Options, WithLegacyPolicy(LegacyStrip))
				legacyStripped = true
			}
		}
	}
	return fix
}
