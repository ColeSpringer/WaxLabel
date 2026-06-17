package mp4

import (
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
)

// item is one decoded ilst child: the four-character atom name and its raw
// payload (everything after the 8-byte atom header). Whether the canonical
// rebuild owns an item — versus preserving it verbatim, the MP4 analogue of ID3's
// opaque frames — is recomputed on demand by owned(), not cached here, so the
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
	free *atomRef // a free atom adjacent to ilst inside meta, if present
	chpl *atomRef // moov.udta.chpl Nero chapter list, if present

	items     []item        // decoded ilst children (nil when no ilst)
	offTables []offsetTable // every stco/co64 in moov, in document order
	mdats     [][2]int64    // mdat payload ranges (offset, length), in document order

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
	// hasQTChapters records that a QuickTime chapter text track is present: it is
	// preserved verbatim on a chapter edit (only chpl is written in this version),
	// which leaves it stale (see WarnChaptersStale).
	chapters      []core.Chapter
	chplVersion   uint8
	chplCount     int // chapters in the chpl atom specifically (for the native view)
	hasQTChapters bool
	// chapterConflict records that the chpl and QuickTime track disagreed at parse;
	// it is carried into a post-write document so its warnings match a fresh parse.
	chapterConflict bool
}

func (d *doc) Format() core.Format { return core.FormatMP4 }

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
		case "ftyp":
			note = "file type"
			if d.majorBrand != "" {
				note = "file type (" + d.majorBrand + ")"
			}
		case "free", "skip":
			note = "padding"
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
			Kind: "moov chapter track", Note: "QuickTime chapter text track (preserved)",
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
