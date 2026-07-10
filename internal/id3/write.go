package id3

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// WriteOpts are the inputs to a frame rebuild. The multi-value policy is the
// shared core type so it can be a public write option without duplication.
type WriteOpts struct {
	Multi        core.ID3MultiValuePolicy
	NumericGenre bool // write TCON as a numeric reference when the genre is standard
}

// StructuredEdit carries the non-tag structures a frame rebuild owns. A structure is
// dropped and re-emitted only when its change flag is set; otherwise the source frames
// are preserved as-is.
type StructuredEdit struct {
	Pictures            []core.Picture
	PicturesChanged     bool
	Chapters            []core.Chapter
	ChaptersChanged     bool
	SyncedLyrics        []core.SyncedLyrics
	SyncedLyricsChanged bool
	// SyncedLyricsCarried marks the synced-lyrics edit as a faithful cross-format carry, so
	// the empty-language fallback to the destination's existing SYLT language is skipped: a
	// carry of a no-language set (FLAC/Ogg store none) must read back with no language, not
	// silently inherit the destination's. An authored line-only edit leaves this false and
	// keeps the documented CLI convenience of preserving the file's existing language.
	SyncedLyricsCarried bool
	// SyncedLyricsCleared marks the synced-lyrics set as explicitly cleared before this edit
	// authored a new one, so the same language/descriptor fallback is skipped: a clear means
	// "start fresh," so an authored set with no language reads back with none instead of
	// inheriting the cleared one. It is distinct from SyncedLyricsCarried (a faithful transfer)
	// so the edit is not mislabeled; both suppress the fallback.
	SyncedLyricsCleared bool
	// MediaDuration is the file's playable length, used only to bound a trailing open-ended
	// chapter (End == 0) at CHAP serialization time so a spec-conforming reader sees a
	// concrete end instead of the 0xFFFFFFFF "unused" sentinel (~49.7 days). Zero (unknown
	// duration) leaves the trailing chapter open, emitting the sentinel as before. The
	// canonical core.Chapter{End:0} "open" model is unchanged; the fill is ID3-local.
	MediaDuration time.Duration
}

// RebuildInfo reports facts about a rebuild the caller surfaces in the write
// report.
type RebuildInfo struct {
	// UsedV23Multi is true when a v2.3 tag was written with NUL-separated
	// multi-values (a nonstandard extension whose compatibility impact is flagged).
	UsedV23Multi bool
	// DroppedDates lists year-anchored date keys whose edited value had no extractable
	// numeric year and so rendered no v2.3 frame at all - a silent drop the caller
	// surfaces as a value-dropped warning. Empty on v2.4 (TDRC/TDOR store the full
	// string) and for values that do carry a year (only the sub-year precision is lost,
	// which is not a drop). See detectDroppedDates.
	DroppedDates []tag.Key
	// ReducedDates lists date keys whose v2.3 rendering silently lost precision finer than
	// the rendered frames capture: a month with no full date drops to the year (TDAT needs a
	// full DDMM), and an hour with no minute drops to the date (TIME needs a full HHMM). Each
	// entry pairs the key with the attempted edited value (e.g. "2021-03", "2021-03-15T10")
	// for the warning text and the precision-aware suppression. Scoped to RecordingDate;
	// OriginalDate's v2.3 reductions are reported through the capability-based value-reduced
	// path (its TORY field is AccessPartial), so listing it here would double-warn. See
	// detectReducedDates and reducesDatePrecision.
	ReducedDates []ReducedDate
	// HasDroppedMalformedPicture is set when a picture edit replaced the APIC frames and
	// at least one original APIC could not be decoded (a malformed cover). Those raw
	// bytes are not carried forward, so the loss is surfaced rather than left silent.
	HasDroppedMalformedPicture bool
	// NumericGenres lists the GENRE values this edit set that are a bare number naming a
	// standard genre by index (e.g. "17"). Written verbatim to TCON, such a value reads
	// back as the genre NAME on the pure-ID3 formats, so the caller surfaces it as a
	// write-time numeric-genre warning - symmetric with the read-time one - suppressed
	// where a native container keeps the literal number (WAV/AIFF INFO/text). See
	// detectNumericGenres.
	NumericGenres []string
	// DroppedTotals lists the TRACKTOTAL/DISCTOTAL keys whose canonical value cannot be composed
	// into a valid "n/total" TRCK/TPOS frame because the number field is non-numeric (e.g.
	// TRACKNUMBER="A1"): the reader would read "A1/12" as one literal value with the total merged
	// in and lost, so renderNumTotal preserves the number verbatim and drops the total. The caller
	// surfaces it as a value-dropped warning keyed to the total. An embedded total in the number
	// itself ("A1/12" with no canonical TRACKTOTAL) is preserved verbatim and is not a drop. See
	// detectDroppedTotals.
	DroppedTotals []tag.Key
	// ChapterOverflow is set when a chapter edit clamped a start or end past the CHAP
	// frame's 32-bit millisecond field (~49.7 days). The caller surfaces it as a
	// chapter-start-overflow warning.
	ChapterOverflow bool
	// DroppedChapterSubframes is set when a chapter edit dropped a source CHAP's subframe
	// other than the TIT2 title (a per-chapter image or URL the flat model cannot hold),
	// so that loss is surfaced rather than left silent.
	DroppedChapterSubframes bool
	// SyncedLyricsOverflow is set when a synced-lyrics edit clamped a line's timestamp past
	// the SYLT frame's 32-bit millisecond field (~49.7 days). The caller surfaces it as a
	// synced-lyrics-timestamp-clamped warning.
	SyncedLyricsOverflow bool
	// SyncedLyricsInvalidNUL is set when a synced-lyrics edit's modeled line text or descriptor
	// carries an embedded NUL, which the NUL-terminated SYLT field would silently truncate. Unlike
	// the warnings above, this is a hard error: the caller turns it into waxerr.ErrInvalidData via
	// RebuildError and refuses the write, rather than writing a truncated frame.
	SyncedLyricsInvalidNUL bool
	// SyncedLyricsLangUndefined is set when an authored synced-lyrics set carried a non-empty
	// language that normalizes to the ID3 "undefined" marker ("xxx"/"XXX"): the value is
	// stored (exit 0) but reads back with no language, so the caller surfaces it as a
	// metadata-dropped warning rather than letting the downgrade go unnoticed. Only an
	// explicitly-authored language triggers it - a faithful carry of a no-language source set
	// has an empty language and reads back empty, so it is not flagged.
	SyncedLyricsLangUndefined bool
}

// ReducedDate pairs a date key with the value an edit attempted to store before a
// lower-fidelity v2.3 rendering reduced its precision.
type ReducedDate struct {
	Key   tag.Key
	Value string
}

