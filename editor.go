package waxlabel

import (
	"cmp"
	"context"
	"fmt"
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

// ResolveAlias returns the canonical key for a recognized alternative tag spelling
// (DATE/YEAR -> RECORDINGDATE, TOTALTRACKS -> TRACKTOTAL, ORGANIZATION -> LABEL, ...),
// or key unchanged when it is not an alias. Front-ends use it before applying an edit
// so an alias targets the real field instead of creating a duplicate custom field.
func ResolveAlias(key tag.Key) tag.Key { return mapping.ResolveAlias(key) }

// Editor records mutations against a [Document] without changing it. Mutations
// accumulate as a presence-aware [tag.TagPatch] (for canonical fields) plus a
// working picture list; [Editor.Prepare] resolves them into a [Plan]. The
// editor methods return the editor for chaining.
type Editor struct {
	doc         *Document
	base        *core.Media
	patch       tag.TagPatch
	pictures    []core.Picture
	picsTouched bool
	// addedMask is parallel to pictures: addedMask[i] is true when pictures[i] was
	// added on this editor via AddPicture (so Prepare validates it), false for a
	// picture Edit seeded from the file. A mask rather than a second slice lets
	// RemovePictures filter both in lockstep with a single evaluation of the caller's
	// match predicate, so a side-effecting or non-deterministic matcher cannot be
	// called twice or desync the added set from what will be written.
	addedMask           []bool
	chapters            []core.Chapter
	chaptersTouched     bool
	syncedLyrics        []core.SyncedLyrics
	syncedLyricsTouched bool
	// carried marks this editor as a faithful carry from a source (the transfer
	// engine), not a user-authored edit, so [Editor.Prepare] suppresses the edit-time
	// sanity warnings that flag authoring mistakes - the chapter past-duration /
	// duplicate-start checks and the single-valued-multi note. Copying a file must not
	// lecture about metadata the user authored none of (a source's own conflicting
	// single-valued key, or its chapter timings).
	carried bool
}

// Apply records an explicit patch (set/clear/add operations) after any already
// recorded, so later edits win on conflicts. Each operation's key is resolved through
// [ResolveAlias] first, exactly as the key-taking methods below do, so a patch built with
// an alias spelling (e.g. DATE) lands on the canonical field rather than a custom key.
func (e *Editor) Apply(p tag.TagPatch) *Editor {
	e.patch.Append(p.MapKeys(ResolveAlias))
	return e
}

// Set replaces a key's values. The key is resolved through [ResolveAlias], so an
// alternative spelling (Set(tag.Key("DATE"), ...)) lands on the canonical field
// (RECORDINGDATE) on every format instead of creating a custom key; a non-alias key is
// unchanged.
//
// Calling Set with no values collapses the key to absent during [Editor.Prepare], matching
// the empty-value cleanup. [Editor.Clear] is the explicit removal call. Set(key, "") is
// distinct: it stores one empty value. A format that cannot store that value may drop it,
// report a removed/no-op change, and let the CLI print an advisory stderr note.
//
// A slash-combined "n/total" on [tag.TrackNumber] or [tag.DiscNumber] is normalized
// at [Editor.Prepare] into the canonical pair (e.g. Set(tag.TrackNumber, "3/12")
// becomes TRACKNUMBER=3 + TRACKTOTAL=12) so every format stores it identically; see
// splitNumberPairs for the precedence rules.
func (e *Editor) Set(key tag.Key, vals ...string) *Editor {
	e.patch.Set(ResolveAlias(key), vals...)
	return e
}

// Clear removes a key (makes it absent). The key is resolved through [ResolveAlias], so
// clearing an alias spelling removes the canonical field.
func (e *Editor) Clear(key tag.Key) *Editor {
	e.patch.Clear(ResolveAlias(key))
	return e
}

// Add appends values to a key. The key is resolved through [ResolveAlias], so adding under
// an alias spelling appends to the canonical field.
func (e *Editor) Add(key tag.Key, vals ...string) *Editor {
	e.patch.Add(ResolveAlias(key), vals...)
	return e
}

// SetTags applies the non-empty fields of a typed [tag.Tags] as sugar (it
// compiles to a patch of Set operations; it cannot clear fields).
func (e *Editor) SetTags(t tag.Tags) *Editor { return e.Apply(t.Patch()) }

// AddPicture appends a picture. Its MIME and dimensions are reconciled with the
// image bytes via an authoritative header sniff ([Picture.SniffAuthoritative]):
// when the bytes are a recognized image the sniffed MIME and dimensions win over
// any the caller set, so a mislabeled cover cannot be embedded under a MIME that
// contradicts it. (A file's stored picture, read by the decoders, keeps its own
// MIME - that path fills only, via [Picture.SniffInto].)
func (e *Editor) AddPicture(p Picture) *Editor {
	p.SniffAuthoritative()
	// Deep-copy the payload so the editor owns its bytes: the caller passes a Picture by
	// value, but Data is a slice aliasing their backing array, so a later mutation of that
	// array (or reuse of the buffer) would otherwise change the bytes this edit writes.
	// The read side (clonePicturesDeep) already detaches on the way out; this detaches on
	// the way in with the same ownership rule.
	p.Data = append([]byte(nil), p.Data...)
	// Pad the mask for any Edit-seeded pictures not yet covered, then mark this one
	// added, keeping addedMask parallel to pictures.
	for len(e.addedMask) < len(e.pictures) {
		e.addedMask = append(e.addedMask, false)
	}
	e.pictures = append(e.pictures, p)
	e.addedMask = append(e.addedMask, true)
	e.picsTouched = true
	return e
}

// RemovePictures drops every picture for which match returns true. match is
// evaluated exactly once per picture, and the parallel added-mask is filtered with
// the same verdicts, so an added-then-removed picture is not validated by Prepare
// and a side-effecting/non-deterministic matcher cannot double-fire or desync.
func (e *Editor) RemovePictures(match func(Picture) bool) *Editor {
	pics := make([]core.Picture, 0, len(e.pictures))
	mask := make([]bool, 0, len(e.pictures))
	for i, p := range e.pictures {
		// Edit() seeds e.pictures via the shallow core.ClonePictures, so each p.Data still
		// aliases the immutable Document's backing array. match is the only place the editor
		// hands a Picture to user code, so detach Data for the probe: a predicate that writes
		// p.Data then cannot mutate the Document (or race a concurrent doc.Pictures()). The
		// retained e.pictures keeps the efficient shallow share; only the probe is a copy.
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

// SetChapters replaces the whole chapter list. Chapters are a timeline, so the
// list is sorted by start time (stably, preserving the order of chapters that
// share a start) because an out-of-order argument can lose a start when a container
// encodes spans relative to the previous chapter. A format that cannot write chapters
// reports that through [Capabilities]. Lists above a format's hard count cap are
// rejected at [Editor.Prepare]; ID3 CTOC and MP4 Nero chpl are capped at 255 entries.
func (e *Editor) SetChapters(chs ...Chapter) *Editor {
	e.chapters = slices.Clone(chs)
	slices.SortStableFunc(e.chapters, func(a, b Chapter) int { return cmp.Compare(a.Start, b.Start) })
	e.chaptersTouched = true
	return e
}

// ClearChapters removes all chapters.
func (e *Editor) ClearChapters() *Editor {
	e.chapters = nil
	e.chaptersTouched = true
	return e
}

// SetSyncedLyrics replaces all synced-lyrics sets. Lines within each set are sorted by
// Time with a stable sort, matching [ParseLRC] and [Editor.SetChapters]. The line slices
// are deep-copied so later caller mutations cannot change the pending edit. A format that
// cannot write synced lyrics reports that through [Capabilities], and [Editor.Prepare]
// rejects the write.
func (e *Editor) SetSyncedLyrics(sls ...SyncedLyrics) *Editor {
	e.syncedLyrics = make([]core.SyncedLyrics, 0, len(sls))
	for _, sl := range sls {
		// A set with no lines carries no model value: writers skip it because an empty SYLT
		// or SYNCEDLYRICS comment projects to nothing on re-read. Dropping it here keeps the
		// authored and rendered counts aligned across codecs, so a plan never reports a set
		// it did not write.
		if len(sl.Lines) == 0 {
			continue
		}
		sl.Lines = slices.Clone(sl.Lines)
		slices.SortStableFunc(sl.Lines, func(a, b SyncedLine) int { return cmp.Compare(a.Time, b.Time) })
		e.syncedLyrics = append(e.syncedLyrics, sl)
	}
	e.syncedLyricsTouched = true
	return e
}

// ClearSyncedLyrics removes all synced lyrics.
func (e *Editor) ClearSyncedLyrics() *Editor {
	e.syncedLyrics = nil
	e.syncedLyricsTouched = true
	return e
}

// Native returns the native inspection view for the original parsed document.
// It does not include pending editor changes; pictures, tags, or chapters added
// on the editor are visible only after a save and reparse. Structural native
// mutation, such as arbitrary block edits, multiple comment blocks, or vendor
// string edits, is not part of the public editing API.
func (e *Editor) Native() NativeEditor {
	return NativeEditor{base: e.base}
}

// Prepare resolves the recorded mutations into a [Plan] under the given write
// options. The plan's [Plan.Report] describes exactly what executing it will
// do; nothing is written yet, and Prepare performs no I/O (the parsed document
// holds everything the planner needs).
func (e *Editor) Prepare(opts ...WriteOption) (*Plan, error) {
	wo := resolveWriteOptions(opts)

	// An editor from a zero-value Document (Document.Edit guards that case) has no
	// base media to plan against; report it cleanly rather than deref a nil base
	// below.
	if e.base == nil {
		return nil, fmt.Errorf("%w: document is not initialized; use ParseFile/Parse", waxerr.ErrInvalidData)
	}

	// Refuse to build a write plan for a file the parser determined has no real audio
	// (WarnNoAudioFrames): writing it would re-render metadata around non-audio bytes
	// and silently bless a contradictory file. Every editing path - set/plan, lint
	// --fix, and a copy's destination editor (transfer.go) - funnels through Prepare, so
	// they inherit this one guard (exit 4), making a no-audio file fail to edit just as
	// it fails to verify. It is a base-document validity check, not an authored-edit
	// warning, so it is not gated on the carried flag. The copy source stays readable: a
	// no-audio file is still dumpable and its tags are real, so copying tags out of one is
	// allowed (only the destination, which writes, is gated here).
	if hasNoAudioWarning(e.base) {
		return nil, fmt.Errorf("%w: file has no audio essence; refusing to write metadata to a no-audio file", waxerr.ErrInvalidData)
	}

	// Validate every key the edit touches before it can reach the native writer
	// and corrupt on round-trip (e.g. a key containing '='). The key list is reused by
	// the NUL scan below, so it is computed once.
	patchKeys := e.patch.Keys()
	for _, k := range patchKeys {
		if !k.Valid() {
			return nil, fmt.Errorf("%w: %q (keys are uppercase printable ASCII without '=' (spaces and punctuation are allowed); build them with tag.ParseKey or tag.MustKey, which accept any case)", waxerr.ErrInvalidKey, k)
		}
	}

	// Share the native document and properties rather than deep-copying them:
	// planning only reads the native (re-cloning the blocks it keeps), so a full
	// copy here - which would duplicate every block body, including embedded
	// cover art - is pure waste. Only the canonical tags (cloned by the patch)
	// and the picture set are replaced.
	editedTags := e.patch.Apply(e.base.Tags)
	// Collapse any key left present with a zero-length value slice to absent before
	// the codec plans or Changes() diffs: a Set/Add of no values on an absent key
	// (or a clear-then-empty-add) leaves the key present-but-empty, which no codec
	// persists - so without this the plan would diff a phantom add against an
	// identical file, reporting a change and bumping mtime over bytes that never
	// moved. The scope is strictly zero-length: a present [""] (what `set KEY=`
	// produces) is a distinct, CLI-reachable empty value and is left untouched.
	dropEmptyValuedKeys(&editedTags)
	// Reject a NUL byte or invalid UTF-8 in any value, chapter title, or picture
	// description this edit introduces: a NUL silently truncates the field on the
	// C-string formats, and invalid UTF-8 is reprojected through the read path (ID3 to
	// U+FFFD, an MP4 chapter title to "") so the written result would not equal a fresh
	// parse - both would corrupt the write, so they are refused at the source.
	if err := e.rejectInvalidValues(editedTags, patchKeys); err != nil {
		return nil, err
	}
	// Trim numeric values introduced by this edit before any number-pair split. That
	// keeps the stored value in the same form WaxLabel already uses for validation and
	// parsing, while still preserving carried values from the source file.
	trimTokenValues(&editedTags, e.patch)
	// Normalize a slash-combined "n/total" track or disc number this edit introduced
	// into the canonical pair every format stores (see splitNumberPairs). It runs
	// after rejectInvalidValues, not before: that scan only covers the patched keys, so
	// splitting first would route a NUL from "3/\x00" into an unscanned derived
	// TRACKTOTAL and smuggle it past the guard onto a C-string format. Splitting after
	// means the NUL is still on the touched TRACKNUMBER and is rejected above.
	// The returned conflict warnings (an explicit total disagreeing with a slash-derived
	// one) are surfaced below, gated on !e.carried like the other authored warnings.
	numberConflicts := splitNumberPairs(&editedTags, e.patch)
	edited := &core.Media{
		Format:       e.base.Format,
		Properties:   e.base.Properties,
		Tags:         editedTags,
		Pictures:     e.base.Pictures,
		Chapters:     e.base.Chapters,
		SyncedLyrics: e.base.SyncedLyrics,
		Families:     e.base.Families,
		Warnings:     e.base.Warnings,
		Native:       e.base.Native,
		Identity:     e.base.Identity,
		AudioStart:   e.base.AudioStart,
		AudioEnd:     e.base.AudioEnd,
		AudioRanges:  e.base.AudioRanges,
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
	// Enforce the icon-count rule only when this edit wrote the picture set. Tags-only
	// edits use the file's existing pictures, so duplicate type-1 or type-2 icons in
	// the source file should not block unrelated tag edits or lint fixes. Picture
	// edits still set picsTouched and are validated here.
	if e.picsTouched {
		if err := validatePictures(edited.Pictures); err != nil {
			return nil, err
		}
	}
	// Validate only the pictures added on this editor (not the file's pre-existing
	// ones, which Edit seeded): a direct caller handing AddPicture empty or junk
	// bytes would otherwise have them embedded as application/octet-stream. The CLI
	// guards the common mistake earlier in loadCovers; this is the library-side
	// safety net. WithUnrecognizedPictures opts a deliberately exotic cover back in
	// (and the transfer engine opts out wholesale, carrying already-embedded art).
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
	// Refuse to silently drop chapters on a format that cannot store them. This is a
	// format-independent capability gate (it reads the destination's Chapters write
	// level); the analogous picture refusal - a cover onto WebM - is enforced
	// separately as a WebM-specific check inside the Matroska writer, not here, so the
	// two share intent but neither site nor mechanism. Guard on a non-empty list so
	// ClearChapters() on a chapterless format stays a harmless no-op. A format-incapable
	// destination in a transfer is handled earlier (ProjectTransfer marks chapters
	// Dropped before SetChapters runs), so chaptersTouched is false there and this
	// never fires.
	if e.chaptersTouched && len(e.chapters) > 0 &&
		caps.Chapters.Write < core.AccessPartial {
		return nil, fmt.Errorf("%w: chapters cannot be written to %s %s file",
			waxerr.ErrUnsupportedTag, core.IndefiniteArticle(e.base.Format.String()), e.base.Format)
	}
	// Enforce the destination's chapter-count limit before the writer sees the list.
	// ID3 CTOC and MP4 Nero chpl use single-byte counts, so allowing 256 entries would
	// produce a malformed container. Transfers apply the same limit before calling
	// SetChapters, which leaves this path for direct edits.
	if e.chaptersTouched && caps.Chapters.MaxItems > 0 && len(e.chapters) > caps.Chapters.MaxItems {
		return nil, fmt.Errorf("%w: %d chapters exceeds the %d %s can store",
			waxerr.ErrUnsupportedTag, len(e.chapters), caps.Chapters.MaxItems, e.base.Format)
	}
	// Reject authored synced lyrics when the destination has no metadata store for them,
	// using the same capability gate as chapters above. MP4 and Matroska can carry timed
	// lyric tracks, but those tracks are outside this metadata model. A clear on an
	// unsupported format stays a no-op, and MaxItems is enforced before the writer sees the
	// list (the LRC store holds a single set).
	if e.syncedLyricsTouched && len(e.syncedLyrics) > 0 &&
		caps.SyncedLyrics.Write < core.AccessPartial {
		return nil, fmt.Errorf("%w: synced lyrics cannot be written to %s %s file",
			waxerr.ErrUnsupportedTag, core.IndefiniteArticle(e.base.Format.String()), e.base.Format)
	}
	if e.syncedLyricsTouched && caps.SyncedLyrics.MaxItems > 0 && len(e.syncedLyrics) > caps.SyncedLyrics.MaxItems {
		return nil, fmt.Errorf("%w: %d synced-lyrics sets exceeds the %d %s can store",
			waxerr.ErrUnsupportedTag, len(e.syncedLyrics), caps.SyncedLyrics.MaxItems, e.base.Format)
	}
	// Do not reject a parsed 1-2 byte SYLT language here. Some files store NUL-padded short
	// codes, and the writer preserves them on read-then-write; longer values are truncated
	// to SYLT's fixed three bytes. The CLI validates author-entered --synced-lyrics-lang
	// values before they reach this path.
	wp, err := codec.Plan(context.Background(), e.base, edited, wo)
	if err != nil {
		return nil, err
	}
	// Surface edit-time chapter sanity warnings (a start past the file end, or two
	// chapters sharing a start) on the plan report so they flow through the same
	// Warnings path the CLI and JSON already render. Only chapters this edit
	// introduces are checked - not the file's pre-existing chapters, which the CLI's
	// --add-chapter merges into the SetChapters list (so warning about them would
	// flag chapters the user never touched). A faithful carry (the transfer engine)
	// authors nothing, so it suppresses these entirely via the carried flag.
	if e.chaptersTouched && !e.carried {
		wp.Report.Warnings = appendChapterWarnings(wp.Report.Warnings, e.chapters, e.base.Chapters, e.base.Properties.Duration())
		// Matroska/WebM can store explicit chapter end times. A CLI chapter rebuild has
		// no end-time syntax, so warn when it replaces ended chapters with open-ended ones.
		// Faithful transfer is suppressed by the carried flag above.
		if matroskaChapterEndsDropped(e.base.Format, e.chapters, e.base.Chapters) {
			wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnChapterEndsDropped,
				"chapters rewrite drops explicit end times (CLI-built chapters are open-ended)")
		}
		// Warn when this destination cannot store every field in the authored chapter
		// list. ChapterLoss is option-independent, so use the capability value already
		// computed for the write plan.
		if loss := caps.Chapters.ChapterLoss; core.ChaptersLoseMetadata(e.chapters, loss) {
			wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnChapterMetadataDropped,
				core.ChapterMetadataDroppedMessage(loss))
		}
	}
	// Warn when the destination cannot store every field in the authored synced-lyrics
	// list. The LRC store keeps timed text but drops the per-set language and descriptor,
	// mirroring the chapter metadata-dropped warning above. SyncedLyricsLoss is
	// option-independent, so use the capability value already computed for the write plan.
	// A transfer that carries source metadata is already graded in its transfer report.
	if e.syncedLyricsTouched && !e.carried {
		if loss := caps.SyncedLyrics.SyncedLyricsLoss; core.SyncedLyricsLoseMetadata(e.syncedLyrics, loss) {
			wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnSyncedLyricsMetadataDropped,
				core.SyncedLyricsMetadataDroppedMessage())
		}
	}
	// Surface edit-time picture sanity warnings for the pictures this edit authored
	// (added via AddPicture, tracked by addedMask) - an unrecognized image embedded
	// under WithUnrecognizedPictures, an added duplicate, or an added front cover that
	// makes a second - so the user sees what a picture edit introduced without being
	// lectured about a file's pre-existing art (which stays the linter's whole-set
	// concern, mirroring how the chapter checks scope to newly-authored chapters). A
	// faithful carry authors nothing, so it suppresses these via the carried flag.
	if e.picsTouched && !e.carried {
		wp.Report.Warnings = appendPictureWarnings(wp.Report.Warnings, e.pictures, e.addedMask)
	}
	// Surface a known single-valued key the edit leaves holding multiple values as a
	// non-fatal plan warning, so a library caller sees the cardinality the typed
	// projection would silently collapse to its first value. It names exactly the
	// keys the CLI's --strict gate acts on, and lets the CLI read the signal off the
	// report once (now also in --json warnings). A faithful carry suppresses it (like the
	// chapter checks): a copy must not flag the source's own conflicting single-valued
	// key as if the user authored it.
	if !e.carried {
		// The single-valued-multi check judges against the EDIT INTENT (edited.Tags), not
		// the codec's re-projected result: a single-valued key is single-valued by the
		// key's own definition regardless of format, and a format that collapses the value
		// in its result (Matroska's Info.Title) would otherwise stay silent on the very
		// loss the warning exists to surface. Diffing base->intent still avoids
		// re-flagging an untouched pre-existing multi.
		wp.Report.Warnings = appendSingleValuedWarnings(wp.Report.Warnings, e.base.Tags, edited.Tags)
		// The legacy-conflict check, by contrast, judges against the plan's result tags
		// (what the codec will actually write): a value the codec re-projects - e.g.
		// GENRE=17 written back as the name "Rock" - must not read as a conflict when the
		// written value in fact still agrees. Suppressed on a faithful carry like the rest.
		result := planResultTags(wp, edited)
		wp.Report.Warnings = appendLegacyConflictWarnings(wp.Report.Warnings, e.base.Families, e.patch, result, wo.Legacy)
		// Warn when a patched value is reduced by the destination's field-level write
		// capability, using the same projected result tags as the legacy conflict check.
		wp.Report.Warnings = appendValueReducedWarnings(wp.Report.Warnings, caps, patchKeys, editedTags, result)
		// Surface a track/disc total-vs-slash conflict this edit authored (computed at the
		// number-pair split above, where the precedence lives). A faithful carry is suppressed
		// by the enclosing !e.carried gate: a copy must not flag the source's own values.
		wp.Report.Warnings = append(wp.Report.Warnings, numberConflicts...)
	}
	return &Plan{doc: e.doc, plan: wp, opts: wo}, nil
}

// rejectInvalidValues refuses a NUL byte or invalid UTF-8 in any value, chapter title,
// or picture description this edit introduces. A NUL silently truncates the field when it
// is written to a C-string format (the ID3 frames MP3/WAV/AIFF store, MP4 atoms). Invalid
// UTF-8 is reprojected through the read path - ID3 reads it back as U+FFFD, an MP4 chapter
// title as "" - so a value passed raw to a writer would not round-trip and the result
// would not equal a fresh parse; refusing it at the source keeps the "result == fresh
// parse" guarantee by construction for every format, even those that carry the bytes
// verbatim today. This is the library and transfer counterpart to the CLI's OS-level
// argument guard. The tag scan covers only the keys the patch touches (their resolved
// values are in editedTags); the file's untouched pre-existing tags are not re-judged.
// Pictures are scoped to those added on this editor (addedMask), like the rest of the
// added-picture validation; chapters cover the full edited list, which SetChapters
// replaces wholesale.
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
		// The Matroska chapter languages are written verbatim into the EBML, and the read
		// path sanitizes them on parse, so a freshly authored invalid-UTF-8 language would
		// not round-trip - reject it at the source like the title (library-only; the CLI has
		// no chapter-language syntax).
		if err := checkWritableText(c.Language, "chapter language"); err != nil {
			return err
		}
		if err := checkWritableText(c.LanguageIETF, "chapter IETF language"); err != nil {
			return err
		}
	}
	// Synced-lyrics text, descriptor, and language are stored in SYLT or LRC and read back
	// through sanitization. Reject newly authored NULs or invalid UTF-8 here, using the
	// same rule as chapter titles, so the written values can round-trip through the model.
	// SetSyncedLyrics replaces the whole list, so the full edited set is scanned.
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

