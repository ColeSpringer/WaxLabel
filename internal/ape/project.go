package ape

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
)

// Projection is the canonical view of an APE tag (mirrors [id3.Projection]).
// No chapter/synced-lyrics members (AccessNone; no APE convention).
type Projection struct {
	Tags     tag.TagSet
	Pictures []core.Picture
	Families []core.FamilyValue
	Warnings []core.Warning
}

// Carried reports whether the canonical view carries item it: a text item whose name maps to a key,
// or a cover item that decodes. Any other binary item is kept without being read, so a duplicate tag
// a rewrite drops loses it.
func Carried(it Item) bool {
	if it.NonText() {
		_, _, ok := coverImage(it)
		return ok
	}
	_, ok := mapping.CanonicalAPE(it.Key)
	return ok
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

// ItemNameWarnings reports, in one warning, the items of t whose names the specification does not
// allow ([InvalidItemName]), keyed by the tags that read them. A text item [InvalidKeyWarnings]
// reports is left to it. An edit that leaves such an item alone keeps it as it is; an edit of its
// key writes a name inside the specification where the file or the key has one, and is refused
// where neither has; a cover item goes with its picture.
func ItemNameWarnings(t *Tag) []core.Warning {
	if t == nil {
		return nil
	}
	var first string
	var keys []tag.Key
	listed := map[tag.Key]bool{}
	n := 0
	for _, it := range t.Items {
		if _, bad := InvalidItemName(it.Key); !bad {
			continue
		}
		key, ok := mapping.CanonicalAPE(it.Key)
		switch {
		case it.NonText(): // no key reads a binary item, a cover among them
		case !ok:
			continue // invalid-tag-key reports it
		case !listed[key]:
			listed[key] = true
			keys = append(keys, key)
		}
		if n++; n == 1 {
			first = it.Key
		}
	}
	if n == 0 {
		return nil
	}
	msg := `APE item name "` + core.WarnSnippet(first) + `" is not the 2 to 255 characters of printable ASCII the specification allows; preserved`
	if n > 1 {
		msg += fmt.Sprintf(" (and %d more)", n-1)
	}
	return core.WarnKeyed(nil, core.WarnMalformedTagEntry, msg, keys...)
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

// Capabilities is the one definition shared by all APE-backed codecs. items are the items of the
// tag the file holds, nil without a file: they say which pictures it already holds, and under
// which names. Fields: free-form UTF-8, lossless. Pictures: Cover Art convention, one item per
// role (descriptions lost). Chapters/synced lyrics/padding: AccessNone. perField nil.
func Capabilities(f core.Format, items []Item) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "APEv2 item", Fidelity: "lossless",
	}
	pictures := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "APEv2 Cover Art item",
		Fidelity:       "image and role stored; descriptions are dropped",
		Constraints: []string{
			"each picture is an item named for its role, Cover Art (<role>): taggers agree on the front and back names only, the other roles are written under the names Mp3tag and Kid3 give them, and the names TagLib# and foobar2000 write are read too",
			"item names are unique, so a role holds one picture written here; descriptions have nowhere to go",
		},
		PictureLoss: core.PictureLossDescriptionOnly,
	}
	// One name per role; shared partition with editor/transfer.
	pictures = core.WithPictureSlots(pictures, func(pics []core.Picture, added []bool) []int {
		keptIdx, _ := PartitionCovers(pics, added, items)
		return keptIdx
	}, CoverSlotsReason)
	return core.NewCapabilities(f, false, fields, pictures, core.Capability{}, core.AccessNone, nil).
		WithFieldClassifier(NewTransferClassifier(items))
}

// NewTransferClassifier returns the classifier for a destination whose tag holds items, nil for a
// destination with no file: a key no
// text item can be written for (a reserved name, one outside the specification, a cover item's:
// [textName]) is Dropped, unless the file already holds the very values, and values ending in an
// empty one are Lossy. Report == write: the predicates are the writer's.
func NewTransferClassifier(items []Item) core.FieldClassifier {
	held := sync.OnceValue(func() map[tag.Key]heldKey {
		names := heldNames(items)
		m := make(map[tag.Key]heldKey, len(names))
		for _, it := range items {
			if it.NonText() {
				continue
			}
			if key, ok := mapping.CanonicalAPE(it.Key); ok {
				m[key] = heldKey{names[key].name, append(m[key].vals, splitItemValues(it.Value)...)}
			}
		}
		return m
	})
	return func(key tag.Key, vals []string, _ tag.TagSet) (core.Disposition, string, bool) {
		return classify(held()[key], key, vals)
	}
}

// heldKey is a key as the destination's tag holds it: the name a rewrite of the key keeps
// ([heldNames]) and the values of every item that reads as it.
type heldKey struct {
	name string
	vals []string
}

// classify grades vals for key against what the destination holds of it.
func classify(held heldKey, key tag.Key, vals []string) (core.Disposition, string, bool) {
	if _, why, reason := textName(key, held.name); why != nameWritable {
		if held.name != "" && slices.Equal(held.vals, vals) {
			return core.Carried, "", false // the file holds these values, so nothing is written
		}
		switch why {
		case nameReserved:
			reason = "the APEv2 specification reserves this item name, so it cannot be written"
		case nameCover:
			reason = "taggers type this cover art item name binary, so a text value cannot be written"
		}
		return core.Dropped, reason, true
	}
	if DropsTrailingEmpty(vals) {
		return core.Lossy, TrailingEmptyReason, true
	}
	return core.Carried, "", false
}
