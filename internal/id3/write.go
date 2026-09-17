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

// StructuredEdit carries non-tag structures a frame rebuild owns. Dropped and
// re-emitted only when the matching change flag is set; otherwise source frames stay.

type StructuredEdit struct {
	Pictures            []core.Picture
	PicturesChanged     bool
	Chapters            []core.Chapter
	ChaptersChanged     bool
	SyncedLyrics        []core.SyncedLyrics
	SyncedLyricsChanged bool
	// Carried: faithful cross-format carry (not user-authored). Skips synced-lyrics lang
	// fallback and COMM description keep from the destination.

	Carried bool
	// SyncedLyricsCleared: clear-then-author; skip lang/descriptor fallback (start fresh).

	SyncedLyricsCleared bool
	// MediaDuration: bounds open trailing chapter (End==0) at CHAP write; 0 keeps sentinel.

	MediaDuration time.Duration
}

// RebuildInfo is rebuild facts the caller surfaces in the write report.

type RebuildInfo struct {
	// UsedV23Multi: v2.3 written with NUL-separated multi-values (nonstandard).

	UsedV23Multi bool
	// DroppedDates: v2.3 date keys with no extractable year (no frame written).

	DroppedDates []tag.Key
	// CoercedDates: v2.3 date spelling change only (space→T on recompose). RecordingDate.

	CoercedDates []ReducedDate
	// ReducedDates: v2.3 precision loss (RecordingDate). OriginalDate uses AccessPartial path.

	ReducedDates []ReducedDate
	// HasDroppedMalformedPicture: picture edit dropped undecodable APIC(s).

	HasDroppedMalformedPicture bool
	// NumericGenres: GENRE set to a bare index (reads back as the name on pure ID3).

	NumericGenres []string
	// DroppedTotals: totals that cannot join a valid n/total frame (non-numeric number).

	DroppedTotals []tag.Key
	// DroppedTrailingValues: trailing empties NUL frames cannot store (v2.4 / v2.3+nullsep).

	DroppedTrailingValues []tag.Key
	// DroppedEmptyValues: all-empty keys that got no frame (read off rendered frames).

	DroppedEmptyValues []tag.Key
	// DroppedInvolvedEmpties: roles with empties TIPL/IPLS cannot store (nameless pairs).

	DroppedInvolvedEmpties []tag.Key
	// ChapterOverflow: chapter start/end clamped to CHAP 32-bit ms (~49.7 days).

	ChapterOverflow bool
	// DroppedChapterSubframes: non-title CHAP subframes lost on chapter edit.

	DroppedChapterSubframes bool
	// SyncedLyricsOverflow: SYLT timestamp clamped to 32-bit ms (~49.7 days).

	SyncedLyricsOverflow bool
	// SyncedLyricsInvalidNUL: embedded NUL in SYLT text/descriptor (hard error).

	SyncedLyricsInvalidNUL bool
	// CommentDescriptionDropped: could not keep COMM description (multi-frame/value or carry).

	CommentDescriptionDropped bool
	// SyncedLyricsLangUndefined: authored lang normalizes to "xxx" (stored; reads empty).

	SyncedLyricsLangUndefined bool
}

// ReducedDate pairs a date key with the value an edit attempted to store before a
// lower-fidelity v2.3 rendering reduced its precision.
type ReducedDate struct {
	Key   tag.Key
	Value string
}

// RebuildFrames builds the new frame list: unchanged/unmodelled frames stay;
// only frames for changed keys (and pictures/chapters/synced lyrics) re-render.