// WritableTextReason returns "" when s can be written faithfully to every supported format,
// else a short reason phrase ("contains a NUL byte" / "contains invalid UTF-8"). It is the
// single source of truth for the NUL / invalid-UTF-8 rule: the internal checkWritableText and
// the public ValidWritableText wrap it in an [waxerr.ErrInvalidData] error, and a front-end (the
// CLI) can read the bare phrase to build its own message without parsing an error string.
func WritableTextReason(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		return "contains a NUL byte"
	}
	if !utf8.ValidString(s) {
		return "contains invalid UTF-8"
	}
	return ""
}

// ValidWritableText reports whether s can be written faithfully to every supported format:
// no NUL byte (which truncates a C-string field) and valid UTF-8 (the read path reprojects
// invalid UTF-8, so it would not round-trip). It returns nil, or an error wrapping
// [waxerr.ErrInvalidData] naming the problem. Editor edits already enforce this on authored
// text; a caller (or a front-end) may pre-check a value with it, or with [WritableTextReason]
// for the bare reason phrase, before building an edit.
func ValidWritableText(s string) error {
	if r := WritableTextReason(s); r != "" {
		return fmt.Errorf("%w: %s", waxerr.ErrInvalidData, r)
	}
	return nil
}

// checkWritableText refuses a freshly authored text value WaxLabel cannot faithfully write
// to every format: a NUL byte (truncates a C-string field) or invalid UTF-8 (reprojected
// by the read path, so it would not round-trip). what names the field for the error. A
// value read back through the (sanitizing) parse path is always valid UTF-8, so this fires
// only on CLI/library input freshly authored by the caller. It shares WritableTextReason with
// the public ValidWritableText, so its "<what> contains ..." messages stay in lockstep.
func checkWritableText(s, what string) error {
	if r := WritableTextReason(s); r != "" {
		return fmt.Errorf("%w: %s %s", waxerr.ErrInvalidData, what, r)
	}
	return nil
}

