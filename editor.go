package waxlabel

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// ResolveAlias maps DATE/YEAR -> RECORDINGDATE, TOTALTRACKS -> TRACKTOTAL, etc.
// Non-aliases returned unchanged.
func ResolveAlias(key tag.Key) tag.Key { return mapping.ResolveAlias(key) }

// Editor records mutations against a [Document]. [Editor.Prepare] builds a [Plan].
// Methods return the editor for chaining.
type Editor struct {
	doc         *Document
	base        *core.Media
	patch       tag.TagPatch
	pictures    []core.Picture
	picsTouched bool
	// Parallel to pictures: true if AddPicture added it (Prepare validates those).
	addedMask           []bool
	chapters            []core.Chapter
	chaptersTouched     bool
	syncedLyrics        []core.SyncedLyrics
	syncedLyricsTouched bool
	// After ClearSyncedLyrics: ID3 SYLT must not inherit dest language/descriptor.
	syncedLyricsCleared bool
	outputGain          int
	outputGainTouched   bool
	// Transfer carry: suppress authoring sanity warnings.
	carried bool
	// 1-based LRC lines dropped; Prepare warns WarnSyncedLyricsLineDropped.
	syncedLyricsDroppedLines []int
	// Roles that matched no picture; Prepare warns WarnPictureSelectorMiss.
	pictureSelectorMisses []string
}

// Apply appends a patch (later ops win). Keys go through [ResolveAlias].
func (e *Editor) Apply(p tag.TagPatch) *Editor {
	e.patch.Append(p.MapKeys(ResolveAlias))
	return e
}

// Set replaces a key's values ([ResolveAlias]). No values -> absent at Prepare.
// Set(key, "") stores one empty value. Slash "n/total" on track/disc splits at Prepare.
func (e *Editor) Set(key tag.Key, vals ...string) *Editor {
	e.patch.Set(ResolveAlias(key), vals...)
	return e
}

// Clear removes a key ([ResolveAlias]).
func (e *Editor) Clear(key tag.Key) *Editor {
	e.patch.Clear(ResolveAlias(key))
	return e
}

// Add appends values ([ResolveAlias]).
func (e *Editor) Add(key tag.Key, vals ...string) *Editor {
	e.patch.Add(ResolveAlias(key), vals...)
	return e
}

// SetTags applies non-empty [tag.Tags] fields as Sets (cannot clear).
func (e *Editor) SetTags(t tag.Tags) *Editor { return e.Apply(t.Patch()) }

// AddPicture appends a picture. Sniff sets MIME/dimensions; unrecognized needs
// [WithUnrecognizedPictures].
func (e *Editor) AddPicture(p Picture) *Editor {
	p.SniffAuthoritative()
	p.Data = append([]byte(nil), p.Data...)
	for len(e.addedMask) < len(e.pictures) {
		e.addedMask = append(e.addedMask, false)
	}
	e.pictures = append(e.pictures, p)
	e.addedMask = append(e.addedMask, true)
	e.picsTouched = true
	return e
}

// RemovePictures drops pictures where match is true (evaluated once each).
func (e *Editor) RemovePictures(match func(Picture) bool) *Editor {
	pics := make([]core.Picture, 0, len(e.pictures))
	mask := make([]bool, 0, len(e.pictures))
	for i, p := range e.pictures {
		// Edit seeds e.pictures with the shallow core.ClonePictures, so p.Data aliases the
		// Document's bytes. Hand match a Data-detached copy, so a predicate that writes
		// p.Data cannot mutate the Document or race doc.Pictures(). The retained
		// e.pictures keeps the shallow share.
		probe := p
		probe.Data = append([]byte(nil), p.Data...)
		if match(probe) {
			continue
		}
		pics = append(pics, p)
		mask = append(mask, i < len(e.addedMask) && e.addedMask[i])
	}
	e.pictures, e.addedMask = pics, mask
	e.picsTouched = true
	return e
}

// ClearPictures removes all pictures.
func (e *Editor) ClearPictures() *Editor {
	e.pictures = nil
	e.addedMask = nil
	e.picsTouched = true
	return e
}

// SetChapters replaces chapters (sorted by start). Cap checked at Prepare.
func (e *Editor) SetChapters(chs ...Chapter) *Editor {
	e.chapters = normalizeChapters(chs)
	e.chaptersTouched = true
	return e
}

// normalizeChapters copies and sorts by start ([Editor]/[Transfer].SetChapters).
func normalizeChapters(chs []Chapter) []core.Chapter {
	out := core.CloneChapters(chs)
	core.SortChaptersByStart(out)
	return out
}

// ClearChapters removes all chapters.
func (e *Editor) ClearChapters() *Editor {
	e.chapters = nil
	e.chaptersTouched = true
	return e
}

// SetSyncedLyrics replaces synced-lyrics sets (lines sorted, deep-copied).
// After ClearSyncedLyrics, does not inherit dest ID3 SYLT language.
func (e *Editor) SetSyncedLyrics(sls ...SyncedLyrics) *Editor {
	e.syncedLyrics = normalizeSyncedLyrics(sls)
	e.syncedLyricsTouched = true
	return e
}

// normalizeSyncedLyrics drops empty sets; deep-copies and sorts lines by time.
func normalizeSyncedLyrics(sls []SyncedLyrics) []core.SyncedLyrics {
	out := make([]core.SyncedLyrics, 0, len(sls))
	for _, sl := range sls {
		if len(sl.Lines) == 0 {
			continue
		}
		sl.Lines = slices.Clone(sl.Lines)
		slices.SortStableFunc(sl.Lines, func(a, b SyncedLine) int { return cmp.Compare(a.Time, b.Time) })
		out = append(out, sl)
	}
	return out
}

// NoteSyncedLyricsDropped records dropped LRC line numbers for Prepare warnings.
func (e *Editor) NoteSyncedLyricsDropped(lines ...int) *Editor {
	if len(lines) > 0 {
		e.syncedLyricsDroppedLines = append(e.syncedLyricsDroppedLines, lines...)
	}
	return e
}

// NotePictureSelectorMiss records roles that matched no picture (Prepare warns).
func (e *Editor) NotePictureSelectorMiss(roles ...string) *Editor {
	if len(roles) > 0 {
		e.pictureSelectorMisses = append(e.pictureSelectorMisses, roles...)
	}
	return e
}

// ClearSyncedLyrics removes synced lyrics and marks clear-then-set as fresh.
func (e *Editor) ClearSyncedLyrics() *Editor {
	e.syncedLyrics = nil
	e.syncedLyricsTouched = true
	e.syncedLyricsCleared = true
	return e
}

