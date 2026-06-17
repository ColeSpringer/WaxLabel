package waxlabel

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/waxerr"

	// Register the codecs. They are internal through v0.x; the blank import
	// wires them into the detection registry.
	_ "github.com/colespringer/waxlabel/internal/aac"
	_ "github.com/colespringer/waxlabel/internal/aiff"
	_ "github.com/colespringer/waxlabel/internal/flac"
	_ "github.com/colespringer/waxlabel/internal/matroska"
	_ "github.com/colespringer/waxlabel/internal/mp3"
	_ "github.com/colespringer/waxlabel/internal/mp4"
	_ "github.com/colespringer/waxlabel/internal/ogg"
	_ "github.com/colespringer/waxlabel/internal/wav"
)

// Parse reads metadata from src, returning a detached [Document]. src is used
// only during the call; the Document retains no reference to it, so to write
// the result you supply a source again via [WriteTo]. Use [ParseFile] when you
// have a path (it records source identity for save-back).
func Parse(ctx context.Context, src ReaderAtSized, opts ...ParseOption) (*Document, error) {
	return parseSource(ctx, src, "", resolveParseOptions(opts))
}

// ParseFile opens path, parses it, and closes it before returning. The
// Document holds no file descriptor; it records a strong source identity so a
// later [Plan.Execute] with [SaveBack] can detect a changed file.
func ParseFile(ctx context.Context, path string, opts ...ParseOption) (*Document, error) {
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
	return &Document{media: media}, nil
}
