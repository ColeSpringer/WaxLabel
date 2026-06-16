package waxlabel

import "github.com/colespringer/waxlabel/internal/core"

// Option types are distinct per phase so an option valid only for one phase
// cannot be passed to another at compile time.
type (
	// ParseOption configures Parse, ParseFile, and OpenSource.
	ParseOption func(*core.ParseOptions)
	// WriteOption configures Prepare and the save destinations.
	WriteOption func(*core.WriteOptions)
	// HashOption configures audio-essence hashing.
	HashOption func(*hashOptions)
)

// WithLimits sets the bounded-allocation and recursion limits for untrusted
// input.
func WithLimits(l Limits) ParseOption {
	return func(o *core.ParseOptions) { o.Limits = l }
}

// WithPadding sets the post-metadata padding policy for writes.
func WithPadding(p PaddingPolicy) WriteOption {
	return func(o *core.WriteOptions) { o.Padding = p }
}

// WithLegacyPolicy sets how legacy/foreign tag containers are handled.
func WithLegacyPolicy(p LegacyPolicy) WriteOption {
	return func(o *core.WriteOptions) { o.Legacy = p }
}

// WithPreserveModTime keeps the file's modification time across a save-back
// (by default the mtime is updated so scanners notice the edit).
func WithPreserveModTime() WriteOption {
	return func(o *core.WriteOptions) { o.PreserveModTime = true }
}

// WithVerifyEssence hashes the audio essence the rewrite copies and checks it
// against the source's parsed extent, confirming the plan copies the audio it
// parsed (and catching a source that changes mid-write). It does not re-read
// the written file, so it is not an end-to-end output check.
func WithVerifyEssence() WriteOption {
	return func(o *core.WriteOptions) { o.VerifyEssence = true }
}

// WithNumericGenre writes a recognized genre as its numeric reference (in
// formats that support one, such as ID3's TCON) instead of the name. By default
// the canonical name is written.
func WithNumericGenre() WriteOption {
	return func(o *core.WriteOptions) { o.NumericGenre = true }
}

// WithID3MultiValue selects how multiple values for one field are stored in an
// ID3v2.3 tag, which has no standard multi-value text form. ID3v2.4 always
// NUL-separates regardless; the v2.3 compatibility impact is flagged in the
// write report.
func WithID3MultiValue(p ID3MultiValuePolicy) WriteOption {
	return func(o *core.WriteOptions) { o.ID3Multi = p }
}

// Policy presets bundle write options into a named intent. Apply one first,
// then override individual options if needed:
//
//	plan, _ := ed.Prepare(waxlabel.Preserve, waxlabel.WithVerifyEssence())
var (
	// Preserve is the default: keep legacy containers, reuse padding in place.
	Preserve WriteOption = func(o *core.WriteOptions) {
		o.Legacy = core.LegacyPreserve
		o.Padding = core.DefaultPadding
	}
	// Compatible favors maximum reader compatibility.
	Compatible WriteOption = func(o *core.WriteOptions) {
		o.Legacy = core.LegacyPreserve
		o.Padding = core.PaddingPolicy{Target: 4096, Max: 1 << 20, ReuseInPlace: true}
	}
	// Canonical normalizes aggressively: legacy containers are reconciled into
	// the native tags, modest padding.
	Canonical WriteOption = func(o *core.WriteOptions) {
		o.Legacy = core.LegacyReconcile
		o.Padding = core.PaddingPolicy{Target: 4096, Max: 1 << 20, ReuseInPlace: true}
	}
	// Minimal writes the smallest reasonable file: no padding, strip legacy.
	Minimal WriteOption = func(o *core.WriteOptions) {
		o.Legacy = core.LegacyStrip
		o.Padding = core.PaddingPolicy{Target: 0, Max: 0}
	}
)

func resolveParseOptions(opts []ParseOption) core.ParseOptions {
	o := core.DefaultParseOptions()
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

func resolveWriteOptions(opts []WriteOption) core.WriteOptions {
	o := core.DefaultWriteOptions()
	for _, fn := range opts {
		fn(&o)
	}
	return o
}