// SetOutputGain sets Opus output_gain (signed Q7.8 dB, 256 = +1 dB). Prepare
// rebases R128_* unless [WithKeepR128Gains] or an explicit Set/Clear of those keys.
func (e *Editor) SetOutputGain(gain int) *Editor {
	e.outputGain = gain
	e.outputGainTouched = true
	return e
}

// Native returns the parsed native document (no pending editor changes).
func (e *Editor) Native() NativeEditor {
	return NativeEditor{base: e.base}
}

// Prepare resolves mutations into a [Plan]. No I/O; Report matches Execute.
func (e *Editor) Prepare(opts ...WriteOption) (*Plan, error) {
	wo := resolveWriteOptions(opts)
	// Carried lets codecs skip author-convenience heuristics on a transfer (e.g. the
	// ID3 SYLT language fallback). transfer.go sets it for every carry path.
	wo.Carried = e.carried
	wo.Touched = touchedKeys(e.patch)
	// SyncedLyricsCleared makes an ID3 SYLT rewrite skip its language/descriptor
	// fallback, so a cleared-then-authored set does not inherit the destination's SYLT
	// metadata. Distinct from Carried, which would mislabel the edit.
	wo.SyncedLyricsCleared = e.syncedLyricsCleared

	// An editor from a zero-value Document has no base media to plan against.
	if e.base == nil {
		return nil, fmt.Errorf("%w: document is not initialized; use ParseFile/Parse", waxerr.ErrInvalidData)
	}
	// The output gain is a signed 16-bit Q7.8 field; a value outside it has no encoding.
	if e.outputGainTouched && (e.outputGain < math.MinInt16 || e.outputGain > math.MaxInt16) {
		return nil, fmt.Errorf("%w: output gain %d is outside the signed 16-bit Q7.8 range", waxerr.ErrInvalidData, e.outputGain)
	}

	// Refuse to plan a write for a file with no audio (WarnNoAudioFrames): it would
	// re-render metadata around non-audio bytes. Every editing path (set/plan, lint
	// --fix, a copy's destination editor) funnels through Prepare, so this one check
	// covers them all (exit 4). It is a base-document check, not gated on carried. A
	// no-audio file is still readable, so copying tags out of one is allowed.
	if hasNoAudioWarning(e.base) {
		return nil, fmt.Errorf("%w: file has no audio essence; refusing to write metadata to a no-audio file", waxerr.ErrInvalidData)
	}

	// Reject an invalid key (e.g. one containing '=') before it reaches the native
	// writer. patchKeys is reused by the NUL scan below.
	patchKeys := e.patch.Keys()
	for _, k := range patchKeys {
		if !k.Valid() {
			return nil, fmt.Errorf("%w: %q (keys are uppercase ASCII 0x20-0x7D without '=' (spaces and punctuation are allowed, '~' is not); build them with tag.ParseKey or tag.MustKey, which accept any case)", waxerr.ErrInvalidKey, k)
		}
	}

	// Share the native document and properties instead of deep-copying: planning
	// only reads the native and re-clones the blocks it keeps. Only the canonical
	// tags (cloned by the patch) and the picture set are replaced.
	editedTags := e.patch.Apply(e.base.Tags)
	// Make a key with a zero-length value slice absent before the codec plans or
	// Changes diffs. A Set/Add of no values leaves the key present-but-empty, which
	// no codec persists, so the plan would report a phantom change. A present [""]
	// (what `set KEY=` produces) is a distinct value and is kept.
	dropEmptyValuedKeys(&editedTags)
	// Reject a NUL byte or invalid UTF-8 in any value, chapter title, or picture
	// description this edit introduces. A NUL truncates the field on C-string
	// formats; invalid UTF-8 is reprojected on read (ID3 to U+FFFD, an MP4 chapter
	// title to ""), so the written result would not equal a fresh parse.
	if err := e.rejectInvalidValues(editedTags, patchKeys); err != nil {
		return nil, err
	}
	// Trim numeric values this edit introduces before the number-pair split, so the
	// stored form matches what validation and parsing use. Carried values are kept.
	trimTokenValues(&editedTags, e.patch)
	// Split a slash-combined "n/total" track or disc number this edit introduced into
	// the pair every format stores. It runs after rejectInvalidValues: that scan
	// covers only patched keys, so splitting first would move a NUL from "3/\x00"
	// into an unscanned derived TRACKTOTAL. The conflict warnings it returns are
	// surfaced below, gated on !e.carried.
	numberConflicts := splitNumberPairs(&editedTags, e.patch)
	edited := &core.Media{
		Format:       e.base.Format,
		Properties:   e.base.Properties,
		Tags:         editedTags,
		Pictures:     e.base.Pictures,
		Chapters:     e.base.Chapters,
		SyncedLyrics: e.base.SyncedLyrics,
		Families:     e.base.Families,
		// Codec result builders recompute this from the bytes they write, but a no-op
		// path returns this Media directly, so it must carry the file's legacy state.
		LegacyOpaqueContent: e.base.LegacyOpaqueContent,
		Warnings:            e.base.Warnings,
		Native:              e.base.Native,
		Identity:            e.base.Identity,
		AudioStart:          e.base.AudioStart,
		AudioEnd:            e.base.AudioEnd,
		AudioRanges:         e.base.AudioRanges,
	}
	if e.picsTouched {
		edited.Pictures = e.pictures
	}
	if e.chaptersTouched {
		edited.Chapters = e.chapters
	}
	if e.syncedLyricsTouched {
		edited.SyncedLyrics = e.syncedLyrics
	}
	// Enforce the icon-count rule only when this edit authored the picture set, so
	// duplicate type-1 or type-2 icons already in the file do not block a tags-only
	// edit or lint fix. A carry authors nothing, so a copy must not reject the
	// source's own duplicate icons; lint still flags the carried result.
	if e.picsTouched && !e.carried {
		if err := validatePictures(edited.Pictures); err != nil {
			return nil, err
		}
	}
	// Validate only pictures added on this editor, not the file's pre-existing ones.
	// Without it, junk or empty AddPicture bytes would embed as
	// application/octet-stream. The CLI checks earlier in loadCovers; this is the
	// library-side net. WithUnrecognizedPictures opts an exotic cover back in; the
	// transfer engine opts out wholesale.
	if !wo.AllowUnrecognizedPictures {
		if err := validateAddedPictures(e.pictures, e.addedMask); err != nil {
			return nil, err
		}
	}

	codec, ok := core.ForFormat(e.base.Format)
	if !ok {
		return nil, fmt.Errorf("%w: no writer for %s", waxerr.ErrUnsupportedFormat, e.base.Format)
	}
	// Compute capabilities once under these write options. The chapter gate below and
	// the value-reduction check after planning must read the same write policy.
	caps := codec.Capabilities(e.base, wo)
	// The output gain lives in the stream header, so no tag/picture/chapter/lyric
	// comparison sees it, and the ASF and fragmented-MP4 planners return a no-op when
	// those are equal. Without this gate a gain edit on such a file would exit 0
	// having written nothing. A read-only file refuses with the codec's own reason.
	var outputGainDropped bool
	gainChanged := e.outputGainTouched && e.outputGain != e.base.Properties.First().OutputGain
	if gainChanged {
		switch {
		case caps.ReadOnly:
			return nil, readOnlyRefusal(caps)
		case caps.OutputGain < core.AccessFull:
			if !wo.AllowUnsupportedDrop {
				return nil, fmt.Errorf("%w: an output gain cannot be written to %s %s file",
					waxerr.ErrUnsupportedTag, core.IndefiniteArticle(e.base.Format.String()), e.base.Format)
			}
			outputGainDropped = true
		}
	}
	// RFC 7845 applies the R128 tags on top of the header gain, so rebase them by the
	// same delta to keep the loudness. edited copied the TagSet by value, so
	// editedTags is reassigned onto it.
	var r128Warnings []core.Warning
	if gainChanged && !outputGainDropped {
		var err error
		r128Warnings, err = rebaseR128Gains(&editedTags, e.patch,
			e.outputGain-e.base.Properties.First().OutputGain, wo.KeepR128Gains)
		if err != nil {
			return nil, err
		}
		edited.Tags = editedTags
	}

	// A structural edit the destination cannot store is a hard error by default, or
	// under AllowUnsupportedDrop is removed with a warning so the storable part still
	// applies. A dropped item resets edited.X and skips its metadata-loss and sanity
	// warnings below, so exactly one warning surfaces per drop. The drops run before
	// the chapter reconcile and codec.Plan. In a transfer, ProjectTransfer marks such
	// an item Dropped before it is set, so the touched flags are false and none of
	// this fires.
	var chaptersDropped, syncedLyricsDropped, picturesDropped bool

	// Skip the structural gates for a read-only file. Dropping an item there would
	// report the format's storage limits ("a WMA file cannot store chapters") and let
	// the edit collapse into an exit-0 no-op, while the same tag edit exits 3.
	// Leaving the item in place lets the codec's own refusal name the reason.
	structuralGates := !caps.ReadOnly

	// Chapters: refuse or drop a chapter edit on a format that cannot write chapters,
	// whether it has no store or a read-only one (Musepack's SV8 packets). The gate
	// is a change against the file's own list, so ClearChapters on a chapterless
	// format is a no-op while a clear on a read-only store is refused.
	if structuralGates && e.chaptersTouched && caps.Chapters.Write < core.AccessPartial && !core.EqualChapters(e.chapters, e.base.Chapters) {
		if !wo.AllowUnsupportedDrop {
			return nil, fmt.Errorf("%w: chapters cannot be written to %s %s file",
				waxerr.ErrUnsupportedTag, core.IndefiniteArticle(e.base.Format.String()), e.base.Format)
		}
		edited.Chapters = e.base.Chapters
		chaptersDropped = true
	}
	// The chapter-count limit is a hard error even under the drop option: ID3 CTOC
	// and MP4 Nero chpl use single-byte counts, so 256 entries would be malformed,
	// and truncating a list is worse than refusing. Transfers apply the limit before
	// SetChapters, so this is for direct edits.
	if !chaptersDropped && e.chaptersTouched && caps.Chapters.MaxItems > 0 && len(e.chapters) > caps.Chapters.MaxItems {
		return nil, fmt.Errorf("%w: %d chapters exceeds the %d %s can store",
			waxerr.ErrUnsupportedTag, len(e.chapters), caps.Chapters.MaxItems, e.base.Format)
	}
	// Synced lyrics: refuse or drop an authored set on a format with no store. MP4
	// and Matroska timed lyric tracks are outside this model. A clear is a no-op.
	if structuralGates && e.syncedLyricsTouched && len(e.syncedLyrics) > 0 && caps.SyncedLyrics.Write < core.AccessPartial {
		if !wo.AllowUnsupportedDrop {
			return nil, fmt.Errorf("%w: synced lyrics cannot be written to %s %s file",
				waxerr.ErrUnsupportedTag, core.IndefiniteArticle(e.base.Format.String()), e.base.Format)
		}
		edited.SyncedLyrics = nil
		syncedLyricsDropped = true
	}
	// The synced-lyrics set-count limit stays a hard error (the LRC store holds a single set).
	if !syncedLyricsDropped && e.syncedLyricsTouched && caps.SyncedLyrics.MaxItems > 0 && len(e.syncedLyrics) > caps.SyncedLyrics.MaxItems {
		return nil, fmt.Errorf("%w: %d synced-lyrics sets exceeds the %d %s can store",
			waxerr.ErrUnsupportedTag, len(e.syncedLyrics), caps.SyncedLyrics.MaxItems, e.base.Format)
	}
	// Cover art: WebM excludes the Attachments element. Under the drop option, drop
	// the cover edit here; otherwise the Matroska writer's plan-time refusal (keyed on
	// the same capability) applies. The gate is a change against the file's own set,
	// so clearing a WebM file's cover is dropped with a warning.
	if structuralGates && wo.AllowUnsupportedDrop && e.picsTouched && caps.Pictures.Write < core.AccessPartial && !core.EqualPictures(e.pictures, e.base.Pictures) {
		edited.Pictures = e.base.Pictures
		picturesDropped = true
	}
	// Cover format: a destination that labels only some image formats (MP4's covr:
	// JPEG/PNG/BMP) drops the covers it cannot label and keeps the rest, so the rest
	// of the edit still applies. Without the drop option the codec's checkCoverFormats
	// refuses (a direct AddPicture of a GIF still fails). The >= AccessPartial guard
	// and !picturesDropped exclude the WebM whole-set case above. Partition once so
	// the kept slice and its added-mask stay aligned for the sanity warnings.
	keptPics, keptMask := e.pictures, e.addedMask
	var pictureFormatsDropped bool
	var droppedPictureMIMEs []string
	if wo.AllowUnsupportedDrop && e.picsTouched && !picturesDropped && len(e.pictures) > 0 &&
		caps.Pictures.Write >= core.AccessPartial {
		kept, keptIdx, dropped := core.PartitionRepresentable(caps.Pictures, e.pictures)
		if len(dropped) > 0 {
			mask := make([]bool, len(kept))
			for i, orig := range keptIdx {
				mask[i] = orig < len(e.addedMask) && e.addedMask[orig]
			}
			edited.Pictures = kept
			keptPics, keptMask = kept, mask
			pictureFormatsDropped = true
			droppedPictureMIMEs = dropped
		}
	}
	// Picture slots: a store of uniquely-named slots (APE's two Cover Art items)
	// holds one picture per slot. The added-aware partition resolves the set here,
	// where the authored pictures are known: an added picture claims the slot of a
	// pre-existing same-role one (adding a front cover replaces the file's front),
	// and an added picture with no slot is refused, or dropped with a warning under
	// the drop option. A displaced pre-existing picture is dropped with its own
	// warning. A transfer never conflicts here: PrepareTransfer filters the source
	// set through the same partition. Resolving before the plan keeps the picture
	// sanity warnings below accurate about the set written.
	var slotDroppedRoles, slotReplacedRoles []core.PictureType
	var slotReason string
	if structuralGates && e.picsTouched && !picturesDropped && len(keptPics) > 0 &&
		caps.Pictures.Write >= core.AccessPartial {
		if keptIdx, reason, ok := core.PartitionPictureSlotsEdited(caps.Pictures, keptPics, keptMask); ok && len(keptIdx) < len(keptPics) {
			slotReason = reason
			keptFlag := make([]bool, len(keptPics))
			for _, i := range keptIdx {
				keptFlag[i] = true
			}
			for i, p := range keptPics {
				if keptFlag[i] {
					continue
				}
				if i < len(keptMask) && keptMask[i] {
					slotDroppedRoles = append(slotDroppedRoles, p.Type)
				} else {
					slotReplacedRoles = append(slotReplacedRoles, p.Type)
				}
			}
			if len(slotDroppedRoles) > 0 && !wo.AllowUnsupportedDrop {
				return nil, fmt.Errorf("%w: the %s picture cannot be stored in %s %s file (%s)",
					waxerr.ErrUnsupportedTag, slotDroppedRoles[0],
					core.IndefiniteArticle(e.base.Format.String()), e.base.Format, slotReason)
			}
			kept := make([]core.Picture, 0, len(keptIdx))
			mask := make([]bool, 0, len(keptIdx))
			for _, i := range keptIdx {
				kept = append(kept, keptPics[i])
				mask = append(mask, i < len(keptMask) && keptMask[i])
			}
			edited.Pictures = kept
			keptPics, keptMask = kept, mask
		}
	}
	// Truncate an over-cap synced-lyrics set to the per-set line cap before planning,
	// so the plan result and the written file agree on the line count.
	var syncedLyricsTruncated bool
	if e.syncedLyricsTouched && !syncedLyricsDropped && len(edited.SyncedLyrics) > 0 {
		if capped, truncated := core.TruncateSyncedLyrics(edited.SyncedLyrics); truncated {
			edited.SyncedLyrics = capped
			syncedLyricsTruncated = true
		}
	}
	// Do not reject a parsed 1-2 byte SYLT language here: some files store NUL-padded
	// short codes, which the writer preserves; longer values are truncated to SYLT's
	// three bytes. The CLI validates --synced-lyrics-lang before this path.
	//
	// Reconcile any overlap this chapter edit introduced before planning. Inserting a
	// start-only chapter between ended chapters leaves the preceding end overlapping
	// the insert; truncating it to the next start fixes the ID3/Matroska overlap and
	// the spurious MP4 chapter-metadata-dropped warning. Reconcile into a clone so a
	// repeated Prepare recomputes identically. Skipped on a carry.
	var chaptersReconciled bool
	if e.chaptersTouched && !e.carried && !chaptersDropped {
		reconciled := core.CloneChapters(e.chapters)
		if core.ReconcileChapterOverlaps(reconciled, e.base.Chapters) {
			edited.Chapters = reconciled
			chaptersReconciled = true
		}
	}
	// edited shares the base's Properties, so overlay a clone. Every format that
	// writes an output gain reports a track; the length check is defensive.
	if gainChanged && !outputGainDropped && len(e.base.Properties.Tracks) > 0 {
		props := e.base.Properties.Clone()
		props.Tracks[0].OutputGain = e.outputGain
		edited.Properties = props
	}
	wp, err := codec.Plan(context.Background(), e.base, edited, wo)
	if err != nil {
		return nil, err
	}
	// Chapter sanity warnings (a start past the file end, two chapters sharing a
	// start) go on the plan report. Only chapters this edit introduces are checked:
	// the CLI's --add-chapter merges the file's chapters into the SetChapters list. A
	// carry authors nothing and a dropped list is not written, so both skip these.
	if e.chaptersTouched && !e.carried && !chaptersDropped {
		wp.Report.Warnings = appendChapterWarnings(wp.Report.Warnings, e.chapters, e.base.Chapters, e.base.Properties.Duration())
		// Matroska/WebM store explicit chapter ends. A CLI chapter rebuild has no
		// end-time syntax, so warn when it replaces ended chapters with open-ended ones.
		if matroskaChapterEndsDropped(e.base.Format, e.chapters, e.base.Chapters) {
			wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnChapterEndsDropped,
				"chapters rewrite drops explicit end times (CLI-built chapters are open-ended)")
		}
		// Warn when the destination cannot store every field of the authored chapters.
		// This reads edited.Chapters (the reconciled list), not e.chapters: a stale end
		// truncated to the next start is inferable, so a start-title format does not
		// report a spurious gapped-end loss. An interior gap (End < next.Start) or an
		// on-disk overlap still warns.
		if loss := caps.Chapters.ChapterLoss; core.ChaptersLoseMetadata(edited.Chapters, loss) {
			wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnChapterMetadataDropped,
				core.ChapterMetadataDroppedMessage(loss))
		}
		// Note the truncation. Informational; it does not escalate --strict.
		if chaptersReconciled {
			wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnChapterOverlapReconciled,
				"a chapter's end overlapped the next chapter's start and was truncated to keep the chapters non-overlapping")
		}
	}
	// A carry still warns about chapters past the destination's duration (a
	// destination-fit signal) while skipping the authoring warnings above. Every
	// copied chapter is new, so no isNew gate: one that equals a pre-existing
	// destination chapter and still overshoots must warn.
	if e.chaptersTouched && e.carried && !chaptersDropped {
		dur := e.base.Properties.Duration()
		for _, c := range core.ChaptersPastDuration(e.chapters, dur) {
			wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnChapterPastDuration,
				core.ChapterPastDurationMessage(c.Start, dur))
		}
	}
	// Warn when the destination cannot store every field of the authored synced
	// lyrics: the LRC store drops the per-set language and descriptor. A transfer is
	// graded in its transfer report; a set dropped whole is not written.
	if e.syncedLyricsTouched && !e.carried && !syncedLyricsDropped {
		if loss := caps.SyncedLyrics.SyncedLyricsLoss; core.SyncedLyricsLoseMetadata(e.syncedLyrics, loss) {
			wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnSyncedLyricsMetadataDropped,
				core.SyncedLyricsMetadataDroppedMessage())
		}
	}
	// LegacyStrip destroys legacy-only values; warn what goes. Against edited tags
	// (setting a key the legacy held is not a loss). Outside !carried: user asked for
	// strip. PlanLintFix never reaches this. WAV/AIFF reuse LegacyStrip differently.
	if wo.Legacy == core.LegacyStrip {
		// A key the file already carried canonically was never legacy-only, whatever
		// this edit did to it. Without the filter, --clear TITLE --legacy strip would
		// report TITLE as held only in the legacy container. It also keeps lint --fix
		// consistent: PlanLintFix clears a stamped ENCODER, and a legacy container
		// echoing that stamp would otherwise read as legacy-only and fail under --strict.
		lost := slices.DeleteFunc(core.LegacyOnlyKeys(e.base.Families, editedTags),
			func(k tag.Key) bool { return e.base.Tags.Has(k) })
		if len(lost) > 0 || e.base.LegacyOpaqueContent {
			wp.Report.Warnings = core.WarnKeyed(wp.Report.Warnings, core.WarnLegacyStripDropped,
				core.LegacyStripDroppedMessage(lost, e.base.LegacyOpaqueContent), lost...)
		}
	}
	// Report the structural drops and the synced-lyrics truncation on the plan
	// report, where --strict escalates them. A drop surfaces even when the remaining
	// edit is a byte-identical no-op.
	if chaptersDropped {
		msg := core.ChaptersUnsupportedMessage(e.base.Format)
		if caps.Chapters.Read != core.AccessNone {
			msg = core.ChaptersReadOnlyMessage(e.base.Format, len(e.base.Chapters) > 0)
		}
		wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnChaptersUnsupported, msg)
	}
	if syncedLyricsDropped {
		wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnSyncedLyricsUnsupported,
			core.SyncedLyricsUnsupportedMessage(e.base.Format))
	}
	if outputGainDropped {
		wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnOutputGainUnsupported,
			core.OutputGainUnsupportedMessage(e.base.Format))
	}
	wp.Report.Warnings = append(wp.Report.Warnings, r128Warnings...)
	if picturesDropped {
		msg := core.PictureUnsupportedMessage()
		if len(e.base.Pictures) > 0 {
			msg = core.PicturesReadOnlyMessage()
		}
		wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnPictureUnsupported, msg)
	}
	// A cover-format drop warns from the drop flag, not the plan: when every added
	// cover is unrepresentable the kept set equals base and the codec's
	// checkCoverFormats never runs. It names the MIMEs and uses
	// WarnPictureUnsupported so --strict escalates it like WebM.
	if pictureFormatsDropped {
		wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnPictureUnsupported,
			core.UnrepresentableReason(e.base.Format, droppedPictureMIMEs))
	}
	// Slot losses resolved above: an added picture with no slot, and a pre-existing
	// picture an added one displaced. Worded like the APE writer's own warning, and
	// emitted from the recorded lists so the loss survives a byte-identical no-op.
	for _, role := range slotDroppedRoles {
		wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnPictureUnsupported,
			fmt.Sprintf("the %s picture was dropped: %s", role, slotReason))
	}
	for _, role := range slotReplacedRoles {
		wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnPictureUnsupported,
			fmt.Sprintf("the file's %s picture was replaced by this edit's picture: %s", role, slotReason))
	}
	if syncedLyricsTruncated {
		wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnSyncedLyricsTruncated,
			core.SyncedLyricsTruncatedMessage())
	}
	// Input diagnostics the CLI recorded on the editor: LRC lines dropped during
	// parse, and a picture-removal role that matched nothing. Both go on the plan
	// report so --strict escalates them.
	if n := len(e.syncedLyricsDroppedLines); n > 0 {
		wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnSyncedLyricsLineDropped, fmt.Sprintf(
			"%d synced-lyric line(s) had no timestamp and were dropped (lines: %s)", n, formatLineList(e.syncedLyricsDroppedLines)))
	}
	for _, role := range e.pictureSelectorMisses {
		wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnPictureSelectorMiss, fmt.Sprintf(
			"no %s picture to remove; the role matched nothing in this file", role))
	}
	// Picture sanity warnings for the pictures this edit added (addedMask): an
	// unrecognized image embedded under WithUnrecognizedPictures, an added duplicate,
	// or an added second front cover. Pre-existing art is the linter's concern. A
	// carry authors nothing and a dropped set is not written, so both skip these.
	if e.picsTouched && !e.carried && !picturesDropped {
		// The kept set and its mask equal e.pictures/e.addedMask unless a cover format
		// was dropped; a dropped --force GIF then draws no invalid/duplicate note.
		wp.Report.Warnings = appendPictureWarnings(wp.Report.Warnings, keptPics, keptMask)
	}
	// Warn about a known single-valued key the edit leaves holding multiple values,
	// which the typed projection collapses to its first value. The warning names the
	// keys the CLI's --strict gate acts on. A carry must not flag the source's own
	// values, so it is suppressed.
	if !e.carried {
		// The single-valued-multi check judges the edit intent (edited.Tags), not the
		// codec's result: a format that collapses the value in its result (Matroska's
		// Info.Title) would otherwise hide the loss. Diffing base->intent avoids
		// re-flagging an untouched pre-existing multi.
		wp.Report.Warnings = appendSingleValuedWarnings(wp.Report.Warnings, e.base.Tags, edited.Tags)
		// The legacy-conflict check judges the plan's result tags (what the codec
		// writes): a re-projected value such as GENRE=17 written as "Rock" must not read
		// as a conflict when the written value still agrees.
		result := planResultTags(wp, edited)
		wp.Report.Warnings = appendLegacyConflictWarnings(wp.Report.Warnings, e.base.Families, e.patch, result, wo.Legacy)
		// Warn when a patched value is reduced by the destination's field-level write
		// capability, using the same projected result tags as the legacy conflict check.
		wp.Report.Warnings = appendValueReducedWarnings(wp.Report.Warnings, caps, patchKeys, editedTags, result)
		// The track/disc total-vs-slash conflicts computed at the number-pair split.
		wp.Report.Warnings = append(wp.Report.Warnings, numberConflicts...)
	}
	return &Plan{doc: e.doc, plan: wp, opts: wo}, nil
}

