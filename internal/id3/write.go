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
// dropped and re-emitted only when its change flag is set; otherwise its source frames
// are kept.
type StructuredEdit struct {
	Pictures            []core.Picture
	PicturesChanged     bool
	Chapters            []core.Chapter
	ChaptersChanged     bool
	SyncedLyrics        []core.SyncedLyrics
	SyncedLyricsChanged bool
	// Carried marks the edit as a cross-format carry rather than an authored one. A carry
	// skips the synced-lyrics language/descriptor fallback (a no-language set from FLAC/Ogg
	// must read back with none) and does not keep the destination's COMM description,
	// which labels text that is no longer there.
	Carried bool
	// SyncedLyricsCleared marks the synced-lyrics set as cleared before this edit authored
	// a new one, so the language/descriptor fallback is skipped and an authored set with
	// no language reads back with none.
	SyncedLyricsCleared bool
	// MediaDuration is the file's playable length. It bounds a trailing open-ended chapter
	// (End == 0) at CHAP write so readers see a concrete end instead of the 0xFFFFFFFF
	// sentinel (~49.7 days). Zero leaves the chapter open. The fill is ID3-local.
	MediaDuration time.Duration
}

// RebuildInfo reports facts about a rebuild the caller surfaces in the write report.
type RebuildInfo struct {
	// UsedV23Multi is set when a v2.3 tag was written with NUL-separated multi-values, a
	// nonstandard extension the caller warns about.
	UsedV23Multi bool
	// DroppedDates lists date keys whose edited value has no extractable 4-digit year and
	// so rendered no v2.3 frame; the caller warns value-dropped. Empty on v2.4, where
	// TDRC/TDOR store the full string. See detectDateFates.
	DroppedDates []tag.Key
	// CoercedDates lists RecordingDate values whose v2.3 rendering keeps every component
	// but reads back respelled: "2001-02-03 10:20" recomposes as "2001-02-03T10:20". The
	// caller warns value-coerced. See classifyV23Date.
	CoercedDates []ReducedDate
	// ReducedDates lists RecordingDate values whose v2.3 rendering lost precision: a month
	// with no day (TDAT needs DDMM) or an hour with no minute (TIME needs HHMM). Each pairs
	// the key with the attempted value. OriginalDate's TORY loss is reported through its
	// AccessPartial capability instead, so listing it here would double-warn.
	ReducedDates []ReducedDate
	// HasDroppedMalformedPicture is set when a picture edit replaced the APIC frames and an
	// original APIC could not be decoded, so its bytes are not carried forward.
	HasDroppedMalformedPicture bool
	// NumericGenres lists the GENRE values this edit set that are a bare genre index ("17").
	// Written verbatim to TCON, such a value reads back as the genre name on the pure-ID3
	// formats; the caller warns unless a native container keeps the literal number. See
	// detectNumericGenres.
	NumericGenres []string
	// DroppedTotals lists TRACKTOTAL/DISCTOTAL/MOVEMENTTOTAL keys whose value cannot join a
	// valid "n/total" frame because the number is non-numeric (TRACKNUMBER="A1"): the
	// number is written verbatim and the total dropped. An embedded total ("A1/12" alone)
	// is not a drop. See detectDroppedTotals.
	DroppedTotals []tag.Key
	// DroppedTrailingValues lists changed keys whose trailing empty value a NUL-separated
	// frame cannot store: the frame emits no trailing terminator and the read path strips a
	// trailing empty. Populated only for v2.4, or v2.3 under ID3MultiNullSep. See
	// detectDroppedTrailingValues.
	DroppedTrailingValues []tag.Key
	// DroppedEmptyValues lists changed keys set to an all-empty value that no frame was
	// written for. Plain text frames store a present-empty value; the genre, number-pair,
	// movement and date frames do not. Read off the rendered frames. See
	// detectDroppedEmptyValues.
	DroppedEmptyValues []tag.Key
	// DroppedInvolvedEmpties lists changed involved-people role keys carrying an empty
	// value. TIPL/IPLS store function/name pairs and drop a nameless pair on write and
	// read, so an empty at any position vanishes. See detectDroppedInvolvedEmpties.
	DroppedInvolvedEmpties []tag.Key
	// ChapterOverflow is set when a chapter edit clamped a start or end past the CHAP
	// frame's 32-bit millisecond field (~49.7 days).
	ChapterOverflow bool
	// DroppedChapterSubframes is set when a chapter edit dropped a source CHAP subframe
	// other than the TIT2 title (a per-chapter image or URL the flat model cannot hold).
	DroppedChapterSubframes bool
	// SyncedLyricsOverflow is set when a synced-lyrics edit clamped a line's timestamp past
	// the SYLT frame's 32-bit millisecond field (~49.7 days).
	SyncedLyricsOverflow bool
	// SyncedLyricsInvalidNUL is set when a synced-lyrics line or descriptor carries an
	// embedded NUL, which the NUL-terminated SYLT field would truncate. RebuildError turns
	// it into a hard waxerr.ErrInvalidData rather than a warning.
	SyncedLyricsInvalidNUL bool
	// CommentDescriptionDropped is set when a Comment rewrite could not keep a managed COMM
	// frame's description: several managed frames, several edited values, or a carry. The
	// authored single-frame, single-value case keeps it; clearing Comment drops it with the
	// value.
	CommentDescriptionDropped bool
	// SyncedLyricsLangUndefined is set when an authored synced-lyrics language normalizes
	// to the ID3 "undefined" marker ("xxx"/"XXX"): it is stored but reads back with no
	// language. A carried no-language set has an empty language and is not flagged.
	SyncedLyricsLangUndefined bool
}

