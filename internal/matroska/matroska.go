// Package matroska implements reading Matroska / WebM (.mka / .webm / .mkv)
// metadata. It is read-only in v1 (write and full target/scope modeling are
// deferred to v2) and internal through v0.x.
//
// A Matroska file is an EBML document: a tree of length-prefixed elements. Tags
// live in Segment.Tags as Tag elements, each scoping a set of SimpleTag
// name/value pairs to the whole segment, a track, an edition, or a chapter via a
// Targets element. The segment title lives in Segment.Info.Title (where ffmpeg
// puts the file's "title"), and cover art lives in Segment.Attachments as an
// image AttachedFile. The audio geometry comes from Segment.Tracks; the cluster
// media payloads are never read — only their byte range is recorded.
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
	"fmt"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// Codec implements core.Codec for Matroska (read-only).
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

// Plan refuses to write: Matroska is read-only in this version. Returning the
// error here (rather than relying on the engine) keeps the reported capabilities
// and the actual write behavior in lockstep, per the Codec contract.
func (Codec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	return nil, fmt.Errorf("%w: Matroska write is deferred to v2 (read-only)", waxerr.ErrUnsupportedFormat)
}

// Capabilities reports Matroska as read-only: tags and cover art are fully
// readable but not writable, and chapters are not modeled in v1.
func (Codec) Capabilities(opts core.WriteOptions) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessNone,
		Representation: "Matroska SimpleTag (target-scoped)", Fidelity: "read-only",
		Constraints: []string{"write and full target/scope modeling deferred to v2"},
	}
	pictures := core.Capability{
		Read: core.AccessFull, Write: core.AccessNone,
		Representation: "AttachedFile (image attachment)", Fidelity: "read-only",
	}
	chapters := core.Capability{
		Read: core.AccessNone, Write: core.AccessNone,
		Representation: "not modeled in v1",
	}
	return core.NewCapabilities(core.FormatMatroska, true, fields, pictures, chapters, nil)
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