// rejectInvalidValues refuses NUL or invalid UTF-8 in touched tag values, added
// picture descriptions, and chapter titles/languages (C-string / round-trip safety).
func (e *Editor) rejectInvalidValues(editedTags tag.TagSet, keys []tag.Key) error {
	for _, k := range keys {
		vals, ok := editedTags.Get(k)
		if !ok {
			continue
		}
		for _, v := range vals {
			if err := checkWritableText(v, fmt.Sprintf("tag value for %q", k)); err != nil {
				return err
			}
		}
	}
	for i, p := range e.pictures {
		if i < len(e.addedMask) && e.addedMask[i] {
			if err := checkWritableText(p.Description, "picture description"); err != nil {
				return err
			}
		}
	}
	for _, c := range e.chapters {
		if err := checkWritableText(c.Title, "chapter title"); err != nil {
			return err
		}
		// Matroska writes chapter languages verbatim and sanitizes them on parse, so an
		// invalid-UTF-8 language would not round-trip. Library-only; the CLI has no
		// chapter-language syntax.
		if err := checkWritableText(c.Language, "chapter language"); err != nil {
			return err
		}
		if err := checkWritableText(c.LanguageIETF, "chapter IETF language"); err != nil {
			return err
		}
	}
	// Synced-lyrics text, descriptor, and language are read back through
	// sanitization, so they get the same rule. SetSyncedLyrics replaces the whole
	// list, so the full set is scanned.
	for _, sl := range e.syncedLyrics {
		if err := checkWritableText(sl.Language, "synced-lyrics language"); err != nil {
			return err
		}
		if err := checkWritableText(sl.Description, "synced-lyrics description"); err != nil {
			return err
		}
		for _, ln := range sl.Lines {
			if err := checkWritableText(ln.Text, "synced-lyrics line"); err != nil {
				return err
			}
		}
	}
	return nil
}