// RebuildFrames produces the new frame list for an edited tag, preserving
// unchanged and unmodelled frames in place and re-rendering only the frames a
// changed canonical key affects. Pictures and chapters are reconciled here as well,
// since APIC and CHAP/CTOC frames are interleaved with text frames.
func RebuildFrames(orig []Frame, base, edited tag.TagSet, version byte,
	se StructuredEdit, opts WriteOpts) ([]Frame, RebuildInfo) {

	picturesChanged := se.PicturesChanged
	changed := diffKeys(base, edited)
	dirty := map[string]bool{}
	for k := range changed {
		for _, rid := range keyRenderIDs(k, version) {
			dirty[rid] = true
		}
	}

	// The read path does not expose the COMM/USLT 3-byte language and stores a TXXX
	// description under its uppercased canonical key, so recover both from the original
	// frames when rewriting. Re-rendered comment and lyric frames keep their language,
	// and custom TXXX frames keep their original description casing.
	// frameRenderID marks a COMM/USLT frame managed only when its description is empty,
	// so there is at most one managed COMM and one managed USLT to reuse.
	origLangs := map[string]string{}    // "COMM"/"USLT"/"SYLT" -> 3-byte language
	var origSyltDesc string             // first projecting lyrics SYLT's content descriptor (authored-set fallback)
	origTXXXDesc := map[string]string{} // TXXX render token -> original description (verbatim casing)
	for _, f := range orig {
		switch f.ID {
		case "COMM", "USLT":
			if rid, managed := frameRenderID(f); managed && len(f.Body) >= 4 {
				origLangs[rid] = string(f.Body[1:4])
			}
		case "SYLT":
			// Recover the first lyrics SYLT's language and content descriptor as fallbacks for a
			// re-rendered set whose modeled value is unset, so a line-only edit keeps them. Only a
			// projecting lyrics frame qualifies; a chord or trivia SYLT that appears first must not
			// donate its metadata to the lyrics set. Both are captured under the origLangs
			// seen-guard, so they come from the same first projecting SYLT.
			if _, seen := origLangs["SYLT"]; !seen && syltProjectsLyrics(f.Body) {
				if l, ok := syltFrameLanguage(f.Body); ok {
					origLangs["SYLT"] = l
				}
				if d, ok := syltFrameDescriptor(f.Body); ok {
					origSyltDesc = d
				}
			}
		case "TXXX":
			if rid, managed := frameRenderID(f); managed {
				if desc, _, ok := decodeUserText(f.Body); ok {
					origTXXXDesc[rid] = desc
				}
			}
		}
	}

	var out []Frame
	var info RebuildInfo
	emitted := map[string]bool{}
	firstAPIC := -1

	for _, f := range orig {
		if f.ID == "APIC" {
			if !picturesChanged {
				out = append(out, f.Clone())
				continue
			}
			// Picture edits replace the original APIC frames with the edited picture set.
			// If an original APIC has a malformed header, it cannot be projected and will
			// not be carried forward, so surface that loss.
			//
			// This is a deliberate per-codec-family difference: the ID3 codecs drop an
			// undecodable cover on a picture edit and warn (via HasDroppedMalformedPicture,
			// reconciled onto the returned document by CarryProjectionWarnings), whereas the
			// Vorbis-comment codecs (FLAC/Ogg) re-append their undecodable PICTURE block
			// verbatim. The two metadata models differ enough - an opaque FLAC block round-trips
			// trivially, an APIC frame does not - that unifying them is a larger design change
			// than this pass; each is internally consistent (its returned document matches a
			// fresh re-parse of its own output).
			if !validAPIC(f.Body) {
				info.HasDroppedMalformedPicture = true
			}
			if firstAPIC < 0 {
				firstAPIC = len(out)
			}
			continue // re-emitted from the edited picture set below
		}
		if (f.ID == "CHAP" || f.ID == "CTOC") && !f.Opaque {
			if !se.ChaptersChanged {
				out = append(out, f.Clone())
				continue
			}
			// Chapter edits replace decoded CHAP/CTOC frames with the edited flat list. Opaque
			// CHAP/CTOC frames are preserved below because their body was never decoded. A CHAP
			// with non-title subframes loses those subframes when replaced, so flag it once.
			if !info.DroppedChapterSubframes && f.ID == "CHAP" && chapHasExtraSubframes(f.Body, version) {
				info.DroppedChapterSubframes = true
			}
			continue
		}
		if f.ID == "SYLT" && !f.Opaque {
			// A synced-lyrics edit replaces the SYLT frames the model owns: lyrics with
			// millisecond timestamps. Non-projecting SYLT frames, such as chord/trivia tracks
			// or MPEG-frame-timestamped entries, stay verbatim because they are outside the
			// lyrics model.
			if !se.SyncedLyricsChanged || !syltProjectsLyrics(f.Body) {
				out = append(out, f.Clone())
				continue
			}
			continue // re-emitted from the edited synced-lyrics set below
		}
		if f.Opaque {
			out = append(out, f.Clone())
			continue
		}
		rid, managed := frameRenderID(f)
		if !managed {
			out = append(out, f.Clone())
			continue
		}
		if dirty[rid] {
			if !emitted[rid] {
				frames, v23multi := renderUnit(rid, edited, version, opts, origLangs, origTXXXDesc)
				out = append(out, frames...)
				info.UsedV23Multi = info.UsedV23Multi || v23multi
				emitted[rid] = true
			}
			continue // a changed key's frame is rendered once; drop duplicates
		}
		// Not the write-version's target for this key. If this frame is a stale
		// alternative representation of a key that changed (e.g. a TXXX:RELEASEDATE
		// or a TDRC left behind when the canonical write target is TDRL or TYER),
		// drop it so the value is not duplicated or the edit silently lost.
		if touchesChangedKey(f, changed) {
			continue
		}
		// A managed text frame carried verbatim can itself hold a v2.3 NUL-separated
		// multi-value (a copy that preserves the destination's existing multi-value field,
		// or an unrelated edit on a v2.3 file that already had one). The re-render path above
		// never sees it, so flag it here too - the caveat is a property of the OUTPUT, which
		// still carries the NUL-separated multi-value some readers do not split. v2.4 always
		// splits cleanly, so only v2.3 applies.
		if version == 3 && len(DecodeText(f)) > 1 {
			info.UsedV23Multi = true
		}
		out = append(out, f.Clone())
	}

	// Append frames for changed keys that had no original frame (newly added),
	// in a deterministic (sorted) order so the same edit always yields the same
	// bytes.
	leftover := make([]string, 0, len(dirty))
	for rid := range dirty {
		if !emitted[rid] {
			leftover = append(leftover, rid)
		}
	}
	slices.Sort(leftover)
	for _, rid := range leftover {
		frames, v23multi := renderUnit(rid, edited, version, opts, origLangs, origTXXXDesc)
		out = append(out, frames...)
		info.UsedV23Multi = info.UsedV23Multi || v23multi
		emitted[rid] = true
	}

	// Place new pictures where the originals were (or at the end if none existed).
	if picturesChanged {
		if firstAPIC < 0 {
			firstAPIC = len(out)
		}
		pics := make([]Frame, 0, len(se.Pictures))
		for _, p := range se.Pictures {
			pics = append(pics, Frame{ID: "APIC", Body: encodeAPIC(p, version)})
		}
		out = slices.Insert(out, firstAPIC, pics...)
	}

	// Append edited chapters after text and picture frames. Readers resolve CHAP/CTOC by
	// element ID, so frame position is not significant.
	if se.ChaptersChanged && len(se.Chapters) > 0 {
		chapFrames, overflow := chapterFrames(se.Chapters, se.MediaDuration, version)
		out = append(out, chapFrames...)
		info.ChapterOverflow = overflow
	}

	// Append edited synced lyrics. SYLT is self-contained (no element-ID references), so
	// frame position is not significant. A set with an empty language or descriptor falls back to
	// the first original SYLT's language and descriptor, so an authored line-only edit preserves
	// them. A faithful carry passes no fallback, so a no-metadata source set (FLAC/Ogg store
	// neither) reads back with none instead of inheriting the destination's.
	if se.SyncedLyricsChanged && len(se.SyncedLyrics) > 0 {
		fallbackLang := origLangs["SYLT"]
		fallbackDesc := origSyltDesc
		// A faithful carry (no inheritance) and an explicit clear-then-author (start fresh) both
		// suppress the fallback, so an authored set with no language reads back with none rather
		// than silently inheriting the destination's existing SYLT language and descriptor.
		if se.SyncedLyricsCarried || se.SyncedLyricsCleared {
			fallbackLang = ""
			fallbackDesc = ""
		}
		syltF, overflow, invalidNUL := syltFrames(se.SyncedLyrics, version, fallbackLang, fallbackDesc)
		out = append(out, syltF...)
		info.SyncedLyricsOverflow = overflow
		info.SyncedLyricsInvalidNUL = invalidNUL
		// An explicitly-authored language that normalizes to the ID3 "undefined" marker
		// ("xxx"/"XXX") is stored but reads back with no language, so flag the silent
		// downgrade. syltLanguage is the read-side normalizer, so this fires exactly when a
		// fresh parse would report no language. A no-language carry has an empty Language and
		// does not trip it.
		for _, sl := range se.SyncedLyrics {
			if sl.Language != "" && syltLanguage(sl.Language) == "" {
				info.SyncedLyricsLangUndefined = true
				break
			}
		}
	}

	info.DroppedDates = detectDroppedDates(changed, edited, version)
	info.ReducedDates = detectReducedDates(changed, edited, version)
	info.NumericGenres = detectNumericGenres(changed, edited)
	info.DroppedTotals = detectDroppedTotals(changed, edited)
	return out, info
}

