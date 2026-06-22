package matroska

import (
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
)

// simpleTag is one parsed Matroska SimpleTag: its name, its string value (or the
// length of its binary value when it is a TagBinary), the language, and any
// nested sub-tags. The full tree - including names that do not project to a
// canonical key - is preserved here so a tagger can inspect everything the file
// carries, matching the plan's "preserve the full scoped tree in Native".
//
// raw holds the SimpleTag element's original bytes (header + payload + any nested
// sub-tags), captured at parse so the write path can preserve a tag it does not
// manage - a binary value, a nested tree, or a custom name - byte-for-byte
// without re-encoding from the lossy decoded view.
type simpleTag struct {
	name     string
	value    string
	hasValue bool // a TagString child was present (an empty TagString is distinct from none)
	lang     string
	binary   int // >0 when the value was a TagBinary of this many bytes (not decoded)
	sub      []simpleTag
	raw      []byte
}

func cloneSimpleTags(in []simpleTag) []simpleTag {
	if in == nil {
		return nil
	}
	out := make([]simpleTag, len(in))
	for i, s := range in {
		s.sub = cloneSimpleTags(s.sub)
		out[i] = s
	}
	return out
}

// tagGroup is one Matroska Tag element: a Targets scope plus the SimpleTags it
// applies to. The scope is resolved from the target's TargetTypeValue and its
// optional track/edition/chapter UID references.
//
// targetsRaw is the group's Targets element bytes (nil when absent), preserved so
// a re-rendered group keeps its track/edition/chapter UID values - which the
// decoded view records only as presence bools. hasCRC notes the group carried a
// leading CRC-32 so a re-render recomputes one.
type tagGroup struct {
	scope           core.Scope
	targetTypeValue uint64
	targetType      string
	trackUID        bool
	editionUID      bool
	chapterUID      bool
	tags            []simpleTag
	targetsRaw      []byte
	raw             []byte // whole Tag element bytes, for verbatim preservation
	hasCRC          bool
}

// attachment summarizes one AttachedFile (cover art and any other embedded file).
// raw holds the AttachedFile element's original bytes so a non-image attachment
// is preserved verbatim when the cover set is rewritten.
type attachment struct {
	name        string
	mime        string
	description string
	size        int
	image       bool
	raw         []byte
}

// doc is the Matroska native document: the parsed tag groups (the scoped tree),
// the segment title, the attachment summaries, and the audio track properties.
// It also carries a writeBase - the byte-level layout the write path preserves
// (Segment header, the ordered top-level children, and the SeekHead/Cues/Info/
// Attachments raw bytes) - captured at parse so Plan can rewrite without the
// source.
type doc struct {
	docType     string // "matroska" or "webm", from the EBML DocType header
	segTitle    string
	hasSegTitle bool // an Info.Title element was present (distinguishes a "" title from none)
	groups      []tagGroup
	attachments []attachment
	chapters    *chapterDoc // parsed Chapters tree, nil when the file has none
	tracks      []core.AudioTrack
	sawNonAudio bool // a video/subtitle/button track was present; gates the audio-only bitrate

	// essence-digest config, captured from the first audio track.
	codecID    string
	sampleRate int
	channels   int
	bitDepth   int

	wb *writeBase // byte-level rewrite base; nil if the layout was unparseable
}

// writeBase is the structural snapshot the writer needs: the Segment header and
// every preserved top-level child, plus the parsed-with-raw-bytes SeekHead, Cues,
// Info, and Attachments. It is immutable after parse, so Clone shares it.
type writeBase struct {
	size int64 // whole-file byte length

	segStart     int64 // the Segment element's first byte (its ID)
	segSizeOff   int64 // offset of the Segment data-size VINT
	segSizeLen   int64 // that VINT's byte width
	segUnknown   bool  // the Segment uses the unknown-size form
	segDataStart int64
	segDataEnd   int64

	children     []l1elem // top-level Segment children, in file order
	clusterStart int64    // first Cluster start (header/tail boundary), or segDataEnd

	seek    *seekHead
	cues    *cuesIndex
	info    *infoBlock
	attach  *attachBlock
	tagsCRC bool // the Tags master element led with a CRC-32
}

// l1elem is one top-level Segment child captured for rewriting: its ID and full
// byte range. The id distinguishes which children are re-rendered (Tags/Info/
// Attachments/SeekHead/Cues) from those copied verbatim (Tracks, Cluster, Void...).
type l1elem struct {
	id        uint64
	start     int64
	dataStart int64
	dataEnd   int64
}

