package waxlabel

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/waxerr"

	// Register the codecs. They are internal through v0.x; the blank import
	// wires them into the detection registry.
	_ "github.com/colespringer/waxlabel/internal/flac"
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
	// 64 bytes spans the Ogg BOS page's identification header, where the codec
	// signature ("\x01vorbis" / "OpusHead") that distinguishes Vorbis from Opus
	// lives; shorter formats (FLAC's "fLaC", ID3) need only the first few.
	header := make([]byte, 64)
	n, _ := src.ReadAt(header, 0)
	header = header[:n]

	codec, ok := core.Detect(path, header)
	if !ok {
		return nil, fmt.Errorf("%w: could not identify %q", waxerr.ErrUnsupportedFormat, path)
	}
	// A leading ID3v2 tag is shared between MP3 and a few containers that tolerate
	// a stray leading ID3 (notably FLAC). When detection lands on MP3 because of
	// the ID3 header, peek just past the tag: if a different format's signature
	// follows, that format wins. This keeps an ID3-prefixed FLAC from being read
	// as MP3 without weakening MP3 detection for the common case.
	//
	// This lives here (not in core.Detect) on purpose: core cannot import id3 to
	// compute the tag length, and the only other ID3-bearing formats (WAV/AIFF)
	// carry ID3 inside a chunk, not as a front tag — so the front-ID3 ambiguity is
	// really just MP3-vs-FLAC. See the build-4 plan note before generalizing.
	if codec.Format() == core.FormatMP3 {
		if total, isID3 := id3.TagSize(header); isID3 && total < src.Size() {
			peek := make([]byte, 64)
			if n, _ := src.ReadAt(peek, total); n > 0 {
				if c2, ok2 := core.Detect(path, peek[:n]); ok2 && c2.Format() != core.FormatMP3 {
					codec = c2
				}
			}
		}
	}
	media, err := codec.Parse(ctx, src, opts)
	if err != nil {
		return nil, err
	}
	return &Document{media: media}, nil
}