// FrontTag is the rendered leading ID3v2 tag a front-tag-only codec (MP3, AAC) emits, plus
// the report fragments the emission contributes. Bytes/Tag are nil when the tag is dropped
// (no frame survives) - the caller writes no tag segment and hands its result builder a nil
// tag (so the output re-parses with no front tag, audioStart 0).
type FrontTag struct {
	Bytes      []byte         // rendered tag (header + frames + padding); nil to drop the tag
	Tag        *Tag           // the new tag for the result document; nil when dropped
	Padding    int64          // padding bytes written (0 when dropped)
	Operations []string       // report operation lines this emission adds, in order
	Warnings   []core.Warning // report warnings this emission adds
}

// ContainerOps returns the write-report operation lines for the embedded ID3 containers -
// pictures, chapters, and synced lyrics - each gated on its own change flag and canonical model
// count. A cleared container (count 0) emits no line: "pictures: 0" reads oddly and the removal is
// already captured by the tag rewrite/removal op. It is the single gate shared by RenderFrontTag
// (MP3/AAC) and the WAV/AIFF planChunks callers, so the four ID3-backed codecs cannot drift on it.
func ContainerOps(picturesChanged bool, pictureCount int, chaptersChanged bool, chapterCount int, syncedLyricsChanged bool, syncedLyricsCount int) []string {
	var ops []string
	if picturesChanged && pictureCount > 0 {
		ops = append(ops, fmt.Sprintf("pictures: %d", pictureCount))
	}
	if chaptersChanged && chapterCount > 0 {
		ops = append(ops, fmt.Sprintf("chapters: %d", chapterCount))
	}
	if syncedLyricsChanged && syncedLyricsCount > 0 {
		ops = append(ops, fmt.Sprintf("synced lyrics: %d", syncedLyricsCount))
	}
	return ops
}

// RenderFrontTag sizes and renders the leading ID3v2 tag for a codec that stores tags only
// as a single front tag (MP3, AAC), centralizing the drop-empty-tag policy so the two cannot
// diverge. It emits the tag only when at least one frame survives: an edit (or a --legacy
// strip) that leaves no frames drops the tag entirely rather than fabricating an empty,
// padding-only container, matching WAV/AIFF's len(frames)>0 chunk guard. hadTag is whether the
// source carried a front tag (so a full clear records an "ID3v2 removal" op instead of a bare
// rewrite); srcTagLen is its byte length for in-place padding reuse; pictureCount,
// chapterCount, and syncedLyricsCount are used for the picture, chapter, and synced-lyrics
// operation lines.
func RenderFrontTag(srcTag *Tag, version byte, newFrames []Frame, info RebuildInfo, pad core.PaddingPolicy,
	srcTagLen int64, hadTag, tagsChanged, picturesChanged bool, pictureCount int,
	chaptersChanged bool, chapterCount int, syncedLyricsChanged bool, syncedLyricsCount int) FrontTag {

	if len(newFrames) == 0 {
		var ft FrontTag
		if hadTag {
			// A full clear of a file that had a front tag: record the removal so a contentful
			// write (a clear-all) is not reported as a bare rewrite.
			ft.Operations = []string{"ID3v2 removal"}
		}
		return ft
	}
	// Size the tag and its padding. Reuse the original region in place when the new content
	// fits, so the audio offset (and file size) need not change.
	nonPad := RenderedSize(newFrames)
	padSize := pad.ReuseOrTarget(srcTagLen, nonPad)
	// Clamp at the sizing layer, not only inside Render: Report().PaddingAfter comes from
	// ft.Padding, so a hidden clamp would make the report overstate the written padding.
	// The practical trigger is a reused tag region larger than the ID3v2 size field.
	padSize, clamped := clampPadding(nonPad, padSize)
	ft := FrontTag{
		Bytes:   Render(version, newFrames, int(padSize)),
		Tag:     srcTag.WithFrames(newFrames),
		Padding: padSize,
	}
	if clamped {
		ft.Warnings = core.Warn(ft.Warnings, core.WarnPaddingClamped,
			fmt.Sprintf("requested ID3v2 padding exceeded the 28-bit size field (max %d bytes) and was clamped to it", maxFrameSize))
	}
	if tagsChanged {
		ft.Operations = append(ft.Operations, "ID3v2 frame rewrite")
	}
	// The pictures/chapters/synced-lyrics op lines come from the shared ContainerOps, slotted here
	// between the frame-rewrite and tag-creation ops.
	ft.Operations = append(ft.Operations, ContainerOps(picturesChanged, pictureCount,
		chaptersChanged, chapterCount, syncedLyricsChanged, syncedLyricsCount)...)
	if !hadTag {
		ft.Operations = append(ft.Operations, fmt.Sprintf("ID3v2.%d tag creation", version))
	}
	if info.UsedV23Multi {
		ft.Operations = append(ft.Operations, "v2.3 multi-value NUL-separated storage")
		ft.Warnings = core.Warn(ft.Warnings, core.WarnID3MultiValue,
			"a multi-value field was written NUL-separated in ID3v2.3, a de-facto extension some readers do not split")
	}
	return ft
}

// frameRenderID returns a frame's render token and whether the frame is managed
// (modelled by the canonical projection, hence re-rendered when its field
// changes). Unmanaged frames - URLs, POPM, PRIV, described comments, non-MB
// UFIDs, opaque frames - are always preserved verbatim.
func frameRenderID(f Frame) (string, bool) {
	if f.Opaque {
		return "", false
	}
	switch f.ID {
	case "APIC":
		return "", false
	case "TXXX":
		desc, _, ok := decodeUserText(f.Body)
		if !ok {
			return "", false
		}
		return "TXXX\x00" + strings.ToUpper(strings.TrimSpace(desc)), true
	case "UFID":
		owner, _, ok := decodeUFID(f.Body)
		if !ok || owner != musicBrainzOwner {
			return "", false
		}
		return "UFID", true
	case "COMM":
		// Only an empty-description COMM is managed as the canonical Comment; a described
		// COMM, such as iTunNORM or a ReplayGain note, is preserved verbatim. The flat
		// Comment model has no per-comment language, so editing Comment merges multiple
		// empty-description COMM frames in different languages into one frame. The texts
		// are still retained under Comment, and untouched Comment frames are preserved
		// verbatim. Preserving the language split would require a language-aware comment
		// model across codecs.
		desc, _, ok := decodeCommentFrame(f.Body)
		if !ok || desc != "" {
			return "", false
		}
		return "COMM", true
	case "USLT":
		desc, _, ok := decodeLangText(f.Body)
		if !ok || desc != "" {
			return "", false
		}
		return "USLT", true
	case "TCON", "TRCK", "TPOS":
		return f.ID, true
	}
	if isDateFrame(f.ID) {
		return f.ID, true
	}
	if strings.HasPrefix(f.ID, "T") {
		return f.ID, true
	}
	return "", false
}

