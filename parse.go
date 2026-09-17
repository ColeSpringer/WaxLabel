package waxlabel

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/waxerr"

	// Register codecs for Detect.
	_ "github.com/colespringer/waxlabel/internal/aac"
	_ "github.com/colespringer/waxlabel/internal/aiff"
	_ "github.com/colespringer/waxlabel/internal/apen"
	_ "github.com/colespringer/waxlabel/internal/asf"
	_ "github.com/colespringer/waxlabel/internal/flac"
	_ "github.com/colespringer/waxlabel/internal/matroska"
	_ "github.com/colespringer/waxlabel/internal/mp3"
	_ "github.com/colespringer/waxlabel/internal/mp4"
	_ "github.com/colespringer/waxlabel/internal/musepack"
	_ "github.com/colespringer/waxlabel/internal/ogg"
	_ "github.com/colespringer/waxlabel/internal/wav"
	_ "github.com/colespringer/waxlabel/internal/wavpack"
)

// errNilContext is returned for a nil context at a public entry point. Unexported:
// nil ctx is a programmer bug, not a waxerr category. CLI always supplies a context.
var errNilContext = errors.New("nil context: pass context.Background() if you have none")

// checkContext rejects nil ctx and reports ctx.Err() for already-done contexts.
func checkContext(ctx context.Context) error {
	if ctx == nil {
		return errNilContext
	}
	return ctx.Err()
}

// Parse reads metadata from src into a detached [Document]. src is not retained;
// supply it again via [WriteTo] to write. Prefer [ParseFile] for path-based save-back.
func Parse(ctx context.Context, src ReaderAtSized, opts ...ParseOption) (*Document, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if src == nil {
		return nil, fmt.Errorf("%w: nil source", waxerr.ErrInvalidData)
	}
	return parseSource(ctx, src, "", resolveParseOptions(opts))
}

// ParseFile opens path, parses, and closes before return. Document holds no FD;
// it records source identity for [SaveBack] change detection.
func ParseFile(ctx context.Context, path string, opts ...ParseOption) (*Document, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, fmt.Errorf("%w: input filename is empty", waxerr.ErrInvalidData)
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
		id.Fingerprint = doc.media.Identity.Fingerprint
		id.HasFinger = doc.media.Identity.HasFinger
		doc.media.Identity = id
	}
	return doc, nil
}

// parseSource detects format and dispatches to the codec.
func parseSource(ctx context.Context, src ReaderAtSized, path string, opts core.ParseOptions) (*Document, error) {
	// Display name for diagnostics: SourceName, else path, else placeholder.
	name := opts.SourceName
	if name == "" {
		name = path
	}
	if name == "" {
		name = "<unnamed input>"
	}
	if src.Size() == 0 {
		return nil, fmt.Errorf("%w: could not identify %q (empty file)", waxerr.ErrUnsupportedFormat, name)
	}
	// Detection looks past a leading ID3v2 when present (MP3/AAC/FLAC).
	codec, ok := core.DetectLeading(src, path, id3.TagSize)
	if !ok {
		return nil, fmt.Errorf("%w: could not identify %q", waxerr.ErrUnsupportedFormat, name)
	}
	media, err := codec.Parse(ctx, src, opts)
	if err != nil {
		return nil, err
	}
	// Canonicalize codec names once for text/JSON/model consistency.
	for i := range media.Properties.Tracks {
		t := &media.Properties.Tracks[i]
		t.Codec, t.CodecProfile = core.CanonicalCodec(t.Codec)
	}
	if noEssence(media.EssenceRanges()) {
		// Zero essence subsumes truncated-audio; report one root cause.
		media.Warnings = slices.DeleteFunc(media.Warnings, func(w core.Warning) bool {
			return w.Code == core.WarnTruncatedAudio
		})
		media.Warnings = core.Warn(media.Warnings, core.WarnNoAudioFrames,
			"no audio essence found; file may be tag-only or truncated")
	}
	return &Document{media: media, limits: opts.Limits}, nil
}
