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

// PictureLoss names which picture metadata a destination format drops when it
// stores cover art as image data with a fixed role. It is recorded on the
// pictures [Capability] so transfer reports and codec write-time warnings use
// the same [PicturesLoseMetadata] predicate.
type PictureLoss uint8

const (
	// PictureLossNone means the format preserves role and description (FLAC, Ogg, ID3).
	PictureLossNone PictureLoss = iota
	// PictureLossRoleOnly means the format preserves the front-cover and Other roles,
	// but reads any other role back as Other. Descriptions survive. Matroska names only
	// cover and small_cover, and small_cover round-trips as Other, so only a role that is
	// neither front cover nor Other is lost.
	PictureLossRoleOnly
	// PictureLossRoleAndDescription means the format stores image bytes only,
	// dropping both role and description. MP4's covr atom does this: every cover reads
	// back as a front cover with no description.
	PictureLossRoleAndDescription
)

// PicturesLoseMetadata reports whether storing pics under a destination whose
// picture loss is loss would drop role and/or description metadata the pictures
// actually carry. Write-time picture-metadata warnings and transfer disposition
// both use this predicate, so a copy reported lossy is the same case whose write
// warns. A plain front cover with no description is never flagged.
func PicturesLoseMetadata(pics []Picture, loss PictureLoss) bool {
	for _, p := range pics {
		switch loss {
		case PictureLossRoleAndDescription:
			if p.Type != PicFrontCover || p.Description != "" {
				return true
			}
		case PictureLossRoleOnly:
			// A PicOther picture already round-trips as Other (Matroska's small_cover), so
			// only a role that is neither front cover nor Other is lost.
			if p.Type != PicFrontCover && p.Type != PicOther {
				return true
			}
		}
	}
	return false
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
// when they are not already set, via a header-only sniff (no decode). It returns
// whether the format was recognized. This is the fill-when-empty variant: it leaves
// a value the caller already set in place and only supplies the ones left zero. The
// CLI's picture-load path (--add-cover/--add-picture) uses it to fill a freshly read
// image's fields. The codec read paths, by contrast, use [Picture.SniffAuthoritative]
// so recognizable bytes win over a mislabeled container MIME.
func (p *Picture) SniffInto() bool { return p.sniff(false) }

// SniffAuthoritative reconciles MIME and dimensions with the picture's own bytes,
// letting a successful sniff overwrite a caller-declared value that disagrees - so a
// mislabeled cover cannot be embedded under a MIME that contradicts its bytes. It is
// the embed-path counterpart to [Picture.SniffInto] ([Editor.AddPicture] uses it);
// each dimension is taken only when the sniff determined it (non-zero), so a sniffer
// that could not fill one does not clobber a caller value with a 0. A failed sniff
// preserves the caller's values, degrading MIME to [UnrecognizedMIME] only when none
// was set.
func (p *Picture) SniffAuthoritative() bool { return p.sniff(true) }

// EffectiveMIME returns the MIME type an authoritative sniff would store: the canonical
// sniffed type when the bytes are recognized, otherwise the stored label, or
// [UnrecognizedMIME] when there is no label. It mirrors [Editor.AddPicture] without
// mutating p, so representability checks use the type the writer will actually see
// rather than a stale or non-canonical container label.
func (p Picture) EffectiveMIME() string {
	if info, ok := bits.SniffImage(p.Data); ok {
		return info.MIME
	}
	if p.MIME == "" {
		return UnrecognizedMIME
	}
	return p.MIME
}

// sniff backs [Picture.SniffInto] (fill-when-empty) and [Picture.SniffAuthoritative]
// (bytes win). On a failed sniff both only set an empty MIME to [UnrecognizedMIME]; on
// a success, authoritative overwrites MIME and every sniff-determined dimension, while
// fill-when-empty sets only the fields the caller left zero.
func (p *Picture) sniff(authoritative bool) bool {
	info, ok := bits.SniffImage(p.Data)
	if !ok {
		if p.MIME == "" {
			p.MIME = UnrecognizedMIME
		}
		return false
	}
	if authoritative || p.MIME == "" {
		p.MIME = info.MIME
	}
	p.Width = pickDim(authoritative, p.Width, info.Width)
	p.Height = pickDim(authoritative, p.Height, info.Height)
	p.Depth = pickDim(authoritative, p.Depth, info.Depth)
	p.Colors = pickDim(authoritative, p.Colors, info.Colors)
	return true
}

// pickDim chooses a picture dimension between the caller's value and the sniffed one.
// Authoritative: the sniffed value wins when it was determined (non-zero), else the
// caller's stands. Fill-when-empty: the sniffed value fills a caller zero only.
func pickDim(authoritative bool, cur, sniffed int) int {
	if authoritative {
		if sniffed != 0 {
			return sniffed
		}
		return cur
	}
	if cur == 0 {
		return sniffed
	}
	return cur
}

// ProjectPictures returns a display copy of ps: an independent clone (the image Data stays shared,
// read-only) whose MIME and dimensions are reconciled with each picture's own bytes via
// [Picture.SniffAuthoritative], so a mislabeled cover reports its real type and a junk cover degrades
// to [UnrecognizedMIME] for the linter to flag. The caller's originals are left alone, which is what
// lets FLAC and Ogg keep a mislabeled cover's on-disk label through an unrelated edit: their writers
// re-serialize from the stored set, so the sniffed type must stay out of it. The accessor and the
// linter project; the codecs keep the raw type.
func ProjectPictures(ps []Picture) []Picture {
	out := ClonePictures(ps)
	for i := range out {
		out[i].SniffAuthoritative()
	}
	return out
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
