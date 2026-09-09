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
//   - inherited-encoder: remove the transcoder-stamp values from ENCODER
//     ([tag.Encoder]), clearing the key only when every value is a stamp, and neutralize a
//     container vendor string or WAV ISFT item through [WithStripEncoderStamp];
//   - stray-leading-id3 / trailing-id3v1 / legacy-ape: strip the legacy
//     ID3v1/APEv2/stray-ID3 containers ([WithLegacyPolicy] [LegacyStrip]), but only
//     when WaxLabel can prove them fully redundant with the canonical set.
//
// The encoder remediation is attempted for every inherited-encoder finding, not gated on
// [Finding.Fixable]. The two say different things and both are wanted: Fixable answers "will
// this finding go away", which is false for a stamp stored under a key nothing here reaches
// (see [Document.encoderStampReachable]), while the option is unconditional because it is
// always safe, writes nothing when there is nothing to strip, and reaches a vendor string no
// canonical key names - a store no document-level predicate can see. A file whose only stamp
// is out of reach therefore plans a write that changes nothing and lints the same afterward,
// which is what lint --fix reports as "not auto-fixed".
//
// The legacy strip is [LegacyStrip], which is all-or-nothing (it strips every legacy
// container). That one IS gated on the finding's own [Finding.Fixable], which the linter
// computes from the same primitives, so the marker a consumer reads and the strip this plans
// cannot disagree: a strip is skipped when any legacy container holds unique data it would destroy
// - a tag present only in a legacy container ([Document.LegacyOnlyKeys]) or non-tag content
// the projection does not fold in ([Document.HasOpaqueLegacyContent]: an APEv2 binary item,
// a leading ID3v2's pictures/chapters/lyrics, or an unreadable container). A mixed file (one redundant container plus one carrying unique data)
// conservatively keeps both; the pre-existing legacy warning still fires so lint exits
// non-zero, and the legacy-only-tags info explains why the container was preserved. An
// explicit [WithLegacyPolicy] [LegacyStrip] still strips unconditionally, but warns about
// what it destroys - the two gates are exact complements, so this plan can never trip that
// warning.
//
// The finding codes are the canonical parse-warning codes (the same ones dump
// prints), so this keys off exactly what lint reports - no private alias to keep in
// step with the linter. No other finding is acted on: dropping an
// unsniffable-but-valid cover would be silent data loss, a malformed date cannot be
// guessed, conflicting families have no winner, and missing audio cannot be
// synthesized. The encoder fix removes the transcoder-stamp values from the canonical
// ENCODER key (clearing it only when every value is a stamp) and, via
// [WithStripEncoderStamp], also strips the WAV ISFT INFO item and neutralizes the
// FLAC/Ogg/Opus comment-header vendor string, which no canonical key reaches. Vendor
// fields are mandatory, so they are rewritten to a neutral value instead of removed. This
// plan is derived from the parsed document only; the saved file's next lint is the final
// result.
func (d *Document) PlanLintFix() LintFix {
	var fix LintFix
	encoderCleared, legacyStripped := false, false
	for _, f := range d.Lint() {
		switch f.Code {
		case "inherited-encoder":
			if !encoderCleared {
				// Remove only the transcoder-stamp values from a (possibly multi-valued) ENCODER,
				// preserving any clean user-set value. The inherited-encoder finding also fires on a
				// bare Vorbis vendor string with no ENCODER tag at all, so an unconditional Clear
				// would destroy a clean ENCODER as collateral - but checking only the FIRST value
				// would equally miss a stamp in a later value (EncoderNoise flags any stamped
				// ENCODER comment) or clear a clean earlier value. So filter:
				// clear when every value is a stamp, set the survivors when only some are, and leave
				// a stamp-free ENCODER untouched. IsTranscoderStamp reuses the linter's own noise
				// test, so the filter can never disagree with the finding: every value it names a
				// stamp (Lavf/libavformat and Lavc/libavcodec alike) goes, and a genuine user-set
				// ENCODER stays. WithStripEncoderStamp stays OUTSIDE this gate: it neutralizes the vendor
				// string, which is no canonical ENCODER tag and must still be remediated when only it
				// carries the stamp. Do not fold them. The WAV ISFT item is reached by both this filter
				// and the option, which apply the same per-value test and so agree.
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
