package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/waxerr"
)

// Codec parses to [Media], plans rewrites, and reports capabilities from one implementation.
type Codec interface {
	Format() Format
	// Extensions are the lowercase file extensions (with dot) this codec claims, for ExtensionsFor:
	// the CLI's directory walk, format names and -o extension warning. Detection only sniffs.
	Extensions() []string
	// Sniff reports whether header, up to [SniffWindow] leading bytes, looks like this format.
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
		lines := []string{NoChangesLine(HasDiscardWarning(r.Warnings))}
		for _, x := range r.Warnings {
			lines = append(lines, "  warning: "+x.String())
		}
		return strings.Join(lines, "\n")
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
	// Result holds what a parse of the written file reads for its tags, pictures, chapters,
	// synced lyrics, first-track output gain and essence ranges. Nothing reads its other
	// fields, which a codec may leave unset.
	Result *Media
}

// NoOpPlan builds unchanged write plan (SaveBack skips; SaveAsFile still writes file). The
// padding it reports is what result, the file as parsed, holds.
func NoOpPlan(report WriteReport, size int64, result *Media) *WritePlan {
	report.NoOp = true
	report.BytesAfter = size
	report.Operations = []string{"no changes"}
	if p, ok := result.Native.(PaddingReporter); ok {
		report.PaddingAfter = p.PaddingBytes()
	}
	return &WritePlan{
		Segments: []bits.Segment{bits.Copy(0, size)},
		NoOp:     true,
		Report:   report,
		Result:   result,
	}
}

// PaddingOp formats the operation line of a padding-only edit: the padding before it and after it.
func PaddingOp(padBefore, padAfter int64) string {
	return fmt.Sprintf("padding %d -> %d", padBefore, padAfter)
}

// LeadingStripOp formats the legacy-strip line for a leading ID3v2 of tagLen bytes and the bytes
// after it up to streamAt, which go with it.
func LeadingStripOp(tagLen, streamAt int64) string {
	if gap := streamAt - tagLen; gap > 0 {
		return fmt.Sprintf("leading ID3v2 strip (%d bytes + %d leading bytes)", tagLen, gap)
	}
	return fmt.Sprintf("leading ID3v2 strip (%d bytes)", tagLen)
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
		!EqualSyncedLyrics(base.SyncedLyrics, result.SyncedLyrics) || !EqualTextItems(base, result) {
		return nil
	}
	np := NoOpPlan(WriteReport{Format: format, BytesBefore: size}, size, base)
	np.Report.Warnings = append(np.Report.Warnings, WarningsWithCode(priorWarnings, WarnValueDropped, WarnValueCoerced, WarnValueReduced, WarnPictureMetadataDropped, WarnPictureUnsupported, WarnNumericGenre, WarnChapterTitleTruncated, WarnChapterMetadataDropped, WarnChapterStartOverflow, WarnChaptersFlattened, WarnSyncedLyricsMetadataDropped, WarnSyncedLyricsTimestampClamped, WarnLegacyStripDropped)...)
	return np
}

// CheckPlanResult refuses a plan whose result holds a different number of chapters, pictures or
// synced lyric sets than edited asks for, unless the report names a discard ([IsDiscardWarning]):
// the write would otherwise lose a requested item silently. A nil result is not checked.
func CheckPlanResult(edited, result *Media, report WriteReport) error {
	if result == nil || HasDiscardWarning(report.Warnings) {
		return nil
	}
	if items, planned, requested, differ := ItemCountDiff(result, edited); differ {
		return fmt.Errorf("%w: the planned write does not hold the requested %s (%d planned, %d requested)",
			waxerr.ErrInvalidData, items, planned, requested)
	}
	return nil
}

