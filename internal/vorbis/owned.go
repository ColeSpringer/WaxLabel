package vorbis

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// An owned comment (CHAPTERxxx, CHAPTERxxxNAME, SYNCEDLYRICS, METADATA_BLOCK_PICTURE) that its
// projector does not read is unread: it is no tag, no chapter, no synced lyric and no picture, so
// nothing shows it. It is reported on read, kept by an edit that leaves its kind alone, and
// removed by a clear of its kind or name.

// unreadKind says which projector passed a comment over.
type unreadKind uint8

const (
	unreadNone unreadKind = iota
	unreadChapter
	unreadLyrics
	unreadPicture
	unreadKinds
)

// OwnedByName says what FLAC and Ogg store under a name spelled like key: "chapters" for
// CHAPTERxxx and CHAPTERxxxNAME, "synced lyrics" for SYNCEDLYRICS, "pictures" for
// METADATA_BLOCK_PICTURE, "" for any other name. A clear of such a key removes only the comments
// under it that were not read ([PlanUnread]).
func OwnedByName(key tag.Key) string {
	switch {
	case isChapterComment(string(key)):
		return "chapters"
	case isSyncedLyricsComment(string(key)):
		return "synced lyrics"
	case IsPictureComment(string(key)):
		return "pictures"
	}
	return ""
}

// ownedRead is what the chapter and synced-lyrics readers make of a comment list, and what they
// pass over.
type ownedRead struct {
	chapters  []chapterRead
	lyricsAt  int               // the SYNCEDLYRICS comment read, -1 for none
	lines     []core.SyncedLine // its lines
	truncated bool              // it held more lines than the cap
	unread    []unreadKind      // per comment, the projector that owns it and did not read it
	n         [unreadKinds]int  // comments by mark
}

// readOwned runs the readers over comments once. The readers themselves mark what they pass
// over, so the marks cannot disagree with what is read. A picture comment is marked wherever it
// stands: a document's list holds only those that did not decode.
func readOwned(comments []Comment) ownedRead {
	r := ownedRead{unread: make([]unreadKind, len(comments))}
	r.chapters = readChapters(comments, r.unread)
	r.lyricsAt, r.lines, r.truncated = readSyncedLyrics(comments, r.unread)
	for i, cm := range comments {
		if !cm.Unseparated && IsPictureComment(cm.Name) {
			r.unread[i] = unreadPicture
		}
		r.n[r.unread[i]]++
	}
	return r
}

// Owned is what the chapter and synced-lyrics projectors make of a comment list.
type Owned struct {
	Chapters     []core.Chapter
	SyncedLyrics []core.SyncedLyrics
	// Warnings: a synced-lyrics document cut at the line cap, the chapter and synced-lyrics
	// comments nothing reads, and titles or lyrics read with U+FFFD in place of invalid bytes.
	Warnings []core.Warning
}

// ProjectOwned is [ProjectChapters] and [ProjectSyncedLyrics] in one pass, with what they
// report. The parse sites use it. A picture comment that does not decode is reported where it
// is decoded.
func ProjectOwned(comments []Comment) Owned {
	r := readOwned(comments)
	o := Owned{Chapters: chaptersOf(r.chapters), SyncedLyrics: syncedLyricsOf(r.lines)}
	if r.truncated {
		o.Warnings = core.Warn(o.Warnings, core.WarnSyncedLyricsTruncated,
			fmt.Sprintf("a SYNCEDLYRICS comment carried more than %d synced-lyric lines; the lines past the limit were dropped on read", core.MaxSyncedLines))
	}

	// One WarnMalformedTagEntry for the chapter comments nothing reads and one for the
	// SYNCEDLYRICS comments, each naming the first.
	for _, k := range []struct {
		kind unreadKind
		as   string
	}{{unreadChapter, "a chapter"}, {unreadLyrics, "synced lyrics"}} {
		if r.n[k.kind] == 0 {
			continue
		}
		first := comments[slices.Index(r.unread, k.kind)]
		msg := fmt.Sprintf("%s comment is not read as %s (preserved in place)", core.WarnSnippet(first.Name), k.as)
		if first.Value != "" {
			msg += ": " + core.WarnSnippet(first.Value)
		}
		o.Warnings = core.Warn(o.Warnings, core.WarnMalformedTagEntry, msg+andMore(r.n[k.kind]-1))
	}

	// One WarnInvalidText for the chapter titles read with a substitution, and one for the lyrics.
	var titles []int
	for _, ch := range r.chapters {
		if ch.titleAt >= 0 && !utf8.ValidString(comments[ch.titleAt].Value) {
			titles = append(titles, ch.titleAt)
		}
	}
	if len(titles) > 0 {
		o.Warnings = core.Warn(o.Warnings, core.WarnInvalidText, invalidTextMessage(comments[slices.Min(titles)].Name)+andMore(len(titles)-1))
	}
	if r.lyricsAt >= 0 && !utf8.ValidString(comments[r.lyricsAt].Value) {
		o.Warnings = core.Warn(o.Warnings, core.WarnInvalidText, invalidTextMessage(comments[r.lyricsAt].Name))
	}
	return o
}

