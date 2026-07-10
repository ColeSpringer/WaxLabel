package waxlabel

import "github.com/colespringer/waxlabel/internal/core"

// DefaultMaxSourceBytes is the default ceiling [OpenSource] applies to a non-seekable
// stream it buffers whole into memory (2 GiB). A stream larger than this fails with
// [waxerr.ErrSizeTooLarge]; pass [WithMaxSourceBytes](0) to lift the cap entirely. The
// CLI applies the same default to buffered standard input via its --max-size flag.
const DefaultMaxSourceBytes = core.DefaultMaxSourceBytes

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
// unlimited): a partial Limits{MaxDepth: 8} keeps the default MaxAllocBytes and
// MaxElements rather than dropping them to zero, which would reject every
// allocation (or, for MaxElements, silently disable the element-count cap).
func WithLimits(l Limits) ParseOption {
	return func(o *core.ParseOptions) {
		d := core.DefaultParseOptions().Limits
		// Non-positive means "unset": fall back to the default rather than passing a negative
		// through. ReadSlice now rejects a non-positive allocation limit, so a negative
		// MaxAllocBytes would otherwise fail every bounded read; a negative depth/element cap is
		// equally nonsensical.
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

// WithMaxSourceBytes bounds how many bytes [OpenSource] buffers from a non-seekable
// stream before parsing. A stream that exceeds n fails with [waxerr.ErrSizeTooLarge]
// instead of exhausting memory as an endless stream is spooled. n <= 0 disables the cap,
// restoring the unbounded read. The default when the option is not supplied is
// [DefaultMaxSourceBytes] (2 GiB). It has no effect on [Parse] or [ParseFile], which read
// from a sized source and never buffer a stream.
func WithMaxSourceBytes(n int64) ParseOption {
	return func(o *core.ParseOptions) { o.MaxSourceBytes = n }
}

// WithSourceName sets the display name used for the source in the
// "could not identify" diagnostics, so a caller that parses buffered or
// temp-file bytes (e.g. standard input) reports the original name instead of the
// temp path. It is display-only: detection sniffs bytes, never names or extensions. The
// option only affects the unidentified-format error; source identity and save-back checks
// are unchanged. Without it the name falls back to the path argument, or "" for [Parse].
func WithSourceName(name string) ParseOption {
	return func(o *core.ParseOptions) { o.SourceName = name }
}

// WithPadding sets the post-metadata padding policy for writes. It marks the policy as an
// explicit request, so a padding-only change is still realized when no tag, picture, or
// legacy edit is pending.
func WithPadding(p PaddingPolicy) WriteOption {
	return func(o *core.WriteOptions) {
		o.Padding = p
		o.PaddingExplicit = true
	}
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

// WithVerifyEssence hashes the audio bytes while a rewrite copies them and compares
// the written output with that copy stream. It is a copy-consistency check: it proves
// the output matches the bytes read during the write, not a separate parse-time
// baseline. For [SaveBack] and [SaveAsFile] it re-reads the temporary output before
// commit, which checks the copy path through the page cache. A streaming [WriteTo]
// cannot be re-read, so it verifies only the bytes copied from the source. The CLI
// parses each file immediately before writing, so its source extent is fresh.
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
// reach: the WAV ISFT INFO item (e.g. ffmpeg's "Lavf...") and the Ogg/Opus/FLAC
// comment-header vendor string. Each is acted on only when it [IsTranscoderStamp].
// The ISFT item is dropped; the vendor string is a mandatory codec field, so it is
// rewritten to WaxLabel's neutral value rather than removed (NeutralizeVendor). Pair it
// with clearing or setting [tag.Encoder] so a canonical ENCODER stamp does not survive.
func WithStripEncoderStamp() WriteOption {
	return func(o *core.WriteOptions) { o.StripEncoderStamp = true }
}

// WithWebMSubset narrows a file-less Matroska [CapabilitiesFor] query to the WebM
// subset, which excludes cover-art attachments - so the format-level answer for
// "webm" reports picture write as unsupported, matching what [Document.Capabilities]
// reports for a parsed.webm file. It affects only the Matroska capability query;
// every other codec ignores it, and it has no effect on a write.
func WithWebMSubset() WriteOption {
	return func(o *core.WriteOptions) { o.WebMSubset = true }
}

// WithAllowUnsupportedDrop makes [Editor.Prepare] drop a whole structural edit the
// destination format cannot store at all - authored synced lyrics or chapters on a format
// that has no such store, or cover art on a WebM file - with a warning, rather than failing
// the write. It also drops just the individual covers whose image format the destination
// stores pictures but cannot label (a GIF added to an MP4, which labels only JPEG/PNG/BMP),
// keeping any it can, so a PNG added alongside survives. It mirrors how a cross-format copy
// silently drops what the destination cannot hold, so a set that combines a storable edit
// with an unstorable one still applies the storable part and succeeds. By default such an
// edit is a hard error. The CLI passes this for set and plan; --strict promotes the resulting
// drop warning back to a failure.
func WithAllowUnsupportedDrop() WriteOption {
	return func(o *core.WriteOptions) { o.AllowUnsupportedDrop = true }
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
//	plan, _:= ed.Prepare(waxlabel.Preserve, waxlabel.WithVerifyEssence())
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
		o.PaddingExplicit = true
	}
	// Minimal writes the smallest reasonable file: no padding, strip legacy.
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