// WritableTextReason returns "" when s can be written to every supported format,
// else a reason phrase ("contains a NUL byte" / "contains invalid UTF-8").
// checkWritableText and [ValidWritableText] wrap it in [waxerr.ErrInvalidData];
// the CLI reads the bare phrase to build its own message.
func WritableTextReason(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		return "contains a NUL byte"
	}
	if !utf8.ValidString(s) {
		return "contains invalid UTF-8"
	}
	return ""
}

// ValidWritableText returns nil when s can be written to every supported format,
// else an error wrapping [waxerr.ErrInvalidData]: a NUL byte truncates a C-string
// field, and invalid UTF-8 is reprojected on read so it would not round-trip.
// Editor edits enforce this on authored text; callers may pre-check with it or
// with [WritableTextReason].
func ValidWritableText(s string) error {
	if r := WritableTextReason(s); r != "" {
		return fmt.Errorf("%w: %s", waxerr.ErrInvalidData, r)
	}
	return nil
}

// checkWritableText is [ValidWritableText] with the field (what) named in the
// error. A value read through the sanitizing parse path is always valid, so this
// fires only on freshly authored input.
func checkWritableText(s, what string) error {
	if r := WritableTextReason(s); r != "" {
		return fmt.Errorf("%w: %s %s", waxerr.ErrInvalidData, what, r)
	}
	return nil
}

