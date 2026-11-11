// Package id3 reads and writes ID3v2 tags (v2.2, v2.3, v2.4) and reads ID3v1. The
// internal codecs that embed ID3 (MP3, raw AAC, WAV, and AIFF) share it, so the frame
// model, text encodings, unsynchronisation, the numeric-genre table, and the canonical
// projection live here once. It is reimplemented from the ID3v2.2/2.3/2.4 specifications;
// reference implementations were consulted for design only.
//
// The native model is preservation-first: every frame is kept in original order (decoded
// ones in a clean, re-renderable form; compressed, encrypted, and unknown ones verbatim),
// so a tag edit rewrites only the affected frames. v2.2 is read in full, normalised to
// v2.3 frame identifiers, and written back as v2.3; v2.3 and v2.4 round-trip at their own
// version.
package id3

import (
	"bytes"
	"errors"
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Header flag bits (the ID3v2 header's sixth byte).
const (
	hdrUnsync      = 0x80 // whole-tag (v2.2/v2.3) or all-frames (v2.4) unsynchronisation
	hdrExtHeader   = 0x40 // an extended header follows (v2.3/v2.4)
	hdrCompression = 0x40 // v2.2: compressed by an undefined scheme; the tag is ignored
	hdrFooter      = 0x10 // a 10-byte footer trails the tag (v2.4)
)

// Tag is a parsed ID3v2 tag: the decoded frames in original order plus the version
// metadata needed to write them back. srcVersion records what was read (2, 3, or 4, or any
// major version of a tag ignored whole); writeVersion is the version emitted (3 or 4; a v2.2
// source is modernised to v2.3; 0 for an ignored tag of a version no writer emits).
type Tag struct {
	srcVersion   byte
	writeVersion byte
	revision     byte
	frames       []Frame
	// padding is the free bytes after the last frame inside the tag region, measured on
	// read. Measuring is right for every source shape (a v2.2 tag's 6-byte frame headers,
	// an extended header, a footer), none of which RenderedSize models, since it sizes what
	// the writer emits.
	padding int64
	// malformedID names the frame header the walk stopped at short of the end of the tag
	// (tailCause says why), and malformedTail counts the bytes from that header to the end of
	// the region. The walk cannot read past it, so those bytes are not free padding (padding
	// is 0 for that stop) and a rewrite does not carry them. WithFrames clears all three.
	malformedID   string
	malformedTail int64
	tailCause     tailCause
	// ignored is the reason the whole tag was skipped (a v2.2 compression flag, a major
	// version no specification defines, a declared size past the end of the file), empty
	// when it parsed. Its frames are empty and its region is unreadable rather than free.
	ignored string
	// plainSizes records a v2.4 tag whose frame sizes were read as plain integers
	// ([v24FrameSizesArePlain]); a rewrite writes them sync-safe.
	plainSizes bool
	// unbounded records an ignored front tag that declares more bytes than the file holds,
	// so its extent past the header is unknown ([Tag.Unbounded]).
	unbounded bool
	// behind counts the bytes of the region [ParseTag] read that lie past the tag, unless they
	// are all zero: what a WAV or AIFF chunk longer than its tag holds ([Tag.Behind]).
	behind int64
}

// Behind reports the bytes that follow the tag in the region it was parsed from, 0 when they are
// all zero. A rewrite of that region does not carry them.
func (t *Tag) Behind() int64 {
	if t == nil {
		return 0
	}
	return t.behind
}

// WarnBehind appends the read warning for the bytes a WAV or AIFF chunk holds behind t.
func WarnBehind(ws []core.Warning, t *Tag) []core.Warning {
	if n := t.Behind(); n > 0 {
		ws = core.Warn(ws, core.WarnMalformedTagEntry, fmt.Sprintf(
			"%d byte(s) follow the ID3v2 tag inside its chunk; kept until an edit rewrites the chunk", n))
	}
	return ws
}

// ChunkKept reports whether a rewrite leaves the WAV or AIFF chunk holding src alone: rebuilt,
// the tag rebuilt from it, holds the same frames, so the chunk is copied whole, with its padding
// and whatever its tag could not read.
func ChunkKept(src, rebuilt *Tag) bool {
	return src != nil && rebuilt != nil && FramesEqual(rebuilt.Frames(), src.Frames())
}

// AppendChunkDropped appends the warnings for what a rewrite of src's chunk, or its removal,
// loses: the unreadable tail of its tag ([AppendMalformedTailDropped]) and the bytes behind it.
func AppendChunkDropped(ws []core.Warning, src *Tag, written bool) []core.Warning {
	ws = AppendMalformedTailDropped(ws, src, written)
	if n := src.Behind(); n > 0 {
		ws = core.Warn(ws, core.WarnMalformedTagEntryDropped, fmt.Sprintf(
			"%d byte(s) that follow the ID3v2 tag inside its chunk are not carried into the rewritten file", n))
	}
	return ws
}

// Unbounded reports an ignored front tag that declares more bytes than the file holds: its
// region runs from the start of the file to where the stream starts, or to the end of the
// file with none, which the codec finds.
func (t *Tag) Unbounded() bool { return t != nil && t.unbounded }

// Ignored reports why the whole tag was skipped, or "" when it parsed.
func (t *Tag) Ignored() string {
	if t == nil {
		return ""
	}
	return t.ignored
}

// Padding reports the free bytes inside the tag region, after the last frame.
func (t *Tag) Padding() int64 { return t.padding }

// MalformedTail reports the frame header the frame walk stopped at, short of the end of the
// tag region, and the byte count from it to that end, or ("", 0) when the tag parsed cleanly.
func (t *Tag) MalformedTail() (string, int64) {
	if t == nil {
		return "", 0
	}
	return t.malformedID, t.malformedTail
}

// tailStop describes where the frame walk stopped short of the end of the tag, for the
// warnings about the bytes from there on.
func (t *Tag) tailStop() string {
	switch t.tailCause {
	case tailBadID:
		return fmt.Sprintf("a frame ID %q with a byte outside printable ASCII", t.malformedID)
	case tailCutHeader:
		return fmt.Sprintf("a frame header %q the tag ends inside", t.malformedID)
	case tailDirtyPad:
		return "padding that holds non-zero bytes"
	}
	return fmt.Sprintf("the %q frame, which declares more bytes than the tag holds", t.malformedID)
}

// SrcVersion reports the ID3v2 major version the tag's header declares (2, 3, or 4 for a tag
// that parsed).
func (t *Tag) SrcVersion() byte { return t.srcVersion }

// WriteVersion reports the version a rewrite will emit (3 or 4), or 0 for an ignored tag of a
// version no writer emits, which [WriteVersionOr] replaces with the format default.
func (t *Tag) WriteVersion() byte { return t.writeVersion }

// WriteVersionOr returns t's write version, or the format default when t is nil or has none. It
// is the one home for which ID3 minor version a codec writes, the v2.3-vs-v2.4 split that
// governs date-frame and numeric-genre fidelity.
func WriteVersionOr(t *Tag, f core.Format) byte {
	if t != nil && t.writeVersion != 0 {
		return t.writeVersion
	}
	return core.DefaultID3Version(f)
}

// id3Doc is the native-document view WriteVersionFor needs: the file's parsed ID3 tag, or
// nil when the file has none. Every ID3-backed codec's *doc implements it with a one-line
// ID3Tag accessor.
type id3Doc interface{ ID3Tag() *Tag }

// WriteVersionFor returns the ID3 minor version codec format f would write for media m: the
// file's own tag version when present, else the format default. m.Native must implement
// id3Doc (each ID3-backed codec's doc does); a nil m or a foreign Native yields the default.
func WriteVersionFor(m *core.Media, f core.Format) byte {
	var t *Tag
	if m != nil {
		if d, ok := m.Native.(id3Doc); ok {
			t = d.ID3Tag()
		}
	}
	return WriteVersionOr(t, f)
}

// Frames returns the decoded frames in order; a nil tag has none.
func (t *Tag) Frames() []Frame {
	if t == nil {
		return nil
	}
	return t.frames
}

// Clone returns a deep copy of the tag.
func (t *Tag) Clone() *Tag {
	c := *t
	c.frames = make([]Frame, len(t.frames))
	for i, f := range t.frames {
		c.frames[i] = f.Clone()
	}
	return &c
}

// WithFrames returns a copy of the tag carrying frames, for building the plan's result.
// The source tag's measured padding describes the on-disk region the rewrite replaces,
// so it is not inherited.
func (t *Tag) WithFrames(frames []Frame) *Tag {
	c := *t
	c.frames = frames
	c.padding = 0
	c.malformedID, c.malformedTail, c.tailCause = "", 0, 0 // a rewritten tag has no unreadable tail
	c.ignored = ""
	c.plainSizes, c.unbounded = false, false
	c.behind = 0
	return &c
}

// NewEmpty returns an empty tag that will be written at the given version
// (clamped to v2.3 or v2.4), for files that have no ID3v2 tag yet.
func NewEmpty(writeVersion byte) *Tag {
	if writeVersion != 4 {
		writeVersion = 3
	}
	return &Tag{srcVersion: writeVersion, writeVersion: writeVersion}
}

// TagSize returns the total on-disk length of the ID3v2 tag whose 10-byte header
// begins header, and whether header is a valid ID3v2 header. The size field is
// sync-safe (7 bits per byte) and excludes the 10-byte header; a v2.4 footer
// adds another 10 bytes.
func TagSize(header []byte) (int64, bool) {
	if len(header) < 10 || header[0] != 'I' || header[1] != 'D' || header[2] != '3' {
		return 0, false
	}
	if header[3] == 0xFF || header[4] == 0xFF {
		return 0, false // reserved; not a real version
	}
	if !syncSafeByte(header[6]) || !syncSafeByte(header[7]) ||
		!syncSafeByte(header[8]) || !syncSafeByte(header[9]) {
		return 0, false
	}
	size := syncSafe(header[6:10])
	total := int64(10) + size
	if header[5]&hdrFooter != 0 {
		total += 10
	}
	return total, true
}

// LeadingDup is an ID3v2 tag in the chain a file opens with other than the one a rewrite keeps,
// Len bytes at Off, of major version Version. A rewrite drops it; [DupLosses] says what that loses,
// from its digest.
type LeadingDup struct {
	Off, Len int64
	Version  byte
	digest   DupDigest
}

// Front is the chain of ID3v2 tags a file opens with, as [ReadFrontAll] reads it: Tag, the one a
// rewrite keeps, Len bytes at Off; the others in file order as duplicates; and where the chain ends.
// A file with no readable leading ID3 has a nil Tag and an End of 0.
type Front struct {
	Tag      *Tag
	Off, Len int64
	Dups     []LeadingDup
	End      int64
}

// ReadFrontAll reads the chain of ID3v2 tags at the start of src, as [core.MaxLeadingTags] bounds the
// chain detection skips. The tag a rewrite keeps is the first that parses and holds frames (the
// WAV id3-chunk precedent, "first that parses"), else the first; an empty or unreadable tag ahead
// of it is a duplicate like any after it. MP3 and raw AAC, whose
// authoritative container is a front ID3v2 tag, share it; a codec that only preserves a stray
// leading ID3 verbatim reads it with [ReadFrontStray]. size is the source size (each tag must fit
// within it); limit bounds each allocation, header included: a header or tag over it is refused with
// [waxerr.ErrSizeTooLarge].
func ReadFrontAll(src core.ReaderAtSized, size, limit int64, maxElements int) (Front, []core.Warning, error) {
	hdr, total, ok, err := frontHeader(src, 0, size, limit)
	if err != nil || !ok {
		return Front{}, nil, err
	}
	if total > size {
		// A real header whose declared size runs past the end of the file is an unreadable
		// tag, not a file without one or audio: ignored and unbounded, so the codec ends its
		// region where the stream starts.
		return Front{Tag: &Tag{srcVersion: hdr[3], revision: hdr[4], unbounded: true, ignored: fmt.Sprintf(
			"the leading ID3v2 tag declares %d bytes but the file holds %d; the tag could not be read", total, size)},
			Len: 10, End: 10}, nil, nil
	}
	// The chain's tags are kept as parsed until the one a rewrite keeps is known, the first that
	// holds frames; those ahead of it hold none. Every other member is held as its digest only.
	var chain []LeadingDup
	var tags []*Tag
	keep := -1
	add := func(d LeadingDup, tg *Tag) {
		if keep >= 0 {
			d.digest = digest(tg)
		} else {
			tags = append(tags, tg)
			if HoldsFrames(tg) {
				keep = len(chain)
			}
		}
		chain = append(chain, d)
	}
	if major := hdr[3]; major < 2 || major > 4 {
		// No specification defines the frames of such a tag, so it is ignored whole, the
		// format's default version taking its place on a rewrite.
		add(LeadingDup{Len: total, Version: major}, &Tag{srcVersion: major, revision: hdr[4], ignored: fmt.Sprintf(
			"the tag declares ID3v2 major version %d, which no ID3v2 specification defines; the tag is ignored", major)})
	} else {
		raw, err := bits.ReadSlice(src, 0, total, limit)
		if err != nil {
			return Front{}, nil, err
		}
		tg, err := ParseTag(raw, maxElements)
		if err != nil {
			return Front{}, nil, err
		}
		add(LeadingDup{Len: total, Version: major}, tg)
	}
	for off := total; len(chain) < core.MaxLeadingTags; {
		_, n, ok, err := frontHeader(src, off, size, limit)
		if err != nil {
			return Front{}, nil, err
		}
		if !ok || n > size-off {
			break
		}
		tg, err := readChained(src, off, n, limit, maxElements)
		if err != nil {
			return Front{}, nil, err
		}
		add(LeadingDup{Off: off, Len: n, Version: tg.version}, tg.tag)
		off += n
	}
	keep = max(keep, 0)
	for i, tg := range tags {
		if i != keep {
			chain[i].digest = digest(tg)
		}
	}
	last := chain[len(chain)-1]
	f := Front{Tag: tags[keep], Off: chain[keep].Off, Len: chain[keep].Len, End: last.Off + last.Len}
	f.Dups = slices.Delete(slices.Clone(chain), keep, keep+1)
	var ws []core.Warning
	if len(f.Dups) > 0 {
		ws = core.Warn(ws, core.WarnDuplicateTagBlock, fmt.Sprintf(
			"the file opens with %d chained ID3v2 tags; the first readable one holding frames, else the first, is authoritative and the rest are dropped on rewrite", len(chain)))
	}
	return f, ws, nil
}

// HoldsFrames reports whether t parsed, is not ignored and holds frames: of several tags a file
// holds where one counts (a chain of front tags, WAV and AIFF id3 chunks), the first such tag is
// the one that counts, else the first that parses.
func HoldsFrames(t *Tag) bool {
	return t != nil && t.ignored == "" && len(t.frames) > 0
}

// PickTag picks, of n ID3v2 tags a file holds where one counts (the id3 chunks of a WAV or AIFF
// file), the one that does: the first that parses and holds frames ([HoldsFrames]), else the first
// that parses, or -1 when none does. body reads tag i, each tag is read once, and only the picked
// tag is kept as parsed; digests holds every other tag's digest. A failed read of a tag up to the
// picked one refuses, as does a tag over the maxElements cap wherever it sits; behind the picked
// one, a short read or a tag that does not parse digests as unreadable, and a fault or a read over
// the allocation limit refuses.
func PickTag(n int, body func(i int) ([]byte, error), maxElements int) (pick int, picked *Tag, digests []DupDigest, err error) {
	tags := make([]*Tag, 0, n)
	first := -1
	pick = -1
	for i := range n {
		b, err := body(i)
		if err != nil {
			return -1, nil, nil, err
		}
		tg, err := ParseTag(b, maxElements)
		// A MaxElements cap breach is a hard error, not "this chunk is not a tag": swallowing
		// it would treat a valid tag as absent and rewrite without it.
		if errors.Is(err, waxerr.ErrSizeTooLarge) {
			return -1, nil, nil, err
		}
		tags = append(tags, tg)
		if tg != nil && first < 0 {
			first = i
		}
		if HoldsFrames(tg) {
			pick = i
			break
		}
	}
	if pick < 0 {
		pick = first
	}
	if pick < 0 {
		return -1, nil, nil, nil
	}
	digests = make([]DupDigest, n)
	for i := range n {
		if i == pick {
			continue
		}
		if i < len(tags) {
			digests[i] = digest(tags[i])
			continue
		}
		b, err := body(i)
		if fatal := bits.Fatal(err); fatal != nil {
			return -1, nil, nil, fatal
		}
		var tg *Tag
		if err == nil {
			if tg, err = ParseTag(b, maxElements); bits.Fatal(err) != nil {
				return -1, nil, nil, err
			}
		}
		digests[i] = digest(tg)
	}
	return pick, tags[pick], digests, nil
}

// readChained reads the n-byte chained tag at off, nil as the tag when it does not parse. A read
// over limit, or a frame count over maxElements, is refused.
func readChained(src core.ReaderAtSized, off, n, limit int64, maxElements int) (chained, error) {
	raw, err := bits.ReadSlice(src, off, n, limit)
	if err != nil {
		return chained{}, err
	}
	tg, err := ParseTag(raw, maxElements)
	if errors.Is(err, waxerr.ErrSizeTooLarge) {
		return chained{}, err
	}
	return chained{tag: tg, version: raw[3]}, nil
}

// chained is a tag of the front chain as [readChained] reads it, and its major version.
type chained struct {
	tag     *Tag
	version byte
}

// ReadFrontStray reads the stray leading ID3v2 tag of a codec that only preserves one (FLAC,
// WavPack, Monkey's Audio, Musepack): the raw tag, nil when src has none, and streamAt, where c's
// stream starts behind it as [core.StreamStart] finds it. Bytes between the two (chained tags or a
// gap) go with the tag. The warnings name the stream. size and limit bound the reads as for
// [ReadFrontAll].
func ReadFrontStray(src core.ReaderAtSized, size, limit int64, c core.Codec, stream string) (raw []byte, streamAt int64, ws []core.Warning, err error) {
	raw, err = ReadFrontRaw(src, size, limit)
	if err != nil || raw == nil {
		return nil, 0, nil, err
	}
	streamAt = int64(len(raw))
	ws = core.Warn(nil, core.WarnStrayLeadingID3, fmt.Sprintf("ID3v2 tag of %d bytes precedes the %s stream; preserved", streamAt, stream))
	at, ok, err := core.StreamStart(src, TagSize, c, limit)
	if err != nil {
		return nil, 0, nil, err
	}
	if ok && at > streamAt {
		ws = core.Warn(ws, core.WarnLeadingBytes, fmt.Sprintf(
			"%d byte(s) sit between the leading ID3v2 tag and the %s stream; preserved with the tag", at-streamAt, stream))
		streamAt = at
	}
	return raw, streamAt, ws, nil
}

// StreamBehindFront finds where c's stream starts behind its front ID3v2 tags, which end at
// frontEnd ([ReadFrontAll]), as [core.StreamStart] finds it past chained tags: at frontEnd or past
// a gap. With no stream confirmed there, scan, unless nil, finds it on the codec's own terms at or
// past from, the tags' end. Bytes between the tags and the stream warn leading-bytes, naming unit
// (the codec's first audio unit). Without a front tag the stream starts at 0, where detection found
// it. ok is false when no stream is found behind the tags; the start is then their end.
func StreamBehindFront(src core.ReaderAtSized, frontEnd, limit int64, c core.Codec, unit string, scan func(from int64) (int64, bool, error)) (at int64, ok bool, ws []core.Warning, err error) {
	if frontEnd == 0 {
		return 0, true, nil, nil
	}
	at, ok, err = core.StreamFrom(src, frontEnd, TagSize, c, limit)
	if err != nil {
		return 0, false, nil, err
	}
	if !ok && scan != nil {
		if at, ok, err = scan(frontEnd); err != nil {
			return 0, false, nil, err
		}
	}
	if !ok || at <= frontEnd {
		return frontEnd, ok, nil, nil
	}
	ws = core.Warn(nil, core.WarnLeadingBytes, fmt.Sprintf(
		"%d byte(s) sit between the ID3v2 tag and the first %s; preserved", at-frontEnd, unit))
	return at, true, ws, nil
}

// DupLosses says what dropping each of dups loses, for [core.AppendDuplicateBlockDropped]: before
// and after are the media as parsed and as the rewrite reads, and frames the kept tag's frames as
// parsed and as written ([NewDupGrader]).
func DupLosses(dups []LeadingDup, before, after *core.Media, frames ...[]Frame) []core.DuplicateContent {
	if len(dups) == 0 {
		return nil
	}
	g := NewDupGrader(before, after, frames...)
	out := make([]core.DuplicateContent, len(dups))
	for i, d := range dups {
		out[i] = g.Loss(d.digest, fmt.Sprintf("unreadable ID3v2.%d tag(s)", d.Version))
	}
	return out
}

// DupDigest is what grading a duplicate tag a rewrite drops needs, taken as the tag is read so a
// file of large duplicate tags holds hashes rather than their bodies: its values, its comment and
// lyrics items and its chapters, the hashes of its pictures and synced lyric sets, the IDs and
// hashes of the frames the canonical view does not carry, and how many of its bytes the frame walk
// could not read. A tag that does not parse, or is ignored whole, digests as unreadable. A digest
// is never modified, so copies share it.
type DupDigest struct {
	readable bool
	tags     tag.TagSet
	comments []core.TextItem
	lyrics   []core.TextItem
	chapters []core.Chapter
	pictures []uint64
	synced   []uint64
	frames   []frameDigest
	unread   int64
}

// frameDigest is a frame's ID and its [frameHash].
type frameDigest struct {
	id   string
	hash uint64
}

// digest takes the [DupDigest] of t, a duplicate tag as parsed, nil when it does not parse.
func digest(t *Tag) DupDigest {
	if t == nil || t.ignored != "" {
		return DupDigest{}
	}
	p := Project(t)
	d := DupDigest{readable: true, tags: p.Tags, comments: p.Comments, lyrics: p.Lyrics, chapters: p.Chapters}
	for _, pic := range p.Pictures {
		d.pictures = append(d.pictures, core.PictureHash(pic))
	}
	for _, l := range p.SyncedLyrics {
		d.synced = append(d.synced, lyricsHash(l))
	}
	for _, f := range t.uncarried() {
		d.frames = append(d.frames, frameDigest{id: f.ID, hash: frameHash(f)})
	}
	// What the tag could not read, and what its chunk holds behind it.
	_, d.unread = t.MalformedTail()
	d.unread += t.behind
	return d
}

// DupGrader grades the duplicate tags a rewrite drops against what the file holds apart from them:
// the frames of the tag it keeps, as parsed and as written (a duplicate frame byte-identical to
// either was the file's own, so dropping it is the edit's doing), and the pictures, chapters and
// synced lyric sets of the media before and after the edit. Each is indexed once, on first need, so
// grading is linear in what the duplicates hold and costs nothing without one. WAV and AIFF grade a
// dropped id3 chunk with it too.
type DupGrader struct {
	before, after *core.Media
	frameSets     [][]Frame

	indexed                  bool
	frames, pictures, synced core.HashSet
	chapters                 map[core.Chapter]bool
	// comments and lyrics hold each item of the media, and its text alone under a bare item.
	comments, lyrics map[core.TextItem]bool
}

// NewDupGrader grades against before and after and the frame lists frames.
func NewDupGrader(before, after *core.Media, frames ...[]Frame) *DupGrader {
	return &DupGrader{before: before, after: after, frameSets: frames}
}

// index builds the indexes the first time a duplicate needs them.
func (g *DupGrader) index() {
	if g.indexed {
		return
	}
	g.indexed = true
	g.frames, g.synced, g.chapters = core.HashSet{}, core.HashSet{}, map[core.Chapter]bool{}
	g.comments, g.lyrics = map[core.TextItem]bool{}, map[core.TextItem]bool{}
	for _, fs := range g.frameSets {
		for _, f := range fs {
			g.frames.Add(frameHash(f))
		}
	}
	g.pictures = core.PictureHashes(g.before, g.after)
	for _, m := range []*core.Media{g.before, g.after} {
		for _, c := range m.Chapters {
			g.chapters[c] = true
		}
		for _, l := range m.SyncedLyrics {
			g.synced.Add(lyricsHash(l))
		}
		for _, it := range m.Comments {
			g.comments[plainLanguage(it)], g.comments[core.TextItem{Text: it.Text}] = true, true
		}
		for _, it := range m.Lyrics {
			g.lyrics[plainLanguage(it)], g.lyrics[core.TextItem{Text: it.Text}] = true, true
		}
	}
}

// Loss says what dropping a duplicate tag, digested as d, loses. An unreadable one loses all it
// holds, counted as one of unreadable, its kind. Else it loses its values, graded by
// [core.AppendDuplicateBlockDropped], the pictures, chapters and synced lyric sets the file holds
// nowhere else, the description and language of a comment or lyric whose text the file holds
// under others, the frames the canonical view does not carry that the kept tag holds neither as
// parsed nor as written, and the bytes its frame walk could not read. Its chapters are compared as
// a read gives them, a last one starting past the end opened up ([core.OpenPastDurationEnds]).
func (g *DupGrader) Loss(d DupDigest, unreadable string) core.DuplicateContent {
	if !d.readable {
		return core.DuplicateContent{Other: []core.ContentCount{{Kind: unreadable, N: 1}}}
	}
	g.index()
	c := core.DuplicateContent{Tags: d.tags}
	for _, h := range d.pictures {
		if !g.pictures.Has(h) {
			c.Pictures++
		}
	}
	for _, ch := range core.ChaptersOpenedPastDuration(slices.Clone(d.chapters), g.before.Properties.Duration()) {
		if !g.chapters[ch] {
			c.Chapters++
		}
	}
	for _, h := range d.synced {
		if !g.synced.Has(h) {
			c.SyncedLyrics++
		}
	}
	var other core.Tally
	for _, q := range []struct {
		kind  string
		items []core.TextItem
		held  map[core.TextItem]bool
	}{{"comment", d.comments, g.comments}, {"lyrics", d.lyrics, g.lyrics}} {
		for _, it := range q.items {
			// A text the file does not hold at all is a lost value, graded with the tags.
			if !q.held[plainLanguage(it)] && q.held[core.TextItem{Text: it.Text}] {
				other.Add(q.kind+" description or language", 1)
			}
		}
	}
	for _, f := range d.frames {
		if !g.frames.Has(f.hash) {
			other.Add(fmt.Sprintf("%q frame(s)", f.id), 1)
		}
	}
	if d.unread > 0 {
		other.Add("unreadable byte(s)", d.unread)
	}
	c.Other = other.Counts()
	return c
}

// plainLanguage returns it with the default language cleared: every ID3 writer fills that one
// in, so losing it against an undefined one loses nothing.
func plainLanguage(it core.TextItem) core.TextItem {
	if it.Language == DefaultLanguage {
		it.Language = ""
	}
	return it
}

// frameHash hashes a frame's ID and body.
func frameHash(f Frame) uint64 {
	h := core.ContentHash()
	h.WriteString(f.ID)
	h.Write(f.Body)
	return h.Sum64()
}

// lyricsHash hashes what [core.EqualSyncedLyrics] compares.
func lyricsHash(l core.SyncedLyrics) uint64 {
	h := core.ContentHash()
	fmt.Fprintf(h, "%s\x00%s\x00", l.Language, l.Description)
	for _, line := range l.Lines {
		fmt.Fprintf(h, "%d\x00%s\x00", line.Time, line.Text)
	}
	return h.Sum64()
}

// SplitDups splits dups at off into those before it and those after it.
func SplitDups(dups []LeadingDup, off int64) (before, after []LeadingDup) {
	i := slices.IndexFunc(dups, func(d LeadingDup) bool { return d.Off > off })
	if i < 0 {
		return dups, nil
	}
	return dups[:i], dups[i:]
}

// DupEntries are the native-view entries of the duplicates.
func DupEntries(dups []LeadingDup) []core.NativeEntry {
	out := make([]core.NativeEntry, len(dups))
	for i, d := range dups {
		out[i] = core.NativeEntry{Kind: fmt.Sprintf("ID3v2.%d", d.Version), Size: int(d.Len), Note: "duplicate, dropped on rewrite"}
	}
	return out
}

// SkipTags passes the tags that open at off, as a file joined there opens with its ID3v2 tags,
// perhaps behind an ID3v1 tag the file before it ends with: at most [core.MaxLeadingTags] of them,
// each by its header alone. It returns where they end and how many it passed. A tag that runs past
// end stops the skip. The headers are read as [bits.ReadWithin] reads: one past the end of the file
// is no tag, and a failed read or one over limit is an error.
func SkipTags(src bits.Sized, off, end, limit int64) (int64, int, error) {
	for tags := range core.MaxLeadingTags {
		head, err := bits.ReadWithin(src, off, max(min(end-off, 128), 0), limit)
		if err != nil || head == nil {
			return off, tags, err
		}
		n, ok := int64(128), LooksLikeID3v1(head)
		if !ok {
			n, ok = TagSize(head)
		}
		if !ok || off+n > end {
			return off, tags, nil
		}
		off += n
	}
	return off, core.MaxLeadingTags, nil
}

// WarnJoinedTags reports, once, the n tags [SkipTags] passed between joined streams of the named
// kind, which stay in the audio unread; none adds nothing.
func WarnJoinedTags(ws []core.Warning, n int, stream string) []core.Warning {
	if n == 0 {
		return ws
	}
	return core.Warn(ws, core.WarnTrailingBytes, fmt.Sprintf(
		"%d ID3 tag(s) between joined %s streams stay in the audio essence; preserved verbatim, not read", n, stream))
}

// ReadFrontRaw reads the raw bytes of a leading ID3v2 tag; nil when src has none. size and limit
// bound the read as for [ReadFrontAll]: the tag must fit within size, and a header or tag over
// limit is refused with [waxerr.ErrSizeTooLarge].
func ReadFrontRaw(src core.ReaderAtSized, size, limit int64) ([]byte, error) {
	_, total, ok, err := frontHeader(src, 0, size, limit)
	if err != nil || !ok || total > size {
		return nil, err
	}
	return bits.ReadSlice(src, 0, total, limit)
}

// frontHeader reads the ID3v2 header at off in src: the header and the tag's total length, or false
// when src holds less than a header there or none. A header over limit is refused, and a read of it
// that fails with a device error is that error.
func frontHeader(src core.ReaderAtSized, off, size, limit int64) ([]byte, int64, bool, error) {
	hdr, err := bits.ReadSlice(src, off, max(min(size-off, 10), 0), limit)
	if fatal := bits.Fatal(err); fatal != nil {
		return nil, 0, false, fatal
	}
	if err != nil {
		return nil, 0, false, nil // a short read: no front tag
	}
	total, ok := TagSize(hdr)
	return hdr, total, ok, nil
}

// syncSafe decodes a 28-bit sync-safe integer from four bytes (each contributes
// its low 7 bits).
func syncSafe(b []byte) int64 {
	return int64(b[0]&0x7F)<<21 | int64(b[1]&0x7F)<<14 | int64(b[2]&0x7F)<<7 | int64(b[3]&0x7F)
}

func syncSafeByte(b byte) bool { return b&0x80 == 0 }

// putSyncSafe writes v as a 28-bit sync-safe integer into a 4-byte slice.
func putSyncSafe(dst []byte, v int64) {
	dst[0] = byte(v>>21) & 0x7F
	dst[1] = byte(v>>14) & 0x7F
	dst[2] = byte(v>>7) & 0x7F
	dst[3] = byte(v) & 0x7F
}

// ParseTag decodes a complete ID3v2 tag region (starting at the "ID3" header).
// It tolerates truncation (parsing what is present) but rejects a missing or
// reserved header. The caller bounds the size of data when reading it from the
// source; maxElements additionally caps the frame count (0 = unlimited) so a body
// of minimum-size frames cannot accumulate descriptors past the shared limit.
func ParseTag(data []byte, maxElements int) (*Tag, error) {
	size, ok := TagSize(data)
	if !ok {
		return nil, fmt.Errorf("%w: not an ID3v2 header", waxerr.ErrInvalidData)
	}
	major := data[3]
	if major < 2 || major > 4 {
		// TagSize already matched an "ID3" header, so an out-of-range major version is a
		// corrupt tag: invalid data (exit 4), not an unsupported format (exit 3), matching
		// the "not an ID3v2 header" sibling above.
		return nil, fmt.Errorf("%w: ID3v2 major version %d out of range", waxerr.ErrInvalidData, major)
	}
	flags := data[5]

	end := size
	if flags&hdrFooter != 0 {
		end -= 10 // the footer is not part of the frame region
	}
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	body := data[10:end]

	t := &Tag{srcVersion: major, revision: data[4]}
	if rest := data[min(size, int64(len(data))):]; len(bytes.TrimRight(rest, "\x00")) > 0 {
		t.behind = int64(len(rest))
	}
	t.writeVersion = major
	if major == 2 {
		t.writeVersion = 3 // modernise obsolete v2.2 on write
	}

	if major == 2 && flags&hdrCompression != 0 {
		t.ignored = "the ID3v2.2 tag sets the compression flag, for which v2.2 defines no scheme; the tag is ignored"
		return t, nil
	}

	// v2.2/v2.3 unsynchronisation covers the whole tag; undo it before parsing.
	// v2.4 signals unsynchronisation per frame (the header bit, if set, means it
	// applies to all frames; handled in parseFrames).
	tagUnsync := flags&hdrUnsync != 0
	if major <= 3 && tagUnsync {
		body = deunsync(body)
	}
	if (major == 3 || major == 4) && flags&hdrExtHeader != 0 {
		body = skipExtHeader(body, major)
	}

	t.plainSizes = major == 4 && v24FrameSizesArePlain(body, maxElements)
	frames, rest, malformed, cause, err := parseFrames(body, major, tagUnsync, t.plainSizes, maxElements)
	if err != nil {
		return nil, err
	}
	if major == 4 {
		syncSafeChapterSubframes(frames)
	}
	t.frames = frames
	if malformed != "" {
		// The remainder is a region the walk could not read, not free space a rewrite may
		// reuse. Counting it as padding would let dump report it as room the file has.
		t.malformedID, t.malformedTail, t.tailCause = malformed, int64(rest), cause
	} else {
		t.padding = int64(rest)
	}
	return t, nil
}

// skipExtHeader advances past the optional extended header. v2.3's size field
// counts the bytes after it (6 or 10); v2.4's sync-safe size counts the whole
// extended header including the size field. A malformed size leaves the body
// untouched rather than discarding frames.
func skipExtHeader(body []byte, major byte) []byte {
	if len(body) < 4 {
		return body
	}
	if major == 4 {
		n := syncSafe(body[0:4])
		if n >= 4 && n <= int64(len(body)) {
			return body[n:]
		}
		return body
	}
	// v2.3: 4-byte plain size of the rest of the extended header.
	n := int64(body[0])<<24 | int64(body[1])<<16 | int64(body[2])<<8 | int64(body[3])
	if total := 4 + n; total >= 4 && total <= int64(len(body)) {
		return body[total:]
	}
	return body
}
