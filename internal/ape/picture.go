package ape

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// Cover art convention (not in the spec): binary "Cover Art (Front|Back)" with
// NUL-terminated file name then image bytes (foobar2000/Mp3tag).
const (
	coverFrontKey = "Cover Art (Front)"
	coverBackKey  = "Cover Art (Back)"
)

// IsCoverKey: cover-art convention name (case-insensitive).
func IsCoverKey(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case strings.ToLower(coverFrontKey), strings.ToLower(coverBackKey):
		return true
	}
	return false
}

// coverPictureType maps cover item name to PictureType.
func coverPictureType(name string) core.PictureType {
	if strings.EqualFold(strings.TrimSpace(name), coverBackKey) {
		return core.PicBackCover
	}
	return core.PicFrontCover
}

// coverKey: Front/Back names; other roles store as front.
func coverKey(t core.PictureType) string {
	if t == core.PicBackCover {
		return coverBackKey
	}
	return coverFrontKey
}

// CoverSlotsReason: shared by transfer drop grade and write warning.
const CoverSlotsReason = "the Cover Art convention stores at most one front and one back cover item, leaving no item name free"

// PartitionCoverSlots: kept vs dropped under the two-name Cover Art convention
// (one front, one back). added marks edit-authored pictures (nil in transfer).
// Shared by capability, editor, and writer; see [assignCoverSlots].
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

// assignCoverSlots: exact front/back claim their own slot; other roles take free
// slots (front first). Within a tier, added beats pre-existing; earlier wins ties.
// blocked: undecodable cover slots; spill prefers unblocked but takes blocked over
// drop. Exact roles claim regardless. keptIdx ascending; names[j] for keptIdx[j].
func assignCoverSlots(pics []core.Picture, added []bool, blocked map[string]bool) (keptIdx []int, names []string) {
	taken := map[string]bool{}
	slot := make(map[int]string, 2)
	isAdded := func(i int) bool { return added != nil && i < len(added) && added[i] }
	// Added first, then pre-existing; order stable within each group.
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

// DecodeCover: file name is not a description; MIME/geometry come from sniff.
func DecodeCover(name string, data []byte) (core.Picture, error) {
	i := bytes.IndexByte(data, 0)
	if i < 0 {
		return core.Picture{}, fmt.Errorf("%w: %s item has no NUL-terminated file name", waxerr.ErrInvalidData, name)
	}
	img := data[i+1:]
	if len(img) == 0 {
		return core.Picture{}, fmt.Errorf("%w: %s item carries no image bytes", waxerr.ErrInvalidData, name)
	}
	// Sniff MIME/geometry; unrecognized => UnrecognizedMIME (lint invalid-picture).
	p := core.Picture{Type: coverPictureType(name), Data: slices.Clone(img)}
	p.SniffInto()
	return p, nil
}

// EncodeCover: convention name + "filename\0" + image bytes (synthesized name).
func EncodeCover(p core.Picture) Item { return encodeCoverAs(p, coverKey(p.Type)) }

// encodeCoverAs: p under an assigned name (may differ from role's own).
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
