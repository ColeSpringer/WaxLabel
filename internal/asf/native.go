package asf

import (
	"fmt"
	"slices"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// objectSummary is one header child object, for the native view.
type objectSummary struct {
	id   guid
	size int
}

// clone copies the summaries so a Document accessor stays detached.
func cloneSummaries(s []objectSummary) []objectSummary { return slices.Clone(s) }

// doc is the ASF native document: the decoded audio description and the header's
// object list. Unlike the writable codecs it holds no preservation state, because
// WaxLabel never rewrites an ASF file - see refuseWrite.
type doc struct {
	objects   []objectSummary
	pictures  []core.Picture
	headerEnd int64
	// dataStart/dataEnd bound the Data Object's media packets: the audio essence. They
	// are zero when the object could not be located, which reports the extent as unknown
	// rather than guessing at the rest of the file.
	dataStart int64
	dataEnd   int64
	size      int64

	fileSize   uint64
	duration   time.Duration
	maxBitrate uint32

	haveAudio     bool
	formatTag     uint16
	channels      int
	sampleRate    int
	byteRate      int
	bitsPerSample int
}

func (d *doc) Format() core.Format { return core.FormatWMA }

// refuseWrite reports why this document cannot be rewritten. For ASF the answer is
// unconditional: WaxLabel reads WMA but never writes it.
//
// Plan returns this error and Capabilities derives its ReadOnly flag from the same
// call, so the capability a caller is shown and the outcome of an actual write cannot
// diverge - which is the reason the refusal lives here rather than in
// [core.Format.Writable], a public-API bookkeeping method no write path consults.
//
// The sentinel is ErrUnsupportedFormat, not ErrUnsupportedTag: the latter is
// documented as "a tag exists that this version cannot model", which misdescribes a
// format that is simply not written.
func (d *doc) refuseWrite() error {
	return fmt.Errorf("%w: WaxLabel reads WMA/ASF but does not write it; save to another format instead",
		waxerr.ErrUnsupportedFormat)
}

// Clone deep-copies the document so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	c.objects = cloneSummaries(d.objects)
	c.pictures = core.ClonePictures(d.pictures)
	return &c
}

// Describe summarizes the header's objects for the dump/native views.
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

// objectName is the spec's name for a header object, for the native view. An object
// this codec does not read is still listed - by name where it is one of the known
// ones, else as an opaque entry - so the view describes the whole header.
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