// ReducedDate pairs a date key with the value an edit attempted to store before a
// lower-fidelity v2.3 rendering reduced its precision.
type ReducedDate struct {
	Key   tag.Key
	Value string
}

// RebuildFrames builds the new frame list for an edited tag. Unchanged and unmodelled
// frames stay in place; only the frames a changed key affects re-render. Pictures,
// chapters and synced lyrics are reconciled here too, since their frames interleave
// with text frames.
func RebuildFrames(orig []Frame, base, edited tag.TagSet, version byte,
	se StructuredEdit, opts WriteOpts) ([]Frame, RebuildInfo) {

	picturesChanged := se.PicturesChanged
	changed := diffKeys(base, edited)
	// produced records which render tokens emitted a frame, for detectDroppedEmptyValues.
	produced := map[string]bool{}
	dirty := map[string]bool{}
	for k := range changed {
		for _, rid := range keyRenderIDs(k, version) {
			dirty[rid] = true
		}
	}
	// A write-encoding option changes how a value is stored, not the value, so diffKeys
	// sees nothing and the frame would be preserved verbatim. keyRenderIDs keeps the render
	// tokens defined in one place; orig only ever holds TCON, since decodeFrame upgrades TCO.
	if encodingRewriteNeeded(orig, version, edited, opts) {
		for _, rid := range keyRenderIDs(tag.Genre, version) {
			dirty[rid] = true
		}
	}

	// The read path drops the COMM/USLT 3-byte language and the COMM description, and
	// uppercases a TXXX description into its key, so recover them from the original frames.
	// Re-rendered comment and lyric frames keep their language, a single re-rendered comment
	// keeps its description, and custom TXXX frames keep their description casing. Several
	// COMM frames can be managed, so each recovery takes the first.
	origLangs := map[string]string{}          // "COMM"/"USLT"/"SYLT" -> 3-byte language
	var origSyltDesc string                   // first projecting lyrics SYLT's content descriptor (authored-set fallback)
	origTXXXDesc := map[string]string{}       // TXXX render token -> original description (verbatim casing)
	var origInvolved []involvedPerson         // unmodelled well-formed TIPL/IPLS involvements, preserved on write
	seenInvolved := map[involvedPerson]bool{} // dedup across repeated or multiple involved-people frames
	var managedComments int                   // managed COMM frames, which a Comment edit merges into one
	var firstCommentDesc string               // first managed COMM's description, kept when the merge is unambiguous
	var anyCommentDesc bool                   // any managed COMM carries a description
	for _, f := range orig {
		switch f.ID {
		case "COMM", "USLT":
			rid, managed := frameRenderID(f)
			if !managed {
				break
			}
			// First wins, matching the SYLT branch below.
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
			// Recover the first lyrics SYLT's language and descriptor as fallbacks for a
			// re-rendered set whose modeled value is unset, so a line-only edit keeps them. Only
			// a projecting lyrics frame qualifies: a chord or trivia SYLT must not donate its
			// metadata. Both come from the same first projecting SYLT via the origLangs guard.
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
			// Recover unmodelled involvements (mastering, recording, ...) so a role edit that
			// re-renders this frame keeps them. The known-check uses the read path's case-folding
			// lookup: a capitalized "Producer" already re-emits from edited, and exact matching
			// would duplicate it. Dedup by exact (function, name) across repeated frames. The
			// write version equals the source version, so a preserved unknown stays in the frame
			// it came from. Splitting v2.4 TMCL instrument credits out of TIPL is deferred (see
			// the package's TMCL scope note).
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

	// A Comment edit merges every managed COMM into one frame, which can keep only one
	// description. Keep it when the merge is unambiguous (one source frame, one edited
	// value); otherwise flatten and let the caller warn. A cleared Comment is not a
	// description drop: the description goes with the value. A carry never keeps it: the
	// description labels the destination's comment, not a value that arrived from another
	// file.
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
			// Picture edits replace the original APIC frames with the edited set. An APIC with
			// a malformed header cannot be projected or carried forward, so surface the loss
			// via HasDroppedMalformedPicture (CarryProjectionWarnings reconciles it onto the
			// returned document). The Vorbis-comment codecs (FLAC/Ogg) instead re-append an
			// undecodable PICTURE block verbatim; each family matches a fresh re-parse of its
			// own output.
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
			// millisecond timestamps. Non-projecting SYLT frames (chord/trivia tracks,
			// MPEG-frame timestamps) stay verbatim.
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
		// Not the write-version's target for this key. Drop a stale alternative
		// representation of a changed key (a TXXX:RELEASEDATE or TDRC left behind when the
		// target is TDRL or TYER) so the value is not duplicated and the edit not lost.
		if touchesChangedKey(f, changed) {
			continue
		}
		// A managed text frame carried verbatim can itself hold a v2.3 NUL-separated
		// multi-value (an unrelated edit on a file that already had one). The re-render path
		// never sees it, so flag it here too: the caveat describes the output. v2.4 splits
		// cleanly.
		if version == 3 && len(DecodeText(f)) > 1 {
			info.UsedV23Multi = true
		}
		out = append(out, f.Clone())
	}

	// Append frames for changed keys with no original frame, in sorted order so the same
	// edit always yields the same bytes.
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

	// Append edited synced lyrics. SYLT is self-contained, so frame position is not
	// significant. A set with an empty language or descriptor falls back to the first
	// original SYLT's, so an authored line-only edit keeps them.
	if se.SyncedLyricsChanged && len(se.SyncedLyrics) > 0 {
		fallbackLang := origLangs["SYLT"]
		fallbackDesc := origSyltDesc
		// A carry and a clear-then-author both suppress the fallback, so the set reads back
		// with no language or descriptor instead of inheriting the destination's.
		if se.Carried || se.SyncedLyricsCleared {
			fallbackLang = ""
			fallbackDesc = ""
		}
		syltF, overflow, invalidNUL := syltFrames(se.SyncedLyrics, version, fallbackLang, fallbackDesc)
		out = append(out, syltF...)
		info.SyncedLyricsOverflow = overflow
		info.SyncedLyricsInvalidNUL = invalidNUL
		// An authored language that normalizes to the ID3 "undefined" marker ("xxx"/"XXX")
		// is stored but reads back empty, so flag it. syltLanguage is the read-side
		// normalizer, so this fires exactly when a fresh parse reports no language.
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
// the report fragments it contributes. Bytes and Tag are nil when no frame survives: the
// caller writes no tag segment and passes a nil tag to its result builder.
type FrontTag struct {
	Bytes      []byte         // rendered tag (header + frames + padding); nil to drop the tag
	Tag        *Tag           // the new tag for the result document; nil when dropped
	Padding    int64          // padding bytes written (0 when dropped)
	Operations []string       // report operation lines this emission adds, in order
	Warnings   []core.Warning // report warnings this emission adds
}

// ContainerOps returns the write-report operation lines for pictures, chapters, and synced
// lyrics, each gated on its change flag and model count. A cleared container (count 0)
// emits no line; the tag rewrite/removal op already covers it. RenderFrontTag (MP3/AAC)
// and the WAV/AIFF planChunks callers share it.
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
// as a front tag (MP3, AAC). It emits the tag only when at least one frame survives; an
// edit or --legacy strip that leaves none drops the tag instead of writing an empty,
// padding-only container, matching WAV/AIFF's len(frames)>0 chunk guard. hadTag makes a
// full clear record an "ID3v2 removal" op; srcTagLen is the source tag's byte length for
// in-place padding reuse; the counts feed the container operation lines.
func RenderFrontTag(srcTag *Tag, version byte, newFrames []Frame, info RebuildInfo, pad core.PaddingPolicy,
	srcTagLen int64, hadTag, tagsChanged, picturesChanged bool, pictureCount int,
	chaptersChanged bool, chapterCount int, syncedLyricsChanged bool, syncedLyricsCount int) FrontTag {

	if len(newFrames) == 0 {
		var ft FrontTag
		if hadTag {
			// A full clear of a tagged file: record the removal so it is not reported as a
			// bare rewrite.
			ft.Operations = []string{"ID3v2 removal"}
		}
		return ft
	}
	// Size the tag and its padding. Reuse the original region in place when the new content
	// fits, so the audio offset (and file size) need not change.
	nonPad := RenderedSize(newFrames)
	padSize := pad.ReuseOrTarget(srcTagLen, nonPad)
	// Clamp here, not only inside Render: Report().PaddingAfter comes from ft.Padding, so a
	// hidden clamp would overstate the written padding. The trigger is a reused tag region
	// larger than the ID3v2 size field.
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
	// The container op lines sit between the frame-rewrite and tag-creation ops.
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
// (modelled by the canonical projection, so re-rendered when its field changes).
// Unmanaged frames (URLs, POPM, PRIV, machine-described comments, described lyrics,
// non-MusicBrainz UFIDs, opaque frames) are always preserved verbatim.
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
		// Managed exactly when projected (project.go), through the same predicate: a
		// machine-described COMM (iTunNORM, ReplayGain) is neither read as COMMENT nor
		// touched on write; every other COMM is both. Projecting without managing would make
		// an edit preserve the described frame and append a fresh COMM, so a re-parse would
		// read two comments. The flat Comment model has no per-comment language, so editing
		// Comment merges several managed COMM frames into one; renderUnit keeps the
		// single-frame case's description and language, and the caller warns when the
		// collapse is ambiguous.
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
		// Managed so a role edit re-renders the frame and drops a stale cross-version
		// sibling. TIPL already reaches the T-prefix tail; naming IPLS makes it managed too.
		// TMCL stays a pass-through T frame.
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

// frameKeys returns the canonical keys a managed frame contributes to. The rebuilder
// uses it to drop a stale alternative representation of a changed key: the same value
// can sit under more than one frame across versions (TYER vs TDRC, TXXX:RELEASEDATE vs
// TDRL, TXXX:ISRC vs TSRC), and only the write-version's target should survive an edit.
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
	// conformantFrameID guarantees len == 4, so s[0] is safe, and keeps the allowed byte
	// set defined in one place.
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
		// Canonicalize a boolean word to "1"/"0", as the default branch does: ITUNESGAPLESS
		// and SHOWMOVEMENT live in TXXX frames, and FLAC/MP4 store "1". COMPILATION renders
		// as TCMP and never reaches this branch.
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
		// merge; the caller warns for every other shape.
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
		// Gather all five role keys from edited, not just changed ones: an unchanged sibling
		// role on the same frame must re-emit. Then append the recovered unknown involvements
		// (already filtered and deduped in RebuildFrames) in original order.
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
		// The function/name NUL-separation is the spec-defined TIPL/IPLS body, not the v2.3
		// multi-value extension, so bypass renderByPolicy and report v23multi=false.
		return []Frame{{ID: token, Body: encodeTextFrame(chooseEncoding(version, flat), flat)}}, false
	default: // simple or pass-through text frame
		key, mapped := mapping.ID3FrameKey(token)
		if !mapped {
			key = tag.Key(token)
		}
		vals, has := edited.Get(key)
		if mapped && (!has || len(vals) == 0) {
			// A raw frame-ID key (--set TBPM=128) carries its values under the frame's own
			// name, not the canonical key. Without this fallback the authored value would be
			// lost.
			if raw := tag.Key(token); raw.Valid() {
				if rv, rok := edited.Get(raw); rok && len(rv) > 0 {
					key, vals, has = raw, rv, true
				}
			}
		}
		if !has || len(vals) == 0 {
			return nil, false
		}
		// Canonicalize a boolean word to "1"/"0" (a COMPILATION edit lands here as TCMP),
		// matching MP4's cpil and FLAC's Vorbis emit. Gated on the resolved key, which
		// renderText cannot see; an unrecognized value stays as text.
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

// renderByPolicy renders a text-like ID3 frame under the multi-value policy. encodeBody
// owns the frame-specific body format; this helper handles repeat-frame, slash-join, and
// the NUL-separated v2.3 extension, and reports whether that extension was used.
// ID3MultiRepeatFrame emits one frame per value, including TXXX and COMM, which can repeat
// a TXXX description or COMM language/description pair; readers tolerate it, the spec
// discourages it, and the policy is explicit caller opt-in.
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

// composeNumTotal is the compose-or-drop decision renderNumTotal and detectDroppedTotals
// share. It returns the TRCK/TPOS value to write and whether a canonical total was
// dropped. "n/total" is composed only when the result is a valid numeric value the reader
// splits back; otherwise the number is written verbatim. An explicit canonical total wins
// over one embedded in the number ("5/12" plus TRACKTOTAL never composes "5/12/20").
// SplitNumberTotal keeps exact digit strings, including leading zeros.
func composeNumTotal(numKey tag.Key, num, canonicalTotal string) (value string, totalDropped bool) {
	// A non-empty number that is not a valid numeric value ("1/2/3", "A1/12") cannot be
	// recomposed without dropping text, so keep it verbatim and report any canonical total
	// dropped. An empty number is representable as "/total", so a lone TRACKTOTAL still
	// round-trips.
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
// renderMovementPair and detectDroppedTotals. MOVEMENT is outside the track/disc machinery
// (tag.NumberTotalSplit is gated to those keys), so the gates are tag.ValidMP4IntValue per
// side and a re-run of movementSplit on the composed value, which read also uses. An
// invalid number (non-numeric, signed, or past uint16) is written verbatim with any
// canonical total reported dropped; a number already carrying pair syntax ("3/12")
// composes, with an explicit canonical total winning over the embedded one.
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

// detectDroppedTotals finds the TRACKTOTAL/DISCTOTAL/MOVEMENTTOTAL keys whose value the pair
// compose cannot fit into a valid "n/total" frame, so the caller can warn. Scoped to a pair
// the edit touched; an embedded total in the number itself ("A1/12" alone) is not a drop.
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
			continue // neither field edited; the original frame is preserved
		}
		num, _ := edited.First(p.numKey)
		total, _ := edited.First(p.totKey)
		if _, totalDropped := p.compose(num, total); totalDropped {
			dropped = append(dropped, p.totKey)
		}
	}
	return dropped
}

// detectDroppedTrailingValues finds the changed keys whose value ends in a trailing empty
// (len > 1, final "") that a NUL-separated frame cannot store: it emits no trailing
// terminator, and decodeStringsTracked strips exactly that empty on read. Only v2.4 and
// v2.3 under ID3MultiNullSep write NUL-separated; repeat-frame keeps the empty as its own
// frame and slash-join collapses the whole value, so neither warns. Involved-people roles
// are excluded: detectDroppedInvolvedEmpties covers them (WRITER is a TXXX key and stays
// here). Sorted for deterministic warning order.
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

// detectDroppedEmptyValues finds the changed keys set to an all-empty value for which no
// frame was written, so the key reads back absent. Keys the date and total detectors
// already report are skipped so one drop warns once.
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

// detectDroppedInvolvedEmpties finds the changed involved-people role keys whose value
// carries an empty element. TIPL/IPLS store function/name pairs; renderUnit skips a
// nameless pair and decodeInvolvedPeople drops one on read, so an empty at any position
// vanishes on every version and policy. Sorted for deterministic order.
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

// TransferClassifier grades the fields whose ID3 transfer fate the format-level capability
// cannot express. A TRACKTOTAL, DISCTOTAL, or MOVEMENTTOTAL whose sibling number cannot
// join it in a valid "number/total" frame is Dropped, decided by the same compose the
// writer uses (see [AppendRebuildWarnings]); a lone total composes "/total" and is not
// dropped. A MOVEMENT already carrying pair syntax ("3/12") is Lossy: MVIN stores it, but
// the read path splits it into MOVEMENT and MOVEMENTTOTAL. MP3, AAC, AIFF, and WAV share
// it. It is a plain [core.FieldClassifier] and allocates no closure.
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

// detectNumericGenres returns the GENRE values this edit set that are a bare integer naming
// a standard genre by index ("17" -> "Rock"). Written verbatim to TCON, such a value reads
// back as the name, so the caller warns (see AppendRebuildWarnings) unless a native
// container keeps the literal number. Only changed values are reported.
//
// Known residuals: diff and copy treat a file holding "17" and one holding "Rock" as
// different; on ID3v2.3 a bare "17" is free text, so resolving it is non-conformant and
// the warning makes that visible; and a parenthesized "(17)" is escaped to "((17)" on
// disk and reads back literal, which is why isNumericGenreRef exempts a leading "(".
func detectNumericGenres(changed map[tag.Key]bool, edited tag.TagSet) []string {
	if !changed[tag.Genre] {
		return nil
	}
	vals, _ := edited.Get(tag.Genre)
	var out []string
	seen := map[string]bool{}
	for _, v := range vals {
		// De-duplicate so a repeated reference (GENRE=17,17) warns once; DowngradeNoOp
		// carries that one warning onto a no-op report.
		if isNumericGenreRef(v) && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// isNumericGenreRef reports whether v reads back as a different genre name after write: a
// bare reference the reader resolves, such as "17" to "Rock", "RX" to "Remix", or "CR" to
// "Cover". Values beginning with "(" are escaped by genreValues and round-trip verbatim.
// The resolver is the parser's.
func isNumericGenreRef(v string) bool {
	if strings.HasPrefix(v, "(") {
		return false // escaped by genreValues, so it round-trips verbatim
	}
	_, numeric := resolveGenres(v)
	return numeric
}

// detectDateFates classifies the changed date keys in one [classifyV23Date] pass into the
// fates a v2.3 tag can give them, so the three warnings cannot disagree about one value.
// v2.4 stores the full string in TDRC/TDOR and classifies nothing. A drop is checked for
// both keys, since TYER and TORY both need a 4-digit year. Reduction and coercion are
// scoped to RecordingDate: OriginalDate renders only TORY, whose sub-year loss the
// capability path reports (reducesToYear), and a year-only frame cannot respell anything.
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

// dateFate is what a v2.3 tag does to a date value, as one classification. detectDateFates
// and the two transfer-capability predicates below read the same answer.
type dateFate uint8

const (
	dateExact   dateFate = iota // reads back byte-identical
	dateDropped                 // no frame renders at all
	dateReduced                 // stored with a component the value carried missing
	dateCoerced                 // every component survives, spelled differently on read-back
)

// classifyV23Date reports what the read path gives back for a value stored by a v2.3 tag.
// v2.3 splits a date-time across TYER (YYYY), TDAT (DDMM) and TIME (HHMM), each
// all-or-nothing, and [composeV23Date] recomposes them on read. A component no frame
// captures is a reduction; a value whose components survive but respell (a space
// separator recomposed as 'T') is a coercion. It models the RecordingDate triple;
// OriginalDate shares only the drop answer, which turns on the year alone.
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

// v23DateDropped reports whether a v2.3 tag drops v entirely: a non-empty value with no
// valid 4-digit year renders no TYER/TORY frame. It is the transfer capability's
// value-drop predicate and reads the same classification detectDateFates does.
func v23DateDropped(v string) bool {
	return classifyV23Date(v) == dateDropped
}

// reducesDatePrecision reports whether a v2.3 tag stores iso with less precision than it
// carries: a month with no day ("2021-03"), an hour with no minute ("2021-03-15T10"), or
// seconds past a full minute ("2021-03-15T10:30:45"). A value with no extractable year
// drops entirely and is v23DateDropped's answer instead. [classifyV23Date] owns the rule.
func reducesDatePrecision(iso string) bool {
	return classifyV23Date(iso) == dateReduced
}

// v23DateNotVerbatim reports whether a v2.3 tag gives the value back changed: a component
// lost or the same components respelled. It is the transfer capability's fidelity test; a
// copy must grade either outcome Lossy, or the report promises a faithful carry that the
// plan's value-coerced warning contradicts and copy --strict fails on.
func v23DateNotVerbatim(iso string) bool {
	switch classifyV23Date(iso) {
	case dateReduced, dateCoerced:
		return true
	}
	return false
}

// hasSubYearPart reports whether iso carries content beyond an extractable 4-digit year:
// "2021-03" and "2021-03-15" do, "2021" does not. Compact and dotted forms ("20210503",
// "2021.05.03") have no extractable year and route to dropped instead.
func hasSubYearPart(iso string) bool {
	return len(iso) > 4 && extractDatePart(iso, partYear) != ""
}

// hasSubDayPart reports whether iso carries an hour or finer after a full date: a 'T' or
// space separator then a digit past the 10-char YYYY-MM-DD, so "2021-03-15T10" does and a
// bare date does not.
func hasSubDayPart(iso string) bool {
	return len(iso) >= 12 && (iso[10] == 'T' || iso[10] == ' ') && iso[11] >= '0' && iso[11] <= '9'
}

// hasSubMinutePart reports whether iso carries seconds or finer after a full minute: a ':'
// at index 16 then a digit at 17, so "2021-03-15T10:30:45" does, but "2021-03-15T10:30"
// and a zone-only "2021-03-15T10:30+05:00" do not. v2.3's HHMM TIME drops the seconds.
// The digit check avoids flagging a malformed trailing colon.
func hasSubMinutePart(iso string) bool {
	return len(iso) >= 18 && iso[16] == ':' && iso[17] >= '0' && iso[17] <= '9'
}

// AppendMalformedTailDropped appends a malformed-tag-entry-dropped warning when the
// rewritten tag carried a region the parser could not read (see [Tag.MalformedTail]); a
// rewrite renders frames plus padding, so those bytes are gone. It is the write-path
// counterpart of the malformed-tag-entry warning [Project] raises. A nil srcTag warns
// nothing. The four codecs that rewrite an ID3 tag share it.
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

// AppendRebuildWarnings appends warnings for losses found while rebuilding ID3 frames,
// such as dates that render no v2.3 frame, dates whose TDAT/TIME rendering drops
// precision, and malformed pictures dropped by a picture edit. Date warnings are
// suppressed when the re-projected output still holds the attempted value in another
// container, such as WAV's LIST/INFO ICRD. Keyed warnings let --strict name the field
// and keep the four ID3-backed codecs' wording aligned.
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
	// Trailing-empty drops are emitted unconditionally: retained is the already-stripped
	// re-projection, so a retained guard would suppress every one, and no container keeps
	// a trailing empty (WAV's LIST/INFO strips it too).
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
		// Same cross-container suppression as the reductions above.
		if v, _ := retained.First(cd.Key); v == cd.Value {
			continue
		}
		ws = core.WarnKeyed(ws, core.WarnValueCoerced,
			fmt.Sprintf("%s value %q is stored as separate ID3v2.3 date frames and reads back as %q",
				cd.Key, cd.Value, v23DateReadBack(cd.Value)), cd.Key)
	}
	for _, gv := range info.NumericGenres {
		// Suppress only where a native container keeps the literal number: WAV's LIST/INFO
		// IGNR retains "17", so no warning; on MP3/AAC/AIFF the retained GENRE reads back as
		// the name, so the warning fires.
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

// RebuildError returns the hard error for a rebuild loss that must fail the write: a
// synced-lyrics line or descriptor with an embedded NUL, which the NUL-terminated SYLT
// field would truncate. It returns nil otherwise. Each ID3-backed codec calls it beside
// CheckSize, so one waxerr.ErrInvalidData message covers MP3, AAC, WAV, and AIFF.
func RebuildError(info RebuildInfo) error {
	if info.SyncedLyricsInvalidNUL {
		return fmt.Errorf("%w: synced-lyrics line contains a NUL byte", waxerr.ErrInvalidData)
	}
	return nil
}

// CarryProjectionWarnings returns the warnings for a post-write MP3/AAC document.
// Front-tag codecs carry source parse warnings forward because buildResult cannot
// recompute the audio and container warnings (trailing-id3v1, legacy-ape, ...). An edit
// can resolve a warning the ID3 projection produced, so a reconciled code
// (chapters-flattened, invalid-picture, malformed-tag-entry) is dropped when
// newTagWarnings, Project(newTag).Warnings, lacks it; otherwise the returned document
// would disagree with a fresh parse. The remaining order is preserved.
func CarryProjectionWarnings(sourceWarnings, newTagWarnings []core.Warning) []core.Warning {
	out := core.CloneWarnings(sourceWarnings)
	for _, code := range []core.WarningCode{core.WarnChaptersFlattened, core.WarnInvalidPicture, core.WarnMalformedTagEntry} {
		if len(core.WarningsWithCode(newTagWarnings, code)) == 0 {
			out = core.WarningsWithoutCode(out, code)
		}
	}
	return out
}

// PerFieldCapabilities builds the per-key capability overrides shared by MP3, AAC, AIFF,
// and WAV. ORIGINALDATE is AccessPartial when the codec writes ID3v2.3, whose TORY frame
// keeps only the year; this drives the transfer grade and the value-reduced edit warning.
// GENRE is AccessPartial when --numeric-genre is set and the ID3 tag is the codec's
// authoritative genre store (genreViaID3: always for MP3/AAC/AIFF, for WAV only with an
// id3 chunk, since LIST/INFO IGNR keeps the genre as text). Without --numeric-genre the
// GENRE override is value-scoped: only a bare numeric reference grades Lossy. Returns nil
// when none applies.
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
		// Without --numeric-genre, a bare numeric reference written verbatim to TCON
		// still reads back as the genre name, so a transfer carrying one grades Lossy.
		// Write stays AccessFull: the edit path reports the loss through WarnNumericGenre.
		add(tag.Genre, core.WithValueReduction(core.Capability{
			Read: core.AccessFull, Write: core.AccessFull,
			Representation: "ID3 TCON",
			Fidelity:       "a bare numeric genre reference reads back as its genre name",
		}, isNumericGenreRef))
	}
	return perField
}

// reducesToYear reports whether storing iso in a year-only field (ID3v2.3 TORY) loses
// information: anything but a bare year, including a value with no parseable year. Unlike
// reducesDatePrecision, a full YYYY-MM-DD counts as reduced.
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

// extractDatePart pulls a component out of an ISO-8601 date "YYYY[-MM-DD[THH:MM]]". The
// year must be exactly 4 digits followed by end-of-string or '-', so a 5-digit year
// ("10000") or a compact/dotted form ("20210503", "2021.05") yields no year and routes to
// dropped (v23DateDropped) instead of truncating to a wrong value.
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
		// Accept 'T' or a space as the date-time separator, as hasSubDayPart does; otherwise
		// "2021-03-15 10:30" would be judged reducible yet yield no TIME frame.
		if len(iso) >= 16 && (iso[10] == 'T' || iso[10] == ' ') && iso[13] == ':' {
			return iso[11:13] + iso[14:16] // HHMM
		}
	}
	return ""
}