// ItemCountDiff names the first of chapters, pictures and synced lyric sets whose count differs
// between a and b, with both counts; differ is false when all three agree.
func ItemCountDiff(a, b *Media) (items string, na, nb int, differ bool) {
	for _, c := range []struct {
		items  string
		na, nb int
	}{
		{"chapters", len(a.Chapters), len(b.Chapters)},
		{"pictures", len(a.Pictures), len(b.Pictures)},
		{"synced lyrics", len(a.SyncedLyrics), len(b.SyncedLyrics)},
	} {
		if c.na != c.nb {
			return c.items, c.na, c.nb, true
		}
	}
	return "", 0, 0, false
}

// Detection windows and bounds.
const (
	// SniffWindow is how many leading bytes detection hands [Codec.Sniff].
	SniffWindow = 1 << 10
	// MaxLeadingTags bounds the chained leading tags detection skips.
	MaxLeadingTags = 8
	// ResyncWindow bounds the search for a stream that does not start right behind the leading tags.
	ResyncWindow = 64 << 10
)

// Resyncer is implemented by a codec that skips leading ID3v2 and can find its stream behind a gap:
// Resync returns the offset in window of the first stream unit its own structure confirms inside
// window (two chained frames, or a marker and the first check its parser makes).
type Resyncer interface {
	Resync(window []byte) (off int, ok bool)
}

