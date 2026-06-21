package waxlabel

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/waxerr"

	// Register the codecs
	_ "github.com/colespringer/waxlabel/internal/aac"
	_ "github.com/colespringer/waxlabel/internal/aiff"
	_ "github.com/colespringer/waxlabel/internal/flac"
	_ "github.com/colespringer/waxlabel/internal/matroska"
	_ "github.com/colespringer/waxlabel/internal/mp3"
	_ "github.com/colespringer/waxlabel/internal/mp4"
	_ "github.com/colespringer/waxlabel/internal/ogg"
	_ "github.com/colespringer/waxlabel/internal/wav"
)

// errNilContext reports a nil [context.Context] handed to a public ctx-taking
// entry point. It is unexported by design: every codec dereferences ctx
// immediately (ctx.Err()), so a nil ctx is a programmer bug, not a runtime
// category a caller branches on with errors.Is - keeping it out of the curated
// [waxerr] vocabulary avoids diluting that surface, while the plain error still
// carries the message (and is test-assertable). The CLI always supplies a
// context, so this never surfaces there.
var errNilContext = errors.New("nil context: pass context.Background() if you have none")

// checkContext is the uniform context guard for every public ctx-taking entry
// point: it rejects a nil context with [errNilContext] (so the call fails fast
// instead of panicking on the first ctx.Err() deref inside a codec - and, for the
// streaming OpenSource, before it reads the whole input into memory), and
// otherwise reports the context's own state, so an already-cancelled or
// expired context aborts up front too. Folding both checks here keeps the six
// entry points consistent (no site stacks a separate ctx.Err()).
func checkContext(ctx context.Context) error {
	if ctx == nil {
		return errNilContext
	}
	return ctx.Err()
}

// Parse reads metadata from src, returning a detached [Document]. src is used
// only during the call; the Document retains no reference to it, so to write
// the result you supply a source again via [WriteTo]. Use [ParseFile] when you
// have a path (it records source identity for save-back).
func Parse(ctx context.Context, src ReaderAtSized, opts ...ParseOption) (*Document, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	// A nil source would panic on the first src.Size() in detection; reject it up
	// front with a clean error, mirroring the context guard above.
	if src == nil {
		return nil, fmt.Errorf("%w: nil source", waxerr.ErrInvalidData)
	}
	return parseSource(ctx, src, "", resolveParseOptions(opts))
}

// ParseFile opens path, parses it, and closes it before returning. The
// Document holds no file descriptor; it records a strong source identity so a
// later [Plan.Execute] with [SaveBack] can detect a changed file.
func ParseFile(ctx context.Context, path string, opts ...ParseOption) (*Document, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	fs, err := openFileSource(path)
	if err != nil {
		return nil, err
	}
	defer fs.Close()

	doc, err := parseSource(ctx, fs, path, resolveParseOptions(opts))
	if err != nil {
		return nil, err
	}
	doc.path = path

	if id, err := fileIdentity(path); err == nil {
		// Keep the codec's structural fingerprint; add filesystem identity.
		id.Fingerprint = doc.media.Identity.Fingerprint
		id.HasFinger = doc.media.Identity.HasFinger
		doc.media.Identity = id
	}
	return doc, nil
}

// parseSource detects the format and dispatches to the codec.
func parseSource(ctx context.Context, src ReaderAtSized, path string, opts core.ParseOptions) (*Document, error) {
	// An empty file carries no signature, so its format cannot be identified
	// regardless of name: normalize it to one outcome (unsupported) here, rather
	// than letting the extension fall through to a codec whose own parse then fails
	// with a different class - e.g. empty.flac as invalid-data but empty.bin as
	// unsupported. Detection stays policy-free ("what format is this"); this one
	// site owns the empty-file rule.
	if src.Size() == 0 {
		return nil, fmt.Errorf("%w: could not identify %q (empty file)", waxerr.ErrUnsupportedFormat, path)
	}
	// Detection looks past a leading ID3v2 tag when present: MP3 sniffs a bare
	// ID3, but FLAC tolerates and raw AAC requires a front tag, so the real format
	// is decided by what sits past the tag. id3.TagSize supplies the tag length so
	// core need not import the id3 codec.
	codec, ok := core.DetectLeading(src, path, id3.TagSize)
	if !ok {
		return nil, fmt.Errorf("%w: could not identify %q", waxerr.ErrUnsupportedFormat, path)
	}
	media, err := codec.Parse(ctx, src, opts)
	if err != nil {
		return nil, err
	}
	// Canonicalize codec names once, here, so the same codec reads identically across
	// containers in the text view, JSON, and the library model. Each codec keeps
	// emitting its container's raw name; the raw detail (fourcc, object type, MPEG
	// version) is preserved in CodecProfile. The native-blocks view keeps the raw name
	// (it intentionally shows container structure).
	for i := range media.Properties.Tracks {
		t := &media.Properties.Tracks[i]
		t.Codec, t.CodecProfile = core.CanonicalCodec(t.Codec)
	}
	// "No audio essence" is one cross-format concept: surface it here, off the same
	// predicate the digest guard uses, so dump/lint flag a tag-only or truncated
	// file for every format - and always agree with verify (which refuses to hash
	// it). This replaces the former MP3-only frame-scan warning.
	if noEssence(media.EssenceRanges()) {
		// Zero essence is "no-audio", which subsumes a truncation: a codec that flagged
		// truncated-audio saw a declared size but nothing survived, so drop that warning
		// here (EssenceRanges accounts for each format's sub-headers, e.g. AIFF's SSND
		// offset) and report the one root cause. truncated-audio is the some-but-not-all
		// case; no-audio owns nothing-at-all.
		media.Warnings = slices.DeleteFunc(media.Warnings, func(w core.Warning) bool {
			return w.Code == core.WarnTruncatedAudio
		})
		media.Warnings = core.Warn(media.Warnings, core.WarnNoAudioFrames,
			"no audio essence found; file may be tag-only or truncated")
	}
	return &Document{media: media}, nil
}
