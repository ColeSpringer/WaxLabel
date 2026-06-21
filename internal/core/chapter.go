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

	// _ makes positional construction (Chapter{a, b, c}) a compile error in other
	// packages, enforcing the keyed-field contract: a later field (a chapter image
	// or URL) can then be added without breaking any caller's literal. It stays
	// comparable, so Chapter values still compare with ==.
	_ struct{}
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
