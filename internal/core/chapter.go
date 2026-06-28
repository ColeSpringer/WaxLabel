package core

import (
	"fmt"
	"time"
)

// FormatChapterTime renders a chapter offset as H:MM:SS.mmm - millisecond
// precision, since adjacent chapters can be seconds apart. A negative offset is
// clamped to zero. It is the single chapter-timestamp format shared by the text
// chapter listing and the chapter sanity warnings, so a timestamp named in a
// warning reads identically to the one in the listing it refers to.
func FormatChapterTime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	d -= s * time.Second
	ms := d / time.Millisecond
	return fmt.Sprintf("%d:%02d:%02d.%03d", h, m, s, ms)
}

// Chapter is a single navigation point in a timed file (an audiobook track, a
// long mix). It is format-neutral: the MP4 Nero chpl list and QuickTime text
// track both project into a []Chapter, and the interval-based Matroska
// ChapterAtom (ChapterTimeStart+ChapterTimeEnd) and boundary-based FLAC CUESHEET
// are designed to project into this same type later without an API change.
//
// End is included from day one (a zero End means "until the next chapter, or
// end of file"): the formats this model targets are interval-based, so a
// Start-only struct could not preserve a chapter that ends before the next
// begins (an audiobook silence gap, trailing credits). Because End is part of
// the struct, callers must construct Chapters with keyed fields
// (Chapter{Start: s, Title: t}); a positional literal would break the next time
// a field is added.
type Chapter struct {
	// Start is the chapter's offset from the start of the media.
	Start time.Duration
	// End is where the chapter stops. Zero means "until the next chapter, or
	// end of file" - the common case for the start-only formats (Nero chpl).
	End time.Duration
	// Title is the chapter name (may be empty).
	Title string

	// Language is the chapter title's language as an ISO-639-2 code (Matroska
	// ChapLanguage), e.g. "eng". Empty means unspecified - the EBML "und" default,
	// normalized away on read so a freshly authored chapter (a zero-value Chapter)
	// renders the same "und" the spec assumes and carries no spurious language.
	Language string
	// LanguageIETF is the title's language as a BCP-47 tag (Matroska
	// ChapLanguageIETF), e.g. "en-US". Modern mkvmerge writes it on essentially
	// every chapter; it is modeled so it round-trips rather than being dropped (and
	// firing a flatten warning) on nearly every real file. Empty means none.
	LanguageIETF string
	// Hidden marks the chapter ChapterFlagHidden=1 (not shown by players). The EBML
	// default is 0, so the zero value is the common visible chapter.
	Hidden bool
	// Disabled marks the chapter ChapterFlagEnabled=0. The EBML default for
	// ChapterFlagEnabled is 1 (enabled), so the non-default state is modeled here as
	// Disabled: a zero-value Chapter renders no flag and stays enabled, exactly as a
	// CLI-authored --add-chapter behaves today.
	Disabled bool

	// _ makes positional construction (Chapter{a, b, c}) a compile error in other
	// packages, enforcing the keyed-field contract: a later field (a chapter image
	// or URL) can then be added without breaking any caller's literal. It stays
	// comparable, so Chapter values still compare with ==.
	_ struct{}
}

// ChapterLoss names chapter metadata a destination format cannot preserve, such as
// formats that store only start+title. It is recorded on the chapters [Capability]
// so transfer reports and direct-edit warnings use the same [ChaptersLoseMetadata]
// predicate, matching [PictureLoss] for pictures.
type ChapterLoss uint8

const (
	// ChapterLossNone means the format preserves chapter end times, per-chapter
	// language, and the hidden/disabled flags (Matroska/WebM).
	ChapterLossNone ChapterLoss = iota
	// ChapterLossStartTitleOnly means the format stores each chapter's start and title
	// only, dropping a gapped end time, per-chapter language, and hidden/disabled
	// flags. MP4's Nero chpl and QuickTime text track use this model.
	ChapterLossStartTitleOnly
)

// ChaptersLoseMetadata reports whether writing chs to a destination with loss would
// discard metadata present in chs. Transfers and direct-edit warnings share this
// predicate, so they classify the same chapter sets as lossy.
//
// For [ChapterLossStartTitleOnly]:
//   - Hidden or Disabled chapters lose those flags.
//   - An explicit End that cannot be inferred from the next Start is lost. An End
//     equal to the next Start is safe because MP4 infers it.
//   - Varying Language or LanguageIETF values are lost, but uniform language values
//     are not treated as loss. mkvmerge commonly writes ChapLanguageIETF on every
//     chapter, so language presence alone would make ordinary Matroska-to-MP4 copies
//     look lossy. ISO and IETF values are counted separately so a uniform
//     "eng"/"en-US" pair is not mistaken for variety.
func ChaptersLoseMetadata(chs []Chapter, loss ChapterLoss) bool {
	if loss != ChapterLossStartTitleOnly {
		return false
	}
	// Lazily allocated: a Hidden/Disabled or gapped-end chapter returns before any
	// language is recorded, so the common early-out path allocates nothing. len() on a
	// nil map is 0, so the final distinct-count check below still holds.
	var iso, ietf map[string]bool
	for i, c := range chs {
		if c.Hidden || c.Disabled {
			return true
		}
		if c.End > 0 && (i == len(chs)-1 || c.End != chs[i+1].Start) {
			return true
		}
		if c.Language != "" {
			if iso == nil {
				iso = make(map[string]bool)
			}
			iso[c.Language] = true
		}
		if c.LanguageIETF != "" {
			if ietf == nil {
				ietf = make(map[string]bool)
			}
			ietf[c.LanguageIETF] = true
		}
	}
	return len(iso) > 1 || len(ietf) > 1
}

// ChapterLossFor returns the chapter-metadata loss a format incurs on write, derived
// from the codec's own chapters capability using default write options. Chapter loss
// is not option-dependent, so this cannot drift from the codec's declaration. It lets
// the editor resolve the loss for the edit-time warning without threading write options.
func ChapterLossFor(format Format) ChapterLoss {
	if c, ok := ForFormat(format); ok {
		return c.Capabilities(nil, WriteOptions{}).Chapters.ChapterLoss
	}
	return ChapterLossNone
}

// EqualChapters reports whether two chapter slices are identical by content,
// including order. It is the chapter analogue of EqualPictures, so a codec can
// detect a chapter edit the same way it detects a picture edit.
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

// CloneChapters returns an independent copy of the slice (Chapter has no
// reference fields, so a shallow element copy fully detaches it). It returns nil
// for a nil input so a chapterless document stays chapterless on round-trip.
func CloneChapters(cs []Chapter) []Chapter {
	if cs == nil {
		return nil
	}
	out := make([]Chapter, len(cs))
	copy(out, cs)
	return out
}
