package core

import (
	"context"
	"slices"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
)

// Codec is the contract every format implementation satisfies. Parsing
// produces a neutral [Media]; planning turns an edited Media into a byte-level
// rewrite. The same Codec instance answers capability queries so the reported
// capabilities and the actual write behavior cannot drift apart.
type Codec interface {
	Format() Format
	// Extensions are the lowercase file extensions (with dot) this codec
	// claims, used for detection alongside Sniff.
	Extensions() []string
	// Sniff reports whether the leading bytes look like this format.
	Sniff(header []byte) bool
	// Parse reads metadata from src into a Media.
	Parse(ctx context.Context, src ReaderAtSized, opts ParseOptions) (*Media, error)
	// Plan computes the rewrite that realizes edited over base (the unedited
	// parse). It works purely from the parsed Media - the native document holds
	// every structural detail needed - so a detached Document can be planned
	// without reopening the source; only Execute reads the source bytes. The
	// returned plan's Report describes exactly what executing it will do.
	Plan(ctx context.Context, base, edited *Media, opts WriteOptions) (*WritePlan, error)
	// Capabilities reports support under the given write options. m is the
	// parsed file the query is about, or nil for a file-agnostic, format-level
	// query (as [Document.PlanTransfer] makes, having no destination file). A
	// codec whose support is uniform across files ignores m; one with a per-file
	// constraint (Matroska, where the WebM subset forbids cover attachments)
	// consults it when present, and must nil-guard. Threading the file in keeps
	// the reported capability honest for the report==result transfer invariant.
	Capabilities(m *Media, opts WriteOptions) Capabilities
	// EssenceExtent returns the inputs to the audio-essence digest for this
	// format: a named, versioned extent identifier and the decoder-critical
	// configuration bytes mixed into the hash ahead of the audio. What counts as
	// "decoder-critical" is codec-specific, so it lives here rather than in the
	// format-agnostic public layer.
	EssenceExtent(m *Media) (version string, config []byte)
}

// WriteReport is the human-and-machine-readable description of a planned
// write. It is produced together with the segments so plan and execution
// share state: Report() on a Plan returns exactly what Execute will carry out.
type WriteReport struct {
	Format       Format
	NoOp         bool
	BytesBefore  int64
	BytesAfter   int64
	PaddingAfter int64
	Operations   []string
	Warnings     []Warning
}

// WritePlan is the codec's output from planning: the rewrite segments, a no-op
// flag, the report, and the resulting post-write Media (so the engine can
// return a Document without re-parsing).
type WritePlan struct {
	Segments []bits.Segment
	NoOp     bool
	Report   WriteReport
	Result   *Media
}

// NoOpPlan builds the "nothing changed" write plan every codec returns when an
// edit touches nothing: a verbatim whole-file copy flagged NoOp (so SaveBack
// skips it, while SaveAsFile/WriteTo still emit the whole file), carrying result
// as the post-write Media. The passed report (already bearing Format and
// BytesBefore) is finalized here - NoOp marker, unchanged byte count, the "no
// changes" operation - so the no-op path cannot drift between codecs.
func NoOpPlan(report WriteReport, size int64, result *Media) *WritePlan {
	report.NoOp = true
	report.BytesAfter = size
	report.Operations = []string{"no changes"}
	return &WritePlan{
		Segments: []bits.Segment{bits.Copy(0, size)},
		NoOp:     true,
		Report:   report,
		Result:   result,
	}
}

// codec registry. Codecs register from their package init; the root package
// imports them for the side effect. The set is closed (no public registry API
// in v1), so this lives in internal/core.
var registry []Codec

// Register adds a codec. It is called from codec package initializers.
func Register(c Codec) { registry = append(registry, c) }

// Codecs returns all registered codecs.
func Codecs() []Codec { return registry }

// ForFormat returns the codec for f, if registered.
func ForFormat(f Format) (Codec, bool) {
	for _, c := range registry {
		if c.Format() == f {
			return c, true
		}
	}
	return nil, false
}

// Detect picks a codec by sniffing the header, then falling back to the path
// extension. header may be short.
func Detect(path string, header []byte) (Codec, bool) {
	for _, c := range registry {
		if c.Sniff(header) {
			return c, true
		}
	}
	ext := lowerExt(path)
	if ext != "" {
		for _, c := range registry {
			if slices.Contains(c.Extensions(), ext) {
				return c, true
			}
		}
	}
	return nil, false
}

// DetectLeading detects src's format, looking past a recognized skippable leading
// region when one is present. leadingLen reports the byte length of such a region
// from the file's first bytes - it is supplied by the caller (as id3.TagSize) so
// core need not import the id3 codec, which is the whole reason this front-tag
// disambiguation cannot live inside id3 or be a method here without the callback.
//
// A leading ID3v2 tag is sniffed as MP3 (the sole bare-ID3 sniffer), but several
// formats tolerate (FLAC) or require (raw AAC) a front ID3. So when a leading
// region is present and a *different* format's signature sits just past it, that
// inner format wins; otherwise the header-level detection stands (MP3 for a real
// ID3-prefixed MP3, the common case). The peek is signature-only (empty path):
// a file extension is a weaker signal than the positively sniffed leading tag, so
// a mere ".aac"/".flac" name must not reclassify bytes that are no signature.
//
// This is the single path every ID3-bearing format (MP3 vs FLAC vs AAC) resolves
// through, rather than a per-format predicate that is correct only while MP3 is
// the sole ID3-sniffing codec.
func DetectLeading(src ReaderAtSized, path string, leadingLen func(header []byte) (int64, bool)) (Codec, bool) {
	// 64 bytes spans the Ogg BOS page's identification header, where the codec
	// signature ("\x01vorbis" / "OpusHead") that distinguishes Vorbis from Opus
	// lives; shorter formats (FLAC's "fLaC", ID3) need only the first few.
	header := make([]byte, 64)
	n, _ := src.ReadAt(header, 0)
	header = header[:n]

	codec, ok := Detect(path, header)
	if !ok {
		return nil, false
	}
	total, isLeading := leadingLen(header)
	if !isLeading || total >= src.Size() {
		return codec, true
	}
	peek := make([]byte, 64)
	pn, _ := src.ReadAt(peek, total)
	if pn <= 0 {
		return codec, true
	}
	if inner, ok := Detect("", peek[:pn]); ok && inner.Format() != codec.Format() {
		return inner, true
	}
	return codec, true
}

func lowerExt(path string) string {
	i := strings.LastIndexByte(path, '.')
	if i < 0 {
		return ""
	}
	return strings.ToLower(path[i:])
}
