package waxlabel

import "github.com/colespringer/waxlabel/internal/core"

// DefaultMaxSourceBytes is the default ceiling [OpenSource] applies when
// buffering a non-seekable stream (2 GiB). Exceeding it fails with
// [waxerr.ErrSizeTooLarge]; [WithMaxSourceBytes](0) lifts the cap. CLI uses the
// same default for stdin via --max-size.
const DefaultMaxSourceBytes = core.DefaultMaxSourceBytes

// Option types are distinct per phase so a wrong-phase option fails at compile time.
type (
	// ParseOption configures Parse, ParseFile, and OpenSource.
	ParseOption func(*core.ParseOptions)
	// WriteOption configures Prepare and save destinations.
	WriteOption func(*core.WriteOptions)
	// HashOption configures audio-essence hashing.
	HashOption func(*hashOptions)
)

// WithLimits sets allocation and recursion limits for untrusted input. A zero
// field uses the default for that field (not unlimited).
func WithLimits(l Limits) ParseOption {
	return func(o *core.ParseOptions) {
		d := core.DefaultParseOptions().Limits
		// Non-positive means unset: use default.
		if l.MaxAllocBytes <= 0 {
			l.MaxAllocBytes = d.MaxAllocBytes
		}
		if l.MaxDepth <= 0 {
			l.MaxDepth = d.MaxDepth
		}
		if l.MaxElements <= 0 {
			l.MaxElements = d.MaxElements
		}
		o.Limits = l
	}
}

// WithMaxSourceBytes bounds how many bytes [OpenSource] buffers from a
// non-seekable stream. n <= 0 disables the cap. Default is [DefaultMaxSourceBytes].
// No effect on [Parse] or [ParseFile].
func WithMaxSourceBytes(n int64) ParseOption {
	return func(o *core.ParseOptions) { o.MaxSourceBytes = n }
}

// WithSourceName sets the display name in "could not identify" diagnostics
// (e.g. original name for buffered stdin). Display-only; detection uses bytes.
func WithSourceName(name string) ParseOption {
	return func(o *core.ParseOptions) { o.SourceName = name }
}

// WithPadding sets post-metadata padding and marks it explicit so a
// padding-only change is still realized.
func WithPadding(p PaddingPolicy) WriteOption {
	return func(o *core.WriteOptions) {
		o.Padding = p
		o.PaddingExplicit = true
	}
}

// WithLegacyPolicy sets legacy-container handling. [LegacyStrip] removes them
// unconditionally; the plan warns [WarnLegacyStripDropped]. [Document.PlanLintFix]
// never chooses strip when that would destroy unique data.
func WithLegacyPolicy(p LegacyPolicy) WriteOption {
	return func(o *core.WriteOptions) { o.Legacy = p }
}

// WithPreserveModTime keeps mtime across save-back (default updates mtime).
func WithPreserveModTime() WriteOption {
	return func(o *core.WriteOptions) { o.PreserveModTime = true }
}

// WithVerifyEssence hashes audio during copy and compares the written output.
// SaveBack/SaveAsFile re-read the temp before commit. WriteTo verifies only
// bytes copied from the source (stream cannot be re-read).
func WithVerifyEssence() WriteOption {
	return func(o *core.WriteOptions) { o.VerifyEssence = true }
}

// WithNumericGenre writes a recognized genre as its numeric reference where
// supported (e.g. ID3 TCON). Default writes the canonical name.
func WithNumericGenre() WriteOption {
	return func(o *core.WriteOptions) { o.NumericGenre = true }
}

// WithUnrecognizedPictures allows embedding a picture [IsRecognizedImage]
// rejects. Default refuses such AddPicture payloads. Pre-existing file pictures
// on a tags-only edit are unaffected.
func WithUnrecognizedPictures() WriteOption {
	return func(o *core.WriteOptions) { o.AllowUnrecognizedPictures = true }
}

// WithStripEncoderStamp drops removable transcoder stamps: WAV ISFT and
// Ogg/Opus/FLAC vendor string (rewritten to a neutral value; vendor is mandatory).
// Judged per-field by [IsTranscoderStamp]. Does not override an edit that sets
// [tag.Encoder].
func WithStripEncoderStamp() WriteOption {
	return func(o *core.WriteOptions) { o.StripEncoderStamp = true }
}

// WithWebMSubset narrows a file-less Matroska [CapabilitiesFor] query to WebM
// (no cover attachments). Write path ignores it.
func WithWebMSubset() WriteOption {
	return func(o *core.WriteOptions) { o.WebMSubset = true }
}

// WithAllowUnsupportedDrop makes [Editor.Prepare] drop unsupported structural
// edits (synced lyrics/chapters with no store, WebM cover, unlabelable image
// formats) with a warning instead of failing. CLI passes this for set/plan;
// --strict promotes the drop warning to failure.
func WithAllowUnsupportedDrop() WriteOption {
	return func(o *core.WriteOptions) { o.AllowUnsupportedDrop = true }
}

// WithKeepR128Gains leaves R128_* untouched when changing output gain, instead
// of rebasing per RFC 7845. Prepare warns output-gain-r128-tags for each kept tag.
func WithKeepR128Gains() WriteOption {
	return func(o *core.WriteOptions) { o.KeepR128Gains = true }
}

// WithID3MultiValue selects ID3v2.3 multi-value storage. ID3v2.4 always NUL-separates.
func WithID3MultiValue(p ID3MultiValuePolicy) WriteOption {
	return func(o *core.WriteOptions) { o.ID3Multi = p }
}

// Policy presets bundle write options. Apply one first, then override:
//
//	plan, _:= ed.Prepare(waxlabel.Preserve, waxlabel.WithVerifyEssence())
var (
	// Preserve is the default: keep legacy, reuse padding in place.
	Preserve WriteOption = func(o *core.WriteOptions) {
		o.Legacy = core.LegacyPreserve
		o.Padding = core.DefaultPadding
	}
	// Compatible favors maximum reader compatibility.
	Compatible WriteOption = func(o *core.WriteOptions) {
		o.Legacy = core.LegacyPreserve
		o.Padding = core.PaddingPolicy{Target: 4096, Max: 1 << 20, ReuseInPlace: true}
		o.PaddingExplicit = true
	}
	// Minimal: no padding, strip legacy (unconditional; warns if unique data lost).
	Minimal WriteOption = func(o *core.WriteOptions) {
		o.Legacy = core.LegacyStrip
		o.Padding = core.PaddingPolicy{Target: 0, Max: 0}
		o.PaddingExplicit = true
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
