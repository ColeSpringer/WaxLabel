package id3

import (
	"strings"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
)

// musicBrainzOwner is the UFID owner identifier under which the MusicBrainz
// recording ID is stored.
const musicBrainzOwner = "http://musicbrainz.org"

// Projection is the canonical view decoded from an ID3v2 tag: the authoritative
// tag set, the embedded pictures, the per-frame family/source entries, and
// whether a numeric genre reference was resolved (so the caller can warn).
type Projection struct {
	Tags         tag.TagSet
	Pictures     []core.Picture
	Families     []core.FamilyValue
	NumericGenre bool
}

// contribution is one canonical value decoded from one frame, tagged with the
// frame identifier it came from so conflicts between distinct frames surface.
type contribution struct {
	key tag.Key
	val string
	src string
}

// EncoderNoise reports inherited-encoder warnings for the tag's TSSE/TENC frames
// (ffmpeg writes "Lavf..." there), the signature of a transcoded/acquired file.
// It is shared by the container codecs that embed ID3v2 (MP3 and WAV) so the
// check lives in one place. A nil tag yields no warnings.
func EncoderNoise(t *Tag) []core.Warning {
	if t == nil {
		return nil
	}
	var ws []core.Warning
	for _, f := range t.Frames() {
		if f.ID != "TSSE" && f.ID != "TENC" {
			continue
		}
		for _, v := range DecodeText(f) {
			if core.IsTranscoderStamp(v) {
				ws = core.Warn(ws, core.WarnInheritedEncoder, "inherited encoder stamp: "+v)
			}
		}
	}
	return ws
}

// Project decodes an ID3v2 tag into the canonical model.
func Project(t *Tag) Projection {
	var contribs []contribution
	var pics []core.Picture
	var dp dateParts
	numeric := false

	emit := func(key tag.Key, val, src string) {
		contribs = append(contribs, contribution{key, val, src})
	}

	for _, f := range t.frames {
		if f.Opaque {
			continue
		}
		switch {
		case f.ID == "APIC":
			if p, ok := decodeAPIC(f.Body); ok {
				pics = append(pics, p)
			}
		case f.ID == "TXXX":
			if desc, vals, ok := decodeUserText(f.Body); ok {
				if key, kok := mapping.ID3TXXXKey(desc); kok {
					src := "TXXX\x00" + strings.ToUpper(strings.TrimSpace(desc))
					for _, v := range vals {
						emit(key, v, src)
					}
				}
			}
		case f.ID == "UFID":
			if owner, id, ok := decodeUFID(f.Body); ok && owner == musicBrainzOwner {
				emit(tag.MBRecordingID, id, "UFID")
			}
		case f.ID == "COMM":
			if desc, vals, ok := decodeCommentFrame(f.Body); ok && desc == "" {
				for _, v := range vals {
					emit(tag.Comment, v, "COMM")
				}
			}
		case f.ID == "USLT":
			if desc, text, ok := decodeLangText(f.Body); ok && desc == "" {
				emit(tag.Lyrics, text, "USLT")
			}
		case f.ID == "TCON":
			for _, v := range decodeTextFrame(f.Body) {
				names, wasNum := resolveGenres(v)
				numeric = numeric || wasNum
				for _, name := range names {
					if name != "" {
						emit(tag.Genre, name, "TCON")
					}
				}
			}
		case f.ID == "TRCK":
			emitNumTotal(emit, decodeTextFrame(f.Body), tag.TrackNumber, tag.TrackTotal, "TRCK")
		case f.ID == "TPOS":
			emitNumTotal(emit, decodeTextFrame(f.Body), tag.DiscNumber, tag.DiscTotal, "TPOS")
		case isDateFrame(f.ID):
			dp.add(f.ID, decodeTextFrame(f.Body))
		case strings.HasPrefix(f.ID, "T"):
			key, ok := mapping.ID3FrameKey(f.ID)
			if !ok {
				k, err := tag.ParseKey(strings.TrimSpace(f.ID))
				if err != nil {
					continue
				}
				key = k
			}
			for _, v := range decodeTextFrame(f.Body) {
				emit(key, v, f.ID)
			}
		}
		// Other frames (W***, POPM, PRIV, RVA2, GEOB, ...) are preserved in the
		// native document but not canonically projected.
	}

	// Compose the date frames gathered above into canonical date keys.
	dp.emit(emit)

	return Projection{
		Tags:         buildTagSet(contribs),
		Pictures:     pics,
		Families:     buildFamilies(contribs),
		NumericGenre: numeric,
	}
}

// emitNumTotal splits "n/total" text values into a number key and a total key.
func emitNumTotal(emit func(tag.Key, string, string), vals []string, numKey, totKey tag.Key, src string) {
	for _, v := range vals {
		num, total, _ := strings.Cut(v, "/")
		num = strings.TrimSpace(num)
		total = strings.TrimSpace(total)
		if num != "" {
			emit(numKey, num, src)
		}
		if total != "" {
			emit(totKey, total, src)
		}
	}
}

// buildTagSet assembles the authoritative TagSet from contributions in order.
func buildTagSet(contribs []contribution) tag.TagSet {
	ts := tag.NewTagSet()
	for _, c := range contribs {
		ts.Add(c.key, c.val)
	}
	return ts
}

// buildFamilies groups contributions by key into family entries, marking an
// entry unselected when distinct frames supplied distinct values for one key
// (e.g. TYER and TDRC disagreeing on the recording date).
func buildFamilies(contribs []contribution) []core.FamilyValue {
	index := map[tag.Key]int{}
	srcs := map[tag.Key]map[string]bool{}
	var fams []core.FamilyValue
	for _, c := range contribs {
		if i, ok := index[c.key]; ok {
			fams[i].Values = append(fams[i].Values, c.val)
		} else {
			index[c.key] = len(fams)
			srcs[c.key] = map[string]bool{}
			fams = append(fams, core.FamilyValue{
				Key: c.key, Family: core.FamilyID3v2, Scope: core.ScopeTrack,
				Values: []string{c.val}, Selected: true,
			})
		}
		srcs[c.key][c.src] = true
	}
	for key, i := range index {
		if len(srcs[key]) > 1 && distinctValues(fams[i].Values) > 1 {
			fams[i].Selected = false
		}
	}
	return fams
}

// distinctValues counts case- and space-insensitive distinct values.
func distinctValues(vals []string) int {
	seen := map[string]bool{}
	for _, v := range vals {
		seen[strings.ToLower(strings.TrimSpace(v))] = true
	}
	return len(seen)
}
