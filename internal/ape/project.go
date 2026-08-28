package ape

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
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
			// APE stores multiple values for one key as NUL-separated runs inside a single
			// item, so the split is the multi-value decode, not a heuristic.
			for v := range strings.SplitSeq(it.Value, "\x00") {
				if v != "" {
					contribs = append(contribs, core.Contribution{Key: key, Value: v, Source: it.Key})
				}
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
			ws = core.Warn(ws, core.WarnInheritedEncoder, "inherited encoder item: "+it.Value)
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
	return core.NewCapabilities(f, readOnly, fields, pictures, core.Capability{}, core.AccessNone, nil)
}