// planResultTags returns the tag set the plan will write: the codec's result when
// present, else the edited set (a NoOp plan may carry no result). [Plan.Changes]
// diffs against the same source.
func planResultTags(wp *core.WritePlan, edited *core.Media) tag.TagSet {
	if wp.Result != nil {
		return wp.Result.Tags
	}
	return edited.Tags
}

// appendSingleValuedWarnings adds a WarnSingleValuedMulti for every known
// single-valued key the edit changes into holding more than one value. It diffs
// base against the edit intent, so a format that collapses the value in its result
// (Matroska's Info.Title) is still flagged and an untouched pre-existing multi is
// not. The shared [tag.Key.SingleValuedMulti] predicate keeps this, the linter, and
// the CLI's --strict gate in agreement. Each warning carries the key (Warning.Keys).
func appendSingleValuedWarnings(ws []core.Warning, base, intent tag.TagSet) []core.Warning {
	for _, c := range tag.Diff(base, intent) {
		if c.Key.SingleValuedMulti(len(c.New)) {
			ws = core.WarnKeyed(ws, core.WarnSingleValuedMulti, fmt.Sprintf(
				"%s is single-valued but is being given %d values; the typed projection reads only the first",
				c.Key, len(c.New)), c.Key)
		}
	}
	return ws
}

