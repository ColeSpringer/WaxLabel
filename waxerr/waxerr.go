// Package waxerr defines the sentinel errors shared across WaxLabel.
//
// Sentinels are compared with [errors.Is]; richer errors that wrap a sentinel
// (so callers can still match the category) are extracted with [errors.AsType].
package waxerr

import "errors"

// Sentinel errors. Each names a category a caller may want to branch on. Use
// errors.Is to test for them; library functions wrap them with context. Their
// messages carry no program prefix - a front-end (the CLI) owns its own
// "waxlabel: " prefix, so embedding one here would double it.
var (
	// ErrUnsupportedFormat means the container/codec is not recognized or not
	// handled in this version.
	ErrUnsupportedFormat = errors.New("unsupported format")
	// ErrInvalidData means the input violated the format specification in a
	// way parsing could not recover from.
	ErrInvalidData = errors.New("invalid data")
	// ErrUnsupportedTag means a tag exists that this version cannot model.
	ErrUnsupportedTag = errors.New("unsupported tag")
	// ErrPictureTooLarge means an embedded picture exceeded a configured or
	// format-imposed size limit.
	ErrPictureTooLarge = errors.New("picture too large")
	// ErrSizeTooLarge means a declared length would exceed the bounded
	// allocation limit for untrusted input.
	ErrSizeTooLarge = errors.New("declared size too large")
	// ErrInputTooLarge means a user-configured resource cap on a streamed input was
	// exceeded - the CLI's --max-size on a buffered '-'/stdin stream, or the library's
	// WithMaxSourceBytes. Unlike ErrSizeTooLarge (a container that declares an
	// oversized internal length, i.e. corruption), a raw stream carries no declared
	// size, so exceeding the cap is a resource-limit refusal, not malformed data. It is
	// a distinct sentinel so the CLI can map it to its own exit code and a message
	// without the "declared size" framing.
	ErrInputTooLarge = errors.New("input too large")
	// ErrTooDeep means nested structure exceeded the recursion-depth limit.
	ErrTooDeep = errors.New("structure nested too deeply")
	// ErrSourceChanged means a save-back target no longer matches the source
	// identity recorded at parse time.
	ErrSourceChanged = errors.New("source changed since parse")
	// ErrChainedStream means an Ogg stream is chained/multiplexed; reading is
	// best-effort but writing is refused.
	ErrChainedStream = errors.New("chained stream")
	// ErrUnalignedStream means an Ogg stream's header and audio are not cleanly
	// page-aligned, so a safe in-place rewrite is not possible. The stream is
	// well-formed (it reads fine) but unwritable, so it is a write-refusal - distinct
	// from ErrInvalidData (a corrupt file) and from ErrChainedStream (a different
	// unwritable shape), so each surfaces its own machine code.
	ErrUnalignedStream = errors.New("stream not cleanly page-aligned")
	// ErrInvalidKey means a canonical key failed validation.
	ErrInvalidKey = errors.New("invalid tag key")
	// ErrNeedsFile means a path-bound operation (SaveBack) was attempted on a
	// document that has no file path - e.g. one from [Parse] or [OpenSource]. The
	// format is fully supported; the caller must supply a destination that names a
	// file (SaveAsFile) or a stream (WriteTo).
	ErrNeedsFile = errors.New("operation needs a file path")
)
