package waxlabel

import "github.com/colespringer/waxlabel/tag"

// LintFix is the safe, non-destructive remediation derived from a document's
// lint findings: the tag patch and write options that, applied together and
// saved, clear what can be safely cleared. It is deliberately conservative -
// only the encoder stamp and stale legacy containers are touched - so applying
// it can never lose data a user might want to keep.
type LintFix struct {
	Patch   tag.TagPatch
	Options []WriteOption
}

// PlanLintFix maps a document's lint findings to the safe remediation. Two
// finding classes are auto-fixed, both non-destructive:
//
//   - inherited-encoder: clear the ENCODER software stamp ([tag.Encoder]);
//   - stray-leading-id3 / trailing-id3v1 / legacy-ape: strip the legacy
//     ID3v1/APEv2/stray-ID3 containers ([WithLegacyPolicy] [LegacyStrip]).
//
// The finding codes are the canonical parse-warning codes (the same ones dump
// prints), so this keys off exactly what lint reports - no private alias to keep in
// step with the linter (C1). No other finding is acted on: dropping an
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
	for _, f := range d.Lint() {
		switch f.Code {
		case "inherited-encoder":
			if !encoderCleared {
				fix.Patch.Clear(tag.Encoder)
				fix.Options = append(fix.Options, WithStripEncoderStamp())
				encoderCleared = true
			}
		case "stray-leading-id3", "trailing-id3v1", "legacy-ape":
			if !legacyStripped {
				fix.Options = append(fix.Options, WithLegacyPolicy(LegacyStrip))
				legacyStripped = true
			}
		}
	}
	return fix
}
