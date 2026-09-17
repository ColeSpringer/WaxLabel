package core

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// FormatChapterTime renders a chapter offset as H:MM:SS.mmm (ms precision, rounded).
// Negative offsets clamp to zero. Shared by chapter listings and chapter warnings.
func FormatChapterTime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Millisecond)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	d -= s * time.Second
	ms := d / time.Millisecond
	return fmt.Sprintf("%d:%02d:%02d.%03d", h, m, s, ms)
}

// Chapter is one timed navigation point. Format-neutral: MP4 chpl/text track,
// Matroska ChapterAtom, and future FLAC CUESHEET all project here.
//
// Zero End means until the next chapter or EOF; non-zero End can preserve gaps.
// Callers outside core must use keyed fields so new fields do not break literals.
type Chapter struct {
	// Start is the chapter's offset from the start of the media.
	Start time.Duration
	// End is where the chapter stops. Zero means "until the next chapter, or
	// end of file" - the common case for the start-only formats (Nero chpl).
	End time.Duration
	// Title is the chapter name (may be empty).
	Title string

	// Language is ISO-639-2 (Matroska ChapLanguage), e.g. "eng". Empty means unspecified;
	// read path normalizes EBML "und" to empty.
	Language string
	// LanguageIETF is BCP-47 (Matroska ChapLanguageIETF), e.g. "en-US". Empty means none.
	LanguageIETF string
	// Hidden is Matroska ChapterFlagHidden=1. Zero value is visible (EBML default 0).
	Hidden bool
	// Disabled is ChapterFlagEnabled=0. Zero value is enabled (EBML default 1).
	Disabled bool

	// _ blocks positional literals outside core; Chapter stays comparable with ==.
	_ struct{}
}

// ChapterLoss names chapter metadata a destination cannot preserve. On the chapters
// [Capability]; [ChaptersLoseMetadata] folds [ChapterLosesMetadata], like [PictureLoss].
type ChapterLoss uint8

const (
	// ChapterLossNone: end times, language, hidden/disabled preserved (Matroska/WebM).
	ChapterLossNone ChapterLoss = iota
	// ChapterLossStartTitleOnly: start+title only (Nero chpl, Vorbis CHAPTERxxx).
	ChapterLossStartTitleOnly
	// ChapterLossLangFlags: start, end, title; no language or flags (ID3 CHAP).
	ChapterLossLangFlags
	// ChapterLossInteriorEndsLangFlags: like start+title but keeps the final chapter's end
	// (MP4 QuickTime text track; WaxLabel's MP4 writer).
	ChapterLossInteriorEndsLangFlags
)

// ChaptersLoseMetadata reports whether writing chs with loss drops metadata they carry.
// Shared by transfers and direct-edit warnings.
//
// [ChapterLossStartTitleOnly]: flags, language, and gapped End are lost; End equal to the
// next Start is OK. Matroska "und" normalizes to empty on read, so default language is not loss.
//
// [ChapterLossLangFlags]: start/end/title survive; language and flags do not.
//
// [ChapterLossInteriorEndsLangFlags]: [ChapterLossStartTitleOnly] except the last chapter's End.
func ChaptersLoseMetadata(chs []Chapter, loss ChapterLoss) bool {
	for i := range chs {
		if ChapterLosesMetadata(chs, i, loss) {
			return true
		}
	}
	return false
}

// ChapterLosesMetadata is the per-chapter form; [ChaptersLoseMetadata] folds it.
// Used by transfer reports to split carried vs lossy chapters.
func ChapterLosesMetadata(chs []Chapter, i int, loss ChapterLoss) bool {
	switch loss {
	case ChapterLossLangFlags:
		c := chs[i]
		return c.Hidden || c.Disabled || c.Language != "" || c.LanguageIETF != ""
	case ChapterLossStartTitleOnly:
		return chapterLosesStartTitle(chs, i, false)
	case ChapterLossInteriorEndsLangFlags:
		return chapterLosesStartTitle(chs, i, true)
	default:
		return false
	}
}

// chapterLosesStartTitle grades start+title models. Flags or language are loss.
// Final End is loss only when keepLastEnd is false; interior End is loss unless it
// reaches the next Start (gapless interval).
func chapterLosesStartTitle(chs []Chapter, i int, keepLastEnd bool) bool {
	c := chs[i]
	if c.Hidden || c.Disabled || c.Language != "" || c.LanguageIETF != "" {
		return true
	}
	if c.End > 0 {
		if i == len(chs)-1 {
			return !keepLastEnd
		}
		return !chapterEndReachesNextStart(chs, i)
	}
	return false
}

// chapterEndReachesNextStart reports whether chapter i's End equals the next Start.
// Shared by loss grading and normalizeReconstructableEnds (diff). Dual of FillInteriorEnds.
func chapterEndReachesNextStart(chs []Chapter, i int) bool {
	return i+1 < len(chs) && chs[i].End == chs[i+1].Start
}

// FillInteriorEnds sets each non-last open chapter's End to the next Start when later.
// Last chapter stays open. Skips out-of-order pairs. Mutates chs in place.
func FillInteriorEnds(chs []Chapter) {
	for i := range chs {
		if chs[i].End == 0 && i+1 < len(chs) && chs[i+1].Start > chs[i].Start {
			chs[i].End = chs[i+1].Start
		}
	}
}

// OpenPastDurationEnds clears End on a final chapter at/ past duration with End==Start,
// matching start-only stores and ID3 CHAP open chapters. No-op when duration is zero.
func OpenPastDurationEnds(chs []Chapter, duration time.Duration) {
	n := len(chs)
	if n == 0 || duration <= 0 {
		return
	}
	last := &chs[n-1]
	if last.Start >= duration.Truncate(time.Millisecond) && last.End == last.Start {
		last.End = 0
	}
}

