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

// Projection is the canonical view of an APE tag: the tag set, the decoded cover
// art, the family/source entries, and any read warnings. It mirrors
// [id3.Projection] so the codecs that own an APE tag assemble their Media the same
// way the ID3-bearing ones do.
//
// There are no chapter or synced-lyrics members because APE has no convention for
// either; both report AccessNone in the capability model rather than being invented
// here.
type Projection struct {
	Tags     tag.TagSet
	Pictures []core.Picture
	Families []core.FamilyValue
	Warnings []core.Warning
}

// Project decodes an APE tag into the canonical model. A nil tag projects to an
// empty tag set, which is what a codec passes for a file with no APE tag and for
// the result of an edit that cleared every item.
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
					// A malformed cover is warned and skipped, never silently dropped; its
					// item survives in the native list and is re-emitted verbatim.
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
	// APE stores a slashed "3/12" track just as the text codecs do, so the same shared
	// normalization runs here; without it a slashed pair would read back differently on
	// WavPack than on FLAC for one file.
	tag.NormalizeNumberPairs(&ts)
	return Projection{
		Tags:     ts,
		Pictures: pics,
		Families: core.BuildFamilies(contribs, core.FamilyAPEv2),
		Warnings: warnings,
	}
}

// InvalidKeyWarnings flags text items whose name cannot be represented as a canonical key,
// so they are preserved in the item list but do not reach the tag set. APEv2 item names run
// the full printable-ASCII range, while the canonical vocabulary stops at 0x7D (the floor the
// Vorbis comment specification sets), so an item named MOOD~X is legal on disk and
// unprojectable here. Without this the value would simply be missing from dump and lint, and
// a copy would report a clean lossless carry while leaving it behind.
//
// It mirrors [Project]'s own drop decision, so the set flagged is exactly the set omitted.
// Cover items and other binary items are not keys and are skipped; a reserved name is a write
// rule, not a read one, so an item that already carries one still projects.
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

// InvalidUTF8Warnings flags text items whose bytes are not valid UTF-8. APEv2
// requires UTF-8, and APEv1 predates that requirement, so such an item is read
// leniently (as Latin-1) rather than dropped - but a file carrying one is worth
// reporting, since its values are a guess and the bytes another reader sees may
// differ.
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

// EncoderNoise flags an inherited transcoder stamp in an APE item ("Encoder" or
// "Tool Name" carrying an ffmpeg "Lavf..." string), the signature of an acquired
// file. It is the APE analog of the Vorbis and ID3 checks, so the three codecs that
// own an APE tag report it identically.
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

// Capabilities describes what an APE tag can hold, for the codecs that use it as
// their native store. It is defined once here rather than per codec so WavPack,
// Monkey's Audio, and Musepack cannot drift.
//
// APE is free-form UTF-8 key/value with NUL-separated multi-values, so generic
// fields are fully and losslessly writable. Cover art is the "Cover Art (Front)"
// binary-item convention, which stores the image and a file name but no MIME type,
// geometry, or description - so it is graded lossy in the metadata it drops.
// Chapters, synced lyrics, and padding have no APE convention at all and report
// AccessNone; inventing one would produce a file nothing else reads.
//
// perField is nil: APE has no length, type, or version limits to express, and
// --numeric-genre has no effect here because APE genre is free text with no
// genre-index convention.
func Capabilities(f core.Format, readOnly bool) core.Capabilities {
	fields := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "APEv2 item", Fidelity: "lossless",
	}
	pictures := core.Capability{
		Read: core.AccessFull, Write: core.AccessFull,
		Representation: "APEv2 Cover Art item",
		Fidelity:       "image stored; description dropped",
		Constraints:    []string{"the Cover Art convention names only front and back covers; any other role is written as a front cover, and descriptions have nowhere to go"},
		PictureLoss:    core.PictureLossRoleAndDescription,
	}
	caps := core.NewCapabilities(f, readOnly, fields, pictures, core.Capability{}, core.AccessNone, nil).
		WithFieldClassifier(TransferClassifier)
	if readOnly {
		// Carry the reason, not just the flag, the way asf and mp4 do: a caller that declines
		// before reaching Plan (the transfer path) then returns the codec's own refusal rather
		// than a generic one. No APEv2-backed container reports read-only today, so this exists
		// so the first that does is not the one to discover the gap.
		caps = caps.WithReadOnlyReason(fmt.Errorf("%w: this %s file cannot be written", waxerr.ErrUnsupportedFormat, f))
	}
	return caps
}

// TransferClassifier grades the one field shape whose APE transfer fate the format-level
// capability cannot express: a key whose item name the specification reserves
// ([ReservedItemName]). The writer drops such a key rather than plant ID3/Ogg/Musepack magic
// inside the tag, so a copy that carried it - reporting a clean carry for a value that then
// vanishes - would break the report-equals-write invariant [core.ProjectTransfer] exists to
// hold. It reuses the writer's own predicate, so the copy report and the write drop cannot
// drift. Every other key is left to the format-level grade.
//
// It is attached here rather than per codec so all four APEv2-backed containers (WavPack,
// Monkey's Audio, Musepack, and any future one) get it from the shared Capabilities, the
// same way they share the writer.
func TransferClassifier(key tag.Key, _ []string, _ tag.TagSet) (core.Disposition, string, bool) {
	if ReservedItemName(mapping.APEName(key)) {
		return core.Dropped, "the APEv2 specification reserves this item name, so it cannot be written", true
	}
	return core.Carried, "", false
}