// genreFrames renders the TCON frame(s) for the edited genre, or no frame when the field
// is absent or all-empty (the genre read path drops an empty TCON; detectDroppedEmptyValues
// reports the drop). The writer and EncodingRewriteNeeded both go through it, so the
// predicate cannot compute a different render than the write.
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

// EncodingRewriteNeeded reports whether re-rendering under the requested write-encoding
// options would store a different representation of an unchanged canonical value, so a
// codec's no-op fast path lets the write through. A nil src (no ID3 container) has nothing
// stored to differ from. Only an explicitly requested option counts: the absence of
// --numeric-genre is not a request to re-encode "(17)" back to its name, so repeated runs
// converge. A stored form the conversion cannot improve is left alone; the guards in
// encodingRewriteNeeded say which. Numeric genre is the only option wired; the v2.3
// multi-value policy is the known second case but needs a policy-explicit write option
// first.
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
	// Compare decoded value lists on both sides. Rendering and decoding back normalizes the
	// slash join, the repeat-frame count, and the text encoding, so a Latin-1 frame does
	// not read as different from a UTF-16 one.
	want := renderedGenreValues(version, edited, opts)
	if len(want) == 0 {
		return false // the edit drops the genre; a removal is not a re-encoding
	}
	// The conversion must have produced a reference. A special reference (RX/CR) has no
	// ID3v1 index, a literal beginning with "(" is only escaped, and a slash join disables
	// the conversion; firing in those cases would replace a stored reference with its name
	// or apply an unrelated escaping.
	if !slices.ContainsFunc(want, generatedReference(edited, version)) {
		return false
	}
	// A stored value packing several canonical values into one (ID3v2.3's "(17)(8)" and
	// "(17)Hard" forms) cannot be re-rendered: the writer emits one value per entry, which
	// would downgrade a spec-legal frame to the NUL-separated extension. Leave it alone.
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

