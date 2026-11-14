// Package matroska implements Matroska/WebM (.mka/.webm/.mkv) metadata.
// Writable: scoped SimpleTags, Info.Title, cover attachments, default-edition
// chapters. Cluster/essence rewrite is out of scope. Codec is internal.
//
// EBML tree: Tags as Tag+Targets+SimpleTag; title in Info.Title; cover in
// Attachments; geometry from Tracks. Cluster payloads are ranged, not read.
// Native doc keeps the full scoped tag tree. Reimplemented from RFC 8794/9559.
//
// The canonical tags are the file's own: a Tag naming an attachment, a chapter, an
// edition or a track without audio is listed in the family view and never edited. In a
// file tagged by level the album-level TITLE and ARTIST are the album's ([titleConvention]).
package matroska

import (
	"context"
	"encoding/binary"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
)

// Codec implements core.Codec for Matroska (read + tag/title/attachment/chapter write).
type Codec struct{}

// New returns a Matroska codec.
func New() Codec { return Codec{} }

func init() { core.Register(New()) }

func (Codec) Format() core.Format  { return core.FormatMatroska }
func (Codec) Extensions() []string { return []string{".mka", ".webm", ".mkv", ".mk3d", ".mks"} }

// SkipsLeadingID3 is false: files begin with an EBML header.
func (Codec) SkipsLeadingID3() bool { return false }

// Sniff matches EBML magic via idEBML (same as the parser).
func (Codec) Sniff(header []byte) bool {
	return len(header) >= 4 && binary.BigEndian.Uint32(header[:4]) == idEBML
}

// Parse reads metadata from src into a Media.
func (c Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities: tags + title + default-edition chapters; cover as AttachedFile
// except WebM (no Attachments). WebM picture Write=AccessNone when m is WebM
// (report==result); nil m stays optimistic Matroska (Plan refuses WebM cover). WebM
// chapters lose the hidden and disabled flags its subset lacks.
func (Codec) Capabilities(m *core.Media, opts core.WriteOptions) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "Matroska SimpleTag + Info.Title",
		Fidelity:       "lossless",
		Constraints: []string{
			"a canonical edit keeps a still-wanted value at the target scope that holds it; " +
				"values the edit removes are dropped from every scope; new values are written at album scope; " +
				"unedited scoped tags are preserved verbatim",
			"a key split across scopes reads back in scope order (album first), so a pure cross-scope reorder is a no-op; " +
				"a key spread over several album-scope Tag blocks is album-owned in all of them and re-emits in edited order on any change",
			"tags targeted at an attachment, a chapter, an edition, or a track that holds no audio are not the file's tags: " +
				"they are listed in the family view and no edit touches them",
			"a title edit writes Info.Title and removes the TITLE SimpleTags; an edit of another key leaves both as they are; only the first TITLE value is stored",
			"in a file tagged by level (a TITLE at album level beside a TITLE at level 40 or under) " +
				"the album-level TITLE and ARTIST are ALBUM and ALBUMARTIST, and the title and artist are written to the Tag that holds the lower TITLE; " +
				"in any other file, one with an Info.Title included, they are the file's TITLE and ARTIST",
		},
	}
	pictures := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "AttachedFile (image attachment)",
		// Role-only loss: cover.<ext>/small_cover.<ext>; description in FileDescription.
		Fidelity: "image bytes lossless; only the front-cover role is preserved (other roles read back as Other)",
		// image/* or octet-stream under cover name (mirrors isCoverAttachment); else Dropped.
		PictureMIMEs: []string{"image/*", core.UnrecognizedMIME},
		PictureLoss:  core.PictureLossRoleOnly,
		Constraints: []string{
			"not writable to WebM (Attachments is outside the WebM subset)",
			"only the front cover preserves its role; other picture roles read back as Other (descriptions are preserved)",
			"an attachment that is no picture, and one whose picture the edit keeps, is copied verbatim (name, type, description, UID); " +
				"a new or changed picture is named cover.<ext> or small_cover.<ext>",
		},
	}
	// WebM (parsed docType or WithWebMSubset): refuse cover write; one gate.
	webm := opts.WebMSubset
	if m != nil {
		if d, ok := m.Native.(*doc); ok && isWebM(d.docType) {
			webm = true
		}
	}
	if webm {
		pictures.Write = core.AccessNone
		pictures.Representation = "Attachments outside the WebM subset"
		pictures.Constraints = nil // reason is in Representation
	}
	// A re-render carries the hidden/disabled flags only where the file can hold them.
	rendered := "title, start/end, the primary display's language, and the hidden/disabled flags"
	if webm {
		rendered = "title, start/end, the primary display's language"
	}
	chapters := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation:  "Chapters > EditionEntry > ChapterAtom (default edition)",
		Fidelity:        "lossless", // chapter times round-trip exactly (absolute nanoseconds)
		ChapterTimeUnit: time.Nanosecond,
		Constraints: []string{
			"edits apply to the default edition; other editions and chapter UIDs are preserved, but clearing the chapters removes every edition",
			"a chapter edit re-renders the default edition to a flat model (" + rendered + ") - nested sub-chapters, additional ChapterDisplays (other-language titles), and other unmodeled atom fields are not preserved (untouched chapters are kept verbatim)",
			"a chapter's end time is read only from an explicit ChapterTimeEnd; an absent end is left open-ended (zero), not inferred from the next chapter's start the way MP4 infers it",
			"the CLI has no end-time syntax, so a --clear-chapters + --add-chapter rewrite drops explicit end times; library callers can set Chapter.End to keep them",
		},
	}
	if webm {
		// The WebM subset has no EditionFlagDefault, ChapterFlagHidden or ChapterFlagEnabled.
		chapters.Fidelity = "start, end, title, and language are stored; hidden and disabled flags are not (outside the WebM subset)"
		chapters.ChapterLoss = core.ChapterLossFlagsOnly
		chapters.Constraints = append(chapters.Constraints,
			"a hidden or disabled chapter is written visible and enabled, and a new edition carries no default flag: the WebM subset has none of the three")
	}
	// ReadOnly from the same refuseWrite Plan uses.
	var refusal error
	if m != nil {
		if d, ok := m.Native.(*doc); ok && d != nil && d.wb != nil {
			refusal = d.refuseWrite()
		}
	}
	return core.NewCapabilities(core.FormatMatroska, refusal != nil, fields, pictures, chapters, core.AccessNone, nil).
		WithTextQualifiers(core.TextLossDescription, "").
		WithFieldClassifier(TransferClassifier).
		WithReadOnlyReason(refusal)
}

// EssenceExtent: matroska-clusters-v2 + first-track CodecID/geometry, then
// per-cluster runs (m.AudioRanges), excluding inter-cluster non-cluster elements.
func (Codec) EssenceExtent(m *core.Media) (string, []byte) {
	var cfg []byte
	if d, ok := m.Native.(*doc); ok {
		cfg = append(cfg, d.codecID...)
		cfg = append(cfg, 0)
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(d.sampleRate))
		cfg = append(cfg, n[:]...)
		binary.BigEndian.PutUint16(n[:2], uint16(d.channels))
		cfg = append(cfg, n[:2]...)
		cfg = append(cfg, byte(d.bitDepth))
	}
	return "matroska-clusters-v2", cfg // v2: per-cluster runs
}
