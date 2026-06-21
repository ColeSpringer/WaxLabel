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
//   - encoder-noise: clear the ENCODER software stamp ([tag.Encoder]);
//   - stale-legacy-tag: strip the legacy ID3v1/APEv2/stray-ID3 containers
//     ([WithLegacyPolicy] [LegacyStrip]).
//
// No other finding is acted on: dropping an unsniffable-but-valid cover would be
// silent data loss, a malformed date cannot be guessed, conflicting families
// have no winner, and missing audio cannot be synthesized. The encoder-noise fix
// both clears the canonical ENCODER key and (via [WithStripEncoderStamp]) drops
// the WAV ISFT stamp that clearing the key cannot reach; the Ogg/Opus/FLAC vendor
// string is a mandatory codec field, so it is reported but not removed and a
// re-lint of one of those still flags it. The honest measure of what was fixed is
// a fresh lint of the saved file, not this plan. It reads only the parsed document
// (no I/O) and never modifies it.
func (d *Document) PlanLintFix() LintFix {
	var fix LintFix
	encoderCleared, legacyStripped := false, false
	for _, f := range d.Lint() {
		switch f.Code {
		case "encoder-noise":
			if !encoderCleared {
				fix.Patch.Clear(tag.Encoder)
				fix.Options = append(fix.Options, WithStripEncoderStamp())
				encoderCleared = true
			}
		case "stale-legacy-tag":
			if !legacyStripped {
				fix.Options = append(fix.Options, WithLegacyPolicy(LegacyStrip))
				legacyStripped = true
			}
		}
	}
	return fix
}