// renderedGenreValues renders the genre under opts and decodes the result back, giving
// the value list the write would store. Going through the renderer normalizes the join,
// the frame count, and the text encoding, so encodingRewriteNeeded's comparison is
// symmetric.
func renderedGenreValues(version byte, edited tag.TagSet, opts WriteOpts) []string {
	frames, _ := genreFrames(version, edited, opts)
	var out []string
	for _, f := range frames {
		out = append(out, DecodeText(f)...)
	}
	return out
}

// genreValues converts standard genre names to numeric references when numeric is set;
// other names pass through. Literal names beginning with "(" are escaped at positions
// where the reader would parse "(ref)" syntax; generated references are never escaped. A
// multi-value ID3MultiSlash join skips numeric conversion: a reference inside the joined
// "a / b / c" value would parse back as a reference plus a slash-prefixed refinement.
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

// genreReference returns the write version's reference form for a value naming a standard
// genre, "(17)" in v2.3 and "17" in v2.4, and whether one exists. A bare reference ("17")
// is resolved through the read path first, so one --numeric-genre pass normalizes it to
// the version's form instead of leaving a library mixing "17" and "(17)". A parenthesized
// value is not resolved: "(17)" is stored as the escaped literal "((17)", which the escape
// branch owns. RX and CR resolve to names with no ID3v1 index and keep the stored
// reference.
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
