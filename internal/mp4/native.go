package mp4

import (
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// item is one decoded ilst child: the four-character atom name and its raw
// payload (everything after the 8-byte atom header). Whether the canonical
// rebuild owns an item - versus preserving it verbatim, the MP4 analogue of ID3's
// opaque frames - is recomputed on demand by owned(), not cached here, so the
// projection stays a pure read of an immutable document.
type item struct {
	name    [4]byte
	payload []byte
}

func (it item) id() string { return string(it.name[:]) }

// doc is the MP4 native document: the top-level atom layout, references to the
// tag-path atoms (moov / udta / meta / ilst) and an adjacent free padding atom,
// the decoded ilst items, every chunk-offset table (for media-offset fixups when
// the metadata is resized), and the mdat essence ranges. It is the
// preservation-first base for rewrites and satisfies core.NativeDoc.
type doc struct {
	topLevel []atomRef // every top-level atom in order

	moov *atomRef // the movie box (required for a tagged/writable file)
	udta *atomRef // moov.udta, if present
	meta *atomRef // moov.udta.meta, if present
	ilst *atomRef // moov.udta.meta.ilst, if present
	free *atomRef // a free or skip padding atom adjacent to ilst inside meta, if present (reusable)
	chpl *atomRef // moov.udta.chpl Nero chapter list, if present

	// QuickTime chapter-write refs (the chapter text track lives in moov as a
	// sibling trak referenced from the audio track's tref "chap"). These let a
	// chapter edit rebuild that track without re-reading the source: the audio
	// trak to attach a tref to, where its mdia starts (the tref insertion point),
	// any existing tref, the existing chapter trak to replace, and the mvhd fields
	// a new track needs (the movie timescale/duration and a free track id).
	audioTrak    *atomRef // the first "soun" trak, if any
	audioMdiaOff int64    // absolute offset of the audio trak's mdia (tref goes before it)
	audioTref    *atomRef // the audio trak's tref, if any
	audioTrefRaw []byte   // the audio tref payload (to re-emit without "chap" on clear)
	audioHasChap bool     // the audio tref carries a "chap" reference (may be dangling)
	chapTrak     *atomRef // the resolved QuickTime chapter text trak, if any
	chapTrackID  uint32   // the chapter text track's track id (reused when rebuilt)

	mvhd           *atomRef
	movieTimescale uint32 // mvhd timescale (chapter track shares it; 0 if unread)
	movieDuration  uint64 // mvhd duration in movie units (the last chapter's span)
	nextTrackID    uint32 // a track id free for a new chapter track
	nextTrackIDOff int64  // absolute offset of mvhd's next_track_ID field (0 if unread)

	items     []item        // decoded ilst children (nil when no ilst)
	offTables []offsetTable // every stbl stco/co64 in moov, in document order
	// auxTables holds every stbl saio in moov, kept apart from offTables because a saio
	// locates sample auxiliary information (CENC) rather than a media chunk. Both live in
	// an mdat and both shift on a resize, but only a chunk offset marks an mdat as audio
	// essence, so merging the two would skew the essence trim (firstNonChapterChunk) and
	// silently change the essence digest.
	auxTables []offsetTable
	mdats     [][2]int64 // mdat payload ranges (offset, length), in document order
	// mdatTruncated records that an mdat atom's declared size overran EOF and was
	// clamped - a truncated file. Set from the atom walk's own clamp, so the 64-bit
	// mdat size that would overflow an offset+size computation never reaches one.
	mdatTruncated bool
	fragmented    bool // a top-level moof: readable, but Plan refuses the rewrite
	auxUnknown    bool // a saio with an unrecognized version: offsets cannot be patched
	// hasIloc records an iloc item-location box inside moov.udta.meta. Its absolute file
	// offsets sit in the very region an ilst rewrite replaces, so a write is refused.
	hasIloc bool

	// udtaRaw is the verbatim moov.udta payload (nil when there is no udta). A
	// chapter rewrite splices the new ilst/chpl byte ranges into it and copies
	// every other udta byte unchanged, so unknown user-data siblings survive.
	udtaRaw []byte

	cfg        fmtCfg
	track      core.AudioTrack
	size       int64
	majorBrand string // ftyp major brand (e.g. "M4A ", "M4B "), for the native view

	// Chapter model. chapters is the projected, deduplicated list (a Nero chpl
	// list and/or a QuickTime chapter text track project into it). chplVersion is
	// the version byte of an existing chpl, preserved when chpl is re-rendered.
	// hasQTChapters records that a QuickTime chapter text track is present (and, on
	// a post-write document, that one was written): a chapter edit rebuilds it from
	// the edited model so both representations stay in sync.
	chapters      []core.Chapter
	chplVersion   uint8
	chplCount     int // chapters in the chpl atom specifically (for the native view)
	hasQTChapters bool
	// chapterConflict records that the chpl and QuickTime track disagreed at parse;
	// it is carried into a post-write document so its warnings match a fresh parse.
	chapterConflict bool
	// oversizedAtom names a final top-level atom whose declared size overran EOF and was
	// clamped, which is also what bytes appended after the last atom look like. Empty when
	// the file tiles cleanly. mdat and moov are excluded: they carry their own
	// truncated-audio warnings.
	oversizedAtom string
}

func (d *doc) Format() core.Format { return core.FormatMP4 }

// refuseWrite reports why this document cannot be rewritten at all, or nil when it can.
// Plan returns the error and Capabilities reports ReadOnly from the same call, so the
// capability a caller is shown and the outcome of an actual write cannot diverge - a
// second, hand-maintained copy of the predicate would drift the moment either side gained
// a case. Each refusal here is unconditional: a file matching one cannot be written by any
// edit, which is what ReadOnly claims. A refusal that depended on what the edit contained
// would belong in Plan alone.
func (d *doc) refuseWrite() error {
	switch {
	case d.fragmented:
		return fmt.Errorf("%w: refusing to rewrite a fragmented MP4; a metadata resize cannot fix up a movie fragment's sample offsets (tags are readable)",
			waxerr.ErrFragmented)
	case d.auxUnknown:
		return fmt.Errorf("%w: MP4 has a saio box this codec cannot decode; its sample auxiliary offsets cannot be shifted safely",
			waxerr.ErrUnsupportedFormat)
	case d.hasIloc:
		// An iloc holds absolute file offsets that nothing patches, so any rewrite that
		// moves bytes strands them. Nothing audio-shaped carries one (it is a HEIF/MPEG-21
		// box), so refuse on presence rather than parse its nibble-packed extent table.
		return fmt.Errorf("%w: MP4 carries an iloc item-location box this codec cannot relocate",
			waxerr.ErrUnsupportedFormat)
	}
	return nil
}

// Clone deep-copies the document so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	c.topLevel = slices.Clone(d.topLevel)
	c.moov = cloneRef(d.moov)
	c.udta = cloneRef(d.udta)
	c.meta = cloneRef(d.meta)
	c.ilst = cloneRef(d.ilst)
	c.free = cloneRef(d.free)
	c.chpl = cloneRef(d.chpl)
	c.audioTrak = cloneRef(d.audioTrak)
	c.audioTref = cloneRef(d.audioTref)
	c.audioTrefRaw = slices.Clone(d.audioTrefRaw)
	c.chapTrak = cloneRef(d.chapTrak)
	c.mvhd = cloneRef(d.mvhd)
	c.chapters = core.CloneChapters(d.chapters)
	c.udtaRaw = slices.Clone(d.udtaRaw)
	c.items = make([]item, len(d.items))
	for i, it := range d.items {
		it.payload = slices.Clone(it.payload)
		c.items[i] = it
	}
	c.offTables = make([]offsetTable, len(d.offTables))
	for i, t := range d.offTables {
		t.entries = slices.Clone(t.entries)
		c.offTables[i] = t
	}
	c.auxTables = make([]offsetTable, len(d.auxTables))
	for i, t := range d.auxTables {
		t.entries = slices.Clone(t.entries)
		c.auxTables[i] = t
	}
	c.mdats = slices.Clone(d.mdats)
	return &c
}