// frameKeys returns the canonical keys a managed frame contributes to. The
// rebuilder uses it to drop a stale alternative representation of a key that
// changed - the same canonical value can sit under more than one frame across
// versions (TYER vs TDRC, TXXX:RELEASEDATE vs TDRL, TXXX:ISRC vs TSRC), and only
// the write-version's target should survive an edit.
func frameKeys(f Frame) []tag.Key {
	if f.Opaque {
		return nil
	}
	switch f.ID {
	case "APIC":
		return nil
	case "TXXX":
		desc, _, ok := decodeUserText(f.Body)
		if !ok {
			return nil
		}
		if k, ok := mapping.ID3TXXXKey(desc); ok {
			return []tag.Key{k}
		}
		return nil
	case "UFID":
		owner, _, ok := decodeUFID(f.Body)
		if !ok || owner != musicBrainzOwner {
			return nil
		}
		return []tag.Key{tag.MBRecordingID}
	case "COMM":
		desc, _, ok := decodeCommentFrame(f.Body)
		if !ok || desc != "" {
			return nil
		}
		return []tag.Key{tag.Comment}
	case "USLT":
		desc, _, ok := decodeLangText(f.Body)
		if !ok || desc != "" {
			return nil
		}
		return []tag.Key{tag.Lyrics}
	case "TCON":
		return []tag.Key{tag.Genre}
	case "TRCK":
		return []tag.Key{tag.TrackNumber, tag.TrackTotal}
	case "TPOS":
		return []tag.Key{tag.DiscNumber, tag.DiscTotal}
	case "TYER", "TDAT", "TIME", "TDRC":
		return []tag.Key{tag.RecordingDate}
	case "TDRL":
		return []tag.Key{tag.ReleaseDate}
	case "TDOR", "TORY":
		return []tag.Key{tag.OriginalDate}
	}
	if strings.HasPrefix(f.ID, "T") {
		if k, ok := mapping.ID3FrameKey(f.ID); ok {
			return []tag.Key{k}
		}
		if k, err := tag.ParseKey(strings.TrimSpace(f.ID)); err == nil {
			return []tag.Key{k}
		}
	}
	return nil
}

// touchesChangedKey reports whether any canonical key the frame contributes to
// is in the changed set.
func touchesChangedKey(f Frame, changed map[tag.Key]bool) bool {
	for _, k := range frameKeys(f) {
		if changed[k] {
			return true
		}
	}
	return false
}

// keyRenderIDs returns the render tokens a change to key dirties under the write
// version.
func keyRenderIDs(key tag.Key, version byte) []string {
	switch key {
	case tag.TrackNumber, tag.TrackTotal:
		return []string{"TRCK"}
	case tag.DiscNumber, tag.DiscTotal:
		return []string{"TPOS"}
	case tag.Genre:
		return []string{"TCON"}
	case tag.MBRecordingID:
		return []string{"UFID"}
	case tag.Comment:
		return []string{"COMM"}
	case tag.Lyrics:
		return []string{"USLT"}
	case tag.RecordingDate:
		if version >= 4 {
			return []string{"TDRC"}
		}
		return []string{"TYER", "TDAT", "TIME"}
	case tag.ReleaseDate:
		if version >= 4 {
			return []string{"TDRL"}
		}
		return []string{"TXXX\x00RELEASEDATE"}
	case tag.OriginalDate:
		if version >= 4 {
			return []string{"TDOR"}
		}
		return []string{"TORY"}
	}
	if id, ok := mapping.ID3KeyFrame(key); ok {
		return []string{id}
	}
	if rawFrameIDKey(key) {
		return []string{string(key)}
	}
	return []string{"TXXX\x00" + strings.ToUpper(mapping.ID3TXXXDesc(key))}
}

// rawFrameIDKey reports whether a canonical key is itself a plain ID3 text-frame
// identifier (four characters beginning with T), so an otherwise-unmapped text
// frame round-trips under its own identifier instead of via TXXX.
func rawFrameIDKey(key tag.Key) bool {
	// A plain text-frame identifier is a conformant 4-char ID that begins with T. Reuse
	// conformantFrameID for the length + character-class check (it guarantees len==4, so the
	// s[0] index is safe) so the allowed byte set stays defined in one place.
	s := string(key)
	return conformantFrameID(s) && s[0] == 'T'
}

// renderUnit renders the frame(s) for a render token from the edited tag set,
// returning an empty slice when the underlying field is now absent (the frame is
// dropped). It also reports whether a v2.3 NUL-separated multi-value was emitted.
func renderUnit(token string, edited tag.TagSet, version byte, opts WriteOpts, origLangs, origTXXXDesc map[string]string) ([]Frame, bool) {
	switch {
	case strings.HasPrefix(token, "TXXX\x00"):
		key := txxxKeyForToken(token[len("TXXX\x00"):])
		vals, ok := edited.Get(key)
		if !ok || len(vals) == 0 {
			return nil, false
		}
		// Use the preferred Picard spelling for an aliased key. For custom keys, reuse
		// the original TXXX description casing when available, matching the Vorbis rebuild.
		desc := mapping.ID3TXXXDesc(key)
		if orig, ok := origTXXXDesc[token]; ok && desc == string(key) {
			desc = orig
		}
		return renderByPolicy(version, "TXXX", vals, opts.Multi,
			func(v []string) []byte { return encodeUserText(version, desc, v) })
	case token == "UFID":
		id, ok := edited.First(tag.MBRecordingID)
		if !ok || id == "" {
			return nil, false
		}
		return []Frame{{ID: "UFID", Body: encodeUFID(musicBrainzOwner, id)}}, false
	case token == "COMM":
		vals, ok := edited.Get(tag.Comment)
		if !ok || len(vals) == 0 {
			return nil, false
		}
		lang := unitLang(origLangs, "COMM")
		return renderByPolicy(version, "COMM", vals, opts.Multi,
			func(v []string) []byte { return encodeComment(version, lang, "", v) })
	case token == "USLT":
		text, ok := edited.First(tag.Lyrics)
		if !ok {
			return nil, false
		}
		return []Frame{{ID: "USLT", Body: encodeLangText(version, unitLang(origLangs, "USLT"), "", text)}}, false
	case token == "TCON":
		vals, ok := edited.Get(tag.Genre)
		if !ok || len(vals) == 0 {
			return nil, false
		}
		return renderText(version, "TCON", genreValues(vals, version, opts.NumericGenre, opts.Multi), opts.Multi)
	case token == "TRCK":
		return renderNumTotal(version, "TRCK", edited, tag.TrackNumber, tag.TrackTotal)
	case token == "TPOS":
		return renderNumTotal(version, "TPOS", edited, tag.DiscNumber, tag.DiscTotal)
	case token == "TDRC":
		return renderDate(version, "TDRC", edited, tag.RecordingDate)
	case token == "TDRL":
		return renderDate(version, "TDRL", edited, tag.ReleaseDate)
	case token == "TDOR":
		return renderDate(version, "TDOR", edited, tag.OriginalDate)
	case token == "TYER":
		return renderDatePart(version, "TYER", edited, tag.RecordingDate, partYear)
	case token == "TDAT":
		return renderDatePart(version, "TDAT", edited, tag.RecordingDate, partDayMonth)
	case token == "TIME":
		return renderDatePart(version, "TIME", edited, tag.RecordingDate, partHourMin)
	case token == "TORY":
		return renderDatePart(version, "TORY", edited, tag.OriginalDate, partYear)
	default: // simple or pass-through text frame
		key, ok := mapping.ID3FrameKey(token)
		if !ok {
			key = tag.Key(token)
		}
		vals, has := edited.Get(key)
		if !has || len(vals) == 0 {
			return nil, false
		}
		return renderText(version, token, vals, opts.Multi)
	}
}

