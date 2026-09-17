package mapping

import "github.com/colespringer/waxlabel/tag"

// AIFF native text-chunk <-> canonical mapping. Fixed chunks: NAME, AUTH, "(c) ", ANNO.
// Unmapped chunks stay native. Rich metadata lives in the embedded "ID3 " chunk (like WAV).
// ANNO is multi-valued; NAME/AUTH/"(c) " are single-valued.

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

// AIFFTextKey returns the canonical key for a native text-chunk identifier.
func AIFFTextKey(id string) (tag.Key, bool) {
	k, ok := aiffTextKeys[id]
	return k, ok
}

// AIFFKeyText returns the native text-chunk identifier a canonical key writes to, if any.
func AIFFKeyText(key tag.Key) (string, bool) {
	id, ok := aiffKeyText[key]
	return id, ok
}
