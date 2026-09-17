// Package core holds shared value types and the Codec contract for waxlabel and internal codecs.
package core

// Format identifies a container/codec. Closed set (uint8); add new formats here.
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

// DefaultID3Version is the ID3v2 minor version for new tags. MP3: 3; others: 4.
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

// Writable reports whether WaxLabel can write the format. Enforcement is in codecs.
func (f Format) Writable() bool {
	switch f {
	case FormatFLAC, FormatOggVorbis, FormatOggOpus, FormatOggFLAC, FormatWavPack, FormatMonkeysAudio, FormatMusepack, FormatMP3, FormatWAV, FormatMP4, FormatAAC, FormatMatroska, FormatAIFF:
		return true
	}
	return false
}
