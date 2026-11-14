package waxlabel

import "github.com/colespringer/waxlabel/tag"

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
//     ([tag.Encoder]), keeping its other values and any name another store of
//     the format's own holds, and clearing only when no such value is left;
//     neutralize vendor string / WAV ISFT via [WithStripEncoderStamp].
//   - stray-leading-id3 / trailing-id3v1 / legacy-ape: strip legacy containers
//     ([WithLegacyPolicy] [LegacyStrip]) only when fully redundant with the
//     canonical set: a key, a value under a shared key, or opaque content that
//     only a legacy container holds keeps it. An explicit [LegacyStrip] strips
//     unconditionally with a warning; this plan never triggers it.
//
// The fix is planned for a file that refuses every write too, so that preparing
// it reports the refusal; [Finding.Fixable] is false there.
//
// No other findings are acted on. Derived from the parsed document only.
func (d *Document) PlanLintFix() LintFix {
	var out LintFix
	if d.zero() {
		return out
	}
	fix := d.safeFix(d.findings())
	if fix.encoder {
		// Keep clean ENCODER values; clear when all are stamps.
		switch {
		case !fix.stamped:
		case len(fix.left) == 0:
			out.Patch.Clear(tag.Encoder)
		default:
			out.Patch.Set(tag.Encoder, fix.left...)
		}
		out.Options = append(out.Options, WithStripEncoderStamp())
	}
	if fix.stripLegacy {
		out.Options = append(out.Options, WithLegacyPolicy(LegacyStrip))
	}
	return out
}
