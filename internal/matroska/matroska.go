// Package matroska implements reading and tag-writing Matroska / WebM
// (.mka / .webm / .mkv) metadata. Tags (scoped SimpleTags), the segment title,
// cover-art attachments, and chapters (the default EditionEntry) are writable;
// cluster/essence rewriting is out of scope (it touches encoded audio). The
// package is internal through v0.x.
//
// A Matroska file is an EBML document: a tree of length-prefixed elements. Tags
// live in Segment.Tags as Tag elements, each scoping a set of SimpleTag
// name/value pairs to the whole segment, a track, an edition, or a chapter via a
// Targets element. The segment title lives in Segment.Info.Title (where ffmpeg
// puts the file's "title"), and cover art lives in Segment.Attachments as an
// image AttachedFile. The audio geometry comes from Segment.Tracks; the cluster
// media payloads are never read - only their byte range is recorded.
//
// The codec is preservation-aware: the full scoped tag tree (including names
// that do not project to a canonical key, and nested sub-tags) is kept in the
// native document for inspection. It is reimplemented from the EBML/Matroska
// specifications (RFC 8794 / RFC 9559); reference implementations informed design
// only.
package matroska

import (
	"context"
	"encoding/binary"

	"github.com/colespringer/waxlabel/internal/core"
)

// Codec implements core.Codec for Matroska: read, plus tag/title/attachment write.
type Codec struct{}

// New returns a Matroska codec.
func New() Codec { return Codec{} }

func init() { core.Register(New()) }

func (Codec) Format() core.Format  { return core.FormatMatroska }
func (Codec) Extensions() []string { return []string{".mka", ".webm", ".mkv", ".mk3d", ".mks"} }

// Sniff matches the EBML magic that opens every Matroska/WebM file, using the
// same idEBML constant the parser matches against so the two cannot drift.
func (Codec) Sniff(header []byte) bool {
	return len(header) >= 4 && binary.BigEndian.Uint32(header[:4]) == idEBML
}

// Parse reads metadata from src into a Media.
func (c Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	return parse(ctx, src, opts)
}

// Capabilities reports Matroska as tag-writable: tags (scoped SimpleTags) and the
// segment title round-trip fully, cover art writes as an image AttachedFile -
// except into a WebM file, whose subset excludes Attachments - and chapters
// (Chapters > EditionEntry > ChapterAtom) round-trip through the default edition.
//
// Cover-write support is file-aware: when m is the parsed file and it is WebM,
// picture write is reported AccessNone (Attachments is outside the WebM subset),
// so a transfer drops the cover up front instead of advertising it carried and
// then failing at Plan time - the report==result transfer invariant. A nil m is a
// format-level query (PlanTransfer, which has no destination file) and keeps the
// optimistic Matroska answer; the Plan-level WebM refusal remains the backstop for
// a direct cover add.
func (Codec) Capabilities(m *core.Media, opts core.WriteOptions) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "Matroska SimpleTag + Info.Title",
		Fidelity:       "lossless",
		Constraints:    []string{"canonical edits written at album scope and removed from any track/edition/chapter scope that also held the key; unedited scoped tags preserved verbatim"},
	}
	pictures := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "AttachedFile (image attachment)", Fidelity: "lossless",
		Constraints: []string{"not writable to WebM (Attachments is outside the WebM subset)"},
	}
	if m != nil {
		if d, ok := m.Native.(*doc); ok && isWebM(d.docType) {
			pictures.Write = core.AccessNone
			pictures.Representation = "Attachments outside the WebM subset"
			// The generic "not writable to WebM" note is now redundant: this is the
			// WebM file's own capability and it already reports AccessNone with the
			// reason in Representation (which is what dispose surfaces).
			pictures.Constraints = nil
		}
	}
	chapters := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "Chapters > EditionEntry > ChapterAtom (default edition)",
		Fidelity:       "lossless", // chapter times round-trip exactly (absolute nanoseconds)
		Constraints: []string{
			"edits apply to the default edition; other editions and chapter UIDs preserved",
			"a chapter edit flattens the default edition to a title/start/end model - nested sub-chapters and secondary-language displays in it are not preserved (untouched chapters are kept verbatim)",
		},
		// No MaxItems: Matroska has no chapter-count cap (unlike MP4's 255-entry chpl).
	}
	return core.NewCapabilities(core.FormatMatroska, false, fields, pictures, chapters, nil)
}

// EssenceExtent returns the Matroska essence-digest inputs: a versioned extent
// name and the decoder-critical config of the first audio track (CodecID plus
// sample rate, channels, and bit depth) mixed in ahead of the hashed cluster
// region, so identical cluster bytes under a different codec or geometry hash
// differently. The hashed extent is the contiguous cluster span recorded at
// parse (m.AudioStart..m.AudioEnd).
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
	return "matroska-clusters-v1", cfg
}
