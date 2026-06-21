// Package core holds the value types shared between the public waxlabel
// package and the internal codecs, plus the Codec contract itself. Splitting
// these out lets codecs stay internal (per the plan, until v1.0) while the
// root package re-exports the types as its public API - without an import
// cycle.
package core

// Format identifies a container/codec combination. The set is closed in v1:
// there is no public registry, because a mutable global conflicts with the
// uint8 representation and invites ordering and collision bugs. New formats
// are added here deliberately.
type Format uint8

const (
	// FormatUnknown is the zero value: not yet identified.
	FormatUnknown Format = iota
	// FormatFLAC was the first writable format (M0).
	FormatFLAC
	// FormatOggVorbis and FormatOggOpus are read/write (build sequence 3).
	FormatOggVorbis
	FormatOggOpus
	FormatMP3
	FormatWAV
	FormatMP4 // .m4a / .alac / AAC-in-MP4
	FormatAAC // raw ADTS
	FormatMatroska
	FormatAIFF
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
	case FormatFLAC, FormatOggVorbis, FormatOggOpus, FormatMP3, FormatWAV, FormatMP4, FormatAAC, FormatMatroska, FormatAIFF:
		return true
	}
	return false
}

// Writable reports whether this version can write the format back. Matroska is
// tag-writable (tags, segment title, attachments) and chapter-writable.
func (f Format) Writable() bool {
	switch f {
	case FormatFLAC, FormatOggVorbis, FormatOggOpus, FormatMP3, FormatWAV, FormatMP4, FormatAAC, FormatMatroska, FormatAIFF:
		return true
	}
	return false
}
