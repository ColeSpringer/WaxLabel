package mapping

import "github.com/colespringer/waxlabel/tag"

// RIFF LIST/INFO <-> canonical mapping for WAV. Fixed four-char identifiers; unmapped ids
// stay native, not projected. Table follows ffmpeg ff_riff_info_conv. ISFT is ENCODER
// (ffprobe encoder=); still filtered for inherited-encoder noise in internal/wav/info.go.

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
	"ITRK": tag.TrackNumber, // ffmpeg reads ITRK too
	"ISFT": tag.Encoder,
	"ITCH": tag.EncodedBy, // ffmpeg encoded_by
	"IENG": tag.Engineer,
}

// riffKeyInfo is the inverse of riffInfoKeys, built at init.
var riffKeyInfo = map[tag.Key]string{}

func init() {
	for id, k := range riffInfoKeys {
		riffKeyInfo[k] = id
	}
	// IPRT and ITRK both read as TrackNumber; write IPRT (ffmpeg default, deterministic).
	riffKeyInfo[tag.TrackNumber] = "IPRT"
}

// RIFFInfoKey returns the canonical key for an INFO identifier.
func RIFFInfoKey(id string) (tag.Key, bool) {
	k, ok := riffInfoKeys[id]
	return k, ok
}

// RIFFKeyInfo returns the INFO identifier a canonical key writes to, if any.
func RIFFKeyInfo(key tag.Key) (string, bool) {
	id, ok := riffKeyInfo[key]
	return id, ok
}
