package testfile

import (
	"encoding/binary"
	"slices"
)

const (
	idEBML        = 0x1A45DFA3
	idDocType     = 0x4282
	IDSegment     = 0x18538067
	IDInfo        = 0x1549A966
	IDDuration    = 0x4489
	IDSegTitle    = 0x7BA9
	IDTracks      = 0x1654AE6B
	IDTrackEntry  = 0xAE
	IDTrackType   = 0x83
	IDCodecID     = 0x86
	IDCluster     = 0x1F43B675
	IDTimestamp   = 0xE7
	IDSimpleBlock = 0xA3
	IDCues        = 0x1C53BB6B
	IDCuePoint    = 0xBB
	idCueTime     = 0xB3
	IDCueTrackPos = 0xB7
	idCueTrack    = 0xF7
	IDTags        = 0x1254C367
	IDTag         = 0x7373
	IDTargets     = 0x63C0
	IDTgtTypeVal  = 0x68CA
	IDTagTrackUID = 0x63C5
	IDSimpleTag   = 0x67C8
	IDTagName     = 0x45A3
	IDTagString   = 0x4487
)

// IDBytes is an element ID's bytes; the ID carries its own length marker.
func IDBytes(id uint64) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(id))
	for len(b) > 1 && b[0] == 0 {
		b = b[1:]
	}
	return b
}

// El is an EBML element with a full-width (8-byte) size, valid whatever the payload's length.
func El(id uint64, payload ...[]byte) []byte {
	p := slices.Concat(payload...)
	return slices.Concat(IDBytes(id), fullSize(len(p)), p)
}

// Uint is an unsigned-integer element with an 8-byte value.
func Uint(id, v uint64) []byte { return El(id, binary.BigEndian.AppendUint64(nil, v)) }

func SimpleTag(name, value string) []byte {
	return El(IDSimpleTag, El(IDTagName, []byte(name)), El(IDTagString, []byte(value)))
}

// TagGroup is a Tag group at album level, scoped to a track when trackUID is not zero.
func TagGroup(trackUID uint64, simples ...[]byte) []byte {
	targets := Uint(IDTgtTypeVal, 50)
	if trackUID != 0 {
		targets = slices.Concat(targets, Uint(IDTagTrackUID, trackUID))
	}
	return El(IDTag, El(IDTargets, targets), slices.Concat(simples...))
}

func EBMLHeader(docType string) []byte { return El(idEBML, El(idDocType, []byte(docType))) }

// AudioCluster is a Cluster at timestamp ts holding one frame of track 1. Without one a file has
// no audio, and the editor refuses to write it.
func AudioCluster(ts uint64) []byte {
	return El(IDCluster, Uint(IDTimestamp, ts), El(IDSimpleBlock, []byte{0x81, 0, 0, 0, 0xAA, 0xBB}))
}

// Matroska is a docType file: an EBML header and a Segment holding an Info with title (none when
// title is empty), AudioCluster(0) and elements.
func Matroska(docType, title string, elements ...[]byte) []byte {
	var info []byte
	if title != "" {
		info = El(IDInfo, El(IDSegTitle, []byte(title)))
	}
	return slices.Concat(EBMLHeader(docType), El(IDSegment, info, AudioCluster(0), slices.Concat(elements...)))
}

// MultiCluster is a three-cluster Matroska whose Cues point at each cluster through a 2-byte
// position, before the clusters when frontCues is set. minimalSegment gives the Segment the
// shortest size VINT, so an edit that grows it widens the VINT.
func MultiCluster(frontCues, minimalSegment bool) []byte {
	info := El(IDInfo, El(IDSegTitle, []byte("x")))
	tracks := El(IDTracks, El(IDTrackEntry, Uint(IDTrackType, 2), El(IDCodecID, []byte("A_PCM/INT/LIT"))))
	clusters := [][]byte{AudioCluster(0), AudioCluster(1), AudioCluster(2)}
	cues := func(first int) []byte {
		var points []byte
		for i, off := 0, first; i < len(clusters); off, i = off+len(clusters[i]), i+1 {
			pos := []byte{0xF1, 0x82, byte(off >> 8), byte(off)} // CueClusterPosition, 2-byte value
			points = append(points, El(IDCuePoint, Uint(idCueTime, uint64(i)),
				El(IDCueTrackPos, Uint(idCueTrack, 1), pos))...)
		}
		return El(IDCues, points)
	}
	pre := len(info) + len(tracks)
	body := slices.Concat(info, tracks, slices.Concat(clusters...), cues(pre))
	if frontCues {
		body = slices.Concat(info, tracks, cues(pre+len(cues(0))), slices.Concat(clusters...))
	}
	segment := El(IDSegment, body)
	if minimalSegment {
		segment = MinWidthSegment(body)
	}
	return slices.Concat(EBMLHeader("matroska"), segment)
}

// MinWidthSegment is a Segment whose size VINT has the shortest valid width.
func MinWidthSegment(body []byte) []byte {
	return slices.Concat(IDBytes(IDSegment), minWidthSize(len(body)), body)
}

func fullSize(n int) []byte {
	b := binary.BigEndian.AppendUint64(nil, uint64(n))
	b[0] = 0x01
	return b
}

func minWidthSize(n int) []byte {
	v := uint64(n)
	for w := 1; w < 8; w++ {
		if v < uint64(1)<<(7*w)-1 {
			b := make([]byte, w)
			for i := w - 1; i >= 0; i-- {
				b[i] = byte(v)
				v >>= 8
			}
			b[0] |= 0x80 >> (w - 1)
			return b
		}
	}
	return fullSize(n)
}
