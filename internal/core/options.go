package core

import "github.com/colespringer/waxlabel/internal/bits"

// LegacyPolicy controls what happens to legacy/foreign tag containers (stray
// leading ID3v2, trailing ID3v1, APEv2) when writing. The default preserves
// them — WaxLabel never strips silently.
type LegacyPolicy uint8

const (
	// LegacyPreserve keeps legacy containers byte-for-byte and warns.
	LegacyPreserve LegacyPolicy = iota
	// LegacyStrip removes them.
	LegacyStrip
	// LegacyReconcile copies their values into the native tags, then removes.
	LegacyReconcile
	// LegacyUpdateExisting rewrites a legacy container only if it already
	// exists, otherwise leaves it absent.
	LegacyUpdateExisting
)

func (p LegacyPolicy) String() string {
	switch p {
	case LegacyStrip:
		return "strip"
	case LegacyReconcile:
		return "reconcile"
	case LegacyUpdateExisting:
		return "update-existing"
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
	// Min and Max bound the padding actually written. Max == 0 means "no
	// explicit limit" (the format's hard cap still applies), so a zero-value
	// policy with a positive Target is honored rather than clamped to nothing;
	// to write no padding, set Target to 0.
	Min int64
	Max int64
	// ReuseInPlace lets a rewrite that fits within existing padding avoid
	// moving the audio at all.
	ReuseInPlace bool
}

// DefaultPadding is a sensible FLAC default: a few KiB, reused in place when
// possible.
var DefaultPadding = PaddingPolicy{Target: 8192, Min: 0, Max: 1 << 20, ReuseInPlace: true}

// ParseOptions are the resolved (non-functional) parse settings a codec sees.
type ParseOptions struct {
	Limits bits.Limits
}

// DefaultParseOptions returns parse options with conservative limits.
func DefaultParseOptions() ParseOptions {
	return ParseOptions{Limits: bits.DefaultLimits}
}

// WriteOptions are the resolved write settings a codec sees.
type WriteOptions struct {
	Limits          bits.Limits
	Padding         PaddingPolicy
	Legacy          LegacyPolicy
	PreserveModTime bool
	// VerifyEssence hashes the audio essence while it is copied and checks it
	// against the source's parsed extent.
	VerifyEssence bool
}

// DefaultWriteOptions returns the preservation-first defaults.
func DefaultWriteOptions() WriteOptions {
	return WriteOptions{
		Limits:  bits.DefaultLimits,
		Padding: DefaultPadding,
		Legacy:  LegacyPreserve,
	}
}