// appendLegacyConflictWarnings flags keys this edit changes that still live in a
// preserved legacy container (ID3v1/APEv2 under LegacyPreserve). Only edit-introduced
// divergence (agreed before, disagrees after). Uses [core.FamilySelected] against
// result tags. One warning per key.
func appendLegacyConflictWarnings(ws []core.Warning, fams []core.FamilyValue, patch tag.TagPatch, result tag.TagSet, legacy core.LegacyPolicy) []core.Warning {
	if legacy != core.LegacyPreserve {
		return ws
	}
	seen := map[tag.Key]bool{}
	for _, f := range fams {
		// Gate on the Legacy marker, not the family name: APEv2 is legacy in MP3 but
		// the native store in WavPack, Monkey's Audio, and Musepack. The parser sets
		// Legacy on exactly the entries a rewrite does not update.
		if !f.Legacy {
			continue
		}
		// Skip an already-warned key, an untouched key, a pre-existing conflict
		// (!f.Selected), or an empty entry. Legacy entries are single-valued.
		if seen[f.Key] || !patch.Touches(f.Key) || !f.Selected || len(f.Values) == 0 {
			continue
		}
		// No conflict while the legacy value agrees with the written values (present
		// among them, or the key was cleared), by the family view's rule.
		if core.FamilySelected(result, f.Key, f.Values[0]) {
			continue
		}
		seen[f.Key] = true
		// --legacy strip always drops the stale container. lint --fix does so only when
		// every legacy container is redundant with the canonical set, so the message
		// qualifies it.
		ws = core.Warn(ws, core.WarnLegacyConflict, fmt.Sprintf(
			"preserved %s tag still holds the old %s value and now conflicts with the edit; use --legacy strip to drop it (lint --fix does so only when the legacy container is fully redundant)",
			f.Family, f.Key))
	}
	return ws
}

// appendValueReducedWarnings reports patched values the destination stores with
// reduced fidelity, e.g. an MP3 ORIGINALDATE written as ID3v2.3 TORY (year only).
// It compares the edited tags with the codec's result, so a value already in the
// reduced form does not warn. The AccessPartial gate keeps ordinary
// canonicalization (GENRE=17 becoming "Rock") out. The reason text is
// Capability.Reason, shared with transfer.
func appendValueReducedWarnings(ws []core.Warning, caps core.Capabilities, patchKeys []tag.Key, edited, result tag.TagSet) []core.Warning {
	for _, k := range patchKeys {
		editedVals, ok := edited.Get(k)
		// Empty values are handled by the empty-value note. If the codec omits one, that
		// is not a fidelity reduction.
		if !ok || !slices.ContainsFunc(editedVals, func(v string) bool { return v != "" }) {
			continue
		}
		fc := caps.Field(k)
		if fc.Write != core.AccessPartial {
			continue
		}
		resultVals, _ := result.Get(k)
		if slices.Equal(editedVals, resultVals) {
			continue // the write did not reduce the value
		}
		if hasKeyedWarning(ws, core.WarnNumericGenre, k) {
			// The numeric-genre warning already reports this loss.
			continue
		}
		ws = core.WarnKeyed(ws, core.WarnValueReduced, fmt.Sprintf("%s: %s", k, fc.Reason()), k)
	}
	return ws
}

