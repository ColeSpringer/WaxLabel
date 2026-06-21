package core

import (
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
)

// PictureType enumerates the cover-art roles, matching the ID3 APIC / FLAC
// PICTURE type IDs so values round-trip across formats unchanged.
type PictureType uint8

const (
	PicOther PictureType = iota
	PicFileIcon
	PicOtherFileIcon
	PicFrontCover
	PicBackCover
	PicLeaflet
	PicMedia
	PicLeadArtist
	PicArtist
	PicConductor
	PicBand
	PicComposer
	PicLyricist
	PicRecordingLocation
	PicDuringRecording
	PicDuringPerformance
	PicVideoScreenCapture
	PicBrightFish
	PicIllustration
	PicBandLogo
	PicPublisherLogo
)

func (p PictureType) String() string {
	names := [...]string{
		"Other", "File icon", "Other file icon", "Front cover", "Back cover",
		"Leaflet", "Media", "Lead artist", "Artist", "Conductor", "Band",
		"Composer", "Lyricist", "Recording location", "During recording",
		"During performance", "Video screen capture", "Bright fish",
		"Illustration", "Band logo", "Publisher logo",
	}
	if int(p) < len(names) {
		return names[p]
	}
	return "reserved"
}

// SingleIcon reports whether p is one of the icon types (1 and 2) that must be
// unique within a file; the writer validates this.
func (p PictureType) SingleIcon() bool {
	return p == PicFileIcon || p == PicOtherFileIcon
}

// UnrecognizedMIME is the MIME a picture is stored under when [Picture.SniffInto]
// cannot identify its bytes as an image (an exotic/unsniffable cover, junk, or an
// empty payload all degrade to this). It is the single string the linter's
// invalid-picture rule and the editor's plan-time picture warning both key on -
// rather than re-sniffing - so a cover a codec already recognized is never
// false-flagged and the two checks cannot drift.
const UnrecognizedMIME = "application/octet-stream"

// CountIcons returns how many type-1 (file icon) and type-2 (other file icon)
// pictures are present. Both must be at most one. The writer's validation and
// the linter share this so their notion of icon validity cannot drift.
func CountIcons(pics []Picture) (icon, otherIcon int) {
	for _, p := range pics {
		if !p.Type.SingleIcon() {
			continue
		}
		if p.Type == PicFileIcon {
			icon++
		} else {
			otherIcon++
		}
	}
	return icon, otherIcon
}

// Picture is an embedded image. Data bytes are reference-shared read-only:
// deep-copying multi-megabyte payloads on every accessor is wasteful, so
// callers must not mutate Data. The structural fields are copied freely.
type Picture struct {
	Type        PictureType
	MIME        string
	Description string
	Width       int
	Height      int
	Depth       int // color depth in bits per pixel
	Colors      int // palette size for indexed images, else 0
	Data        []byte
}

// Hash returns a content hash of the image bytes, for cross-track cover
// deduplication. It is the identity of the *image*, so the same artwork used as
// both a front and back cover (or with different descriptions) hashes equal -
// type and description are usage metadata, not the picture.
func (p Picture) Hash() [32]byte {
	return bits.SHA256(p.Data)
}

// Unrecognized reports whether the picture is stored under [UnrecognizedMIME] -
// i.e. its bytes were not a recognized image header. The shared predicate behind
// the linter's invalid-picture finding and the editor's plan-time picture warning.
func (p Picture) Unrecognized() bool { return p.MIME == UnrecognizedMIME }

// CloneMeta returns a copy whose structural fields are independent but whose
// Data slice is shared (read-only). This is what accessors hand out.
func (p Picture) CloneMeta() Picture {
	c := p // Data shared by design
	return c
}

// SniffInto fills MIME, Width, Height, and Depth from the picture's own bytes
// when they are not already set, using a header-only sniff (no decode). It
// returns whether the format was recognized.
func (p *Picture) SniffInto() bool {
	info, ok := bits.SniffImage(p.Data)
	if !ok {
		if p.MIME == "" {
			p.MIME = UnrecognizedMIME
		}
		return false
	}
	if p.MIME == "" {
		p.MIME = info.MIME
	}
	if p.Width == 0 {
		p.Width = info.Width
	}
	if p.Height == 0 {
		p.Height = info.Height
	}
	if p.Depth == 0 {
		p.Depth = info.Depth
	}
	return true
}

// ClonePictures deep-copies the slice header and structural fields (sharing
// Data) for handing out detached accessor results.
func ClonePictures(ps []Picture) []Picture {
	if ps == nil {
		return nil
	}
	out := make([]Picture, len(ps))
	for i, p := range ps {
		out[i] = p.CloneMeta()
	}
	return out
}

// EqualPictures reports whether two picture slices are identical by content.
func EqualPictures(a, b []Picture) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].MIME != b[i].MIME ||
			a[i].Description != b[i].Description || a[i].Width != b[i].Width ||
			a[i].Height != b[i].Height || a[i].Depth != b[i].Depth ||
			a[i].Colors != b[i].Colors || !slices.Equal(a[i].Data, b[i].Data) {
			return false
		}
	}
	return true
}