// planResultTags returns the tag set the plan will write: the codec's computed
// result when present, else the edited set (a NoOp plan may carry no result). It
// is the same source [Plan.Changes] diffs against, so a warning derived from it
// matches the plan's reported changes.
func planResultTags(wp *core.WritePlan, edited *core.Media) tag.TagSet {
	if wp.Result != nil {
		return wp.Result.Tags
	}
	return edited.Tags
}

// appendSingleValuedWarnings adds a WarnSingleValuedMulti for every known
// single-valued key the edit changes into holding more than one value. It diffs base
// against the edit INTENT (the edited tag set), not the codec's re-projected result,
// so a format that collapses the value in its own result (Matroska's Info.Title) is
// still flagged - the cardinality is a property of the key, not the format.
// Diffing against base avoids re-flagging an untouched pre-existing multi (already
// reported by Lint), and the shared [tag.Key.SingleValuedMulti] predicate keeps the
// library warning, the linter's finding, and the CLI's --strict gate from disagreeing.
// Each warning carries the offending key (Warning.Keys) so the gate can name it.
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

// appendLegacyConflictWarnings flags a canonical key the edit changes whose value is
// also carried in a preserved legacy container the family view surfaces - an ID3v1 or
// APEv2 tag on the ID3-based formats (MP3/AAC) - which the default LegacyPreserve policy
// keeps verbatim, so the legacy copy now disagrees with the freshly written native tag.
// It is driven by the family view, so it covers exactly the legacy containers a codec
// projects into fams; a FLAC trailing ID3v1, which the parser preserves but does not
// project into families, is surfaced by the trailing-id3v1 parse warning and the
// "trailing ID3v1 preservation" operation, not this edit-conflict warning.
//
// It fires only for an EDIT-INTRODUCED divergence: the legacy value agreed with the
// native value before this edit (f.Selected) but the edit changed the written value so
// the legacy copy is no longer among it. A pre-existing disagreement - already
// unselected, e.g. an ID3v1 field the parser truncated to 30 bytes - is the linter's
// conflicting-families job, not this edit-time warning. Agreement is judged with
// [core.FamilySelected] against the plan's result tags (what the codec will actually
// write, not the raw edited value - so a re-projected GENRE=17 that writes back as
// "Rock" does not falsely conflict), the same presence test the parser and linter use,
// so a multi-value key whose legacy value still survives the edit (ID3v2 ARTIST=[A,B]
// against an ID3v1 "B") is not falsely flagged - a slice-equality check would be, since
// each legacy family entry is single-valued by construction (one entry per legacy
// value). It fires only under LegacyPreserve (strip resolves the divergence on write)
// and only for a key the patch touches; clearing a key does not fire it (the native key
// is then absent, which FamilySelected - like the linter - treats as no conflict). The
// value is still written and the legacy container preserved as promised; this only
// surfaces the divergence and the remedy. One warning per conflicting key.
func appendLegacyConflictWarnings(ws []core.Warning, fams []core.FamilyValue, patch tag.TagPatch, result tag.TagSet, legacy core.LegacyPolicy) []core.Warning {
	if legacy != core.LegacyPreserve {
		return ws
	}
	seen := map[tag.Key]bool{}
	for _, f := range fams {
		if f.Family != core.FamilyID3v1 && f.Family != core.FamilyAPEv2 {
			continue
		}
		// Skip an already-warned key, a key the edit does not touch, a pre-existing
		// conflict (!f.Selected - not edit-introduced), or a malformed empty legacy entry.
		// Legacy entries are single-valued by construction, so f.Values[0] is the value.
		if seen[f.Key] || !patch.Touches(f.Key) || !f.Selected || len(f.Values) == 0 {
			continue
		}
		// No conflict while the legacy value still agrees with the written native values
		// (present among them, or the key was cleared) - the same rule the family view uses.
		if core.FamilySelected(result, f.Key, f.Values[0]) {
			continue
		}
		seen[f.Key] = true
		// The remedy names the options that actually resolve the conflict: drop the stale
		// legacy container (--legacy strip) or let lint --fix do it.
		ws = core.Warn(ws, core.WarnLegacyConflict, fmt.Sprintf(
			"preserved %s tag still holds the old %s value and now conflicts with the edit; use --legacy strip or lint --fix",
			f.Family, f.Key))
	}
	return ws
}