func (e l1elem) total() int64 { return e.dataEnd - e.start }

// crcSpot locates a CRC-32 within a captured raw buffer: the 4-byte value's
// offset and the start of the content it covers (everything after itself).
type crcSpot struct {
	valOff       int
	contentStart int
}

// seekHead is a captured SeekHead: its file range, raw bytes, optional CRC, and
// the SeekPosition values (each a segment-relative offset) with their in-raw
// location and width, so the writer can patch a moved target in place.
type seekHead struct {
	start, end int64
	raw        []byte
	crc        *crcSpot
	entries    []seekEntry
}

type seekEntry struct {
	idRaw  []byte // the SeekID value bytes (the referenced element's ID), for a full re-encode
	valOff int    // SeekPosition data offset within raw
	valLen int    // its byte width
	target uint64 // the stored segment-relative position
}

// cuesIndex is a captured Cues element with its CueClusterPosition values (each a
// segment-relative cluster offset) for the same in-place patching.
type cuesIndex struct {
	start, end int64
	raw        []byte
	crc        *crcSpot
	clusters   []seekEntry
}

// infoBlock is a captured Info element: raw bytes, optional CRC, and the Title
// child's location within raw (titleOff<0 when absent), plus the offset at which
// a new Title is spliced (after the CRC, before the other children).
type infoBlock struct {
	start, end int64
	raw        []byte
	crc        *crcSpot
	titleOff   int
	titleEnd   int
	insertOff  int
}

// attachBlock is the captured Attachments element framing (file range and whether
// it carried a CRC); the AttachedFile bodies live on doc.attachments.
type attachBlock struct {
	start, end int64
	hasCRC     bool
}

func (d *doc) Format() core.Format { return core.FormatMatroska }

// Clone deep-copies the document so Document accessors stay detached. The
// writeBase is immutable after parse, so it is shared by pointer rather than
// copied.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	c.groups = make([]tagGroup, len(d.groups))
	for i, g := range d.groups {
		g.tags = cloneSimpleTags(g.tags)
		c.groups[i] = g
	}
	c.attachments = slices.Clone(d.attachments)
	c.tracks = slices.Clone(d.tracks)
	return &c
}

// Describe summarizes the scoped tag tree, attachments, and tracks for the
// dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	out := make([]core.NativeEntry, 0, len(d.groups)+len(d.attachments)+2)
	dt := d.docType
	if dt == "" {
		dt = "matroska"
	}
	out = append(out, core.NativeEntry{Kind: "EBML", Note: "DocType " + dt})
	if d.hasSegTitle {
		// No Size: the Note already shows the title verbatim, so its char-length would
		// be a redundant (and bytes-mislabeled) column. A present-but-empty title shows
		// an empty Note, distinct from a file with no Info.Title at all.
		out = append(out, core.NativeEntry{Kind: "Info.Title", Note: d.segTitle})
	}
	for _, g := range d.groups {
		tt := g.targetType
		if tt == "" {
			tt = fmt.Sprintf("%d", g.level())
		}
		out = append(out, core.NativeEntry{
			Kind: "Tag",
			Size: len(g.tags),
			Unit: "tags",
			Note: fmt.Sprintf("scope=%s target=%s", g.scope, tt),
		})
		for _, st := range g.tags {
			out = append(out, describeSimpleTag(st, "  ")...)
		}
	}
	for _, a := range d.attachments {
		note := a.mime
		if a.image {
			note += " (cover art)"
		}
		out = append(out, core.NativeEntry{Kind: "Attachment " + a.name, Size: a.size, Note: note})
	}
	if cd := d.chapters; cd != nil && cd.defIdx >= 0 {
		ed := cd.editions[cd.defIdx]
		note := "default edition"
		if len(cd.editions) > 1 {
			note += fmt.Sprintf(" (%d editions total)", len(cd.editions))
		}
		out = append(out, core.NativeEntry{Kind: "Chapters", Size: len(ed.uids), Unit: "chapters", Note: note})
	}
	return out
}

func describeSimpleTag(st simpleTag, indent string) []core.NativeEntry {
	val := st.value
	if st.binary > 0 {
		val = fmt.Sprintf("<%d binary bytes>", st.binary)
	}
	out := []core.NativeEntry{{Kind: indent + st.name, Size: len(st.value), Note: val}}
	for _, s := range st.sub {
		out = append(out, describeSimpleTag(s, indent+"  ")...)
	}
	return out
}