// ChaptersOpenedPastDuration is [OpenPastDurationEnds] inline; mutates and returns chs.
func ChaptersOpenedPastDuration(chs []Chapter, duration time.Duration) []Chapter {
	OpenPastDurationEnds(chs, duration)
	return chs
}

// OpenRunToEOFEnd clears End on a final chapter that runs to source EOF so the destination
// can refill to its own EOF. Ms truncation matches normalizeReconstructableEnds.
func OpenRunToEOFEnd(chs []Chapter, srcDuration time.Duration) []Chapter {
	n := len(chs)
	srcEOF := srcDuration.Truncate(time.Millisecond)
	if n == 0 || srcEOF <= 0 || chs[n-1].End < srcEOF {
		return chs
	}
	out := CloneChapters(chs)
	out[n-1].End = 0
	return out
}

// ChapterMetadataDroppedMessage returns the edit-time warning text for the fields a
// destination cannot preserve.
func ChapterMetadataDroppedMessage(loss ChapterLoss) string {
	switch loss {
	case ChapterLossLangFlags:
		return "ID3 chapters store start, end, and title only; per-chapter language and hidden/disabled flags are dropped"
	case ChapterLossInteriorEndsLangFlags:
		return "MP4 chapters store start, title, and the final chapter's end; interior gapped end times, per-chapter language, and hidden/disabled flags are dropped"
	default:
		return "chapters store start and title only; gapped end times, per-chapter language, and hidden/disabled flags are dropped"
	}
}

// ChaptersUnsupportedMessage is for formats with no chapter store (whole list dropped).
func ChaptersUnsupportedMessage(f Format) string {
	return fmt.Sprintf("%s %s file cannot store chapters; they were dropped", IndefiniteArticle(f.String()), f)
}

// ChaptersReadOnlyMessage is for read-only chapter stores. kept: file already had chapters.
func ChaptersReadOnlyMessage(f Format, kept bool) string {
	if kept {
		return fmt.Sprintf("chapters in %s %s file are read-only; the chapter edit was dropped and the file keeps its chapters", IndefiniteArticle(f.String()), f)
	}
	return fmt.Sprintf("chapters in %s %s file are read-only; the added chapters were dropped", IndefiniteArticle(f.String()), f)
}

// SortChaptersByStart stable-sorts by Start. Shared by all projectors and the editor.
func SortChaptersByStart(chs []Chapter) {
	slices.SortStableFunc(chs, func(a, b Chapter) int { return cmp.Compare(a.Start, b.Start) })
}

// EqualChapters compares chapter slices literally, including End and order.
// For diff, use EqualChaptersModuloEnds to ignore reconstructable End differences.
func EqualChapters(a, b []Chapter) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// EqualChaptersModuloEnds compares lists after normalizeReconstructableEnds per duration.
// Used by diff. Equality is defined on the normalized form, so it is transitive.
//
// Fast path when durA==durB and lists match literally; dur equality is required because
// trailing-end normalization depends on duration.
//
// Interior gapless ends (End==next.Start) match copy grading via chapterEndReachesNextStart.
// Trailing EOF ends normalize for diff but may still grade lossy on copy when the store
// cannot hold a last end. Unknown duration (0): trailing EOF cannot be proved, so End stays.
func EqualChaptersModuloEnds(a, b []Chapter, durA, durB time.Duration) bool {
	// Same duration and literal match: skip normalization clones (see doc).
	if durA == durB && EqualChapters(a, b) {
		return true
	}
	return EqualChapters(normalizeReconstructableEnds(a, durA), normalizeReconstructableEnds(b, durB))
}

// normalizeReconstructableEnds zeroes reconstructable End values: already open, gapless interior
// (chapterEndReachesNextStart), or trailing End >= ms-truncated duration. Truncation matches
// ID3 CHAP ms flooring. eof<=0 leaves trailing End distinct (unknown duration).
func normalizeReconstructableEnds(chs []Chapter, dur time.Duration) []Chapter {
	out := CloneChapters(chs)
	eof := dur.Truncate(time.Millisecond)
	for i := range out {
		switch {
		case out[i].End == 0:
		case chapterEndReachesNextStart(out, i):
			out[i].End = 0
		case i == len(out)-1 && eof > 0 && out[i].End >= eof:
			out[i].End = 0
		}
	}
	return out
}

// ReconcileChapterOverlaps truncates stale End to the next Start when an edit caused overlap.
// Only timing values not in base trigger reconciliation; pre-existing overlaps are preserved.
// Mutates chs. Requires next > chs[i].Start so End stays above Start.
func ReconcileChapterOverlaps(chs, base []Chapter) bool {
	baseStarts := make(map[time.Duration]bool, len(base))
	baseEnds := make(map[time.Duration]bool, len(base))
	for _, c := range base {
		baseStarts[c.Start] = true
		if c.End > 0 {
			baseEnds[c.End] = true
		}
	}
	changed := false
	for i := 0; i+1 < len(chs); i++ {
		next := chs[i+1].Start
		if chs[i].End > 0 && chs[i].End > next && next > chs[i].Start &&
			(!baseEnds[chs[i].End] || !baseStarts[next]) {
			chs[i].End = next
			changed = true
		}
	}
	return changed
}

// CloneChapters shallow-copies the slice. Nil in, nil out.
func CloneChapters(cs []Chapter) []Chapter {
	if cs == nil {
		return nil
	}
	out := make([]Chapter, len(cs))
	copy(out, cs)
	return out
}