func cloneRef(r *atomRef) *atomRef {
	if r == nil {
		return nil
	}
	c := *r
	return &c
}

// PaddingBytes reports the payload of the free/skip atom adjacent to ilst - the only
// padding this codec reuses in place, and the number a plan reports as PaddingAfter.
// A stray free atom elsewhere in the file is not counted: the writer cannot grow into it,
// so counting it would promise slack that does not exist.
//
// The subtraction is the 8-byte header the writer will emit, not the source atom's own
// headerLen. A rewrite replaces the region with a freshly rendered free atom, which always
// uses the 32-bit header; a source atom written in the 64-bit largesize form therefore
// yields 8 more usable bytes than its payload, and reporting its payload would make the
// figure jump across an otherwise in-place save-back.
func (d *doc) PaddingBytes() int64 {
	if d.free == nil {
		return 0
	}
	return max(0, d.free.size-freeAtomHeaderLen)
}

// Describe summarizes the native atom structure for the dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	out := make([]core.NativeEntry, 0, len(d.topLevel)+len(d.items)+2)
	for _, a := range d.topLevel {
		note := "preserved"
		switch a.id() {
		case "moov":
			note = "movie box"
		case "mdat":
			note = d.track.Codec + " media data"
		case "moof":
			// Fragmented files are dumpable, so name the box rather than let it fall
			// through to "preserved", which would imply inert padding.
			note = "movie fragment"
		case "ftyp":
			note = "file type"
			if d.majorBrand != "" {
				note = "file type (" + d.majorBrand + ")"
			}
		default:
			// Single-source the padding set via reusablePadding (free/skip) so the dump label
			// and the in-place reuse logic cannot drift on which atoms count as padding.
			if reusablePadding(a.id()) {
				note = "padding"
			}
		}
		out = append(out, core.NativeEntry{Kind: a.id(), Size: int(a.size), Note: note})
	}
	if d.ilst != nil {
		out = append(out, core.NativeEntry{
			Kind: "moov.udta.meta.ilst", Size: int(d.ilst.size),
			Note: fmt.Sprintf("%d items", len(d.items)),
		})
		for _, it := range d.items {
			note := ""
			if !owned(it) {
				note = "preserved"
			}
			out = append(out, core.NativeEntry{Kind: "  " + itemLabel(it.name), Size: len(it.payload) + 8, Note: note})
		}
	}
	if d.chpl != nil {
		note := fmt.Sprintf("%d chapters (Nero, v%d)", d.chplCount, d.chplVersion)
		if d.chplCount == 0 {
			note = "preserved (unparsed)"
		}
		out = append(out, core.NativeEntry{Kind: "moov.udta.chpl", Size: int(d.chpl.size), Note: note})
	}
	if d.hasQTChapters {
		out = append(out, core.NativeEntry{
			Kind: "moov chapter track", Note: "QuickTime chapter text track",
		})
	}
	return out
}

// itemLabel renders an ilst item name for display, showing the 0xA9 prefix atoms
// as "(c)nam" rather than an unprintable byte.
func itemLabel(name [4]byte) string {
	if name[0] == 0xA9 {
		return "(c)" + string(name[1:])
	}
	return string(name[:])
}