// StartResyncer marks a [Resyncer] whose sync a checksum confirms. Detection trusts it at the
// start of a file too, behind bytes no codec knows, where a sync by shape alone would be a guess.
type StartResyncer interface {
	Resyncer
	ChecksummedSync()
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

// Detect picks codec by header sniff. header may be short.
func Detect(header []byte) (Codec, bool) {
	for _, c := range registry {
		if c.Sniff(header) {
			return c, true
		}
	}
	return nil, false
}

// DetectLeading picks the codec for src from its first [SniffWindow] bytes. Behind leading tags (up
// to [MaxLeadingTags] chained ones, each sized by leadingLen from its header) the inner format wins
// when its codec skips leading ID3: sniffed where the tags end or, when nothing sniffs there, the
// nearest stream a [Resyncer] confirms within [ResyncWindow] of that end. Another inner format is
// unsupported, and no inner stream keeps the outer codec. When no codec knows the first bytes, a
// [StartResyncer] may still confirm its stream within [ResyncWindow] of the start. A window read
// that fails inside src is an error, classified as [bits.FillWithin] classifies it; ok is false
// with no error when no format matches.
func DetectLeading(src ReaderAtSized, leadingLen func(header []byte) (int64, bool)) (Codec, bool, error) {
	header, err := readWindow(src, 0, SniffWindow)
	if err != nil {
		return nil, false, err
	}
	outer, ok := Detect(header)
	if !ok {
		var checksummed []Codec
		for _, c := range registry {
			if _, ok := c.(StartResyncer); ok {
				checksummed = append(checksummed, c)
			}
		}
		c, _, ok, err := resync(src, 0, checksummed)
		return c, ok, err
	}
	end, inner, err := leadingChain(src, leadingLen, header)
	switch {
	case err != nil:
		return nil, false, err
	case end == 0 || end >= src.Size():
		return outer, true, nil
	}
	if c, ok := Detect(inner); ok {
		switch {
		case c.Format() == outer.Format():
			return outer, true, nil
		case c.SkipsLeadingID3():
			return c, true, nil
		}
		return nil, false, nil
	}
	c, _, ok, err := resync(src, end, registry)
	switch {
	case err != nil:
		return nil, false, err
	case ok:
		return c, true, nil
	}
	return outer, true, nil
}

// StreamStart returns where c's stream starts in src behind its leading tags, found as
// [DetectLeading] finds it: where the chained tags end when c sniffs there, else at c's first sync
// confirmed within [ResyncWindow] of that end. A tag past the chain's bound is no stream start, though
// c may sniff it as one. ok is false when src starts with no tag or c finds no stream. A window over
// limit is refused, and one that fails to read is an error as for [DetectLeading].
func StreamStart(src ReaderAtSized, leadingLen func(header []byte) (int64, bool), c Codec, limit int64) (int64, bool, error) {
	if err := bits.CheckAlloc(min(SniffWindow, src.Size()), limit); err != nil {
		return 0, false, err
	}
	header, err := readWindow(src, 0, SniffWindow)
	if err != nil {
		return 0, false, err
	}
	end, inner, err := leadingChain(src, leadingLen, header)
	switch {
	case err != nil:
		return 0, false, err
	case end == 0:
		return 0, false, nil
	}
	return streamAt(src, end, inner, leadingLen, c, limit)
}

// StreamFrom returns where c's stream starts in src at or past at: at itself when c sniffs there,
// else c's first sync confirmed within [ResyncWindow] of at. A codec that has read the leading tags
// itself finds its stream from where they end as [StreamStart] would; a tag there (one leadingLen
// sizes from its header) is no stream start, though c may sniff it as one. ok is false when c finds
// no stream. A window over limit is refused, and one that fails to read is an error as for
// [DetectLeading].
func StreamFrom(src ReaderAtSized, at int64, leadingLen func(header []byte) (int64, bool), c Codec, limit int64) (int64, bool, error) {
	if err := bits.CheckAlloc(min(SniffWindow, max(src.Size()-at, 0)), limit); err != nil {
		return 0, false, err
	}
	window, err := readWindow(src, at, SniffWindow)
	if err != nil {
		return 0, false, err
	}
	return streamAt(src, at, window, leadingLen, c, limit)
}

// streamAt is where c's stream starts at or past at, given window, the sniff window there.
func streamAt(src ReaderAtSized, at int64, window []byte, leadingLen func(header []byte) (int64, bool), c Codec, limit int64) (int64, bool, error) {
	if _, tag := leadingLen(window); !tag && c.Sniff(window) {
		return at, true, nil
	}
	if err := bits.CheckAlloc(min(ResyncWindow, src.Size()-at), limit); err != nil {
		return 0, false, err
	}
	_, off, ok, err := resync(src, at, []Codec{c})
	return off, ok, err
}

// leadingChain skips up to MaxLeadingTags chained leading tags at the start of src, each sized by
// leadingLen from its header, given window, the sniff window at the start; one whose declared size
// runs past the end of src ends the chain. It returns where the chain ends, 0 when src starts with no
// tag, and the sniff window there, or the failure of a window read.
func leadingChain(src ReaderAtSized, leadingLen func(header []byte) (int64, bool), window []byte) (int64, []byte, error) {
	var end int64
	for range MaxLeadingTags {
		total, ok := leadingLen(window)
		if !ok || total <= 0 || total > src.Size()-end {
			break
		}
		end += total
		var err error
		if window, err = readWindow(src, end, SniffWindow); err != nil {
			return 0, nil, err
		}
	}
	return end, window, nil
}

// resync returns the codec among cs, of those skipping leading ID3 that implement [Resyncer], whose
// first confirmed sync within ResyncWindow of at comes first, and that sync's offset in src, or the
// failure of the window's read.
func resync(src ReaderAtSized, at int64, cs []Codec) (Codec, int64, bool, error) {
	window, err := readWindow(src, at, ResyncWindow)
	if err != nil {
		return nil, 0, false, err
	}
	var best Codec
	var bestOff int
	for _, c := range cs {
		r, ok := c.(Resyncer)
		if !ok || !c.SkipsLeadingID3() {
			continue
		}
		if off, ok := r.Resync(window); ok && (best == nil || off < bestOff) {
			best, bestOff = c, off
		}
	}
	return best, at + int64(bestOff), best != nil, nil
}

// readWindow reads up to n bytes of src at off, fewer at its end. A read that fails is an error: a
// short read is invalid data, and a device error keeps its own cause.
func readWindow(src ReaderAtSized, off, n int64) ([]byte, error) {
	n = min(n, src.Size()-off)
	if n <= 0 {
		return nil, nil
	}
	b := make([]byte, n)
	if ok, err := bits.FillWithin(src, b, off); !ok {
		return nil, err
	}
	return b, nil
}
