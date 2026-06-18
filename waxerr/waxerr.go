// Package waxerr defines the sentinel errors shared across WaxLabel and a
// small generic helper for extracting typed errors from a wrapped chain.
//
// Sentinels are compared with [errors.Is]; richer errors that wrap a sentinel
// (so callers can still match the category) are extracted with [AsType].
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
	// ErrNoTags means the file parsed cleanly but carried no tag block.
	ErrNoTags = errors.New("no tags present")
	// ErrUnsupportedTag means a tag exists that this version cannot model.
	ErrUnsupportedTag = errors.New("unsupported tag")
	// ErrPictureTooLarge means an embedded picture exceeded a configured or
	// format-imposed size limit.
	ErrPictureTooLarge = errors.New("picture too large")
	// ErrSizeTooLarge means a declared length would exceed the bounded
	// allocation limit for untrusted input.
	ErrSizeTooLarge = errors.New("declared size too large")
	// ErrTooDeep means nested structure exceeded the recursion-depth limit.
	ErrTooDeep = errors.New("structure nested too deeply")
	// ErrSourceChanged means a save-back target no longer matches the source
	// identity recorded at parse time.
	ErrSourceChanged = errors.New("source changed since parse")
	// ErrChainedStream means an Ogg stream is chained/multiplexed; reading is
	// best-effort but writing is refused.
	ErrChainedStream = errors.New("chained stream")
	// ErrInvalidKey means a canonical key failed validation.
	ErrInvalidKey = errors.New("invalid tag key")
)

// AsType walks err's wrapped chain ([errors.As] semantics) and returns the
// first error that is assignable to T, reporting whether one was found. It is
// the typed counterpart to [errors.Is] for the structured errors WaxLabel
// returns alongside the sentinels above.
func AsType[T error](err error) (T, bool) {
	var target T
	ok := errors.As(err, &target)
	return target, ok
}