// hasKeyedWarning reports whether ws already carries a warning with the given
// code keyed to k.
func hasKeyedWarning(ws []core.Warning, code core.WarningCode, k tag.Key) bool {
	for _, w := range ws {
		if w.Code == code && slices.Contains(w.Keys, k) {
			return true
		}
	}
	return false
}

// appendChapterWarnings adds chapter sanity warnings for the chapters in chapters
// but not in base: WarnChapterPastDuration for a start beyond the file's playable
// length, and WarnDuplicateChapter for a start shared with another chapter. A
// pre-existing chapter merged in by the CLI's --add-chapter is not flagged, but a
// collision a new chapter causes is. chapters is sorted by Start, so each distinct
// collision is reported once. Duration 0 (a truncated or header-only file, which
// already warns no-audio) skips the past-duration check.
func appendChapterWarnings(ws []core.Warning, chapters, base []core.Chapter, duration time.Duration) []core.Warning {
	baseSet := make(map[core.Chapter]bool, len(base))
	for _, c := range base {
		baseSet[c] = true
	}
	isNew := func(c core.Chapter) bool { return !baseSet[c] }

	for _, c := range core.ChaptersPastDuration(chapters, duration) {
		if isNew(c) {
			ws = core.Warn(ws, core.WarnChapterPastDuration,
				core.ChapterPastDurationMessage(c.Start, duration))
		}
	}
	// Warn about a collision only when a new chapter is part of it; lint reports
	// collisions among pre-existing chapters.
	for _, start := range core.DuplicateChapterStarts(chapters) {
		for _, c := range chapters {
			if c.Start == start && isNew(c) {
				ws = core.Warn(ws, core.WarnDuplicateChapter, core.DuplicateChapterMessage(start))
				break
			}
		}
	}
	return ws
}

// matroskaChapterEndsDropped reports whether a Matroska/WebM chapter rewrite
// replaces explicit ChapterTimeEnd values with an open-ended list. MP4 infers ends
// from the next start. A bare clear does not warn; an append keeps the existing
// ended chapters.
func matroskaChapterEndsDropped(format core.Format, newCh, baseCh []core.Chapter) bool {
	if format != core.FormatMatroska || len(newCh) == 0 {
		return false
	}
	baseHadEnd := false
	for _, c := range baseCh {
		if c.End > 0 {
			baseHadEnd = true
			break
		}
	}
	if !baseHadEnd {
		return false
	}
	for _, c := range newCh {
		if c.End > 0 {
			return false // the rewrite still carries explicit ends
		}
	}
	return true
}

// appendPictureWarnings: invalid/duplicate/multi-front for AddPicture pics only.
func appendPictureWarnings(ws []core.Warning, pics []core.Picture, addedMask []bool) []core.Warning {
	added := func(i int) bool { return i < len(addedMask) && addedMask[i] }

	// One pass without hashing: flag added unrecognized images, tally front covers,
	// and record added byte lengths for the duplicate scan below.
	var anyAdded, frontAdded bool
	fronts := 0
	addedLens := map[int]bool{}
	for i, p := range pics {
		if added(i) {
			anyAdded = true
			addedLens[len(p.Data)] = true
			if p.Unrecognized() {
				ws = core.Warn(ws, core.WarnInvalidPicture, fmt.Sprintf(
					"added %s picture is not a recognized image type (%s)", p.Type, p.MIME))
			}
			// The file-icon shape rule, scoped to added pictures like the other checks.
			if reason, bad := core.NonConformingIcon(p); bad {
				ws = core.Warn(ws, core.WarnNonConformingIcon, "added "+reason)
			}
		}
		if p.Type == core.PicFrontCover {
			fronts++
			if added(i) {
				frontAdded = true
			}
		}
	}
	// Nothing added (a removal-only edit): nothing to warn about, nothing hashed.
	if !anyAdded {
		return ws
	}

	// Duplicates: hash only pictures whose byte length some added picture shares, so
	// a large pre-existing cover of another size is never hashed. Warn once per
	// duplicate group an added picture belongs to.
	hashes := map[int][32]byte{}
	counts := map[[32]byte]int{}
	for i, p := range pics {
		if !addedLens[len(p.Data)] {
			continue
		}
		h := p.Hash()
		hashes[i] = h
		counts[h]++
	}
	warned := map[[32]byte]bool{}
	for i := range pics { // pic order, so the warnings are deterministic
		if h, ok := hashes[i]; ok && added(i) && counts[h] > 1 && !warned[h] {
			warned[h] = true
			// Name every role the identical bytes appear under, so the message
			// matches the linter's finding.
			ws = core.Warn(ws, core.WarnDuplicatePicture, duplicatePictureMessage(distinctSortedRoles(pics, hashes, h)))
		}
	}

	if frontAdded && fronts > 1 {
		ws = core.Warn(ws, core.WarnMultipleFrontCovers, multipleFrontCoversMessage(fronts))
	}
	return ws
}

// validateAddedPictures rejects an added picture (addedMask[i] true) whose bytes
// [IsRecognizedImage] does not know, including an empty payload. It sniffs Data
// and ignores the declared MIME. Library counterpart to the CLI's loadCovers
// check; WithUnrecognizedPictures (the CLI's --force) skips it. Pre-existing
// pictures are never re-judged.
func validateAddedPictures(pics []core.Picture, addedMask []bool) error {
	for i, p := range pics {
		if i < len(addedMask) && addedMask[i] && !IsRecognizedImage(p.Data) {
			return fmt.Errorf("%w: added %q picture is not a recognized image "+
				"(%s); pass WithUnrecognizedPictures to embed it anyway",
				waxerr.ErrInvalidData, p.Type, RecognizedImageFormats)
		}
	}
	return nil
}

// dropEmptyValuedKeys makes every key present with a zero-length value slice
// absent. An Add/Set of no values produces such a key, and no codec stores it, so
// leaving it would make IsNoOp/Changes disagree with the bytes written. A present
// [""] is a distinct state and is kept.
func dropEmptyValuedKeys(ts *tag.TagSet) {
	for _, k := range ts.Keys() {
		if vs, ok := ts.Get(k); ok && len(vs) == 0 {
			ts.Delete(k)
		}
	}
}

// touchedKeys is the set of canonical keys this edit named, whatever the op. A
// native store that can hold a value the projection did not select (WAV LIST/INFO,
// AIFF text chunks) re-renders exactly these, so an explicit set resolves such a
// conflict.
func touchedKeys(p tag.TagPatch) map[tag.Key]bool {
	keys := p.Keys()
	if len(keys) == 0 {
		return nil
	}
	out := make(map[tag.Key]bool, len(keys))
	for _, k := range keys {
		out[k] = true
	}
	return out
}

