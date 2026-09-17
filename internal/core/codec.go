package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
)

// Codec parses to [Media], plans rewrites, and reports capabilities from one implementation.
type Codec interface {
	Format() Format
	// Extensions are the lowercase file extensions (with dot) this codec
	// claims, used for detection alongside Sniff.
	Extensions() []string
	// Sniff reports whether the leading bytes look like this format.
	Sniff(header []byte) bool
	// SkipsLeadingID3: parser accepts leading ID3v2 ([DetectLeading]).
	SkipsLeadingID3() bool
	// Parse reads metadata from src into a Media.
	Parse(ctx context.Context, src ReaderAtSized, opts ParseOptions) (*Media, error)
	// Plan computes rewrite from parsed Media only; Execute reads source bytes.
	Plan(ctx context.Context, base, edited *Media, opts WriteOptions) (*WritePlan, error)
	// Capabilities reports support; m is parsed file or nil for format-level query.
	Capabilities(m *Media, opts WriteOptions) Capabilities
	// EssenceExtent returns digest version and decoder-critical config bytes.
	EssenceExtent(m *Media) (version string, config []byte)
}

// WriteReport describes a planned write; matches execution.
type WriteReport struct {
	Format       Format
	NoOp         bool
	BytesBefore  int64
	BytesAfter   int64
	PaddingAfter int64
	Operations   []string
	Warnings     []Warning
}

// String renders human-readable plan summary. Warnings via [Warning.String] (sanitized).
func (r WriteReport) String() string {
	if r.NoOp {
		// No-op may still carry discard warnings (value-dropped, etc.).
		s := NoChangesLine(HasDiscardWarning(r.Warnings))
		for _, x := range r.Warnings {
			s += "\n  warning: " + x.String()
		}
		return s
	}
	var lines []string
	if len(r.Operations) == 0 {
		lines = append(lines, "  - rewrite metadata")
	}
	for _, op := range r.Operations {
		lines = append(lines, "  - "+op)
	}
	lines = append(lines, fmt.Sprintf("  size:    %s -> %s", bits.HumanBytes(r.BytesBefore), bits.HumanBytes(r.BytesAfter)))
	if r.PaddingAfter > 0 {
		lines = append(lines, fmt.Sprintf("  padding: %s", bits.HumanBytes(r.PaddingAfter)))
	}
	for _, x := range r.Warnings {
		lines = append(lines, "  warning: "+x.String())
	}
	return strings.Join(lines, "\n")
}

// WritePlan is plan output: segments, report, post-write Media.
type WritePlan struct {
	Segments []bits.Segment
	NoOp     bool
	Report   WriteReport
	Result   *Media
}

// NoOpPlan builds unchanged write plan (SaveBack skips; SaveAsFile still writes file).
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

// PaddingOp formats padding operation line for display.
func PaddingOp(oldRegion, newContent, padAfter int64) string {
	return fmt.Sprintf("padding %d -> %d", max(int64(0), oldRegion-newContent), padAfter)
}

// EncodingRewriteOp formats encoding-only rewrite line (e.g. numeric genre).
func EncodingRewriteOp(what string) string {
	return what + " encoding rewrite"
}

// DowngradeNoOp returns no-op plan when projected result equals base and no structural change.
// Re-attaches input-loss warnings from priorWarnings; omits write-mechanics warnings.
func DowngradeNoOp(format Format, size int64, base, result *Media, tagsEqual, structuralChange bool, priorWarnings []Warning) *WritePlan {
	if structuralChange || !tagsEqual {
		return nil
	}
	if !EqualPictures(base.Pictures, result.Pictures) || !EqualChapters(base.Chapters, result.Chapters) ||
		!EqualSyncedLyrics(base.SyncedLyrics, result.SyncedLyrics) {
		return nil
	}
	np := NoOpPlan(WriteReport{Format: format, BytesBefore: size}, size, base)
	np.Report.Warnings = append(np.Report.Warnings, WarningsWithCode(priorWarnings, WarnValueDropped, WarnValueCoerced, WarnValueReduced, WarnPictureMetadataDropped, WarnPictureUnsupported, WarnNumericGenre, WarnChapterTitleTruncated, WarnChapterMetadataDropped, WarnChapterStartOverflow, WarnChaptersFlattened, WarnSyncedLyricsMetadataDropped, WarnSyncedLyricsTimestampClamped, WarnCommentDescriptionDropped, WarnLegacyStripDropped)...)
	return np
}

// Codec registry (init registration; not user-extensible).
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

// Detect picks codec by header sniff only (path ignored). header may be short.
func Detect(path string, header []byte) (Codec, bool) {
	for _, c := range registry {
		if c.Sniff(header) {
			return c, true
		}
	}
	return nil, false
}

// DetectLeading sniffs past skippable leading region (ID3). Inner format wins if SkipsLeadingID3.
func DetectLeading(src ReaderAtSized, path string, leadingLen func(header []byte) (int64, bool)) (Codec, bool) {
	// 64 bytes covers Ogg BOS id header and shorter signatures.
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
		// Inner format only when SkipsLeadingID3; else unsupported input.
		if inner.SkipsLeadingID3() {
			return inner, true
		}
		return nil, false
	}
	return codec, true
}
