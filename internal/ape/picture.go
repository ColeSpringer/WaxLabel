package ape

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// Cover art in APE is a convention, not part of the specification: a binary item
// named "Cover Art (Front)" or "Cover Art (Back)" whose payload is a NUL-terminated
// file name followed by the image bytes. That is what foobar2000 and Mp3tag write
// and read, which is the whole reason it is supported here - WaxLabel adopts APE
// conventions that already exist rather than porting its own into a format that has
// none.
const (
	coverFrontKey = "Cover Art (Front)"
	coverBackKey  = "Cover Art (Back)"
)

// IsCoverKey reports whether an item name is one of the cover-art conventions this
// package owns. Matching is case-insensitive because APE names are.
func IsCoverKey(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case strings.ToLower(coverFrontKey), strings.ToLower(coverBackKey):
		return true
	}
	return false
}

// coverPictureType maps a cover item name to the picture type it denotes.
func coverPictureType(name string) core.PictureType {
	if strings.EqualFold(strings.TrimSpace(name), coverBackKey) {
		return core.PicBackCover
	}
	return core.PicFrontCover
}

// coverKey is the item name for a picture type. Only front and back have a
// convention; every other role is written as a front cover, which is what a reader
// expecting the convention will find.
func coverKey(t core.PictureType) string {
	if t == core.PicBackCover {
		return coverBackKey
	}
	return coverFrontKey
}

// DecodeCover decodes a cover-art item's payload into a Picture. The stored file
// name is a file name, not a description - that is what the tools writing this
// convention put there - so it is not projected as one; the MIME type and geometry
// are sniffed from the image bytes, since the convention stores neither.
func DecodeCover(name string, data []byte) (core.Picture, error) {
	i := bytes.IndexByte(data, 0)
	if i < 0 {
		return core.Picture{}, fmt.Errorf("%w: %s item has no NUL-terminated file name", waxerr.ErrInvalidData, name)
	}
	img := data[i+1:]
	if len(img) == 0 {
		return core.Picture{}, fmt.Errorf("%w: %s item carries no image bytes", waxerr.ErrInvalidData, name)
	}
	// The convention stores neither MIME nor geometry, so both come from the image
	// header via the shared sniffer; unrecognized bytes degrade to UnrecognizedMIME,
	// which is what the linter's invalid-picture rule keys on.
	p := core.Picture{Type: coverPictureType(name), Data: slices.Clone(img)}
	p.SniffInto()
	return p, nil
}

// EncodeCover renders a Picture as a cover-art item: the item name the convention
// uses for its type, and a payload of "filename\0" plus the image bytes. The file
// name is synthesized from the role and the image's type, since the picture's
// description has no home in this convention and a file name is not one.
func EncodeCover(p core.Picture) Item {
	name := coverKey(p.Type)
	file := strings.ToLower(strings.ReplaceAll(name, " ", "_")) + coverExt(p.EffectiveMIME())
	data := make([]byte, 0, len(file)+1+len(p.Data))
	data = append(data, file...)
	data = append(data, 0)
	data = append(data, p.Data...)
	return Item{Key: name, Data: data, Flags: itemTypeBinary << itemTypeShift}
}

// coverExt is the conventional file extension for a picture MIME, used only to
// build the stored file name. An unrecognized type gets none rather than a
// misleading one.
func coverExt(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/bmp":
		return ".bmp"
	case "image/webp":
		return ".webp"
	case "image/tiff":
		return ".tiff"
	}
	return ""
}
