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
// input. A zero field uses the default bound for that field (it is not
// unlimited): a partial Limits{MaxDepth: 8} keeps the default MaxAllocBytes
// rather than dropping it to zero, which would reject every allocation.
func WithLimits(l Limits) ParseOption {
	return func(o *core.ParseOptions) {
		d := core.DefaultParseOptions().Limits
		if l.MaxAllocBytes == 0 {
			l.MaxAllocBytes = d.MaxAllocBytes
		}
		if l.MaxDepth == 0 {
			l.MaxDepth = d.MaxDepth
		}
		o.Limits = l
	}
}

// WithSourceName sets the display name used for the source in the
// "could not identify" diagnostics, so a caller that parses buffered or
// temp-file bytes (e.g. standard input) reports the original name instead of the
// temp path. It is display-only - detection still keys on the real path's
// extension - and affects only the unidentified-format error; everything else
// (including the source identity ParseFile records for save-back) is unchanged.
// Without it the name falls back to the path argument, or "" for [Parse].
func WithSourceName(name string) ParseOption {
	return func(o *core.ParseOptions) { o.SourceName = name }
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
// parsed (and catching a source that changes mid-write). For a file destination
// ([SaveBack]/[SaveAsFile]) it then re-reads the written temp file's audio extent
// and compares before the commit, so the check is end-to-end through the page
// cache (it guards the copy logic, not the disk media). A streaming [WriteTo]
// cannot be re-read, so there it verifies the copied source bytes only.
func WithVerifyEssence() WriteOption {
	return func(o *core.WriteOptions) { o.VerifyEssence = true }
}

// WithNumericGenre writes a recognized genre as its numeric reference (in
// formats that support one, such as ID3's TCON) instead of the name. By default
// the canonical name is written.
func WithNumericGenre() WriteOption {
	return func(o *core.WriteOptions) { o.NumericGenre = true }
}

// WithUnrecognizedPictures allows a picture whose bytes [IsRecognizedImage] does
// not recognize (an empty payload, junk, or a deliberately exotic HEIC/AVIF/JXL
// cover the header sniff cannot identify) to be embedded by [Editor.Prepare].
// By default such a picture is rejected ([waxerr.ErrInvalidData]) so a direct
// library caller cannot silently embed an application/octet-stream picture; pass
// this to opt a known-exotic cover back in. Only pictures added via
// [Editor.AddPicture] are validated - a file's pre-existing picture carried
// through a tags-only edit is never affected.
func WithUnrecognizedPictures() WriteOption {
	return func(o *core.WriteOptions) { o.AllowUnrecognizedPictures = true }
}

// WithStripEncoderStamp asks the writer to drop a removable inherited
// transcoder/encoder stamp that lives in a native field no canonical-tag edit can
// reach - today the WAV ISFT INFO item (e.g. ffmpeg's "Lavf..."), dropped only
// when it [IsTranscoderStamp]. Other codecs ignore it: the Ogg/Opus/FLAC vendor
// string is a mandatory codec field, reported but never overwritten. Pair it with
// clearing or setting [tag.Encoder] so a WAV's stamp does not survive the edit.
func WithStripEncoderStamp() WriteOption {
	return func(o *core.WriteOptions) { o.StripEncoderStamp = true }
}

// WithWebMSubset narrows a file-less Matroska [CapabilitiesFor] query to the WebM
// subset, which excludes cover-art attachments - so the format-level answer for
// "webm" reports picture write as unsupported, matching what [Document.Capabilities]
// reports for a parsed .webm file. It affects only the Matroska capability query;
// every other codec ignores it, and it has no effect on a write.
func WithWebMSubset() WriteOption {
	return func(o *core.WriteOptions) { o.WebMSubset = true }
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
