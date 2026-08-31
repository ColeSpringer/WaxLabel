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

// CoverSlotsReason explains a picture dropped because the convention's item names ran
// out. The transfer report's dropped-picture item and the write-time warning both use it,
// so a copy's grade and the write it predicts describe the same loss the same way.
const CoverSlotsReason = "the Cover Art convention stores at most one front and one back cover item, leaving no item name free"

// PartitionCoverSlots splits pics into the pictures the two-name Cover Art convention can
// hold and the rest. APEv2 item names are unique within a tag, so at most one picture per
// name can be written: one front item and one back item. added marks the pictures an edit
// authored (nil when the distinction does not exist, as in a transfer projection); see
// [assignCoverSlots] for the claim order. Both index slices are ascending, so emission
// keeps the set's order.
//
// It is the selection behind the capability's slot partition, the editor's pre-plan
// resolution, and the writer's emit, so the three cannot disagree about who has a slot.
func PartitionCoverSlots(pics []core.Picture, added []bool) (keptIdx, droppedIdx []int) {
	keptIdx, _ = assignCoverSlots(pics, added, nil)
	kept := make([]bool, len(pics))
	for _, i := range keptIdx {
		kept[i] = true
	}
	for i := range pics {
		if !kept[i] {
			droppedIdx = append(droppedIdx, i)
		}
	}
	return keptIdx, droppedIdx
}

// assignCoverSlots is the one slot-assignment engine. An exact front or back claims its
// own slot and never the other, since writing a known front as the back cover would
// falsify a role the source asserted; any other role's name is already being rewritten,
// so it takes whichever slot is free, front before back. Within a claim tier a picture
// the edit added beats one the file already had (the edit targets the slot; a nil added
// treats all pictures alike), and the earlier picture wins among equals - what a
// faithful transfer of a two-front source carries.
//
// blocked names slots an undecodable cover item holds. A spilling picture prefers an
// unblocked slot so the junk bytes survive, but takes a blocked one over being dropped;
// an exact role claims its slot regardless, since that is a targeted replacement. Blocking
// never changes who is kept - only which name they get and whether junk is displaced - so
// the file-blind capability partition and the writer agree on every picture's fate.
//
// keptIdx is ascending; names[j] is the item name assigned to pics[keptIdx[j]].
func assignCoverSlots(pics []core.Picture, added []bool, blocked map[string]bool) (keptIdx []int, names []string) {
	taken := map[string]bool{}
	slot := make(map[int]string, 2)
	isAdded := func(i int) bool { return added != nil && i < len(added) && added[i] }
	// Two sweeps per tier put added pictures ahead of pre-existing ones while keeping
	// each group's own order stable.
	forEachByPriority := func(visit func(i int, p core.Picture)) {
		for i, p := range pics {
			if isAdded(i) {
				visit(i, p)
			}
		}
		for i, p := range pics {
			if !isAdded(i) {
				visit(i, p)
			}
		}
	}
	forEachByPriority(func(i int, p core.Picture) {
		if p.Type != core.PicFrontCover && p.Type != core.PicBackCover {
			return
		}
		if name := coverKey(p.Type); !taken[name] {
			taken[name] = true
			slot[i] = name
		}
	})
	forEachByPriority(func(i int, p core.Picture) {
		if p.Type == core.PicFrontCover || p.Type == core.PicBackCover {
			return
		}
		free := ""
		for _, name := range []string{coverFrontKey, coverBackKey} {
			if taken[name] {
				continue
			}
			if !blocked[name] {
				free = name
				break
			}
			if free == "" {
				free = name // junk-held: displace it only when nothing else is free
			}
		}
		if free != "" {
			taken[free] = true
			slot[i] = free
		}
	})
	for i := range pics {
		if name, ok := slot[i]; ok {
			keptIdx = append(keptIdx, i)
			names = append(names, name)
		}
	}
	return keptIdx, names
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
func EncodeCover(p core.Picture) Item { return encodeCoverAs(p, coverKey(p.Type)) }

// encodeCoverAs renders p under an assigned item name, which the slot assignment may
// pick apart from the role's own (a spilled role stored under the free back name).
func encodeCoverAs(p core.Picture, name string) Item {
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
