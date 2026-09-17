package asf

import (
	"fmt"
	"slices"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// objectSummary is one header child for the native view.
type objectSummary struct {
	id   guid
	size int
}

func cloneSummaries(s []objectSummary) []objectSummary { return slices.Clone(s) }

// doc is the ASF native document: audio description and header object list.
// No preservation state (ASF is never rewritten); see refuseWrite.
type doc struct {
	objects   []objectSummary
	pictures  []core.Picture
	headerEnd int64
	// dataStart/dataEnd: Data Object media packets. Zero when not located
	// (unknown extent; do not guess rest-of-file).
	dataStart int64
	dataEnd   int64
	size      int64

	fileSize   uint64
	duration   time.Duration
	maxBitrate uint32

	// preroll from File Properties; subtracted when projecting markers.
	preroll time.Duration
	// markers as stored (preroll still in presentation times). Chapters projects them.
	markers []marker

	haveAudio     bool
	formatTag     uint16
	channels      int
	sampleRate    int
	byteRate      int
	bitsPerSample int
	// losslessDepth from WMA Lossless codec extra bytes (0 if N/A). Shadows
	// bitsPerSample on the track only; digest salt stays the stored structure.
	losslessDepth int
	// waveFormat: first 16 WAVEFORMATEX bytes as stored (essence-digest salt).
	waveFormat [16]byte
	// invalidKeys: descriptors the canonical vocabulary cannot represent.
	invalidKeys []string
}

// marker is one Marker Object entry.
type marker struct {
	at   time.Duration
	desc string
}

func (d *doc) Format() core.Format { return core.FormatWMA }

// chapters projects markers onto the playback timeline (subtract preroll; clamp
// at 0). Sorted by start, stably, so equal-time markers keep file order.
func (d *doc) chapters() []core.Chapter {
	if len(d.markers) == 0 {
		return nil
	}
	chs := make([]core.Chapter, 0, len(d.markers))
	for _, m := range d.markers {
		chs = append(chs, core.Chapter{Start: max(0, m.at-d.preroll), Title: m.desc})
	}
	core.SortChaptersByStart(chs)
	return chs
}

// refuseWrite explains why ASF cannot be rewritten. Shared by Plan and
// Capabilities so advertised capability and write outcome cannot diverge.
// Uses ErrUnsupportedFormat (not ErrUnsupportedTag).
func refuseWrite() error {
	return fmt.Errorf("%w: WaxLabel reads WMA/ASF but does not write it; save to another format instead",
		waxerr.ErrUnsupportedFormat)
}

// Clone deep-copies so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	c.objects = cloneSummaries(d.objects)
	c.pictures = core.ClonePictures(d.pictures)
	c.markers = slices.Clone(d.markers)
	return &c
}

// Describe summarizes header objects for dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	out := make([]core.NativeEntry, 0, len(d.objects)+2)
	for _, o := range d.objects {
		out = append(out, core.NativeEntry{Kind: objectName(o.id), Size: o.size})
	}
	for range d.pictures {
		out = append(out, core.NativeEntry{Kind: "WM/Picture", Note: "embedded picture"})
	}
	out = append(out, core.NativeEntry{
		Kind: "data", Size: int(d.dataEnd - d.dataStart), Note: codecName(d.formatTag),
	})
	return out
}

// objectName is the spec name for a header object (opaque if unknown).
func objectName(id guid) string {
	switch id {
	case guidFileProps:
		return "File Properties"
	case guidStreamProps:
		return "Stream Properties"
	case guidHeaderExt:
		return "Header Extension"
	case guidContentDesc:
		return "Content Description"
	case guidExtContentDesc:
		return "Extended Content Description"
	case guidMetadata:
		return "Metadata"
	case guidMetadataLibrary:
		return "Metadata Library"
	case guidMarker:
		return "Marker"
	case guidCodecList:
		return "Codec List"
	case guidStreamBitrate:
		return "Stream Bitrate Properties"
	case guidContentEncryption:
		return "Content Encryption"
	case guidExtContentEncryption:
		return "Extended Content Encryption"
	}
	return "header object"
}
