// Package core holds the value types shared between the public waxlabel
// package and the internal codecs, plus the Codec contract itself. Keeping
// these definitions here lets the root package re-export public types while
// codecs remain internal, without introducing an import cycle.
package core

// Format identifies a container/codec combination. The set is closed: there is
// no public registry, because a mutable global conflicts with the uint8
// representation and invites ordering and collision bugs. New formats are added
// here deliberately.
type Format uint8

const (
	// FormatUnknown is the zero value: not yet identified.
	FormatUnknown Format = iota
	// FormatFLAC identifies FLAC.
	FormatFLAC
	// FormatOggVorbis, FormatOggOpus, and FormatOggFLAC identify Ogg-hosted
	// Vorbis, Opus, and FLAC streams.
	FormatOggVorbis
	FormatOggOpus
	FormatMP3
	FormatWAV
	FormatMP4 // .m4a / .alac / AAC-in-MP4
	FormatAAC // raw ADTS
	FormatMatroska
	FormatAIFF
	FormatOggFLAC
	FormatWavPack
	FormatMonkeysAudio
	FormatWMA
	FormatMusepack
)

func (f Format) String() string {
	switch f {
	case FormatFLAC:
		return "FLAC"
	case FormatOggVorbis:
		return "Ogg Vorbis"
	case FormatOggOpus:
		return "Ogg Opus"
	case FormatMP3:
		return "MP3"
	case FormatWAV:
		return "WAV"
	case FormatMP4:
		return "MP4"
	case FormatAAC:
		return "AAC (ADTS)"
	case FormatMatroska:
		return "Matroska"
	case FormatAIFF:
		return "AIFF"
	case FormatOggFLAC:
		return "Ogg FLAC"
	case FormatWavPack:
		return "WavPack"
	case FormatMonkeysAudio:
		return "Monkey's Audio"
	case FormatWMA:
		return "WMA"
	case FormatMusepack:
		return "Musepack"
	default:
		return "unknown"
	}
}

// DefaultID3Version returns the ID3v2 minor version a freshly created id3 tag uses
// for format f - the from-scratch default only, since a file with an existing id3
// tag keeps that tag's own version. MP3 defaults to v2.3: id3 is its primary tag and
// a real ecosystem of legacy hardware players reads it directly and handles v2.3 most
// reliably. Every other id3-bearing format - raw AAC, and the id3 chunk embedded in
// WAV/AIFF - is read only by modern software, so it defaults to v2.4 (UTF-8, lossless
// multi-value). This is the single place the per-format choice lives, so the codecs
// cannot drift; NewEmpty clamps any other value to a valid one.
func DefaultID3Version(f Format) byte {
	if f == FormatMP3 {
		return 3
	}
	return 4
}

// Implemented reports whether this version can parse the format at all.
func (f Format) Implemented() bool {
	switch f {
	case FormatFLAC, FormatOggVorbis, FormatOggOpus, FormatOggFLAC, FormatWavPack, FormatMonkeysAudio, FormatWMA, FormatMusepack, FormatMP3, FormatWAV, FormatMP4, FormatAAC, FormatMatroska, FormatAIFF:
		return true
	}
	return false
}

// Writable reports whether this version can write the format back. Matroska is
// tag-writable (tags, segment title, attachments) and chapter-writable. WMA is
// read-only - WaxLabel never writes an ASF file - though nothing depends on this
// method to enforce it: the refusal lives in the codec, where the reported
// capability and the actual write outcome come from one predicate.
func (f Format) Writable() bool {
	switch f {
	case FormatFLAC, FormatOggVorbis, FormatOggOpus, FormatOggFLAC, FormatWavPack, FormatMonkeysAudio, FormatMusepack, FormatMP3, FormatWAV, FormatMP4, FormatAAC, FormatMatroska, FormatAIFF:
		return true
	}
	return false
}
