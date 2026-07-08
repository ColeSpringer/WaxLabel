package core

import "github.com/colespringer/waxlabel/internal/bits"

// LegacyPolicy controls what happens to legacy/foreign tag containers (stray
// leading ID3v2, trailing ID3v1, APEv2) when writing. The default preserves
// them - WaxLabel never strips silently.
type LegacyPolicy uint8

const (
	// LegacyPreserve keeps legacy containers byte-for-byte and warns.
	LegacyPreserve LegacyPolicy = iota
	// LegacyStrip removes them.
	LegacyStrip
)

func (p LegacyPolicy) String() string {
	switch p {
	case LegacyStrip:
		return "strip"
	default:
		return "preserve"
	}
}

// PaddingPolicy controls how much free space to leave after the metadata so a
// later edit can grow in place without rewriting the audio. It is per-format
// configurable rather than a blanket allowance: too little will not fit a
// cover, too much wastes space at library scale.
type PaddingPolicy struct {
	// Target is the padding to aim for after a rewrite, in bytes.
	Target int64
	// Min and Max bound the padding actually written, and Min is also a reuse
	// floor: a rewrite reuses the existing region in place only while the leftover
	// is still >= Min, otherwise it falls back to the clamped Target (so an
	// explicit "reserve at least Min" grows a too-small region instead of silently
	// reusing it). Max == 0 means "no explicit limit" (the format's hard cap still
	// applies), so a zero-value policy with a positive Target is honored rather than
	// clamped to nothing; to write no padding, set Target to 0.
	Min int64
	Max int64
	// ReuseInPlace lets a rewrite that fits within existing padding avoid
	// moving the audio at all.
	ReuseInPlace bool
}

// DefaultPadding is a sensible FLAC default: a few KiB, reused in place when
// possible.
var DefaultPadding = PaddingPolicy{Target: 8192, Min: 0, Max: 1 << 20, ReuseInPlace: true}

// ClampTarget returns Target bounded by Min and Max: a Max of 0 means "no upper
// bound" (the caller still applies the format's hard cap), Target is floored to
// Min, and a negative result to 0. This is the single definition of the padding
// clamp the codecs share, so the "Max == 0 is unbounded" contract cannot drift
// between per-codec copies.
func (p PaddingPolicy) ClampTarget() int64 {
	v := p.Target
	if p.Max > 0 && v > p.Max {
		v = p.Max
	}
	if v < p.Min {
		v = p.Min
	}
	if v < 0 {
		v = 0
	}
	return v
}

// ReuseOrTarget sizes the padding for a rewrite whose metadata sits in a single
// front region (the ID3 front-tag codecs, MP3 and AAC). With ReuseInPlace and
// new content that fits the original region, it fills the region exactly so the
// audio offset and file size do not change - but only while the leftover is still
// >= Min, so an explicit padding floor grows a too-small region rather than
// reusing it; otherwise it falls back to the clamped Target (which also floors to
// Min). origLen is the original region length, contentLen the new non-padding
// content length. The origLen >= contentLen guard runs first, so origLen-contentLen
// is non-negative before the floor comparison.
func (p PaddingPolicy) ReuseOrTarget(origLen, contentLen int64) int64 {
	if p.ReuseInPlace && origLen >= contentLen && origLen-contentLen >= p.Min {
		return origLen - contentLen
	}
	return p.ClampTarget()
}

// ID3MultiValuePolicy controls how multiple values for one field are written in
// ID3v2.3, which has no standard multi-value text representation. ID3v2.4 always
// NUL-separates regardless of this setting; the compatibility impact of the v2.3
// choice is flagged in the write report.
type ID3MultiValuePolicy uint8

const (
	// ID3MultiNullSep stores values in one frame separated by NUL bytes - the
	// v2.4 form; round-trips losslessly but is a de-facto extension in v2.3.
	ID3MultiNullSep ID3MultiValuePolicy = iota
	// ID3MultiRepeatFrame writes one frame per value, so a reader that takes the
	// first frame still sees a value.
	ID3MultiRepeatFrame
	// ID3MultiSlash joins values with " / " into a single value: maximally
	// compatible but not separable on read-back.
	ID3MultiSlash
)