// appendValueReducedWarnings reports patched values that the destination stores with
// reduced fidelity. Today that applies to an MP3 ORIGINALDATE written as ID3v2.3,
// where TORY keeps only the year.
//
// The check compares the edited tags with the codec's projected result, so a value that
// already matches the reduced form does not warn. The AccessPartial capability gate
// keeps ordinary canonicalization, such as GENRE=17 becoming "Rock", out of this path.
// The reason text comes from the same Capability.Reason helper used by transfer.
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
			continue // the write did not actually reduce the value
		}
		// Use the same reason text as transfer so edit and copy describe the loss the
		// same way.
		ws = core.WarnKeyed(ws, core.WarnValueReduced, fmt.Sprintf("%s: %s", k, fc.Reason()), k)
	}
	return ws
}

// appendChapterWarnings adds the non-fatal chapter sanity warnings for the
// chapters this edit introduces (those in chapters but not in base):
// WarnChapterPastDuration for a newly-added chapter starting beyond the file's
// playable length, and WarnDuplicateChapter for a start a newly-added chapter
// shares with another. Scoping to the new chapters means a pre-existing on-disk
// chapter merged into the list (the CLI's --add-chapter appends to the file's
// chapters) is not flagged, while a collision the new chapter causes still is.
// chapters is sorted by Start (SetChapters), so equal starts are adjacent and each
// distinct collision is reported once. The past-duration check is gated on a known,
// non-zero duration: a truncated or header-only file reports duration 0 (and
// already warns no-audio), which would otherwise flag every chapter as beyond 0:00.
func appendChapterWarnings(ws []core.Warning, chapters, base []core.Chapter, duration time.Duration) []core.Warning {
	baseSet := make(map[core.Chapter]bool, len(base))
	for _, c := range base {
		baseSet[c] = true
	}
	isNew := func(c core.Chapter) bool { return !baseSet[c] }

	if duration > 0 {
		for _, c := range chapters {
			if isNew(c) && c.Start > duration {
				ws = core.Warn(ws, core.WarnChapterPastDuration, fmt.Sprintf(
					"chapter at %s starts past the file duration (%s); check the timestamp",
					core.FormatChapterTime(c.Start), core.FormatChapterTime(duration)))
			}
		}
	}
	// Walk each run of equal starts once; warn only when the run contains a
	// newly-added chapter, so a collision among untouched pre-existing chapters stays
	// quiet while one the edit introduces is surfaced.
	for i := 0; i < len(chapters); {
		j := i
		for j+1 < len(chapters) && chapters[j+1].Start == chapters[i].Start {
			j++
		}
		if j > i {
			runHasNew := false
			for k := i; k <= j; k++ {
				if isNew(chapters[k]) {
					runHasNew = true
					break
				}
			}
			if runHasNew {
				ws = core.Warn(ws, core.WarnDuplicateChapter, fmt.Sprintf(
					"two or more chapters share the start %s", core.FormatChapterTime(chapters[i].Start)))
			}
		}
		i = j + 1
	}
	return ws
}

