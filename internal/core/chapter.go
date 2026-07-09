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
// End is explicit because several formats store intervals. A zero End means "until
// the next chapter, or end of file"; a non-zero End can preserve gaps before the next
// chapter. Callers outside core must use keyed fields, such as Chapter{Start: s,
// Title: t}, so new fields can be added without breaking positional literals.
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
	// flags. MP4's Nero chpl uses this model, as does the VorbisComment CHAPTERxxx
	// convention (FLAC/Ogg), which stores a start and a name. (MP4's QuickTime text
	// track is the richer ChapterLossInteriorEndsLangFlags model below - it spares the
	// final chapter's end - and is what WaxLabel's MP4 codec actually uses.)
	ChapterLossStartTitleOnly
	// ChapterLossLangFlags means the format stores each chapter's start, end, and title
	// but no per-chapter language or hidden/disabled flags. ID3v2 CHAP frames use this
	// model: explicit ends survive, while language and visibility metadata do not.
	ChapterLossLangFlags
	// ChapterLossInteriorEndsLangFlags is the MP4 QuickTime-text-track model: each
	// chapter's start and title are stored, the final chapter's explicit end is kept (the
	// text track carries it), but an interior chapter's gapped end, per-chapter language,
	// and hidden/disabled flags are dropped. It differs from ChapterLossStartTitleOnly
	// (FLAC/Ogg, MP4 Nero chpl) only in sparing the last chapter's end, which those
	// start-only stores cannot hold.
	ChapterLossInteriorEndsLangFlags
)

// ChaptersLoseMetadata reports whether writing chs to a destination with loss would
// discard metadata present in chs. Transfers and direct-edit warnings share this
// predicate, so they classify the same chapter sets as lossy.
//
// For [ChapterLossStartTitleOnly]:
//   - Hidden or Disabled chapters lose those flags.
//   - An explicit End that cannot be inferred from the next Start is lost. An End
//     equal to the next Start is safe because MP4 infers it.
//   - Any Language or LanguageIETF value is lost, because these stores hold no
//     per-chapter language field. The read path normalizes the ubiquitous "und"/absent
//     Matroska default to empty, so an ordinary mkvmerge file whose chapters all default
//     to und carries no language here and is not flagged; only a genuine, non-default
//     language ("en-US", "deu") counts.
//
// For [ChapterLossLangFlags] (ID3v2 CHAP), the start, end, and title survive. Any
// language or Hidden/Disabled flag is lost because CHAP has no field for it. The language
// axis matches the start+title formats above: any per-chapter language present is a loss.
//
// For [ChapterLossInteriorEndsLangFlags] (MP4 QuickTime text track), the rule is
// [ChapterLossStartTitleOnly]'s except the final chapter's explicit end is kept, because
// the text track stores it - only an interior gapped end vanishes.
func ChaptersLoseMetadata(chs []Chapter, loss ChapterLoss) bool {
	switch loss {
	case ChapterLossLangFlags:
		for _, c := range chs {
			if c.Hidden || c.Disabled || c.Language != "" || c.LanguageIETF != "" {
				return true
			}
		}
		return false
	case ChapterLossStartTitleOnly:
		return chaptersLoseStartTitle(chs, false)
	case ChapterLossInteriorEndsLangFlags:
		return chaptersLoseStartTitle(chs, true)
	default:
		return false
	}
}

// chaptersLoseStartTitle is the shared predicate for the start+title chapter models. A
// Hidden or Disabled chapter, an interior chapter's gapped end (an End that is neither zero
// nor the next chapter's Start), or any per-chapter language (ISO or IETF) counts as loss,
// since none of these stores holds a per-chapter language field - the language axis matches
// the ID3 CHAP path exactly. Only a genuine language counts: the read path has already
// normalized the ubiquitous Matroska "und" default to empty, so a plain mkvmerge file carries
// none here (ChaptersLoseMetadata's doc has the full rationale). The final chapter's explicit
// end counts as loss only when keepLastEnd is false: FLAC/Ogg and MP4's Nero chpl store no end
// at all, so the last end vanishes there, but MP4's QuickTime text track stores it, so
// keepLastEnd spares it.
func chaptersLoseStartTitle(chs []Chapter, keepLastEnd bool) bool {
	for i, c := range chs {
		if c.Hidden || c.Disabled || c.Language != "" || c.LanguageIETF != "" {
			return true
		}
		if c.End > 0 {
			if i == len(chs)-1 {
				if !keepLastEnd {
					return true // the store holds no end at all; the final end is lost
				}
			} else if !chapterEndReachesNextStart(chs, i) {
				return true // an interior gapped end cannot be inferred from the next start
			}
		}
	}
	return false
}