// unitLang returns the 3-byte language for a managed COMM/USLT frame: the
// original frame's language recovered in RebuildFrames, or "eng" for a newly
// added comment or lyric with no original frame. A garbage 3-byte language
// round-trips verbatim because langBytes neither normalizes nor rejects it.
func unitLang(origLangs map[string]string, token string) string {
	if l, ok := origLangs[token]; ok {
		return l
	}
	return "eng"
}

// txxxKeyForToken resolves a TXXX render token (an uppercased description) back
// to its canonical key.
func txxxKeyForToken(upperDesc string) tag.Key {
	if k, ok := mapping.ID3TXXXKey(upperDesc); ok {
		return k
	}
	return tag.Key(upperDesc)
}

// renderText renders a plain text frame under the multi-value policy. ID3v2.4
// always uses NUL-separated values.
func renderText(version byte, id string, values []string, pol core.ID3MultiValuePolicy) ([]Frame, bool) {
	return renderByPolicy(version, id, values, pol,
		func(v []string) []byte { return encodeTextFrame(chooseEncoding(version, v), v) })
}

// renderByPolicy renders a text-like ID3 frame under the configured multi-value
// policy. encodeBody owns the frame-specific body format, while this helper
// handles repeat-frame, slash-join, and NUL-separated v2.3 extension behavior.
// The bool return reports whether the v2.3 NUL-separated extension was used.
//
// ID3MultiRepeatFrame emits one frame per value, including TXXX and COMM. That
// can repeat a TXXX description or COMM language/description pair, which ID3
// readers commonly tolerate but the spec discourages. The policy is explicit
// caller opt-in, so it is applied uniformly.
func renderByPolicy(version byte, id string, values []string, pol core.ID3MultiValuePolicy,
	encodeBody func([]string) []byte) ([]Frame, bool) {
	if len(values) <= 1 || version >= 4 {
		return []Frame{{ID: id, Body: encodeBody(values)}}, false
	}
	switch pol {
	case core.ID3MultiRepeatFrame:
		var frames []Frame
		for _, v := range values {
			frames = append(frames, Frame{ID: id, Body: encodeBody([]string{v})})
		}
		return frames, false
	case core.ID3MultiSlash:
		return []Frame{{ID: id, Body: encodeBody([]string{strings.Join(values, " / ")})}}, false
	default: // ID3MultiNullSep - a v2.3 extension
		return []Frame{{ID: id, Body: encodeBody(values)}}, true
	}
}

// renderNumTotal renders a TRCK/TPOS frame from a number key and an optional
// total key as "n/total".
func renderNumTotal(version byte, id string, edited tag.TagSet, numKey, totKey tag.Key) ([]Frame, bool) {
	num, _ := edited.First(numKey)
	total, _ := edited.First(totKey)
	if num == "" && total == "" {
		return nil, false
	}
	value, _ := composeNumTotal(numKey, num, total)
	enc := chooseEncoding(version, []string{value})
	return []Frame{{ID: id, Body: encodeTextFrame(enc, []string{value})}}, false
}

// composeNumTotal is the single compose-or-drop decision renderNumTotal (what to write) and
// detectDroppedTotals (whether to warn) share, so the write and its value-dropped warning cannot
// drift. It returns the TRCK/TPOS number field to write and whether a canonical total was thereby
// dropped. It composes "n/total" only when the result is a valid numeric value the reader will split
// back; a non-numeric number (e.g. "A1/12") otherwise reads as one literal value with the total
// merged in and lost, so the number is preserved verbatim instead. An explicit canonical total wins
// over one embedded in the number ("5/12" plus TRACKTOTAL never composes "5/12/20"); SplitNumberTotal
// keeps exact digit strings, including leading zeros, unlike tag.ParseNumPair. totalDropped is true
// only when a canonical total (from the total key, not an embedded one) is made unrepresentable.
func composeNumTotal(numKey tag.Key, num, canonicalTotal string) (value string, totalDropped bool) {
	// A non-empty number field that is not itself a valid numeric value ("1/2/3", "A1/12")
	// cannot be recomposed as "n/total" without silently dropping the extra text - splitting
	// "1/2/3" to nPart "1" and re-joining "1/<total>" would discard "/2/3" with no warning.
	// Preserve it verbatim (matching both the no-total path and the pre-parse "kept as text"
	// note) and mark any canonical total dropped, so a value-dropped warning fires instead of a
	// silent loss. The empty/whitespace check is why a lone TRACKTOTAL still round-trips: an
	// empty number is cleanly representable as "/total" (composed below), not dropped here.
	if strings.TrimSpace(num) != "" && !tag.ValidNumericValue(numKey, num) {
		return num, canonicalTotal != ""
	}
	nPart, embeddedTotal := tag.SplitNumberTotal(num)
	finalTotal := canonicalTotal
	if finalTotal == "" {
		finalTotal = embeddedTotal
	}
	if finalTotal == "" {
		return nPart, false
	}
	if composed := nPart + "/" + finalTotal; tag.ValidNumericValue(numKey, composed) {
		return composed, false
	}
	// Compose failed: preserve the number verbatim (a literal "A1/12" round-trips as-is). A canonical
	// total is thereby dropped; an embedded one stays inside the preserved num.
	return num, canonicalTotal != ""
}

// detectDroppedTotals finds the TRACKTOTAL/DISCTOTAL keys whose canonical value composeNumTotal
// cannot fit into a valid "n/total" frame, so the caller can warn rather than drop the total
// silently. Scoped to a pair the edit touched: an untouched pair keeps its original frame, and an
// embedded total in the number itself ("A1/12" alone) is preserved verbatim and is not a drop.
func detectDroppedTotals(changed map[tag.Key]bool, edited tag.TagSet) []tag.Key {
	var dropped []tag.Key
	for _, p := range []struct{ numKey, totKey tag.Key }{
		{tag.TrackNumber, tag.TrackTotal},
		{tag.DiscNumber, tag.DiscTotal},
	} {
		if !changed[p.numKey] && !changed[p.totKey] {
			continue // neither field edited; the original frame is preserved, nothing newly dropped
		}
		num, _ := edited.First(p.numKey)
		total, _ := edited.First(p.totKey)
		if _, totalDropped := composeNumTotal(p.numKey, num, total); totalDropped {
			dropped = append(dropped, p.totKey)
		}
	}
	return dropped
}

// TransferClassifier grades the fields whose ID3 transfer fate the format-level capability
// cannot express: a TRACKTOTAL or DISCTOTAL whose sibling number is non-numeric. ID3 stores
// a total only as the second half of a "number/total" frame, so when the number cannot form
// a valid numeric value the total has nowhere to go and the writer drops it (see
// [AppendRebuildWarnings]). A copy that carries such a total - a TRACKTOTAL beside a
// non-numeric TRACKNUMBER - must report it Dropped rather than a clean carry. It calls the
// same composeNumTotal decision the writer uses, so the copy report and the write drop
// cannot drift; composeNumTotal composes a lone total cleanly ("/total"), so a standalone
// TRACKTOTAL is not falsely dropped. The four ID3-backed codecs (MP3, AAC, AIFF, WAV) share
// it; every other field is left to the format-level grade. It is a plain
// [core.FieldClassifier] (registered by value, not called), so it captures nothing and
// allocates no closure.
func TransferClassifier(key tag.Key, _ []string, all tag.TagSet) (core.Disposition, string, bool) {
	var numKey tag.Key
	switch key {
	case tag.TrackTotal:
		numKey = tag.TrackNumber
	case tag.DiscTotal:
		numKey = tag.DiscNumber
	default:
		return core.Carried, "", false
	}
	num, _ := all.First(numKey)
	total, _ := all.First(key)
	if _, dropped := composeNumTotal(numKey, num, total); dropped {
		return core.Dropped, "the number it attaches to is non-numeric, so ID3 cannot store this total (a total is written only as the second half of \"number/total\")", true
	}
	return core.Carried, "", false
}