// matroskaChapterEndsDropped reports whether a Matroska/WebM chapter rewrite replaces
// explicit ChapterTimeEnd values with an open-ended list. MP4 does not need this check
// because its chapter ends are inferred from the next start time.
//
// A bare clear is a deletion, not an open-ended rewrite, so it does not warn. Appending
// a chapter keeps the existing ended chapters in the new list, and library callers that
// set Chapter.End keep their ends as well.
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

// appendPictureWarnings adds the non-fatal picture sanity warnings for the
// pictures this edit authored - those with addedMask[i] true (added via
// AddPicture), not the ones Edit seeded from the file. Scoping to the added set is
// the picture counterpart to appendChapterWarnings' new-chapter scope: a copy or a
// tags-only edit must not be lectured about a file's pre-existing art, which the
// linter already covers whole-set. Three checks, each off a predicate the linter
// shares so the rule cannot drift:
//   - invalid-picture: an added picture stored under [core.UnrecognizedMIME]. This
//     reaches here only under WithUnrecognizedPictures (the CLI's --force); without
//     it validateAddedPictures has already rejected the picture.
//   - duplicate-picture: an added picture whose image bytes ([core.Picture.Hash])
//     match another in the set. Reported once per duplicate group an added picture
//     belongs to, whether the twin is another added picture or a pre-existing one.
//   - multiple-front-covers: an added front cover that leaves the set holding more
//     than one front cover (a pair the user did not touch stays the linter's job).
func appendPictureWarnings(ws []core.Warning, pics []core.Picture, addedMask []bool) []core.Warning {
	added := func(i int) bool { return i < len(addedMask) && addedMask[i] }

	// One cheap pass over the set (no hashing): flag each added unrecognized image,
	// tally front covers, record the byte lengths of added pictures (for the duplicate
	// scan below), and note whether anything was added at all.
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
		}
		if p.Type == core.PicFrontCover {
			fronts++
			if added(i) {
				frontAdded = true
			}
		}
	}
	// Nothing added (e.g. a removal-only edit, where addedMask is all-false): there is
	// nothing to warn about, and - importantly - no picture has been hashed.
	if !anyAdded {
		return ws
	}

	// Duplicate detection. Two images of different byte length can never be equal, so
	// hash only pictures whose length some added picture shares - a large pre-existing
	// cover of a different size is never SHA-256'd. Then warn once per duplicate group an
	// added picture belongs to (whether its twin is another added or a carried picture).
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
			// Name every role the identical bytes appear under (sorted), not this occurrence's
			// role, so the message matches the linter's whole-set finding regardless of which
			// occurrence each site reaches first (M9).
			ws = core.Warn(ws, core.WarnDuplicatePicture, duplicatePictureMessage(distinctSortedRoles(pics, hashes, h)))
		}
	}

	if frontAdded && fronts > 1 {
		ws = core.Warn(ws, core.WarnMultipleFrontCovers, multipleFrontCoversMessage(fronts))
	}
	return ws
}