// invalidTextMessage is the WarnInvalidText wording for the key or owned comment name.
func invalidTextMessage(name string) string {
	return strings.ToUpper(name) + " is not valid UTF-8; its invalid bytes are shown as U+FFFD"
}

func andMore(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf(" (and %d more)", n)
}

// UnreadPlan is what an edit does with what a file holds unread: the owned comments no
// projector reads and the PICTURE blocks that did not decode.
type UnreadPlan struct {
	// Comments is the document's list without the comments a cleared name, or a clear of the
	// pictures, removes.
	Comments []Comment
	// ChaptersChanged and LyricsChanged are the caller's findings, set as well where an explicit
	// clear takes unread comments, so that [Rebuild] drops them.
	ChaptersChanged, LyricsChanged bool
	// DropBlocks: a clear of the pictures takes the PICTURE blocks that did not decode.
	DropBlocks bool
	// Ops lists what the edit was asked to remove; Warnings what a replacement drops unasked.
	Ops      []string
	Warnings []core.Warning
}

// Removes reports whether the edit removes anything unread. No projection shows that, so the
// caller counts it as a change of its own when it decides whether the plan is a no-op.
func (p UnreadPlan) Removes() bool { return len(p.Ops)+len(p.Warnings) > 0 }

// PlanUnread settles what an edit does with the owned comments no projector reads; orig is a
// document's list, which holds no cover that decoded, and blocks counts its PICTURE blocks that
// did not decode. An explicit clear of the chapters or synced lyrics (opts.ChaptersCleared,
// opts.SyncedLyricsCleared) takes the unread comments of that kind with it, as an operation. A
// key the edit cleared that is spelled like one (CHAPTER001, CHAPTER001NAME, SYNCEDLYRICS,
// METADATA_BLOCK_PICTURE) removes the unread comments under that name, in any ASCII case and no
// other spelling of a chapter's number; a clear of the pictures (opts.PicturesCleared) removes
// the picture comments and the blocks. A replacement that drops unread comments unasked warns
// WarnMalformedTagEntryDropped.
func PlanUnread(orig []Comment, blocks int, edited tag.TagSet, opts core.WriteOptions, chaptersChanged, lyricsChanged bool) UnreadPlan {
	p := UnreadPlan{Comments: orig, ChaptersChanged: chaptersChanged, LyricsChanged: lyricsChanged}
	var names []string // the owned names the edit cleared
	for k := range opts.Touched {
		if !edited.Has(k) && OwnedByName(k) != "" {
			names = append(names, string(k))
		}
	}
	// Most edits reach nothing unread, and then the list is not read again.
	if !opts.ChaptersCleared && !opts.SyncedLyricsCleared && !opts.PicturesCleared && !chaptersChanged && !lyricsChanged && len(names) == 0 {
		return p
	}

	r := readOwned(orig)
	var byName [unreadKinds]int
	if len(names) > 0 || opts.PicturesCleared && r.n[unreadPicture] > 0 {
		p.Comments = make([]Comment, 0, len(orig))
		for i, cm := range orig {
			kind := r.unread[i]
			if kind != unreadNone && (kind == unreadPicture && opts.PicturesCleared ||
				slices.ContainsFunc(names, func(name string) bool { return equalFoldASCII(cm.Name, name) })) {
				byName[kind]++
				continue
			}
			p.Comments = append(p.Comments, cm)
		}
	}

	for _, k := range []struct {
		kind    unreadKind
		cleared bool
		changed *bool
		what    string
	}{
		{unreadChapter, opts.ChaptersCleared, &p.ChaptersChanged, "CHAPTERxxx"},
		{unreadLyrics, opts.SyncedLyricsCleared, &p.LyricsChanged, "SYNCEDLYRICS"},
	} {
		removed, left := byName[k.kind], r.n[k.kind]-byName[k.kind]
		if left > 0 && k.cleared {
			*k.changed = true
			removed, left = r.n[k.kind], 0
		}
		if removed > 0 {
			p.Ops = append(p.Ops, fmt.Sprintf("unread %s comments removed: %d", k.what, removed))
		}
		if left > 0 && *k.changed {
			p.Warnings = core.Warn(p.Warnings, core.WarnMalformedTagEntryDropped,
				fmt.Sprintf("%d %s comment(s) that were not read are dropped with the ones replaced", left, k.what))
		}
	}
	// Rebuild keeps a picture comment whatever the edit, so only its name or a clear removes one.
	if n := byName[unreadPicture]; n > 0 {
		p.Ops = append(p.Ops, fmt.Sprintf("unread %s comments removed: %d", PictureComment, n))
	}
	if p.DropBlocks = opts.PicturesCleared && blocks > 0; p.DropBlocks {
		p.Ops = append(p.Ops, fmt.Sprintf("unreadable PICTURE blocks removed: %d", blocks))
	}
	return p
}
