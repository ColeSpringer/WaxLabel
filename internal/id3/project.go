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

// Project decodes an ID3v2 tag into the canonical model. A nil tag projects to the
// empty Projection: a write that drops the front ID3v2 entirely (an edit clearing every
// frame, or a --legacy strip on a tagless file) passes nil here for the result document,
// which must equal a fresh parse of the now-tagless output.
func Project(t *Tag) Projection {
	if t == nil {
		return Projection{}
	}
	var contribs []core.Contribution
	var pics []core.Picture
	var dp dateParts
	numeric := false

	emit := func(key tag.Key, val, src string) {
		contribs = append(contribs, core.Contribution{Key: key, Value: val, Source: src})
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
		Tags:         core.BuildTagSet(contribs),
		Pictures:     pics,
		Families:     core.BuildFamilies(contribs, core.FamilyID3v2),
		NumericGenre: numeric,
	}
}

// emitNumTotal splits "n/total" text values into a number key and a total key,
// via the shared [tag.SplitNumberTotal] so the substring split cannot drift from the
// edit-time pair normalization.
func emitNumTotal(emit func(tag.Key, string, string), vals []string, numKey, totKey tag.Key, src string) {
	for _, v := range vals {
		num, total := tag.SplitNumberTotal(v)
		if num != "" {
			emit(numKey, num, src)
		}
		if total != "" {
			emit(totKey, total, src)
		}
	}
}