// validateAddedPictures rejects an added picture (one with addedMask[i] true) whose
// bytes are not a recognized image - an empty payload or a header [IsRecognizedImage]
// does not know. It re-sniffs Data and ignores any caller-declared MIME, so
// MIME:"image/png" on junk bytes is still rejected. The message names the picture's
// type and the opt-out. It is the library counterpart to the CLI's loadCovers
// pre-check, run at Prepare so a direct API user cannot embed an
// application/octet-stream picture by mistake; WithUnrecognizedPictures (and the
// CLI's --force) skip it for a deliberately exotic cover. Pre-existing pictures
// Edit seeded (addedMask false) are never re-judged.
func validateAddedPictures(pics []core.Picture, addedMask []bool) error {
	for i, p := range pics {
		if i < len(addedMask) && addedMask[i] && !IsRecognizedImage(p.Data) {
			return fmt.Errorf("%w: added %q picture is not a recognized image "+
				"(PNG/JPEG/GIF/WebP/BMP/TIFF); pass WithUnrecognizedPictures to embed it anyway",
				waxerr.ErrInvalidData, p.Type)
		}
	}
	return nil
}

// dropEmptyValuedKeys removes every key that is present with a zero-length value
// slice, making it absent. It is the [Editor.Prepare] normalization that keeps a
// plan honest (see the call site): such a key is what an Add/Set of no values
// against an absent key produces, and no codec stores it, so leaving it present
// would make IsNoOp/Changes disagree with the bytes actually written. It is
// scoped strictly to a zero-length slice - a present [""] (one empty string) is a
// distinct, intentional state and is preserved.
func dropEmptyValuedKeys(ts *tag.TagSet) {
	for _, k := range ts.Keys() {
		if vs, ok := ts.Get(k); ok && len(vs) == 0 {
			ts.Delete(k)
		}
	}
}