func RebuildFrames(orig []Frame, base, edited tag.TagSet, version byte,
	se StructuredEdit, opts WriteOpts) ([]Frame, RebuildInfo) {

	picturesChanged := se.PicturesChanged
	changed := diffKeys(base, edited)
	// produced records which render tokens actually emitted a frame, so the all-empty drop is
	// read off the render rather than re-derived per key.
	produced := map[string]bool{}
	dirty := map[string]bool{}
	for k := range changed {
		for _, rid := range keyRenderIDs(k, version) {
			dirty[rid] = true
		}
	}
	// A write-encoding option changes how a value is stored, not the value itself, so
	// diffKeys sees nothing and the frame would otherwise be preserved verbatim. Route
	// through keyRenderIDs so a key's render tokens stay defined in one place - not for v2.2
	// coverage: decodeFrame upgrades TCO to TCON through the v2.2 table, so orig only ever
	// holds TCON.
	if encodingRewriteNeeded(orig, version, edited, opts) {
		for _, rid := range keyRenderIDs(tag.Genre, version) {
			dirty[rid] = true
		}
	}

	// The read path does not expose the COMM/USLT 3-byte language or a COMM description, and
	// stores a TXXX description under its uppercased canonical key, so recover them from the
	// original frames when rewriting. Re-rendered comment and lyric frames keep their
	// language, a single re-rendered comment keeps its description, and custom TXXX frames
	// keep their original description casing. There can be several managed COMM frames
	// (every non-technical one is managed), so each recovery takes the first.
	origLangs := map[string]string{}          // "COMM"/"USLT"/"SYLT" -> 3-byte language
	var origSyltDesc string                   // first projecting lyrics SYLT's content descriptor (authored-set fallback)
	origTXXXDesc := map[string]string{}       // TXXX render token -> original description (verbatim casing)
	var origInvolved []involvedPerson         // well-formed TIPL/IPLS involvements we do not model, preserved on write
	seenInvolved := map[involvedPerson]bool{} // dedup across repeated or multiple involved-people frames
	var managedComments int                   // managed COMM frames, which a Comment edit merges into one
	var firstCommentDesc string               // first managed COMM's description, kept when the merge is unambiguous
	var anyCommentDesc bool                   // any managed COMM carries a description worth preserving
	for _, f := range orig {
		switch f.ID {
		case "COMM", "USLT":
			rid, managed := frameRenderID(f)
			if !managed {
				break
			}
			// First wins, matching the SYLT branch below. Without the guard the LAST managed
			// frame's language won, which was latent while only one COMM could be managed and
			// is reachable now that described ones are.
			if _, seen := origLangs[rid]; !seen && len(f.Body) >= 4 {
				origLangs[rid] = string(f.Body[1:4])
			}
			if f.ID == "COMM" {
				managedComments++
				if desc, _, ok := decodeCommentFrame(f.Body); ok && desc != "" {
					anyCommentDesc = true
					if managedComments == 1 {
						firstCommentDesc = desc
					}
				}
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
		case "TIPL", "IPLS":
			// Recover well-formed involvements we do not model (mastering, recording, ...) so a
			// role edit that re-renders this frame does not silently drop them. Fold case on the
			// known-check via the same lookup the read path uses: a capitalized "Producer" already
			// re-emits from `edited`, so exact-string matching would misclassify it as unknown and
			// duplicate it. Dedup by exact (function, name) so repeated or multiple frames (the scan
			// walks them all) do not write the same pair twice. The write version always equals the
			// source version, so a preserved unknown stays in the frame it came from (v2.3 IPLS
			// legitimately holds instrument credits); splitting v2.4 TMCL instrument credits out of
			// TIPL is a separate, deferred feature (see the package's TMCL scope note).
			for _, p := range decodeInvolvedPeople(f.Body) {
				if _, known := mapping.ID3InvolvedRoleKey(p.Function); known {
					continue
				}
				if seenInvolved[p] {
					continue
				}
				seenInvolved[p] = true
				origInvolved = append(origInvolved, p)
			}
		}
	}

	var out []Frame
	var info RebuildInfo
	emitted := map[string]bool{}
	firstAPIC := -1

	// A Comment edit merges every managed COMM into one frame, which can only keep one
	// description. Keep it when the merge is unambiguous - one source frame, one edited
	// value - and otherwise flatten and let the caller warn. A cleared Comment is not a
	// description drop: the value and its description go together, deliberately.
	//
	// A faithful carry never keeps it. The description labels the comment the DESTINATION
	// had; a value arriving from another file is not the thing it labels, so stamping the
	// destination's "Ripped by EAC" onto the source's comment would assert something false.
	// An authored edit is the opposite case: the user is editing that comment's own text,
	// and the description is its field name.
	keepCommentDesc := ""
	editedComments, _ := edited.Get(tag.Comment)
	if managedComments == 1 && len(editedComments) == 1 && !se.Carried {
		keepCommentDesc = firstCommentDesc
	}
	if dirty["COMM"] && anyCommentDesc && len(editedComments) > 0 && keepCommentDesc == "" {
		info.CommentDescriptionDropped = true
	}

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
				frames, v23multi := renderUnit(rid, edited, version, opts, origLangs, origTXXXDesc, origInvolved, keepCommentDesc)
				out = append(out, frames...)
				info.UsedV23Multi = info.UsedV23Multi || v23multi
				emitted[rid] = true
				produced[rid] = len(frames) > 0
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
		frames, v23multi := renderUnit(rid, edited, version, opts, origLangs, origTXXXDesc, origInvolved, keepCommentDesc)
		out = append(out, frames...)
		info.UsedV23Multi = info.UsedV23Multi || v23multi
		emitted[rid] = true
		produced[rid] = len(frames) > 0
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
		if se.Carried || se.SyncedLyricsCleared {
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

	info.DroppedDates, info.ReducedDates, info.CoercedDates = detectDateFates(changed, edited, version)
	info.NumericGenres = detectNumericGenres(changed, edited)
	info.DroppedTotals = detectDroppedTotals(changed, edited)
	info.DroppedTrailingValues = detectDroppedTrailingValues(changed, edited, version, opts.Multi)
	info.DroppedEmptyValues = detectDroppedEmptyValues(changed, edited, produced, version, info)
	info.DroppedInvolvedEmpties = detectDroppedInvolvedEmpties(changed, edited)
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
		Tag:     srcTag.WithFrames(newFrames, padSize),
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
// (modelled by the canonical projection, hence re-rendered when its field changes).
// Unmanaged frames - URLs, POPM, PRIV, machine-described comments, described lyrics,
// non-MusicBrainz UFIDs, opaque frames - are always preserved verbatim.
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
		// Managed exactly when projected (internal/id3/project.go), through the same
		// predicate: a machine-described COMM (iTunNORM, ReplayGain) is neither read as
		// COMMENT nor touched on write, while every other COMM - described or not - is both.
		// Projecting without managing is the combination that must not ship: an edit would
		// mark the unit dirty, preserve the described frame verbatim, and then append a
		// second fresh COMM, so a re-parse would read two comments where the plan promised
		// one.
		//
		// The flat Comment model has no per-comment language, so editing Comment merges
		// several managed COMM frames into one; renderUnit keeps the single-frame case's
		// description and language, and the caller warns when the collapse is genuinely
		// ambiguous. Untouched Comment frames are preserved verbatim.
		desc, _, ok := decodeCommentFrame(f.Body)
		if !ok || mapping.ID3TechnicalCommentDesc(desc) {
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
	case "MVIN", "MVNM":
		// Apple's movement frames are managed but not T-prefixed, so the T-prefix
		// tail below never reaches them.
		return f.ID, true
	case "TIPL", "IPLS":
		// Managed so a role edit re-renders the frame (and drops a stale cross-version
		// sibling). TIPL already lands here via the T-prefix tail; naming IPLS makes it
		// managed too (it round-tripped unmanaged before). TMCL is intentionally left as a
		// pass-through T frame.
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
		if !ok || mapping.ID3TechnicalCommentDesc(desc) {
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
	case "MVIN":
		return []tag.Key{tag.Movement, tag.MovementTotal}
	case "MVNM":
		return []tag.Key{tag.MovementName}
	case "TIPL", "IPLS":
		return mapping.ID3InvolvedKeys()
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
	case tag.Movement, tag.MovementTotal:
		return []string{"MVIN"}
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
	case tag.Producer, tag.Engineer, tag.Mixer, tag.Arranger, tag.DJMixer:
		// The involved-people list: TIPL in v2.4, IPLS in v2.3. Without this the roles would
		// wrongly fall through to a TXXX:PRODUCER user frame below.
		if version >= 4 {
			return []string{"TIPL"}
		}
		return []string{"IPLS"}
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
func renderUnit(token string, edited tag.TagSet, version byte, opts WriteOpts, origLangs, origTXXXDesc map[string]string, origInvolved []involvedPerson, commentDesc string) ([]Frame, bool) {
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
		// Canonicalize a recognized boolean word to "1"/"0", mirroring the default branch
		// below: ITUNESGAPLESS/SHOWMOVEMENT are boolean keys whose ID3 home is a TXXX user
		// frame, and without this "yes" would be stored verbatim here while FLAC/MP4 store
		// "1". COMPILATION never reaches this branch (its render token is always TCMP).
		if tag.IsBooleanKey(key) {
			vals = canonicalBoolValues(vals)
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
		// commentDesc is non-empty only for the unambiguous single-frame, single-value
		// merge, so a described comment from Windows Explorer or a CDDB-era tagger survives
		// an edit intact rather than being flattened to a bare one. The caller warns for
		// every other shape.
		lang := unitLang(origLangs, "COMM")
		return renderByPolicy(version, "COMM", vals, opts.Multi,
			func(v []string) []byte { return encodeComment(version, lang, commentDesc, v) })
	case token == "USLT":
		text, ok := edited.First(tag.Lyrics)
		if !ok {
			return nil, false
		}
		return []Frame{{ID: "USLT", Body: encodeLangText(version, unitLang(origLangs, "USLT"), "", text)}}, false
	case token == "TCON":
		return genreFrames(version, edited, opts)
	case token == "TRCK":
		return renderNumTotal(version, "TRCK", edited, tag.TrackNumber, tag.TrackTotal)
	case token == "TPOS":
		return renderNumTotal(version, "TPOS", edited, tag.DiscNumber, tag.DiscTotal)
	case token == "MVIN":
		return renderMovementPair(version, edited)
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
	case token == "TIPL" || token == "IPLS":
		// Gather all five role keys from `edited`, not just changed ones: an unchanged sibling
		// role on the same frame must re-emit, which is the property that lets a role edit avoid
		// a StructuredEdit flag. Then append the recovered unknown involvements (already
		// case-folded-known-filtered and deduped in RebuildFrames), in original order.
		var flat []string
		for _, k := range mapping.ID3InvolvedKeys() {
			fn, ok := mapping.ID3InvolvedFunction(k)
			if !ok {
				continue
			}
			vals, has := edited.Get(k)
			if !has {
				continue
			}
			for _, name := range vals {
				if name == "" {
					continue // a nameless credit carries no data and would not read back
				}
				flat = append(flat, fn, name)
			}
		}
		for _, p := range origInvolved {
			flat = append(flat, p.Function, p.Name)
		}
		if len(flat) == 0 {
			return nil, false
		}
		// The internal function/name NUL-separation is the spec-defined TIPL/IPLS body, not the
		// de-facto v2.3 multi-value extension, so bypass renderByPolicy and report v23multi=false:
		// routing a conformant multi-person IPLS through the policy would falsely trip the v2.3
		// multi-value warning.
		return []Frame{{ID: token, Body: encodeTextFrame(chooseEncoding(version, flat), flat)}}, false
	default: // simple or pass-through text frame
		key, mapped := mapping.ID3FrameKey(token)
		if !mapped {
			key = tag.Key(token)
		}
		vals, has := edited.Get(key)
		if mapped && (!has || len(vals) == 0) {
			// A raw frame-ID key (--set TBPM=128) carries its values under the frame's own
			// name, not the canonical key the frame maps to. Without this fallback the
			// token would resolve only to the absent canonical key and the authored value
			// would be silently lost (the pre-mapping behavior emitted the frame).
			if raw := tag.Key(token); raw.Valid() {
				if rv, rok := edited.Get(raw); rok && len(rv) > 0 {
					key, vals, has = raw, rv, true
				}
			}
		}
		if !has || len(vals) == 0 {
			return nil, false
		}
		// Canonicalize a recognized boolean word to "1"/"0" before rendering (TCMP has no
		// dedicated case, so a COMPILATION edit lands here), matching MP4's cpil and FLAC's
		// Vorbis emit so the three formats store the flag identically. Gated on the resolved
		// canonical key, which renderText (frame-token only) cannot see; an unrecognized value
		// stays as text. Copied rather than mutated in place so the edited tag set is untouched.
		if tag.IsBooleanKey(key) {
			vals = canonicalBoolValues(vals)
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

// canonicalBoolValues returns a copy of vals with each recognized boolean word normalized to
// "1"/"0" via [tag.CanonicalBoolValue]. A fresh slice is returned so the caller's edited tag set
// is not mutated in place.
func canonicalBoolValues(vals []string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = tag.CanonicalBoolValue(v)
	}
	return out
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
// detectDroppedTotals: totals that cannot join a valid n/total frame
// (non-numeric number). Number kept; total dropped.

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

// renderMovementPair renders the MVIN frame from the movement number and optional total as
// "n/total", the movement sibling of renderNumTotal.
func renderMovementPair(version byte, edited tag.TagSet) ([]Frame, bool) {
	num, _ := edited.First(tag.Movement)
	total, _ := edited.First(tag.MovementTotal)
	if num == "" && total == "" {
		return nil, false
	}
	value, _ := composeMovementPair(num, total)
	enc := chooseEncoding(version, []string{value})
	return []Frame{{ID: "MVIN", Body: encodeTextFrame(enc, []string{value})}}, false
}

// composeMovementPair is composeNumTotal's sibling for the MVIN movement pair, shared by
// renderMovementPair (what to write) and detectDroppedTotals (whether to warn). MOVEMENT stays
// outside the track/disc machinery (tag.NumberTotalSplit is hard-gated to those keys, and its
// ValidNumericValue gate passes anything for a non-member), so the gates here are
// tag.ValidMP4IntValue per side and a re-run of movementSplit on the composed value - compose
// and read share that one helper, so a composed frame always splits back. An invalid number
// (non-numeric, signed, or past uint16) is preserved verbatim with any canonical total reported
// dropped, TRCK-equivalent; a number already carrying valid pair syntax ("3/12") composes with
// an explicit canonical total winning over the embedded one.
func composeMovementPair(num, canonicalTotal string) (value string, totalDropped bool) {
	if _, _, split := movementSplit(num); strings.TrimSpace(num) != "" && !split &&
		!tag.ValidMP4IntValue(tag.Movement, num) {
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
	if composed := nPart + "/" + finalTotal; validMovementPair(composed) {
		return composed, false
	}
	// Compose failed: preserve the number verbatim. A canonical total is thereby dropped; an
	// embedded one stays inside the preserved num.
	return num, canonicalTotal != ""
}

// validMovementPair reports whether a composed MVIN value splits back through movementSplit,
// so what compose emits is exactly what the read path will split.
func validMovementPair(v string) bool {
	_, _, split := movementSplit(v)
	return split
}

// detectDroppedTotals finds the TRACKTOTAL/DISCTOTAL/MOVEMENTTOTAL keys whose canonical value
// the pair compose cannot fit into a valid "n/total" frame, so the caller can warn rather than
// drop the total silently. Scoped to a pair the edit touched: an untouched pair keeps its
// original frame, and an embedded total in the number itself ("A1/12" alone) is preserved
// verbatim and is not a drop.
func detectDroppedTotals(changed map[tag.Key]bool, edited tag.TagSet) []tag.Key {
	var dropped []tag.Key
	for _, p := range []struct {
		numKey, totKey tag.Key
		compose        func(num, total string) (string, bool)
	}{
		{tag.TrackNumber, tag.TrackTotal,
			func(n, t string) (string, bool) { return composeNumTotal(tag.TrackNumber, n, t) }},
		{tag.DiscNumber, tag.DiscTotal,
			func(n, t string) (string, bool) { return composeNumTotal(tag.DiscNumber, n, t) }},
		{tag.Movement, tag.MovementTotal, composeMovementPair},
	} {
		if !changed[p.numKey] && !changed[p.totKey] {
			continue // neither field edited; the original frame is preserved, nothing newly dropped
		}
		num, _ := edited.First(p.numKey)
		total, _ := edited.First(p.totKey)
		if _, totalDropped := p.compose(num, total); totalDropped {
			dropped = append(dropped, p.totKey)
		}
	}
	return dropped
}

// detectDroppedTrailingValues: keys whose trailing empty the write cannot store
// (NUL frames emit no trailing terminator; read strips trailing empty). Only when
// NUL-separated (v2.4 or v2.3+ID3MultiNullSep).

func detectDroppedTrailingValues(changed map[tag.Key]bool, edited tag.TagSet, version byte, pol core.ID3MultiValuePolicy) []tag.Key {
	if version < 4 && pol != core.ID3MultiNullSep {
		return nil // repeat-frame preserves the empty; slash-join collapses the whole multi-value
	}
	var dropped []tag.Key
	for k := range changed {
		if _, isInvolvedRole := mapping.ID3InvolvedFunction(k); isInvolvedRole {
			continue // handled by detectDroppedInvolvedEmpties (all positions, all versions/policies)
		}
		vals, ok := edited.Get(k)
		if !ok || len(vals) <= 1 {
			continue
		}
		if vals[len(vals)-1] == "" {
			dropped = append(dropped, k)
		}
	}
	slices.Sort(dropped)
	return dropped
}

// detectDroppedEmptyValues: all-empty keys that got no frame. Read off rendered frames.

func detectDroppedEmptyValues(changed map[tag.Key]bool, edited tag.TagSet, produced map[string]bool, version byte, info RebuildInfo) []tag.Key {
	reported := make(map[tag.Key]bool, len(info.DroppedDates)+len(info.DroppedTotals))
	for _, k := range info.DroppedDates {
		reported[k] = true
	}
	for _, k := range info.DroppedTotals {
		reported[k] = true
	}
	var dropped []tag.Key
	for k := range changed {
		vals, ok := edited.Get(k)
		if !ok || len(vals) == 0 || !allEmpty(vals) || reported[k] {
			continue
		}
		wrote := false
		for _, rid := range keyRenderIDs(k, version) {
			wrote = wrote || produced[rid]
		}
		if !wrote {
			dropped = append(dropped, k)
		}
	}
	slices.Sort(dropped)
	return dropped
}

// detectDroppedInvolvedEmpties: changed involved-people keys with empties
// TIPL/IPLS cannot store. Sorted for deterministic order.

func detectDroppedInvolvedEmpties(changed map[tag.Key]bool, edited tag.TagSet) []tag.Key {
	var dropped []tag.Key
	for _, k := range mapping.ID3InvolvedKeys() {
		if !changed[k] {
			continue
		}
		if vals, ok := edited.Get(k); ok && slices.Contains(vals, "") {
			dropped = append(dropped, k)
		}
	}
	slices.Sort(dropped)
	return dropped
}

// TransferClassifier: ID3 transfer fates capabilities cannot express.
// TRACKTOTAL/DISCTOTAL/MOVEMENTTOTAL with no valid number/total join → Dropped.

func TransferClassifier(key tag.Key, values []string, all tag.TagSet) (core.Disposition, string, bool) {
	switch key {
	case tag.TrackTotal, tag.DiscTotal:
		numKey := tag.TrackNumber
		if key == tag.DiscTotal {
			numKey = tag.DiscNumber
		}
		num, _ := all.First(numKey)
		total, _ := all.First(key)
		if _, dropped := composeNumTotal(numKey, num, total); dropped {
			return core.Dropped, "the number it attaches to is non-numeric, so ID3 cannot store this total (a total is written only as the second half of \"number/total\")", true
		}
	case tag.MovementTotal:
		num, _ := all.First(tag.Movement)
		total, _ := all.First(key)
		if _, dropped := composeMovementPair(num, total); dropped {
			return core.Dropped, "it cannot join the movement number in a valid \"number/total\" MVIN frame, so ID3 cannot store this total", true
		}
	case tag.Movement:
		if len(values) > 0 {
			if _, _, split := movementSplit(values[0]); split {
				return core.Lossy, "stored in the MVIN \"number/total\" frame, which reads back split into MOVEMENT and MOVEMENTTOTAL", true
			}
		}
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

// detectNumericGenres: GENRE values that are a bare genre index (e.g. "17").
// Written to TCON they read back as the name; caller warns.

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

// detectDateFates classifies changed date keys into drop/coerce/reduce for v2.3
// (one [classifyV23Date] pass). v2.4 stores full strings (empty fates).

func detectDateFates(changed map[tag.Key]bool, edited tag.TagSet, version byte) (dropped []tag.Key, reduced, coerced []ReducedDate) {
	if version >= 4 {
		return nil, nil, nil
	}
	for _, k := range []tag.Key{tag.RecordingDate, tag.OriginalDate} {
		if !changed[k] {
			continue
		}
		v, _ := edited.First(k)
		switch classifyV23Date(v) {
		case dateDropped:
			dropped = append(dropped, k)
		case dateReduced:
			if k == tag.RecordingDate {
				reduced = append(reduced, ReducedDate{Key: k, Value: v})
			}
		case dateCoerced:
			if k == tag.RecordingDate {
				coerced = append(coerced, ReducedDate{Key: k, Value: v})
			}
		}
	}
	return dropped, reduced, coerced
}

// dateFate is what a v2.3 tag does to a date value, as one classification rather than a set
// of independent tests. Every consumer - detectDateFates, and the two transfer-capability
// predicates below - reads the same answer, so they cannot come to disagree about one value.
type dateFate uint8

const (
	dateExact   dateFate = iota // reads back byte-identical
	dateDropped                 // no frame renders at all
	dateReduced                 // stored with a component the value carried missing
	dateCoerced                 // every component survives, spelled differently on read-back
)

// classifyV23Date: what the read path returns after v2.3 TYER/TDAT/TIME split
// and composeV23Date recompose. Derives the four fates from one answer.

func classifyV23Date(iso string) dateFate {
	if iso == "" {
		return dateExact
	}
	year := extractDatePart(iso, partYear)
	if year == "" {
		return dateDropped // both TYER and TORY need a valid 4-digit year
	}
	ddmm := extractDatePart(iso, partDayMonth)
	hhmm := extractDatePart(iso, partHourMin)
	switch {
	case hasSubYearPart(iso) && ddmm == "": // a month with no full date: TDAT needs DDMM
		return dateReduced
	case hasSubDayPart(iso) && hhmm == "": // an hour with no minute: TIME needs HHMM
		return dateReduced
	case hasSubMinutePart(iso) && hhmm != "": // TIME has no seconds field
		return dateReduced
	}
	if composeV23Date(year, ddmm, hhmm) != iso {
		return dateCoerced
	}
	return dateExact
}

// v23DateReadBack composes the value the read path returns for iso once a v2.3 tag has
// stored it, so a warning can quote the round-trip rather than describe it in prose.
func v23DateReadBack(iso string) string {
	return composeV23Date(
		extractDatePart(iso, partYear),
		extractDatePart(iso, partDayMonth),
		extractDatePart(iso, partHourMin))
}

// v23DateDropped reports whether a v2.3 tag drops the date value v entirely: a non-empty
// value with no valid 4-digit year renders no TYER/TORY frame (both fields need one, and a
// 5-digit or compact non-canonical form yields none). It is the transfer capability's
// value-drop predicate, reading the same classification detectDateFates does, so the transfer
// report cannot drift from the write.
func v23DateDropped(v string) bool {
	return classifyV23Date(v) == dateDropped
}

// reducesDatePrecision reports whether a v2.3 tag would store iso with less precision than
// it carries: a month with no full date ("2021-03"), an hour with no minute
// ("2021-03-15T10"), or seconds past a full minute ("2021-03-15T10:30:45"). A bare year, a
// full date, or a date-time to the minute render losslessly. A value with no extractable
// year drops entirely and is v23DateDropped's answer instead. See [classifyV23Date], which
// owns the rule.
func reducesDatePrecision(iso string) bool {
	return classifyV23Date(iso) == dateReduced
}

// v23DateNotVerbatim reports whether a v2.3 tag gives the value back changed - a component
// lost, or the same components respelled. It is the transfer capability's fidelity test,
// which is a wider question than the write path's: a copy must grade the field Lossy for
// either outcome, or the report promises a faithful carry that the plan's own value-coerced
// warning then contradicts and copy --strict fails on. The write path keeps the two apart,
// because each has its own warning.
func v23DateNotVerbatim(iso string) bool {
	switch classifyV23Date(iso) {
	case dateReduced, dateCoerced:
		return true
	}
	return false
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

// AppendMalformedTailDropped appends a malformed-tag-entry-dropped warning when the tag
// being rewritten carried a region the parser could not read (see [Tag.MalformedTail]). A
// rewrite renders frames plus padding, so those bytes are replaced and gone. It is the
// write-path counterpart of the malformed-tag-entry warning [Project] raises, split from it
// for the same reason as the duplicate-tag-block pair: the read code describes the file
// before any edit. srcTag may be nil (a file with no ID3 tag), which warns nothing. Shared
// by the four codecs that rewrite an ID3 tag so they cannot word one loss four ways.
func AppendMalformedTailDropped(ws []core.Warning, srcTag *Tag) []core.Warning {
	if srcTag.Ignored() != "" {
		return core.Warn(ws, core.WarnMalformedTagEntryDropped,
			"the ignored ID3v2.2 tag is replaced by the rewritten tag and its contents are not carried")
	}
	id, n := srcTag.MalformedTail()
	if id == "" || n <= 0 {
		return ws
	}
	return core.Warn(ws, core.WarnMalformedTagEntryDropped,
		fmt.Sprintf("%d byte(s) after the malformed %s frame could not be read and are not carried into the rewritten tag",
			n, core.WarnSnippet(id)))
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
		msg := fmt.Sprintf("%s cannot be represented in ID3 because the number it attaches to is non-numeric (the total is stored only as the second half of \"number/total\") and was dropped", k)
		if k == tag.MovementTotal {
			// The movement pair can also fail to compose on a valid-looking but
			// out-of-contract side (a sign, or past 65535), so "non-numeric" would mislead.
			msg = fmt.Sprintf("%s cannot join the movement number in a valid \"number/total\" MVIN frame and was dropped", k)
		}
		ws = core.WarnKeyed(ws, core.WarnValueDropped, msg, k)
	}
	// Emit the trailing-empty drops unconditionally, not behind the retained-guard the date and
	// total loops use: retained is the re-projected (already-stripped) set, so retained.First(ARTIST)
	// returns the surviving "A", and a retained-guarded loop would then suppress every trailing-empty
	// warning. No container keeps a trailing empty (WAV's LIST/INFO strips it too), so emitting
	// unconditionally is correct.
	for _, k := range info.DroppedTrailingValues {
		ws = core.WarnKeyed(ws, core.WarnValueDropped,
			fmt.Sprintf("%s: a trailing empty value cannot be represented in an ID3 text frame and was dropped", k), k)
	}
	// Suppressed when another container still stores the empty value (WAV's LIST/INFO holds
	// one), since the file as a whole did not lose it.
	for _, k := range info.DroppedEmptyValues {
		if v, ok := retained.Get(k); ok && len(v) > 0 {
			continue
		}
		ws = core.WarnKeyed(ws, core.WarnValueDropped,
			fmt.Sprintf("%s: an empty value cannot be represented in this ID3 frame and was dropped; use --clear to remove the key", k), k)
	}
	for _, k := range info.DroppedInvolvedEmpties {
		ws = core.WarnKeyed(ws, core.WarnValueDropped,
			fmt.Sprintf("%s: an empty credit value cannot be represented in the ID3 involved-people frame and was dropped", k), k)
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
	for _, cd := range info.CoercedDates {
		// Same cross-container suppression as the reductions above: a container that still
		// carries the literal value (WAV's ICRD) makes the round-trip lossless overall.
		if v, _ := retained.First(cd.Key); v == cd.Value {
			continue
		}
		ws = core.WarnKeyed(ws, core.WarnValueCoerced,
			fmt.Sprintf("%s value %q is stored as separate ID3v2.3 date frames and reads back as %q",
				cd.Key, cd.Value, v23DateReadBack(cd.Value)), cd.Key)
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
	if info.CommentDescriptionDropped {
		ws = core.WarnKeyed(ws, core.WarnCommentDescriptionDropped,
			"a description one of this file's comment frames carried could not be kept when the comment was rewritten; the comment text is written in full",
			tag.Comment)
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

// CarryProjectionWarnings: post-write MP3/AAC warnings from rebuild losses and
// projection (dates, genres, chapters, synced lyrics, comments).

func CarryProjectionWarnings(sourceWarnings, newTagWarnings []core.Warning) []core.Warning {
	out := core.CloneWarnings(sourceWarnings)
	for _, code := range []core.WarningCode{core.WarnChaptersFlattened, core.WarnInvalidPicture, core.WarnMalformedTagEntry} {
		if len(core.WarningsWithCode(newTagWarnings, code)) == 0 {
			out = core.WarningsWithoutCode(out, code)
		}
	}
	return out
}

// PerFieldCapabilities: shared ID3 per-key overrides (MP3/AAC/AIFF/WAV).
// ORIGINALDATE AccessPartial on v2.3 (TORY year-only).

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
		add(tag.RecordingDate, core.WithValueDrop(core.WithValueReduction(core.RecordingDateV23Capability(), v23DateNotVerbatim), v23DateDropped))
	}
	switch {
	case numericGenre && genreViaID3:
		add(tag.Genre, core.NumericGenreCapability("numeric ID3 TCON reference"))
	case genreViaID3:
		// Even without --numeric-genre, a bare numeric reference written verbatim to
		// TCON is resolved back to the genre name on read, so a transfer carrying
		// such a value grades Lossy. Write stays AccessFull: the edit path reports
		// this loss through WarnNumericGenre, not a capability reduction.
		add(tag.Genre, core.WithValueReduction(core.Capability{
			Read: core.AccessFull, Write: core.AccessFull,
			Representation: "ID3 TCON",
			Fidelity:       "a bare numeric genre reference reads back as its genre name",
		}, isNumericGenreRef))
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

// genreFrames renders the TCON frame(s) for the edited genre under the write options,
// returning no frame when the field is absent or empty (the frame is dropped). Both the
// writer and EncodingRewriteNeeded go through it, so the predicate can never compute a
// different render than the write performs.
//
// The all-empty guard is what the doc above claimed and the code did not: the old len == 0
// test let GENRE="" through to a TCON the genre read path then drops. The plain text frames
// store a present-empty value and read it back, so the guard belongs here, not in the shared
// render path. detectDroppedEmptyValues reports the drop.
func genreFrames(version byte, edited tag.TagSet, opts WriteOpts) ([]Frame, bool) {
	vals, ok := edited.Get(tag.Genre)
	if !ok || allEmpty(vals) {
		return nil, false
	}
	return renderText(version, "TCON", genreValues(vals, version, opts.NumericGenre, opts.Multi), opts.Multi)
}

// allEmpty reports whether every value in a set is the empty string (including the empty
// set).
func allEmpty(values []string) bool {
	for _, v := range values {
		if v != "" {
			return false
		}
	}
	return true
}

// EncodingRewriteNeeded: write-encoding would change representation of an unchanged
// value, so the no-op path lets the write through. nil src = no ID3 container.

func EncodingRewriteNeeded(src *Tag, edited tag.TagSet, opts WriteOpts) bool {
	if src == nil {
		return false
	}
	return encodingRewriteNeeded(src.Frames(), src.WriteVersion(), edited, opts)
}

// encodingRewriteNeeded is EncodingRewriteNeeded over a raw frame list, so the rebuilder can
// reach the same verdict from the frames it already holds.
func encodingRewriteNeeded(orig []Frame, version byte, edited tag.TagSet, opts WriteOpts) bool {
	if !opts.NumericGenre {
		return false
	}
	var stored []string
	found := false
	for _, f := range orig {
		if f.ID != "TCON" || f.Opaque {
			continue
		}
		found = true
		stored = append(stored, DecodeText(f)...)
	}
	if !found {
		return false // no stored genre to re-encode
	}
	// Both sides are compared as decoded value lists. Comparing genreValues' output against
	// the stored frame directly would be asymmetric and churn correct files: a slash join
	// collapses N values into one frame body, and a repeat-frame policy spreads them across N
	// frames. Rendering and decoding back normalizes the join, the frame count, and the text
	// encoding (so a Latin-1 frame does not read as different from a UTF-16 one) in one step.
	want := renderedGenreValues(version, edited, opts)
	if len(want) == 0 {
		return false // the edit drops the genre; a removal is not a re-encoding
	}
	// The conversion must actually have produced a reference. A special reference (RX/CR) has
	// no ID3v1 index, a literal beginning with "(" is only escaped, and a slash join disables
	// the conversion outright - in each case the render carries no generated reference, and
	// firing would replace a reference the file already holds with its plain name, or apply
	// an unrelated escaping, rather than the normalisation the flag asked for. Testing the
	// rendered values against the references the conversion could generate covers all three
	// without re-deriving when genreValues decides to convert.
	if !slices.ContainsFunc(want, generatedReference(edited, version)) {
		return false
	}
	// A stored value that packs several canonical values into one frame value - ID3v2.3's
	// "(17)(8)" and "(17)Hard" reference-and-refinement forms - cannot be re-rendered: the
	// writer emits one value per canonical entry, so the rewrite would silently downgrade a
	// spec-legal frame to the nonstandard NUL-separated extension (and raise its own
	// compatibility warning). Leave it alone rather than trade one representation problem
	// for another.
	if len(stored) != len(want) {
		return false
	}
	return !slices.Equal(stored, want)
}

// generatedReference returns a predicate reporting whether a rendered genre value is one the
// numeric conversion generated, rather than a name or an escaped literal that passed through.
func generatedReference(edited tag.TagSet, version byte) func(string) bool {
	vals, _ := edited.Get(tag.Genre)
	refs := make(map[string]bool, len(vals))
	for _, v := range vals {
		if r, ok := genreReference(v, version); ok {
			refs[r] = true
		}
	}
	return func(rendered string) bool { return refs[rendered] }
}

// renderedGenreValues renders the genre under opts and decodes the result back, giving the
// value list the write would actually store. Going through the renderer rather than reading
// genreValues directly is what normalizes the multi-value join, the frame count, and the
// text encoding, so the comparison in encodingRewriteNeeded is symmetric on both sides.
func renderedGenreValues(version byte, edited tag.TagSet, opts WriteOpts) []string {
	frames, _ := genreFrames(version, edited, opts)
	var out []string
	for _, f := range frames {
		out = append(out, DecodeText(f)...)
	}
	return out
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
			if ref, ok := genreReference(n, version); ok {
				out[i] = ref
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

// genreReference: write version's reference form for a named standard genre.

func genreReference(name string, version byte) (string, bool) {
	idx := genreIndex(name)
	if idx < 0 && !strings.HasPrefix(name, "(") {
		if resolved, isRef := resolveGenres(name); isRef && len(resolved) == 1 {
			idx = genreIndex(resolved[0])
		}
	}
	if idx < 0 {
		return "", false
	}
	if version >= 4 {
		return strconv.Itoa(idx), true
	}
	return "(" + strconv.Itoa(idx) + ")", true
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
