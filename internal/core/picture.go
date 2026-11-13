package core

import (
	"fmt"
	"hash/maphash"
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
	// PictureLossDescriptionOnly: every named role preserved; a description is lost, and a role
	// the ID3 table does not name (APE).
	PictureLossDescriptionOnly
)

// pictureLosesMetadata is per-picture; [PicturesLoseMetadata] folds it.
func pictureLosesMetadata(p Picture, loss PictureLoss) bool {
	switch loss {
	case PictureLossRoleAndDescription:
		return p.Type != PicFrontCover || p.Description != ""
	case PictureLossRoleOnly:
		// PicOther round-trips; only non-front, non-Other roles are loss.
		return p.Type != PicFrontCover && p.Type != PicOther
	case PictureLossDescriptionOnly:
		return p.Description != "" || p.Type > PicPublisherLogo
	}
	return false
}

// declaredGeometryReason is the loss of a size only the source's format had a field for.
const declaredGeometryReason = "the size declared for a picture whose bytes give none is not stored"

// declaresGeometry reports a size the picture's bytes do not give: one a file declared beside a
// link, an image type the sniffer does not read, or one it names without a size (AVIF). Only a
// format with size fields keeps it.
func (p Picture) declaresGeometry() bool {
	q := p.withoutDeclaredGeometry()
	return p.Width != q.Width || p.Height != q.Height || p.Depth != q.Depth || p.Colors != q.Colors
}

// withoutDeclaredGeometry is p as a format with no size fields reads it back: a dimension its
// bytes do not give is gone. One they do give is not the file's word to lose ([SniffStored]
// settles it from the bytes in every format).
func (p Picture) withoutDeclaredGeometry() Picture {
	info, _ := bits.SniffImage(p.Data)
	for _, d := range []struct {
		held    *int
		sniffed int
	}{{&p.Width, info.Width}, {&p.Height, info.Height}, {&p.Depth, info.Depth}, {&p.Colors, info.Colors}} {
		if d.sniffed == 0 {
			*d.held = 0
		}
	}
	return p
}

// WithoutDeclaredGeometry is pics as a format with no size fields holds them: a size the bytes do
// not give is gone. pics is returned as given when no picture declares one.
func WithoutDeclaredGeometry(pics []Picture) []Picture {
	out := pics
	for i, p := range pics {
		if !p.declaresGeometry() {
			continue
		}
		if &out[0] == &pics[0] {
			out = slices.Clone(pics)
		}
		out[i] = p.withoutDeclaredGeometry()
	}
	return out
}

// pictureLossReason is why c stores p in less than the form given, "" when it stores it whole.
func pictureLossReason(p Picture, c Capability) string {
	switch {
	case pictureLosesMetadata(p, c.PictureLoss):
		return c.Reason()
	case !c.PictureGeometry && p.declaresGeometry():
		return declaredGeometryReason
	}
	return ""
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
	c := p // Data stays shared
	return c
}

// SniffInto fills empty MIME/dimensions from bytes (fill-when-empty). CLI picture load uses this.
func (p *Picture) SniffInto() bool { return p.sniff(false) }

// SniffAuthoritative lets bytes override caller MIME/dimensions. Failed sniff -> [UnrecognizedMIME].
// [LinkMIME] is unchanged.
func (p *Picture) SniffAuthoritative() bool { return p.sniff(true) }

// SniffStored is [Picture.SniffAuthoritative] for a picture as a file holds it: an image type
// declared for bytes the sniffer has no signature for (SVG) stands, with the size declared
// beside it, since nothing here can verify either. A type the sniffer knows, declared for other
// bytes, is still unrecognized.
func SniffStored(p *Picture) bool {
	declared := *p
	ok := p.SniffAuthoritative()
	if !ok && unverifiableImageType(declared.MIME) {
		p.MIME, p.Width, p.Height, p.Depth, p.Colors = declared.MIME, declared.Width, declared.Height, declared.Depth, declared.Colors
	}
	return ok
}

// CanonicalImageMIME folds a declared picture type onto the MIME [bits.SniffImage] reports for
// such bytes, so a comparison against a sniff survives the spellings real taggers write:
// "image/jpg", "image/x-png", "JPEG", a bare "PNG", an upper-cased type, a parameter after a
// semicolon. A declaration that names no type the sniffer identifies returns "", which matches
// no sniff.
func CanonicalImageMIME(declared string) string {
	s, _, _ := strings.Cut(strings.ToLower(declared), ";")
	sub := strings.TrimPrefix(strings.TrimSpace(s), "image/")
	switch sub {
	case "jpg", "pjpeg":
		sub = "jpeg"
	case "x-png":
		sub = "png"
	case "x-bmp", "x-ms-bmp":
		sub = "bmp"
	case "tif", "x-tiff":
		sub = "tiff"
	}
	if bits.ImageExtension("image/"+sub) == "" {
		return ""
	}
	return "image/" + sub
}

// unverifiableImageType reports an image MIME naming a type [bits.SniffImage] does not identify,
// under any spelling of it.
func unverifiableImageType(mime string) bool {
	s, _, _ := strings.Cut(strings.ToLower(mime), ";")
	sub, ok := strings.CutPrefix(strings.TrimSpace(s), "image/")
	return ok && sub != "" && CanonicalImageMIME(mime) == ""
}

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

// ProjectPictures clones ps and settles each picture's type for display/lint ([SniffStored]).
// Originals unchanged so writers keep on-disk labels.
func ProjectPictures(ps []Picture) []Picture {
	out := ClonePictures(ps)
	for i := range out {
		SniffStored(&out[i])
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

// contentSeed seeds [ContentHash], once per process.
var contentSeed = maphash.MakeSeed()

// ContentHash returns a hash that stands for file content such as a picture, a frame or a tag item,
// so a set of hashes ([HashSet]) can stand for the content itself. Its seed is drawn once per
// process, so no file can pick contents that collide: two contents that hash alike differ only by
// a 64-bit chance.
func ContentHash() *maphash.Hash {
	h := new(maphash.Hash)
	h.SetSeed(contentSeed)
	return h
}

// HashSet is a set of [ContentHash] values.
type HashSet map[uint64]struct{}

// Add puts h in s.
func (s HashSet) Add(h uint64) { s[h] = struct{}{} }

// Has reports whether s holds h.
func (s HashSet) Has(h uint64) bool {
	_, ok := s[h]
	return ok
}

// PictureHashes is the set of the [PictureHash] values of the pictures ms hold.
func PictureHashes(ms ...*Media) HashSet {
	s := HashSet{}
	for _, m := range ms {
		for _, p := range m.Pictures {
			s.Add(PictureHash(p))
		}
	}
	return s
}

// PictureHash hashes what [EqualPictures] compares: equal pictures hash alike.
func PictureHash(p Picture) uint64 {
	h := ContentHash()
	fmt.Fprintf(h, "%d\x00%s\x00%s\x00%d %d %d %d\x00", p.Type, p.MIME, p.Description, p.Width, p.Height, p.Depth, p.Colors)
	h.Write(p.Data)
	return h.Sum64()
}

// EqualPictures reports whether two picture slices are identical by content.
func EqualPictures(a, b []Picture) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !equalPicture(a[i], b[i]) {
			return false
		}
	}
	return true
}

// equalPicture reports whether two pictures are identical by content.
func equalPicture(a, b Picture) bool {
	return a.Type == b.Type && a.MIME == b.MIME && a.Description == b.Description &&
		a.Width == b.Width && a.Height == b.Height && a.Depth == b.Depth &&
		a.Colors == b.Colors && slices.Equal(a.Data, b.Data)
}
