package mapping

import "github.com/colespringer/waxlabel/tag"

// This file holds the AIFF native-text-chunk <-> canonical mapping shared by the
// aiff codec. AIFF (like RIFF) carries a small, fixed vocabulary of text chunks,
// each a top-level chunk holding a single run of characters: NAME (the sampled
// sound's name), AUTH (the author), "(c) " (the copyright notice), and ANNO (an
// annotation, which may appear more than once). They are far less expressive
// than ID3 or Vorbis comments, so only these established identifiers map to
// canonical keys; every other chunk (COMM, SSND, FVER, MARK, INST, APPL, ...) is
// preserved verbatim in the native document but not projected.
//
// This is the AIFF analogue of RIFF LIST/INFO: the chunks ffmpeg's AIFF muxer
// writes by default (NAME for the title, ANNO for the comment) and reads back,
// hence the realistic acquired-AIFF case and the differential anchor. The richer
// MusicBrainz/Picard long tail and cover art live in the embedded "ID3 " chunk
// (decoded by internal/id3), exactly as for WAV.
//
// ANNO maps to Comment and is the one multi-valued slot: several ANNO chunks
// become several Comment values, and several Comment values write back as
// several ANNO chunks. NAME/AUTH/"(c) " are single-valued.

// aiffTextKeys maps a native AIFF text-chunk identifier to its canonical key.
var aiffTextKeys = map[string]tag.Key{
	"NAME": tag.Title,
	"AUTH": tag.Artist,
	"(c) ": tag.Copyright,
	"ANNO": tag.Comment,
}

// aiffKeyText is the inverse of aiffTextKeys, built at init.
var aiffKeyText = map[tag.Key]string{}

func init() {
	for id, k := range aiffTextKeys {
		aiffKeyText[k] = id
	}
}

// AIFFTextKey returns the canonical key for a native text-chunk identifier and
// whether it is one of the mapped identifiers. Unmapped identifiers are
// preserved natively but not projected.
func AIFFTextKey(id string) (tag.Key, bool) {
	k, ok := aiffTextKeys[id]
	return k, ok
}

// AIFFKeyText returns the native text-chunk identifier a canonical key writes
// to, and whether one exists. Keys without a native identifier can only be
// stored in the richer embedded "ID3 " chunk.
func AIFFKeyText(key tag.Key) (string, bool) {
	id, ok := aiffKeyText[key]
	return id, ok
}