// trimTokenValues applies [tag.TrimTokenValue] to the trimmable keys ([tag.IsTrimmableKey]:
// numeric, date, media-type, and ReplayGain) touched by this edit, so stored values match the
// trimmed form the validators accept. It is scoped to patched keys, like splitNumberPairs, so
// carried source values are not rewritten.
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

// splitNumberPairs normalizes a slash-combined "n/total" value on a track or disc
// number that THIS edit introduced into the canonical pair every format stores -
// TRACKNUMBER + TRACKTOTAL (and DISCNUMBER + DISCTOTAL). Without it a
// Set(tag.TrackNumber, "3/12") splits on ID3/MP4/Matroska (whose native track field
// is spec'd as number/total) but survives as the literal "3/12" on Vorbis/WAV (where
// a slash is non-standard - the convention is a separate TRACKTOTAL comment), so the
// canonical layer would disagree with itself. Doing it here, in Prepare, makes every
// write - CLI and library alike - converge, since every write flows through Prepare.
//
// It uses the shared [tag.SplitNumberTotal] (the same substring split the ID3 read
// path uses), setting each side only when non-empty (so "3/" yields just the number
// and "/12" just the total) - preserving the exact substrings, including any leading
// zeros, rather than renumbering through tag.ParseNumPair.
//
// Two gates keep it from churning unrelated state:
//   - Only a number key the patch Touches is split, never a literal "3/12" merely
//     carried from the base file - editing an unrelated field must not silently
//     rewrite a pre-existing track number.
//   - The total side is written only when the patch does not also Touch the total
//     key, so an explicit Set/Clear of TRACKTOTAL in the same edit wins, while a slash
//     total still updates a total carried from the base file.
//
// TRACKNUMBER/DISCNUMBER are canonically single-valued, so only a single-valued edit
// is split; a multi-valued one (e.g. --add TRACKNUMBER=4/12 --add TRACKNUMBER=3) is
// left untouched rather than collapsed to one value via Set, which would silently drop
// the others - the single-valued-key warning flags that misuse separately. A
// present-but-empty number ([""], from `set TRACKNUMBER=`) carries no slash and so is
// left untouched.
//
// It returns a warning per pair whose explicit total (set by the same edit) disagrees with
// the slash-derived one - the redundant derived total is dropped by design, and surfacing
// the disagreement is the caller's to gate on e.carried (a faithful copy must not flag the
// source's own values). The split itself always runs, so a carried edit still normalizes.
func splitNumberPairs(ts *tag.TagSet, patch tag.TagPatch) []core.Warning {
	var ws []core.Warning
	for _, numKey := range []tag.Key{tag.TrackNumber, tag.DiscNumber} {
		if !patch.Touches(numKey) {
			continue
		}
		vals, ok := ts.Get(numKey)
		if !ok || len(vals) != 1 {
			continue // absent, or multi-valued (out of scope - never lose a value)
		}
		totKey := tag.TotalKey(numKey)
		touchesTotal := patch.Touches(totKey)
		// When the same edit also sets the total explicitly, that explicit value wins (the
		// SplitNumberValue call below is told not to write the derived total). Warn when the two
		// numerically disagree so the unused slash-derived total is not a silent surprise.
		// Detection lives here, where the precedence lives, so the two cannot drift. A
		// leading-zero-only difference ("1/07" + TRACKTOTAL=7) is not a conflict - the same total,
		// and the derived "07" is discarded anyway; agreement, a malformed number (no derived
		// total), or an edit that does not touch the total also does not warn.
		_, derived, split := tag.NumberTotalSplit(numKey, vals[0])
		explicit, _ := ts.First(totKey)
		if touchesTotal && split && derived != "" && explicit != "" && !sameTotal(explicit, derived) {
			ws = core.WarnKeyed(ws, core.WarnNumberTotalConflict,
				fmt.Sprintf("%s %s overrides the total %s derived from %s %q",
					totKey, explicit, derived, numKey, vals[0]), totKey, numKey)
		}
		// Split through the shared split-and-assign body, so this edit-time site cannot drift
		// from the codec read paths ([tag.NormalizeNumberPairs] uses the same helper). A value
		// with no slash, or a malformed pair ("abc/1", "1/2/3"), is left verbatim on the number
		// key (the set-time note flags it). The total is written unless the patch also touches
		// the total key, so an explicit Set/Clear of the total in the same edit wins while a
		// slash total still updates a base-carried one.
		tag.SplitNumberValue(ts, numKey, vals[0], !touchesTotal)
	}
	return ws
}

// sameTotal reports whether two track/disc total strings denote the same number, so a
// leading-zero-only difference ("07" vs "7", "012" vs "12") is not flagged as a conflict. The
// derived side is already validated numeric; a non-numeric or out-of-range explicit total never
// parses equal, so it still counts as a genuine disagreement (and its own malformed-value note
// fires separately). Exact-string equality short-circuits the common case.
func sameTotal(explicit, derived string) bool {
	if explicit == derived {
		return true
	}
	e, eerr := strconv.Atoi(explicit)
	d, derr := strconv.Atoi(derived)
	return eerr == nil && derr == nil && e == d
}

// validatePictures enforces the single-icon rule: picture types 1 and 2 must
// each appear at most once.
func validatePictures(pics []core.Picture) error {
	icon, otherIcon := core.CountIcons(pics)
	if icon > 1 {
		return fmt.Errorf("%w: more than one 32x32 file-icon picture (type 1)", waxerr.ErrInvalidData)
	}
	if otherIcon > 1 {
		return fmt.Errorf("%w: more than one other-file-icon picture (type 2)", waxerr.ErrInvalidData)
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
