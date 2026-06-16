package matroska

import (
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
)

// simpleTag is one parsed Matroska SimpleTag: its name, its string value (or the
// length of its binary value when it is a TagBinary), the language, and any
// nested sub-tags. The full tree — including names that do not project to a
// canonical key — is preserved here so a tagger can inspect everything the file
// carries, matching the plan's "preserve the full scoped tree in Native".
type simpleTag struct {
	name   string
	value  string
	lang   string
	binary int // >0 when the value was a TagBinary of this many bytes (not decoded)
	sub    []simpleTag
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
type tagGroup struct {
	scope           core.Scope
	targetTypeValue uint64
	targetType      string
	trackUID        bool
	editionUID      bool
	chapterUID      bool
	tags            []simpleTag
}

// attachment summarizes one AttachedFile (cover art and any other embedded file).
type attachment struct {
	name        string
	mime        string
	description string
	size        int
	image       bool
}

// doc is the Matroska native document: the parsed tag groups (the scoped tree),
// the segment title, the attachment summaries, and the audio track properties.
// Matroska is read-only in v1, so this is an inspection base rather than a
// rewrite base — there is no byte-level layout to preserve for editing.
type doc struct {
	docType     string // "matroska" or "webm", from the EBML DocType header
	segTitle    string
	groups      []tagGroup
	attachments []attachment
	tracks      []core.AudioTrack

	// essence-digest config, captured from the first audio track.
	codecID    string
	sampleRate int
	channels   int
	bitDepth   int
}

func (d *doc) Format() core.Format { return core.FormatMatroska }

// Clone deep-copies the document so Document accessors stay detached.
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
	if d.segTitle != "" {
		out = append(out, core.NativeEntry{Kind: "Info.Title", Size: len(d.segTitle), Note: d.segTitle})
	}
	for _, g := range d.groups {
		tt := g.targetType
		if tt == "" {
			tt = fmt.Sprintf("%d", g.level())
		}
		out = append(out, core.NativeEntry{
			Kind: "Tag",
			Size: len(g.tags),
			Note: fmt.Sprintf("scope=%s target=%s (%d simple tags)", g.scope, tt, len(g.tags)),
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
