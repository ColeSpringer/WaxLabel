// Package waxerr defines the sentinel errors shared across WaxLabel.
//
// Compare with [errors.Is]. Richer errors that wrap a sentinel are extracted
// with [errors.AsType].
package waxerr

import "errors"

// Sentinel errors. Each names a category a caller may branch on. Library
// functions wrap them with context. Messages carry no "waxlabel: " prefix; the
// CLI owns that framing.
var (
	// ErrUnsupportedFormat means the container/codec is not recognized or not
	// handled in this version.
	ErrUnsupportedFormat = errors.New("unsupported format")
	// ErrInvalidData means the input violated the format specification in a
	// way parsing could not recover from.
	ErrInvalidData = errors.New("invalid data")
	// ErrUnsupportedTag means a tag exists that this version cannot model.
	ErrUnsupportedTag = errors.New("unsupported tag")
	// ErrPictureTooLarge means write content exceeded a configured or
	// format-imposed size limit (FLAC PICTURE, MP4 covr, ID3 APIC, or a whole
	// Ogg comment header that holds base64 cover art). The input file is
	// well-formed; only the write is refused. Distinct from ErrInvalidData.
	ErrPictureTooLarge = errors.New("picture too large")
	// ErrSizeTooLarge means a declared length would exceed the bounded
	// allocation limit for untrusted input.
	ErrSizeTooLarge = errors.New("declared size too large")
	// ErrInputTooLarge means a user-configured stream cap was exceeded (CLI
	// --max-size on stdin, or WithMaxSourceBytes). Distinct from
	// ErrSizeTooLarge (a container declaring an oversized internal length).
	ErrInputTooLarge = errors.New("input too large")
	// ErrTooDeep means nested structure exceeded the recursion-depth limit.
	ErrTooDeep = errors.New("structure nested too deeply")
	// ErrSourceChanged means a save-back target no longer matches the source
	// identity recorded at parse time.
	ErrSourceChanged = errors.New("source changed since parse")
	// ErrChainedStream means an Ogg stream is chained/multiplexed; reading is
	// best-effort but writing is refused.
	ErrChainedStream = errors.New("chained stream")
	// ErrUnalignedStream means an Ogg stream's header and audio are not
	// cleanly page-aligned, so in-place rewrite is unsafe. Well-formed but
	// unwritable. Distinct from ErrInvalidData and ErrChainedStream.
	ErrUnalignedStream = errors.New("stream not cleanly page-aligned")
	// ErrFragmented means an MP4 carries movie fragments (moof). Offset fixups
	// cannot reach fragment sample offsets; a metadata resize would desync
	// media. Well-formed but unwritable. Distinct from ErrInvalidData and
	// ErrUnsupportedFormat.
	ErrFragmented = errors.New("fragmented container")
	// ErrInvalidKey means a canonical key failed validation.
	ErrInvalidKey = errors.New("invalid tag key")
	// ErrNeedsFile means a path-bound operation (SaveBack) was attempted on a
	// document with no file path (e.g. from [Parse] or [OpenSource]). Use
	// SaveAsFile or WriteTo instead.
	ErrNeedsFile = errors.New("operation needs a file path")
)
