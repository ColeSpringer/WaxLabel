package mapping

import "github.com/colespringer/waxlabel/tag"

// This file holds the RIFF LIST/INFO <-> canonical mapping shared by the wav
// codec. RIFF INFO is a small, fixed vocabulary of four-character chunk
// identifiers, each holding a single NUL-terminated string — far less
// expressive than ID3 or Vorbis comments. Only the well-established identifiers
// map to canonical keys; anything else (IENG, ILNG, ISBJ, IKEY, the ISFT
// software stamp, ...) is preserved verbatim in the native document but not
// projected, since inventing a canonical key from an arbitrary 4CC would be
// both ugly and lossy on round-trip.
//
// The mapped set mirrors ffmpeg's ff_riff_info_conv so files written by the
// ffmpeg family (the realistic acquired-WAV case) read correctly and our output
// reads back in ffprobe. ISFT is deliberately left out: it is the encoder
// stamp (the WAV analogue of an "encoder=Lavf" comment), so it is preserved and
// scanned for inherited-encoder noise rather than surfaced as a tag.

// riffInfoKeys maps a four-character INFO identifier to its canonical key.
var riffInfoKeys = map[string]tag.Key{
	"INAM": tag.Title,
	"IART": tag.Artist,
	"IPRD": tag.Album,
	"ICRD": tag.RecordingDate,
	"IGNR": tag.Genre,
	"ICMT": tag.Comment,
	"ICOP": tag.Copyright,
	"IPRT": tag.TrackNumber,
}

// riffKeyInfo is the inverse of riffInfoKeys, built at init.
var riffKeyInfo = map[tag.Key]string{}

func init() {
	for id, k := range riffInfoKeys {
		riffKeyInfo[k] = id
	}
}

// RIFFInfoKey returns the canonical key for an INFO identifier and whether it is
// one of the mapped identifiers. Unmapped identifiers are preserved natively but
// not projected.
func RIFFInfoKey(id string) (tag.Key, bool) {
	k, ok := riffInfoKeys[id]
	return k, ok
}

// RIFFKeyInfo returns the INFO identifier a canonical key writes to, and whether
// one exists. Keys without an INFO identifier can only be stored in the richer
// embedded id3 chunk.
func RIFFKeyInfo(key tag.Key) (string, bool) {
	id, ok := riffKeyInfo[key]
	return id, ok
}
