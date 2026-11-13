package ape

import (
	"bytes"
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// Cover art convention (not in the spec): a binary item named "Cover Art (<role>)" holding a
// NUL-terminated file name, then the image bytes. Taggers agree on the front and back names only.
// A picture of another role is written under the name Mp3tag and Kid3 give it, and the names
// TagLib# and foobar2000 write are read too.
const (
	coverPrefix = "cover art"
	// artistPictureKey is foobar2000's item for the artist picture, the one name outside the pattern.
	artistPictureKey = "artist picture"
)

// roleNames holds the role each picture type is written under, as "Cover Art (<role>)".
var roleNames = [...]string{
	core.PicOther:              "Other",
	core.PicFileIcon:           "Png Icon",
	core.PicOtherFileIcon:      "Icon",
	core.PicFrontCover:         "Front",
	core.PicBackCover:          "Back",
	core.PicLeaflet:            "Leaflet",
	core.PicMedia:              "Media",
	core.PicLeadArtist:         "Lead Artist",
	core.PicArtist:             "Artist",
	core.PicConductor:          "Conductor",
	core.PicBand:               "Band",
	core.PicComposer:           "Composer",
	core.PicLyricist:           "Lyricist",
	core.PicRecordingLocation:  "Recording Location",
	core.PicDuringRecording:    "During Recording",
	core.PicDuringPerformance:  "During Performance",
	core.PicVideoScreenCapture: "Video Capture",
	core.PicBrightFish:         "Fish",
	core.PicIllustration:       "Illustration",
	core.PicBandLogo:           "Band Logotype",
	core.PicPublisherLogo:      "Publisher Logotype",
}

// rolesByName maps a role as an item names it, lower case, to its picture type: the names written
// here, the ones TagLib# writes where they differ, foobar2000's disc, and the ID3 names of the
// types. TagLib# calls the file icon "icon", which here and in Mp3tag is the other icon.
var rolesByName = func() map[string]core.PictureType {
	m := map[string]core.PictureType{
		"file icon":            core.PicFileIcon,
		"other icon":           core.PicOtherFileIcon,
		"other file icon":      core.PicOtherFileIcon,
		"front cover":          core.PicFrontCover,
		"back cover":           core.PicBackCover,
		"disc":                 core.PicMedia,
		"lead":                 core.PicLeadArtist,
		"studio":               core.PicRecordingLocation,
		"recording":            core.PicDuringRecording,
		"performance":          core.PicDuringPerformance,
		"movie scene":          core.PicVideoScreenCapture,
		"video screen capture": core.PicVideoScreenCapture,
		"colored fish":         core.PicBrightFish,
		"bright fish":          core.PicBrightFish,
		"band logo":            core.PicBandLogo,
		"publisher logo":       core.PicPublisherLogo,
	}
	for t, name := range roleNames {
		m[strings.ToLower(name)] = core.PictureType(t)
	}
	return m
}()

// RoleName returns the item name a picture of type t is written under: "Cover Art (<role>)". A
// type the ID3 table does not name is written as Other.
func RoleName(t core.PictureType) string {
	if int(t) >= len(roleNames) {
		t = core.PicOther
	}
	return "Cover Art (" + roleNames[t] + ")"
}

// RoleFromName reports whether name is a cover item's, in any case, and the picture type it names:
// "Cover Art", alone or with a role in parentheses, or foobar2000's "Artist Picture". A role no
// tagger is known to write reads as Other.
func RoleFromName(name string) (core.PictureType, bool) {
	s := strings.ToLower(strings.TrimSpace(name))
	if s == artistPictureKey {
		return core.PicArtist, true
	}
	rest, ok := strings.CutPrefix(s, coverPrefix)
	if !ok {
		return 0, false
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return core.PicOther, true
	}
	if rest[0] != '(' {
		return 0, false // a name that only starts alike, such as "Cover Artist"
	}
	if t, ok := rolesByName[strings.TrimSpace(strings.TrimSuffix(rest[1:], ")"))]; ok {
		return t, true
	}
	return core.PicOther, true
}

// IsCoverKey reports a cover item name ([RoleFromName]).
func IsCoverKey(name string) bool {
	_, ok := RoleFromName(name)
	return ok
}

// CoverSlotsReason: shared by transfer drop grade and write warning.
const CoverSlotsReason = "APEv2 item names are unique, and a picture the file does not hold is written under the one Cover Art name of its role"

// coverSlot is where [assignCovers] stores picture pic: under name, as the item orig[from] when the
// file already holds the picture, from being -1 for a picture written anew.
type coverSlot struct {
	pic  int
	name string
	from int
}

// assignCovers gives each picture the item it is stored as, in picture order. A picture the file
// holds, role and image bytes, in an item of orig no other picture has taken is stored as that item,
// under its name; any other is written anew under its role's name ([RoleName]). Item names are
// unique, compared without case: where a picture written anew is one of two that get a name, the
// one an edit added (added[i]) wins, else the earlier, and the loser gets no slot. Items of orig
// that share a name keep it as found.
//
// Of the items that hold one picture, those under another name than the role's own are taken
// first. A picture written anew can only displace the item under the role's own name, so the
// pictures that stay take the same items when they are assigned again without the displaced one:
// the editor's choice, a transfer's and the writer's agree.
func assignCovers(pics []core.Picture, added []bool, orig []Item) []coverSlot {
	type content struct {
		role  core.PictureType
		image uint64
	}
	held := map[content][]int{} // the items of orig that hold a picture, each taken once
	for i, it := range orig {
		if t, img, ok := coverImage(it); ok {
			c := content{t, imageHash(img)}
			held[c] = append(held[c], i)
		}
	}
	for c, at := range held {
		own := strings.ToLower(RoleName(c.role))
		slices.SortStableFunc(at, func(a, b int) int {
			return cmp.Compare(btoi(strings.ToLower(orig[a].Key) == own), btoi(strings.ToLower(orig[b].Key) == own))
		})
	}
	const kept, anew = 1, 2 // what holds a name: items of the file, or a picture written anew
	names := map[string]int{}
	slots := make([]coverSlot, len(pics))
	assign := func(i int) {
		p := pics[i]
		s := coverSlot{pic: i, name: RoleName(p.Type), from: -1}
		if len(held) > 0 { // a tag with no cover items holds no picture: nothing to hash
			c := content{p.Type, imageHash(p.Data)}
			if at := held[c]; len(at) > 0 {
				// Another image under the same hash is not this picture: it is written anew.
				if _, img, _ := coverImage(orig[at[0]]); bytes.Equal(img, p.Data) {
					s.name, s.from = orig[at[0]].Key, at[0]
					held[c] = at[1:]
				}
			}
		}
		fold := strings.ToLower(s.name)
		switch {
		case s.from >= 0 && names[fold] != anew:
			names[fold] = kept
		case s.from < 0 && names[fold] == 0:
			names[fold] = anew
		default:
			s.pic = -1
		}
		slots[i] = s
	}
	isAdded := func(i int) bool { return i < len(added) && added[i] }
	for i := range pics {
		if isAdded(i) {
			assign(i)
		}
	}
	for i := range pics {
		if !isAdded(i) {
			assign(i)
		}
	}
	return slices.DeleteFunc(slots, func(s coverSlot) bool { return s.pic < 0 })
}

// btoi orders false before true.
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// PartitionCovers splits pics into the pictures a tag whose items are orig can hold and the ones
// left without an item name ([assignCovers]). added marks the pictures an edit added, nil in a
// transfer. Shared by the capability, the editor and the writer.
func PartitionCovers(pics []core.Picture, added []bool, orig []Item) (keptIdx, droppedIdx []int) {
	kept := make([]bool, len(pics))
	for _, s := range assignCovers(pics, added, orig) {
		kept[s.pic] = true
		keptIdx = append(keptIdx, s.pic)
	}
	for i := range pics {
		if !kept[i] {
			droppedIdx = append(droppedIdx, i)
		}
	}
	return keptIdx, droppedIdx
}

// coverImage returns the role and image bytes of a cover item that decodes, the bytes shared with
// the item.
func coverImage(it Item) (core.PictureType, []byte, bool) {
	t, ok := RoleFromName(it.Key)
	if !ok || !it.NonText() {
		return 0, nil, false
	}
	_, img, ok := bytes.Cut(it.Payload(), []byte{0})
	return t, img, ok && len(img) > 0
}

// imageHash hashes image bytes.
func imageHash(img []byte) uint64 {
	h := core.ContentHash()
	h.Write(img)
	return h.Sum64()
}

// DecodeCover: file name is not a description; MIME/geometry come from sniff.
func DecodeCover(name string, data []byte) (core.Picture, error) {
	_, img, ok := bytes.Cut(data, []byte{0})
	if !ok {
		return core.Picture{}, fmt.Errorf("%w: %s item has no NUL-terminated file name", waxerr.ErrInvalidData, name)
	}
	if len(img) == 0 {
		return core.Picture{}, fmt.Errorf("%w: %s item carries no image bytes", waxerr.ErrInvalidData, name)
	}
	// Sniff MIME/geometry; unrecognized => UnrecognizedMIME (lint invalid-picture).
	t, _ := RoleFromName(name)
	p := core.Picture{Type: t, Data: slices.Clone(img)}
	p.SniffInto()
	return p, nil
}

// EncodeCover: the role's name + "filename\0" + image bytes (synthesized name).
func EncodeCover(p core.Picture) Item { return encodeCoverAs(p, RoleName(p.Type)) }

// encodeCoverAs: p under name, its file name made from it.
func encodeCoverAs(p core.Picture, name string) Item {
	file := strings.ToLower(strings.ReplaceAll(name, " ", "_")) + coverExt(p.EffectiveMIME())
	data := make([]byte, 0, len(file)+1+len(p.Data))
	data = append(data, file...)
	data = append(data, 0)
	data = append(data, p.Data...)
	return Item{Key: name, Data: data, Flags: itemTypeBinary << itemTypeShift}
}

// coverExt: sniffer extension for the stored file name (none if unrecognized).
func coverExt(mime string) string { return bits.ImageExtension(mime) }
