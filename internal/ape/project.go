package ape

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Projection is the canonical view of an APE tag (mirrors [id3.Projection]).
// No chapter/synced-lyrics members (AccessNone; no APE convention).
type Projection struct {
	Tags     tag.TagSet
	Pictures []core.Picture
	Families []core.FamilyValue
	Warnings []core.Warning
}

// Project decodes an APE tag; nil => empty set.
func Project(t *Tag) Projection {
	var contribs []core.Contribution
	var pics []core.Picture
	var warnings []core.Warning

	if t != nil {
		for _, it := range t.Items {
			if it.NonText() {
				if !IsCoverKey(it.Key) {
					continue // preserved in the item list, but not canonically projected
				}
				p, err := DecodeCover(it.Key, it.Data)
				if err != nil {
					// Warn and skip; item stays in native list for verbatim re-emit.
					warnings = core.Warn(warnings, core.WarnInvalidPicture, err.Error())
					continue
				}
				pics = append(pics, p)
				continue
			}
			key, ok := mapping.CanonicalAPE(it.Key)
			if !ok {
				continue
			}
			for _, v := range splitItemValues(it.Value) {
				contribs = append(contribs, core.Contribution{Key: key, Value: v, Source: it.Key})
			}
		}
	}

	ts := core.BuildTagSet(contribs)
	// Same slashed track/disc normalization as the text codecs.
	tag.NormalizeNumberPairs(&ts)
	return Projection{
		Tags:     ts,
		Pictures: pics,
		Families: core.BuildFamilies(contribs, core.FamilyAPEv2),
		Warnings: warnings,
	}
}

// InvalidKeyWarnings: text items whose name is unprojectable (APE printable ASCII
// vs canonical max 0x7D). Mirrors [Project]'s drop set. Binary/cover skipped;
// reserved names still project on read.
func InvalidKeyWarnings(t *Tag) []core.Warning {
	if t == nil {
		return nil
	}
	var ws []core.Warning
	for _, it := range t.Items {
		if it.NonText() {
			continue
		}
		if _, ok := mapping.CanonicalAPE(it.Key); ok {
			continue
		}
		ws = core.WarnInvalidKey(ws, it.Key)
	}
	return ws
}

// InvalidUTF8Warnings: non-UTF-8 text (read as Latin-1; worth reporting).
func InvalidUTF8Warnings(t *Tag) []core.Warning {
	if t == nil {
		return nil
	}
	var ws []core.Warning
	for _, it := range t.Items {
		if it.NonText() || utf8.Valid(it.Data) {
			continue
		}
		ws = core.Warn(ws, core.WarnInvalidText,
			fmt.Sprintf("APE item %q is not valid UTF-8; read as Latin-1", it.Key))
	}
	return ws
}

// EncoderNoise: inherited Lavf/... stamp (same as Vorbis/ID3 checks).
func EncoderNoise(items []Item) []core.Warning {
	var ws []core.Warning
	for _, it := range items {
		if it.NonText() {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(it.Key)) {
		case "encoder", "tool name", "encodedby", "encoded_by":
		default:
			continue
		}
		if core.IsTranscoderStamp(it.Value) {
			ws = core.Warn(ws, core.WarnInheritedEncoder, "inherited encoder item: "+core.WarnSnippet(it.Value))
		}
	}
	return ws
}

// Capabilities for APE-backed codecs (defined once so they cannot drift).
// Fields: free-form UTF-8, lossless. Pictures: Cover Art convention (lossy role/
// description). Chapters/synced lyrics/padding: AccessNone. perField nil.
func Capabilities(f core.Format, readOnly bool) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "APEv2 item", Fidelity: "lossless",
	}
	pictures := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "APEv2 Cover Art item",
		Fidelity:       "image stored; only the front and back cover roles round-trip, and descriptions are dropped",
		Constraints:    []string{"the Cover Art convention holds one front and one back cover; another role is stored under a free cover name and reads back as that cover, and descriptions have nowhere to go"},
		PictureLoss:    core.PictureLossNonCoverRoleAndDescription,
	}
	// Two cover names are slots; shared partition with editor/transfer.
	pictures = core.WithPictureSlots(pictures, func(pics []core.Picture, added []bool) []int {
		keptIdx, _ := PartitionCoverSlots(pics, added)
		return keptIdx
	}, CoverSlotsReason)
	caps := core.NewCapabilities(f, readOnly, fields, pictures, core.Capability{}, core.AccessNone, nil).
		WithFieldClassifier(TransferClassifier)
	if readOnly {
		// Carry refusal reason (asf/mp4 pattern) for transfer decline before Plan.
		caps = caps.WithReadOnlyReason(fmt.Errorf("%w: this %s file cannot be written", waxerr.ErrUnsupportedFormat, f))
	}
	return caps
}

// TransferClassifier: reserved names and Cover Art text keys => Dropped
// (report==write; same predicates as the writer). Attached via shared Capabilities.
func TransferClassifier(key tag.Key, _ []string, _ tag.TagSet) (core.Disposition, string, bool) {
	name := mapping.APEName(key)
	if ReservedItemName(name) {
		return core.Dropped, "the APEv2 specification reserves this item name, so it cannot be written", true
	}
	if IsCoverKey(name) {
		return core.Dropped, "the Cover Art convention types this item name binary, so a text value cannot be written", true
	}
	return core.Carried, "", false
}
