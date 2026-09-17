package core

import (
	"fmt"
	"slices"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
)

// PictureType is cover-art role (ID3 APIC / FLAC PICTURE type IDs).
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

// SingleIcon reports type-1 or type-2 icons, which must be unique per file.
func (p PictureType) SingleIcon() bool {
	return p == PicFileIcon || p == PicOtherFileIcon
}

// UnrecognizedMIME is stored when sniff finds no image header. Shared by linter and editor.
const UnrecognizedMIME = "application/octet-stream"

// LinkMIME is APIC/PICTURE URL payload ("-->"). Sniff does not alter it.
const LinkMIME = "-->"

// CountIcons counts type-1 and type-2 icons. Shared by writer validation and linter.
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

// fileIconSide is required type-1 icon size (ID3v2 4.14).
const fileIconSide = 32

// fileIconMIME is required type-1 icon MIME (ID3v2 4.14).
const fileIconMIME = "image/png"

// NonConformingIcon reports a type-1 icon that is not 32x32 PNG. Skips zero dimensions
// and [UnrecognizedMIME] pictures. MIME compare is case-folded.
func NonConformingIcon(p Picture) (string, bool) {
	if p.Type != PicFileIcon || p.Unrecognized() {
		return "", false
	}
	switch {
	case p.MIME != "" && !strings.EqualFold(p.MIME, fileIconMIME):
		return fmt.Sprintf("file-icon picture is %s; the type requires %s", p.MIME, fileIconMIME), true
	case p.Width > 0 && p.Height > 0 && (p.Width != fileIconSide || p.Height != fileIconSide):
		return fmt.Sprintf("file-icon picture is %dx%d; the type requires %dx%d",
			p.Width, p.Height, fileIconSide, fileIconSide), true
	}
	return "", false
}

// PictureLoss names picture metadata a destination drops. On pictures [Capability];
// [PicturesLoseMetadata] is shared by transfers and write warnings.
type PictureLoss uint8

const (
	// PictureLossNone: role and description preserved.
	PictureLossNone PictureLoss = iota
	// PictureLossRoleOnly: front and Other preserved; other roles read as Other (Matroska).
	PictureLossRoleOnly
	// PictureLossRoleAndDescription: bytes only (MP4 covr).
	PictureLossRoleAndDescription
	// PictureLossNonCoverRoleAndDescription: front/back exact; other roles remapped (APE).
	PictureLossNonCoverRoleAndDescription
)

// pictureLosesMetadata is per-picture; [PicturesLoseMetadata] folds it.
func pictureLosesMetadata(p Picture, loss PictureLoss) bool {
	switch loss {
	case PictureLossRoleAndDescription:
		return p.Type != PicFrontCover || p.Description != ""
	case PictureLossRoleOnly:
		// PicOther round-trips; only non-front, non-Other roles are loss.
		return p.Type != PicFrontCover && p.Type != PicOther
	case PictureLossNonCoverRoleAndDescription:
		// Front/back exact; other roles or any description are loss.
		return (p.Type != PicFrontCover && p.Type != PicBackCover) || p.Description != ""
	}
	return false
}

// PicturesLoseMetadata folds pictureLosesMetadata over pics.
func PicturesLoseMetadata(pics []Picture, loss PictureLoss) bool {
	for _, p := range pics {
		if pictureLosesMetadata(p, loss) {
			return true
		}
	}
	return false
}

// Picture is an embedded image. Data is shared read-only; do not mutate Data.
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

// Hash is SHA256 of image bytes (ignores type and description).
func (p Picture) Hash() [32]byte {
	return bits.SHA256(p.Data)
}

// Unrecognized reports [UnrecognizedMIME].
func (p Picture) Unrecognized() bool { return p.MIME == UnrecognizedMIME }

// CloneMeta copies structural fields; Data stays shared.
func (p Picture) CloneMeta() Picture {
	c := p // Data shared by design
	return c
}

// SniffInto fills empty MIME/dimensions from bytes (fill-when-empty). CLI picture load uses this.
func (p *Picture) SniffInto() bool { return p.sniff(false) }

// SniffAuthoritative lets bytes override caller MIME/dimensions. Failed sniff -> [UnrecognizedMIME].
// [LinkMIME] is unchanged.
func (p *Picture) SniffAuthoritative() bool { return p.sniff(true) }

// EffectiveMIME is sniffed MIME, [LinkMIME], or [UnrecognizedMIME] without mutating p.
func (p Picture) EffectiveMIME() string {
	if info, ok := bits.SniffImage(p.Data); ok {
		return info.MIME
	}
	if p.MIME == LinkMIME {
		return LinkMIME
	}
	return UnrecognizedMIME
}

// sniff backs SniffInto and SniffAuthoritative.
func (p *Picture) sniff(authoritative bool) bool {
	if p.MIME == LinkMIME {
		return false
	}
	info, ok := bits.SniffImage(p.Data)
	if !ok {
		if authoritative || p.MIME == "" {
			p.MIME = UnrecognizedMIME
		}
		if authoritative {
			p.Width, p.Height, p.Depth, p.Colors = 0, 0, 0, 0
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

// pickDim merges caller and sniffed dimension per authoritative vs fill-when-empty rules.
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

// ProjectPictures clones ps and SniffAuthoritative each picture for display/lint.
// Originals unchanged so writers keep on-disk labels.
func ProjectPictures(ps []Picture) []Picture {
	out := ClonePictures(ps)
	for i := range out {
		out[i].SniffAuthoritative()
	}
	return out
}

// ClonePictures clones slice and meta; Data shared. Nil in, nil out.
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

// PictureUnsupportedMessage is for formats that cannot store cover art.
func PictureUnsupportedMessage() string {
	return "this file's format cannot store cover art; the picture was dropped"
}

// PicturesReadOnlyMessage is for read-only cover art (e.g. WebM attachments).
func PicturesReadOnlyMessage() string {
	return "cover art in this file is read-only; the picture edit was dropped and the file keeps its cover art"
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