// chapterEndReachesNextStart reports whether chapter i's End coincides with the next
// chapter's Start - a gapless interior interval a start-only store can reconstruct by
// inferring the end from the following start. It is the single interior-end predicate
// shared by chaptersLoseStartTitle (chapter-loss grading) and normalizeReconstructableEnds
// (diff equivalence), so the two cannot drift on what "gapless" means. A last chapter (no
// i+1) is never gapless-interior. It is the query-side dual of FillInteriorEnds (which sets
// the end this predicate then recognizes).
func chapterEndReachesNextStart(chs []Chapter, i int) bool {
	return i+1 < len(chs) && chs[i].End == chs[i+1].Start
}

// FillInteriorEnds sets each non-last open chapter's End to the next chapter's Start when that
// start is later, so a start-only source (MP4 Nero chpl on read, an authored list on write)
// yields gapless closed intervals. The last chapter is left open ("until end of file"); a store
// that bounds the final chapter (ID3 CHAP, from the media duration) does that separately. The
// later-start guard skips an out-of-order pair, so it never produces End < Start. It mutates chs
// in place, so callers that must not disturb their input pass a CloneChapters copy.
//
// It is the single interior end-fill shared by the MP4 read/write paths and the ID3 CHAP writer,
// so the gapless-interior fill rule lives in one place rather than drifting between packages.
func FillInteriorEnds(chs []Chapter) {
	for i := range chs {
		if chs[i].End == 0 && i+1 < len(chs) && chs[i+1].Start > chs[i].Start {
			chs[i].End = chs[i+1].Start
		}
	}
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

// EqualChapters reports whether two chapter slices are identical by content,
// including order. It is the chapter analogue of EqualPictures, so a codec can
// detect a chapter edit the same way it detects a picture edit.
//
// It compares End literally, which is required for codec change-detection
// (chaptersChanged := !EqualChapters(...)): a genuine end edit must be detected. Callers
// that want to treat a reconstructable end difference as equal (the diff command, matching
// how copy grades such a difference as reconstructable) use EqualChaptersModuloEnds instead.
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

// EqualChaptersModuloEnds reports whether two chapter lists are equal once each list's
// reconstructable ends are normalized away, so a difference the destination codecs would
// themselves reconstruct is not reported as a change. It is what diff uses in place of
// EqualChapters. Two lists are equal when they share the same canonical normalized form: each is
// reduced by normalizeReconstructableEnds against its own media duration, and the results are
// compared literally. Because equality is defined by the canonical form, it is transitive, so A==B
// and B==C imply A==C.
//
// The fast path fires only on equal durations. When durA == durB and the lists are byte-identical,
// they normalize to the same form, so the slow path would return true anyway; short-circuiting
// there just skips the two normalization clones on the common diff case, a file compared against a
// metadata-only-edited copy of itself. The durA == durB condition is not optional. Two
// byte-identical lists at different durations can normalize differently, because the trailing-end
// rule below depends on the duration: a 50s trailing end runs to EOF in a 50s file but sits
// mid-file in a 100s file. A duration-blind fast path would call those equal and break transitivity
// (mka equals mp3 and mka equals flac while mp3 differs from flac).
//
// On the interior gapless rule (End == next.Start) diff and copy agree that the end is
// reconstructable ("0 lossy"); the shared chapterEndReachesNextStart predicate keeps grading and
// diff from drifting on it. The trailing run-to-EOF rule is diff-specific and does not mirror
// copy: copy grades any trailing end dropped to a start-only store (FLAC/Ogg,
// chaptersLoseStartTitle with keepLastEnd=false) as lossy even when it runs to EOF, while diff
// treats a last end that reaches EOF as reconstructable. The divergence is confined to the
// trailing end and is intended: a run-to-EOF end carries nothing a shorter store would lose,
// while copy's grade turns on whether the store can hold a last end at all.
//
// durA/durB are the two files' media durations, used only for the trailing rule. When a file's
// duration is unknown (0), its trailing end cannot be shown to run to EOF, so a bounded trailing
// end there stays distinct. Reporting it equal instead would bring back the non-transitive
// mka/mp3/flac shape, so the conservative reading, "cannot prove equal, so not equal", is the one
// that keeps transitivity. Non-end fields (Start, Title, Language, Hidden, Disabled, and so on) are
// compared with ==, so any real difference outside the reconstructable-end axis still counts.
func EqualChaptersModuloEnds(a, b []Chapter, durA, durB time.Duration) bool {
	// Byte-identical lists at the same duration normalize to the same form, so the slow path would
	// return true anyway; skip its two clones. See the doc comment for why the guard must gate on
	// durA == durB.
	if durA == durB && EqualChapters(a, b) {
		return true
	}
	return EqualChapters(normalizeReconstructableEnds(a, durA), normalizeReconstructableEnds(b, durB))
}

// normalizeReconstructableEnds returns a copy of chs with every reconstructable End set to 0
// ("open"), so two lists that differ only in ends a codec would reconstruct compare equal. An
// End is reconstructable when it is already 0, when it is a gapless interior end (equal to the
// next chapter's Start - the codecs' own inference rule, shared via chapterEndReachesNextStart),
// or when it is the trailing chapter's end and the chapter runs to end-of-file (End >= the media
// duration).
//
// The trailing rule intentionally differs from the format-based grading in
// chaptersLoseStartTitle (keepLastEnd): grading asks whether the destination store *can hold* a
// last end, while diff asks whether the last end is merely "until EOF" and so carries no
// information a shorter store would lose. dur is truncated to whole milliseconds before the
// comparison because the ID3 CHAP writer floors a filled trailing end to ms (durationToMs), so
// a written end reads back as floor(duration) ms while Properties().Duration() is
// nanosecond-precise; without the truncation floor(dur)ms >= dur would be false and a genuine
// run-to-EOF trailing chapter would wrongly count as different.
//
// A duration that truncates to 0 ms is treated like an unknown one, so the durMs > 0 guard below
// leaves the trailing end distinct rather than normalizing it. This covers both an unknown duration
// (0) and the sub-millisecond case, which real media never produces. At whole-ms resolution a
// sub-ms end cannot be shown to reach EOF: End >= 0 holds for every end, which would normalize even
// a chapter that stops well short of it. Leaving it distinct keeps the conservative "cannot prove
// equal, so not equal" reading that transitivity depends on.
func normalizeReconstructableEnds(chs []Chapter, dur time.Duration) []Chapter {
	out := CloneChapters(chs)
	durMs := dur.Truncate(time.Millisecond)
	for i := range out {
		switch {
		case out[i].End == 0:
		case chapterEndReachesNextStart(out, i): // gapless interior
			out[i].End = 0
		case i == len(out)-1 && durMs > 0 && out[i].End >= durMs: // trailing runs to EOF
			out[i].End = 0
		}
	}
	return out
}

// ReconcileChapterOverlaps truncates a chapter's stale explicit end to the following
// chapter's start wherever the edit introduced an overlap, returning whether any end
// changed. For a start-sorted chs, an adjacent pair i/i+1 where chs[i] has a non-zero End
// past chs[i+1].Start is reconciled (chs[i].End set to chs[i+1].Start) only when the overlap
// is timing-introduced: either the overshooting End or the overshot Start is a value no base
// chapter carries - an inserted marker's new start, or an edited/lengthened end. Inserting a
// start-only marker between two already-ended chapters is the motivating case: the preceding
// chapter's end otherwise overlaps the marker (ID3/Matroska write it verbatim; MP4 fires a
// spurious drop warning).
//
// Keying on the timing values, not the whole struct, is deliberate: retitling a chapter or
// editing any non-timing field leaves both End and Start on base, so a file's own pre-existing
// on-disk overlap is left verbatim - preservation-first, scoping the change to exactly the
// overlap the edit's timing caused. (A whole-struct membership check would treat a retitle as
// "new" and shorten an unrelated pre-existing overlap.)
//
// It mutates chs in place, so the caller passes a clone. The truncation is guarded by
// next >= chs[i].Start, so End can never drop below Start even if a caller passes an unsorted
// list (a no-op there rather than latent End<Start corruption). A list shorter than two
// chapters is a safe no-op.
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
		if chs[i].End > 0 && chs[i].End > next && next >= chs[i].Start &&
			(!baseEnds[chs[i].End] || !baseStarts[next]) {
			chs[i].End = next
			changed = true
		}
	}
	return changed
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