// trimTokenValues applies [tag.TrimTokenValue] to the trimmable keys
// ([tag.IsTrimmableKey]: numeric, date, MP4-integer, BPM, ReplayGain, R128 gain,
// release-country) this edit touches, so stored values match the form the
// validators accept. Carried source values are not rewritten.
func trimTokenValues(ts *tag.TagSet, patch tag.TagPatch) {
	for _, k := range patch.Keys() {
		if !tag.IsTrimmableKey(k) {
			continue
		}
		vals, ok := ts.Get(k)
		if !ok {
			continue
		}
		changed := false
		for i, v := range vals {
			if trimmed := tag.TrimTokenValue(k, v); trimmed != v {
				vals[i] = trimmed
				changed = true
			}
		}
		if changed {
			ts.Set(k, vals...)
		}
	}
}

// rebaseR128Gains adjusts R128_* by -delta (RFC 7845, same Q7.8 scale as output
// gain). Skips keys the patch touches; keep leaves values and warns. Out-of-range
// rebase refuses the edit.
func rebaseR128Gains(ts *tag.TagSet, patch tag.TagPatch, delta int, keep bool) ([]core.Warning, error) {
	var warnings []core.Warning
	for _, k := range ts.Keys() {
		if !tag.IsR128GainKey(k) || patch.Touches(k) {
			continue
		}
		if keep {
			warnings = core.WarnKeyed(warnings, core.WarnOutputGainR128Tags,
				fmt.Sprintf("output gain changed and %s was kept as requested; RFC 7845 applies it on top of the header gain, so a compliant player's loudness moves with the header", k), k)
			continue
		}
		vals, _ := ts.Get(k)
		rebased := make([]string, 0, len(vals))
		for _, v := range vals {
			if !tag.ValidR128GainValue(k, v) {
				warnings = core.WarnKeyed(warnings, core.WarnOutputGainR128Tags,
					fmt.Sprintf("output gain changed but %s=%q is not a Q7.8 integer, so it was not rebased; RFC 7845 applies it on top of the header gain, so set or clear it in the same edit", k, v), k)
				rebased = nil
				break
			}
			old, _ := strconv.Atoi(strings.TrimSpace(v)) // the validator already accepted it
			n := old - delta
			if n < math.MinInt16 || n > math.MaxInt16 {
				// The file is fine; this one tag cannot hold the result, so refuse
				// the write.
				return nil, fmt.Errorf("%w: rebasing %s from %d by %d leaves %d, outside the signed 16-bit range the field holds; set or clear it in the same edit, or pass WithKeepR128Gains (--keep-r128) to leave it alone",
					waxerr.ErrUnsupportedTag, k, old, delta, n)
			}
			rebased = append(rebased, strconv.Itoa(n))
		}
		if rebased != nil {
			ts.Set(k, rebased...)
		}
	}
	return warnings, nil
}

// splitNumberPairs turns an edit-introduced "n/total" on TRACKNUMBER/DISCNUMBER
// into the number+total pair every format stores. Only touched number keys;
// explicit total in the same edit wins. Multi-valued numbers are left alone.
// Returns conflict warnings when an explicit total disagrees with the slash side.
func splitNumberPairs(ts *tag.TagSet, patch tag.TagPatch) []core.Warning {
	var ws []core.Warning
	for _, numKey := range []tag.Key{tag.TrackNumber, tag.DiscNumber} {
		if !patch.Touches(numKey) {
			continue
		}
		vals, ok := ts.Get(numKey)
		if !ok || len(vals) != 1 {
			continue // absent, or multi-valued (out of scope; never lose a value)
		}
		totKey := tag.TotalKey(numKey)
		touchesTotal := patch.Touches(totKey)
		// An explicit total in the same edit wins; warn when it numerically disagrees
		// with the slash-derived one. A leading-zero-only difference ("1/07" +
		// TRACKTOTAL=7) is not a conflict. A malformed number derives no total.
		_, derived, split := tag.NumberTotalSplit(numKey, vals[0])
		explicit, _ := ts.First(totKey)
		if touchesTotal && split && derived != "" && explicit != "" && !sameTotal(explicit, derived) {
			ws = core.WarnKeyed(ws, core.WarnNumberTotalConflict,
				fmt.Sprintf("%s %s overrides the total %s derived from %s %q",
					totKey, explicit, derived, numKey, vals[0]), totKey, numKey)
		}
		// The shared helper ([tag.NormalizeNumberPairs] uses it too) leaves a value with
		// no slash or a malformed pair ("abc/1", "1/2/3") verbatim; the set-time note
		// flags it. The total is written unless the patch touches the total key, so an
		// explicit Set/Clear wins while a slash total still updates a base-carried one.
		tag.SplitNumberValue(ts, numKey, vals[0], !touchesTotal)
	}
	return ws
}

// sameTotal reports whether two total strings denote the same number, so "07" vs
// "7" is not a conflict. A non-numeric or out-of-range explicit total never parses
// equal and still counts as a disagreement; its own malformed-value note fires
// separately.
func sameTotal(explicit, derived string) bool {
	if explicit == derived {
		return true
	}
	e, eerr := strconv.Atoi(explicit)
	d, derr := strconv.Atoi(derived)
	return eerr == nil && derr == nil && e == d
}

// formatLineList renders 1-based line numbers as a comma-separated string for a warning message.
func formatLineList(lines []int) string {
	s := make([]string, len(lines))
	for i, n := range lines {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ", ")
}

// validatePictures enforces the single-icon rule: picture types 1 and 2 each
// appear at most once. The sentinel is ErrUnsupportedTag, not ErrInvalidData: the
// file parsed fine and only the write is impossible, and a bad flag combination
// must not outrank a corrupt file in a multi-file run's exit code.
func validatePictures(pics []core.Picture) error {
	icon, otherIcon := core.CountIcons(pics)
	if icon > 1 {
		return fmt.Errorf("%w: more than one file-icon picture (type 1)", waxerr.ErrUnsupportedTag)
	}
	if otherIcon > 1 {
		return fmt.Errorf("%w: more than one other-file-icon picture (type 2)", waxerr.ErrUnsupportedTag)
	}
	return nil
}

// NativeEditor exposes the native document's structure for inspection, so a
// caller can see exactly what is preserved. It does not mutate native metadata.
type NativeEditor struct {
	base *core.Media
}

// Entries summarizes the native metadata blocks.
func (n NativeEditor) Entries() []NativeEntry {
	if n.base == nil || n.base.Native == nil {
		return nil
	}
	return n.base.Native.Describe()
}