// renderDate renders a v2.4 date frame directly from an ISO date key.
func renderDate(version byte, id string, edited tag.TagSet, key tag.Key) ([]Frame, bool) {
	v, ok := edited.First(key)
	if !ok || v == "" {
		return nil, false
	}
	enc := chooseEncoding(version, []string{v})
	return []Frame{{ID: id, Body: encodeTextFrame(enc, []string{v})}}, false
}

type datePart uint8

const (
	partYear datePart = iota
	partDayMonth
	partHourMin
)

// detectNumericGenres returns the GENRE values this edit set that are a bare integer
// naming a standard genre by index (e.g. "17" -> "Rock"). Written verbatim to TCON, such a
// value is resolved back to the genre NAME by the read path, so GENRE=17 round-trips as
// "Rock" - a surprising change the caller surfaces (see AppendRebuildWarnings), suppressed
// where a native container keeps the literal number. Only a value the edit actually changed
// is reported, so an untouched pre-existing numeric genre does not warn.
//
// Three residuals of this handling are known and intentional, so a QA pass should not re-flag
// them: (a) diff and copy treat a file holding "17" and one holding "Rock" as different, because
// the read projection resolves "17" to "Rock" while the on-disk bytes differ; (b) on ID3v2.3 a
// bare "17" is free text, so resolving it to "Rock" is non-conformant to what WaxLabel itself
// wrote - the warning makes the surprise visible rather than rewriting the bytes; and (c) a
// parenthesized "(17)" is escaped to "((17)" on disk and reads back as the literal "(17)", never
// resolved (which is why isNumericGenreRef exempts a leading "(").
func detectNumericGenres(changed map[tag.Key]bool, edited tag.TagSet) []string {
	if !changed[tag.Genre] {
		return nil
	}
	vals, _ := edited.Get(tag.Genre)
	var out []string
	seen := map[string]bool{}
	for _, v := range vals {
		// De-duplicate by value so a repeated reference (GENRE=17,17) warns once, not once
		// per occurrence - and so the same single warning is what DowngradeNoOp carries onto
		// a no-op report.
		if isNumericGenreRef(v) && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// isNumericGenreRef reports whether v reads back as a different genre name after write.
// Values beginning with "(" are escaped by genreValues and round-trip verbatim, so the
// remaining asymmetry is a bare reference the reader resolves to a name, such as
// "17" to "Rock", "RX" to "Remix", or "CR" to "Cover".
// The final resolver is the same one the parser uses.
func isNumericGenreRef(v string) bool {
	if strings.HasPrefix(v, "(") {
		return false // escaped by genreValues, so it round-trips verbatim
	}
	_, numeric := resolveGenres(v)
	return numeric
}

// detectDroppedDates finds the year-anchored date keys whose edited value cannot be
// represented at all on a v2.3 tag, so the caller can warn rather than drop silently.
// TYER (RecordingDate) and TORY (OriginalDate) need a numeric year: a touched value
// with none (e.g. "Unknown Date") renders no frame, a silent loss. ReleaseDate is
// excluded - on v2.3 it maps to TXXX:RELEASEDATE, which stores the string verbatim,
// so it never drops. v2.4 stores the full string (TDRC/TDOR) and returns nothing here.
//
// The check is per key, not per render token: a single RecordingDate dirties three
// tokens (TYER/TDAT/TIME), and a common "2021"/"2021-05" legitimately renders only
// TYER while TDAT/TIME yield nothing - so a per-token "no frame" test would falsely
// flag a perfectly stored date. A date is dropped iff the key has a non-empty value
// but no extractable year, which also preserves a shaped-but-invalid "2021-13-45"
// (its year extracts, so only the day/month is lost, not the whole value).
func detectDroppedDates(changed map[tag.Key]bool, edited tag.TagSet, version byte) []tag.Key {
	if version >= 4 {
		return nil
	}
	var dropped []tag.Key
	for _, k := range []tag.Key{tag.RecordingDate, tag.OriginalDate} {
		if !changed[k] {
			continue
		}
		if v, _ := edited.First(k); v23DateDropped(v) {
			dropped = append(dropped, k)
		}
	}
	return dropped
}

// v23DateDropped reports whether a v2.3 tag drops the date value v entirely: a non-empty
// value with no valid 4-digit year renders no TYER/TORY frame (both fields need one, and a
// 5-digit or compact non-canonical form yields none). It is the single predicate the write
// (detectDroppedDates) and the transfer capability's value-drop grading share, so the transfer
// report cannot drift from the write.
func v23DateDropped(v string) bool {
	return v != "" && extractDatePart(v, partYear) == ""
}

// detectReducedDates finds RecordingDate edits whose finer precision a v2.3 tag drops:
// a month with no full date, or an hour with no minute. OriginalDate is intentionally
// excluded because the capability path reports its v2.3 TORY reduction and uses different
// cross-container suppression. Listing it here too would double-warn.
func detectReducedDates(changed map[tag.Key]bool, edited tag.TagSet, version byte) []ReducedDate {
	if version >= 4 {
		return nil
	}
	var reduced []ReducedDate
	k := tag.RecordingDate
	if changed[k] {
		if v, _ := edited.First(k); reducesDatePrecision(v) {
			reduced = append(reduced, ReducedDate{Key: k, Value: v})
		}
	}
	return reduced
}

// reducesDatePrecision reports whether a v2.3 tag would store iso with less precision
// than it carries. v2.3 splits a date-time across TYER (year), TDAT (DDMM, needs a full
// YYYY-MM-DD) and TIME (HHMM, needs YYYY-MM-DDTHH:MM), each all-or-nothing. A value
// carrying a component the rendered frames cannot capture loses it:
//   - a month with no full date ("2021-03", non-canonical "2021-3"/"2021-03-1") -> only TYER,
//     month/day dropped;
//   - an hour with no minute ("2021-03-15T10") -> TYER+TDAT only, the hour dropped;
//   - seconds past a full minute ("2021-03-15T10:30:45") -> TIME stores only HHMM, the
//     seconds dropped.
//
// A bare year, a full date, or a date-time to the minute render losslessly and are excluded.
// The tool stores values verbatim (no normalization), so the non-canonical forms are reachable
// too. A value with no extractable year drops entirely and is handled by detectDroppedDates
// instead.
func reducesDatePrecision(iso string) bool {
	if extractDatePart(iso, partYear) == "" {
		return false
	}
	monthLost := hasSubYearPart(iso) && extractDatePart(iso, partDayMonth) == ""
	hourLost := hasSubDayPart(iso) && extractDatePart(iso, partHourMin) == ""
	// HHMM is stored (a full minute renders) yet the value carries seconds: v2.3 TIME has
	// no seconds field, so they are dropped.
	secondsLost := hasSubMinutePart(iso) && extractDatePart(iso, partHourMin) != ""
	return monthLost || hourLost || secondsLost
}

// hasSubYearPart reports whether iso carries content beyond its 4-digit year, in a form whose
// year is still extractable: "2021-03" and "2021-03-15" do, but "2021" does not - and neither do
// non-canonical compact/dotted forms ("20210503", "2021.05.03"), whose year extractDatePart no
// longer accepts, so they carry no valid year at all and route to dropped rather than reducing to
// one. ID3v2.3 year-only fields truncate a dash-form sub-year value to the year.
func hasSubYearPart(iso string) bool {
	return len(iso) > 4 && extractDatePart(iso, partYear) != ""
}

// hasSubDayPart reports whether iso carries an hour-or-finer component after a full date: a
// 'T' (or space) date-time separator then at least one digit past the 10-char YYYY-MM-DD (so
// "2021-03-15T10" does, but a bare date does not). It separates a reducible partial
// date-time from a lossless full date.
func hasSubDayPart(iso string) bool {
	return len(iso) >= 12 && (iso[10] == 'T' || iso[10] == ' ') && iso[11] >= '0' && iso[11] <= '9'
}

// hasSubMinutePart reports whether iso carries a seconds-or-finer component after a full
// minute: the ':ss' at the YYYY-MM-DDThh:mm boundary - a ':' at index 16 then a digit at 17
// (so "2021-03-15T10:30:45" does, but a minute-precision "2021-03-15T10:30", or one with only
// a trailing zone like "2021-03-15T10:30+05:00", does not). It separates a value v2.3's HHMM
// TIME stores losslessly from one whose seconds it drops. The trailing-digit check mirrors
// hasSubYearPart/hasSubDayPart and avoids flagging a malformed trailing-colon value.
func hasSubMinutePart(iso string) bool {
	return len(iso) >= 18 && iso[16] == ':' && iso[17] >= '0' && iso[17] <= '9'
}

// AppendRebuildWarnings appends warnings for losses found while rebuilding ID3 frames:
// dates that render no v2.3 frame at all, dates whose v2.3 TDAT/TIME rendering drops
// precision, and malformed pictures dropped during a picture edit. Date warnings are
// suppressed when the re-projected output still retains the attempted value in another
// container, such as WAV's LIST/INFO ICRD. The keyed warnings let --strict name the field
// and keep the four ID3-backed codecs' wording and suppression rules aligned.
func AppendRebuildWarnings(ws []core.Warning, info RebuildInfo, retained tag.TagSet) []core.Warning {
	for _, k := range info.DroppedDates {
		if v, _ := retained.First(k); v != "" {
			continue // retained in another container (e.g. WAV's ICRD); not actually dropped
		}
		ws = core.WarnKeyed(ws, core.WarnValueDropped,
			fmt.Sprintf("%s value cannot be represented in ID3v2.3 (it has no valid 4-digit year) and was dropped", k), k)
	}
	for _, k := range info.DroppedTotals {
		if v, _ := retained.First(k); v != "" {
			continue // retained in another container (e.g. WAV's LIST/INFO); not actually dropped
		}
		ws = core.WarnKeyed(ws, core.WarnValueDropped,
			fmt.Sprintf("%s cannot be represented in ID3 because the number it attaches to is non-numeric (the total is stored only as the second half of \"number/total\") and was dropped", k), k)
	}
	for _, rd := range info.ReducedDates {
		// Suppress only when another container still carries the attempted precision:
		// a retained "2021" must not suppress an attempted "2021-03".
		if v, _ := retained.First(rd.Key); v == rd.Value {
			continue
		}
		ws = core.WarnKeyed(ws, core.WarnValueReduced,
			fmt.Sprintf("%s value %q carries finer precision than ID3v2.3 date frames can store (TDAT needs a full day, TIME a full minute) and was reduced", rd.Key, rd.Value), rd.Key)
	}
	for _, gv := range info.NumericGenres {
		// Suppress only where a native container still carries the literal number: on MP3/AAC (and
		// AIFF, whose genre lives only in its ID3 chunk) the retained GENRE reads back as the genre
		// name, so gv is absent and the warning fires; only WAV keeps "17" verbatim in its LIST/INFO
		// IGNR slot, so it is retained and the round-trip did not change - no warning. This mirrors
		// the date-warning suppression and keeps the write-time note aligned with the read-time one.
		if genres, _ := retained.Get(tag.Genre); slices.Contains(genres, gv) {
			continue
		}
		ws = core.WarnKeyed(ws, core.WarnNumericGenre,
			fmt.Sprintf("GENRE %q is a numeric reference that reads back as its genre name on ID3-based formats", gv), tag.Genre)
	}
	if info.HasDroppedMalformedPicture {
		ws = core.Warn(ws, core.WarnInvalidPicture,
			"a malformed embedded picture could not be decoded and was dropped during a picture edit")
	}
	if info.ChapterOverflow {
		ws = core.Warn(ws, core.WarnChapterStartOverflow,
			"a chapter time exceeded the CHAP frame's 32-bit millisecond field (~49.7 days) and was clamped")
	}
	if info.DroppedChapterSubframes {
		ws = core.Warn(ws, core.WarnChapterMetadataDropped,
			"a per-chapter subframe other than the title (e.g. an image) could not be represented and was dropped")
	}
	if info.SyncedLyricsOverflow {
		ws = core.Warn(ws, core.WarnSyncedLyricsTimestampClamped,
			"a synced-lyric timestamp exceeded the SYLT frame's 32-bit millisecond field (~49.7 days) and was clamped")
	}
	if info.SyncedLyricsLangUndefined {
		ws = core.Warn(ws, core.WarnSyncedLyricsMetadataDropped,
			"the synced-lyrics language \"xxx\" is the ID3 \"undefined\" marker, so it is stored but reads back with no language")
	}
	return ws
}

// RebuildError returns a hard error for a rebuild loss that must fail the write rather than only
// warn: a synced-lyrics line or descriptor carrying an embedded NUL, which the NUL-terminated SYLT
// field would silently truncate. It returns nil when no such loss occurred. Each ID3-backed codec
// calls it right where it calls CheckSize, so one sentinel and one message cover MP3, AAC, WAV, and
// AIFF - the same waxerr.ErrInvalidData a faithful copy already produces for such text.
func RebuildError(info RebuildInfo) error {
	if info.SyncedLyricsInvalidNUL {
		return fmt.Errorf("%w: synced-lyrics line contains a NUL byte", waxerr.ErrInvalidData)
	}
	return nil
}

// CarryProjectionWarnings returns warnings for a post-write MP3/AAC document. Front-tag codecs
// carry source parse warnings forward because buildResult cannot recompute the audio and
// container warnings (trailing-id3v1, legacy-ape, ...). But an edit can resolve a warning the
// ID3 projection produced, so any such warning the rewritten tag no longer projects must be
// dropped, or the returned document would disagree with a fresh parse of the written bytes.
// newTagWarnings is Project(newTag).Warnings; each reconciled code is stripped from the carried
// set only when the new projection lacks it, preserving the remaining warning order:
//   - chapters-flattened: a nested CTOC rewritten as a flat list no longer flattens;
//   - invalid-picture: a picture edit drops a malformed APIC (HasDroppedMalformedPicture), so
//     the malformed cover the source-parse warning described is gone from the output.
func CarryProjectionWarnings(sourceWarnings, newTagWarnings []core.Warning) []core.Warning {
	out := core.CloneWarnings(sourceWarnings)
	for _, code := range []core.WarningCode{core.WarnChaptersFlattened, core.WarnInvalidPicture} {
		if len(core.WarningsWithCode(newTagWarnings, code)) == 0 {
			out = core.WarningsWithoutCode(out, code)
		}
	}
	return out
}

// PerFieldCapabilities builds the per-key capability overrides shared by every
// ID3-backed codec (MP3/AAC/AIFF/WAV). The two value-mutating ID3 cases are declared
// once here rather than copied into each codec:
//
//   - ORIGINALDATE is AccessPartial when the codec writes ID3v2.3 (writeVersion == 3),
//     whose TORY frame keeps only the year. This drives both the transfer grade and the
//     value-reduced edit warning (editor.appendValueReducedWarnings), the latter confirming
//     the value actually changed before warning.
//   - GENRE is AccessPartial when --numeric-genre is set and the ID3 tag is the authoritative
//     genre store for this codec (genreViaID3). That holds always for MP3/AAC/AIFF (no native
//     genre slot wins over ID3), but for WAV only when an id3 chunk is present - a bare WAV's
//     native LIST/INFO IGNR keeps the genre as text, losslessly.
//
// Returns nil when neither applies, so a codec with a lossless write passes no overrides.
func PerFieldCapabilities(writeVersion byte, numericGenre, genreViaID3 bool) map[tag.Key]core.Capability {
	var perField map[tag.Key]core.Capability
	add := func(k tag.Key, c core.Capability) {
		if perField == nil {
			perField = map[tag.Key]core.Capability{}
		}
		perField[k] = c
	}
	if writeVersion == 3 {
		// Grade v2.3 date transfers by value. TORY is year-only; TYER+TDAT+TIME keeps date
		// values to the minute. Values with no numeric year render no frame, so the drop
		// predicate matches the write path.
		add(tag.OriginalDate, core.WithValueDrop(core.WithValueReduction(core.OriginalDateV23Capability(), reducesToYear), v23DateDropped))
		add(tag.RecordingDate, core.WithValueDrop(core.WithValueReduction(core.RecordingDateV23Capability(), reducesDatePrecision), v23DateDropped))
	}
	if numericGenre && genreViaID3 {
		add(tag.Genre, core.NumericGenreCapability("numeric ID3 TCON reference"))
	}
	return perField
}

// reducesToYear reports whether storing iso in a year-only field (ID3v2.3 TORY) loses
// information. Anything that is not exactly a bare year, including a fuller date or a
// value with no parseable year, is reduced or dropped. Distinct from reducesDatePrecision,
// which treats a full YYYY-MM-DD as lossless and would wrongly grade it Carried for TORY.
func reducesToYear(iso string) bool {
	return extractDatePart(iso, partYear) == "" || hasSubYearPart(iso)
}

// renderDatePart renders a v2.3 date component (TYER/TDAT/TIME/TORY) extracted
// from an ISO date key.
func renderDatePart(version byte, id string, edited tag.TagSet, key tag.Key, part datePart) ([]Frame, bool) {
	iso, ok := edited.First(key)
	if !ok {
		return nil, false
	}
	v := extractDatePart(iso, part)
	if v == "" {
		return nil, false
	}
	enc := chooseEncoding(version, []string{v})
	return []Frame{{ID: id, Body: encodeTextFrame(enc, []string{v})}}, false
}

// extractDatePart pulls a component out of an ISO-8601 date "YYYY[-MM-DD[THH:MM]]". The year
// must be exactly 4 digits bounded by end-of-string or a '-' separator, so a malformed 5-digit
// year ("10000") or a non-canonical compact/dotted form ("20210503", "2021.05") is not silently
// truncated to a valid-but-wrong "1000"/"2021"; such a value yields no year and routes to
// dropped (v23DateDropped) rather than corrupted.
func extractDatePart(iso string, part datePart) string {
	switch part {
	case partYear:
		if len(iso) >= 4 && allDigits(iso[:4]) && (len(iso) == 4 || iso[4] == '-') {
			return iso[:4]
		}
	case partDayMonth:
		if len(iso) >= 10 && iso[4] == '-' && iso[7] == '-' {
			return iso[8:10] + iso[5:7] // DDMM
		}
	case partHourMin:
		// Accept either ISO date-time separator: 'T' (the canonical form) or a space (a
		// common variant). hasSubDayPart accepts both when deciding a value carries a time,
		// so this must too - else "2021-03-15 10:30" would be judged reducible yet yield no
		// TIME frame, spuriously firing [value-reduced] while the 'T' form keeps the time.
		if len(iso) >= 16 && (iso[10] == 'T' || iso[10] == ' ') && iso[13] == ':' {
			return iso[11:13] + iso[14:16] // HHMM
		}
	}
	return ""
}

// genreValues converts standard genre names to numeric references when WithNumericGenre is
// set; other names pass through. Literal names beginning with "(" are escaped at positions
// where the reader would parse "(ref)" syntax. Generated numeric and special references
// are left unescaped so the reader can resolve them.
//
// A multi-value ID3MultiSlash join skips numeric conversion because the values become one
// "a / b / c" frame value. A generated reference in the middle would be parsed back as a
// reference plus a slash-prefixed refinement instead of the intended joined value.
func genreValues(names []string, version byte, numeric bool, pol core.ID3MultiValuePolicy) []string {
	if numeric && pol == core.ID3MultiSlash && len(names) > 1 {
		numeric = false
	}
	out := make([]string, len(names))
	for i, n := range names {
		if numeric {
			if idx := genreIndex(n); idx >= 0 {
				if version >= 4 {
					out[i] = strconv.Itoa(idx)
				} else {
					out[i] = "(" + strconv.Itoa(idx) + ")"
				}
				continue // a generated reference is never escaped
			}
		}
		// Escape literal names beginning with "(" where the reader would parse from the
		// start of this value.
		if strings.HasPrefix(n, "(") && genreParseLeading(i, len(names), version, pol) {
			out[i] = "(" + n
		} else {
			out[i] = n
		}
	}
	return out
}

// genreParseLeading reports whether value i lands where resolveGenres parses from the
// start of the value. Only those positions need "(" escaping.
func genreParseLeading(i, n int, version byte, pol core.ID3MultiValuePolicy) bool {
	if n <= 1 || version >= 4 {
		return true
	}
	if pol == core.ID3MultiSlash {
		return i == 0 // joined into one frame value; only the first is parse-leading
	}
	return true // ID3MultiNullSep, ID3MultiRepeatFrame: each value is parse-leading
}

// encodeUserText renders a TXXX body: encoding, description, then the value(s).
func encodeUserText(version byte, desc string, values []string) []byte {
	enc := chooseEncoding(version, append([]string{desc}, values...))
	return encodeDescValues(enc, "", desc, values)
}

// encodeUFID renders a UFID body: the owner identifier, a NUL, then the raw id.
func encodeUFID(owner, id string) []byte {
	out := append(encodeLatin1(owner), 0)
	return append(out, []byte(id)...)
}

// encodeComment renders a COMM body: encoding, language, description, value(s).
func encodeComment(version byte, lang, desc string, values []string) []byte {
	enc := chooseEncoding(version, append([]string{desc}, values...))
	return encodeDescValues(enc, lang, desc, values)
}

// encodeLangText renders a USLT body: encoding, language, descriptor, text.
func encodeLangText(version byte, lang, desc, text string) []byte {
	enc := chooseEncoding(version, []string{desc, text})
	return encodeDescValues(enc, lang, desc, []string{text})
}

// encodeDescValues builds the common frame body shared by TXXX, COMM, and USLT:
// the encoding byte, an optional 3-byte language code, a terminated description,
// then the values separated by the encoding's terminator.
func encodeDescValues(enc byte, lang, desc string, values []string) []byte {
	out := []byte{enc}
	if lang != "" {
		out = append(out, langBytes(lang)...)
	}
	out = append(out, encodeString(enc, desc)...)
	out = append(out, term(enc)...)
	return appendValues(out, enc, values)
}

// appendValues appends values to out, separated by the encoding's terminator
// (no trailing terminator). Shared by the description frames and the plain
// text-frame encoder.
func appendValues(out []byte, enc byte, values []string) []byte {
	t := term(enc)
	for i, v := range values {
		if i > 0 {
			out = append(out, t...)
		}
		out = append(out, encodeString(enc, v)...)
	}
	return out
}

// langBytes returns a 3-byte language code, padding or truncating to fit.
func langBytes(lang string) []byte {
	b := []byte(lang)
	for len(b) < 3 {
		b = append(b, 'X')
	}
	return b[:3]
}

// diffKeys returns the canonical keys whose values differ between base and
// edited.
func diffKeys(base, edited tag.TagSet) map[tag.Key]bool {
	changed := map[tag.Key]bool{}
	for _, k := range base.Keys() {
		bv, _ := base.Get(k)
		ev, has := edited.Get(k)
		if !has || !slices.Equal(bv, ev) {
			changed[k] = true
		}
	}
	for _, k := range edited.Keys() {
		if !base.Has(k) {
			changed[k] = true
		}
	}
	return changed
}