func (p ID3MultiValuePolicy) String() string {
	switch p {
	case ID3MultiRepeatFrame:
		return "repeat-frame"
	case ID3MultiSlash:
		return "slash-join"
	default:
		return "null-separated"
	}
}

// ParseOptions are the resolved (non-functional) parse settings a codec sees.
type ParseOptions struct {
	Limits bits.Limits
	// SourceName is the display name for this source in detection diagnostics (the
	// "could not identify %q" error). A caller parsing from a temp file or buffer
	// passes the original name so the temp path never leaks - the CLI supplies "-"'s
	// display form for buffered standard input. It is display-only: detection still
	// keys on the real path's extension. Empty falls back to the path argument (and
	// is "" for the path-less Parse/OpenSource, which is exactly what this fixes).
	SourceName string
}

// DefaultParseOptions returns parse options with conservative limits.
func DefaultParseOptions() ParseOptions {
	return ParseOptions{Limits: bits.DefaultLimits}
}

// WriteOptions are the resolved write settings a codec sees.
type WriteOptions struct {
	Limits  bits.Limits
	Padding PaddingPolicy
	// PaddingExplicit marks the padding policy as a user request rather than the
	// opportunistic default. Codecs use it to run their authoritative serializer even when
	// no tag, picture, or legacy change is pending, so padding-only edits are not lost to
	// the fast-path no-op gate.
	PaddingExplicit bool
	Legacy          LegacyPolicy
	PreserveModTime bool
	// VerifyEssence hashes the audio essence while it is copied and checks it
	// against the source's parsed extent.
	VerifyEssence bool
	// NumericGenre writes a recognized genre as its numeric reference (ID3 TCON)
	// rather than the name. Off by default (canonical name on write).
	NumericGenre bool
	// ID3Multi selects the ID3v2.3 multi-value representation.
	ID3Multi ID3MultiValuePolicy
	// AllowUnrecognizedPictures opts the added-picture validation in [Editor.Prepare]
	// out, so a picture whose bytes are not a recognized image header (an exotic
	// HEIC/AVIF/JXL cover, or a transfer carrying an already-embedded one) is embedded
	// rather than rejected. Off by default: a junk or empty picture is refused.
	AllowUnrecognizedPictures bool
	// StripEncoderStamp asks writers to remove inherited encoder stamps held outside the
	// canonical tag set. WAV drops a transcoder-stamped ISFT INFO item. FLAC, Ogg Vorbis,
	// and Opus rewrite a transcoder-stamped comment-header vendor string to a neutral value
	// because the vendor field is mandatory. All paths gate on IsTranscoderStamp. Off by
	// default; the CLI enables it when an edit clears, sets, or strips ENCODER.
	StripEncoderStamp bool
	// WebMSubset narrows a file-less Matroska capability query to the WebM subset, so
	// the format-level question ("what can a .webm hold?") reports cover-art write as
	// unsupported - the same restriction the Matroska codec applies to a parsed WebM
	// file, reused here so the file-aware and file-less views cannot drift. Only the
	// Matroska Capabilities path consults it; every other codec ignores it.
	WebMSubset bool
	// Carried marks this write as a faithful cross-format carry (a transfer/copy), not a
	// user-authored edit, so codecs suppress author-convenience heuristics that would
	// mislabel carried data. Off by default; the transfer engine sets it. Its first use is
	// the ID3 SYLT language fallback: an authored line-only edit inherits the destination's
	// existing SYLT language (a documented CLI convenience), but a carry of a no-language
	// lyric set (FLAC/Ogg have no language) must not, or the report says "carried" while the
	// bytes gain a language the source never had.
	Carried bool
}

// DefaultWriteOptions returns the preservation-first defaults.
func DefaultWriteOptions() WriteOptions {
	return WriteOptions{
		Limits:  bits.DefaultLimits,
		Padding: DefaultPadding,
		Legacy:  LegacyPreserve,
	}
}
