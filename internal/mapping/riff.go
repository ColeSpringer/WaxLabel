package mapping

import "github.com/colespringer/waxlabel/tag"

// This file holds the RIFF LIST/INFO <-> canonical mapping shared by the wav
// codec. RIFF INFO is a small, fixed vocabulary of four-character chunk
// identifiers, each holding a single NUL-terminated string - far less
// expressive than ID3 or Vorbis comments. Only the well-established identifiers
// map to canonical keys; anything else (IENG, ILNG, ISBJ, IKEY, ...) is
// preserved verbatim in the native document but not projected, since inventing
// a canonical key from an arbitrary 4CC would be both ugly and lossy on
// round-trip.
//
// The mapped set mirrors ffmpeg's ff_riff_info_conv so files written by the
// ffmpeg family (the realistic acquired-WAV case) read correctly and our output
// reads back in ffprobe. ISFT is the software stamp ffprobe reports as
// "encoder=", so it is ENCODER's INFO home on both sides: reading it means dump
// agrees with ffprobe, and writing it means a canonical ENCODER edit no longer
// spawns an id3 chunk to hold a value INFO has a slot for. It is still scanned
// for inherited-encoder noise (internal/wav/info.go), which is a judgement about
// the value, not about where it lives.

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
	"ITRK": tag.TrackNumber, // ffmpeg also reads track numbers from ITRK
	"ISFT": tag.Encoder,
}

// riffKeyInfo is the inverse of riffInfoKeys, built at init.
var riffKeyInfo = map[tag.Key]string{}

func init() {
	for id, k := range riffInfoKeys {
		riffKeyInfo[k] = id
	}
	// IPRT and ITRK both read as TrackNumber, so the inverse loop above would choose
	// one nondeterministically from map iteration order. Write IPRT, matching ffmpeg's
	// common choice and keeping output deterministic.
	riffKeyInfo[tag.TrackNumber] = "IPRT"
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
