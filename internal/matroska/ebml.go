package matroska

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// Matroska is an EBML document: a tree of elements, each [Element ID (VINT)][Element
// Data Size (VINT)][data]. A VINT's first byte holds a length descriptor: the position
// of its most significant set bit gives the total byte length (0x80 => 1 byte, 0x40 =>
// 2 bytes, ...).

// Element IDs this codec reads, including the structural elements the write path
// preserves: SeekHead/Seek/SeekPosition, Cues/CueClusterPosition, Void, and CRC-32.
const (
	idEBML            = 0x1A45DFA3
	idDocType         = 0x4282
	idSegment         = 0x18538067
	idSeekHead        = 0x114D9B74
	idSeek            = 0x4DBB
	idSeekID          = 0x53AB
	idSeekPosition    = 0x53AC
	idInfo            = 0x1549A966
	idTimestampScl    = 0x2AD7B1
	idDuration        = 0x4489
	idSegTitle        = 0x7BA9
	idTracks          = 0x1654AE6B
	idTrackEntry      = 0xAE
	idTrackNumber     = 0xD7
	idTrackUID        = 0x73C5
	idTrackType       = 0x83
	idCodecID         = 0x86
	idAudio           = 0xE1
	idSampFreq        = 0xB5
	idOutSampFreq     = 0x78B5
	idChannels        = 0x9F
	idBitDepth        = 0x6264
	idCodecPrivate    = 0x63A2
	idTags            = 0x1254C367
	idTag             = 0x7373
	idTargets         = 0x63C0
	idTgtTypeVal      = 0x68CA
	idTgtType         = 0x63CA
	idTagTrackUID     = 0x63C5
	idTagEditUID      = 0x63C9
	idTagChapUID      = 0x63C4
	idTagAttachUID    = 0x63C6
	idSimpleTag       = 0x67C8
	idTagName         = 0x45A3
	idTagString       = 0x4487
	idTagBinary       = 0x4485
	idTagLang         = 0x447A
	idTagLangBCP47    = 0x447B
	idTagDefault      = 0x4484
	idAttachments     = 0x1941A469
	idAttached        = 0x61A7
	idFileName        = 0x466E
	idFileMime        = 0x4660
	idFileData        = 0x465C
	idFileDesc        = 0x467E
	idFileUID         = 0x46AE
	idCluster         = 0x1F43B675
	idCues            = 0x1C53BB6B
	idCuePoint        = 0xBB
	idCueTrackPos     = 0xB7
	idCueClusterPos   = 0xF1
	idChapters        = 0x1043A770
	idEditionEntry    = 0x45B9
	idEditionUID      = 0x45BC
	idEditionFlagDf   = 0x45DB
	idChapterAtom     = 0xB6
	idChapterUID      = 0x73C4
	idChapTimeStart   = 0x91
	idChapTimeEnd     = 0x92
	idChapFlagHidden  = 0x98
	idChapFlagEnabled = 0x4598
	idChapDisplay     = 0x80
	idChapString      = 0x85
	idChapLang        = 0x437C
	idChapLangIETF    = 0x437D
	idVoid            = 0xEC
	idCRC32           = 0xBF
)

// level1IDs are the Segment's direct children: the elements an unknown-size element
// (in practice a streamed Cluster) ends at, per the EBML rule that an unknown-size
// element runs until the next equal-or-higher-level element.
var level1IDs = map[uint64]bool{
	idSeekHead: true, idInfo: true, idTracks: true, idTags: true,
	idAttachments: true, idCluster: true, idCues: true, idChapters: true,
}

// maxIDLen is the longest element ID, in bytes (EBMLMaxIDLength).
const maxIDLen = 4

// trackTypeAudio is the TrackType value for an audio stream.
const trackTypeAudio = 2

// maxElement caps how many bytes of a single leaf element this codec reads into memory
// (tag strings, attachment metadata, cover art).
const maxElement = 64 << 20

// vintLen returns the byte length a VINT occupies given its first byte, or 0 if
// the byte has no length-descriptor bit set (invalid).
func vintLen(first byte) int {
	for i := range 8 {
		if first&(0x80>>i) != 0 {
			return i + 1
		}
	}
	return 0
}

// readVINT reads a variable-length integer in [off, end). ok is false for one that is not there or
// does not decode, an element ID (keepMarker) of more than four bytes among them: Matroska has
// none, and no other reader takes such bytes for an element. A read that fails fatally
// ([bits.Fatal]) is the error.
func readVINT(src core.ReaderAtSized, off, end, limit int64, keepMarker bool) (val uint64, n int64, unknown, ok bool, err error) {
	if off < 0 || off >= end {
		return 0, 0, false, false, nil
	}
	// One read of up to 8 bytes (a VINT's max length) instead of a first-byte read
	// followed by a second for the rest.
	want := min(end-off, 8)
	buf, err := bits.ReadSlice(src, off, want, limit)
	if err != nil {
		return 0, 0, false, false, bits.Fatal(err)
	}
	length := vintLen(buf[0])
	if length == 0 || int64(length) > want || keepMarker && length > maxIDLen {
		return 0, 0, false, false, nil
	}
	buf = buf[:length]
	if keepMarker {
		for _, c := range buf {
			val = val<<8 | uint64(c)
		}
		return val, int64(length), false, true, nil
	}
	mask := byte(0x80 >> (length - 1))
	val = uint64(buf[0] &^ mask)
	for i := 1; i < length; i++ {
		val = val<<8 | uint64(buf[i])
	}
	maxVal := (uint64(1) << uint(7*length)) - 1
	return val, int64(length), val == maxVal, true, nil
}

// element is one parsed EBML element header: its ID and the byte range of its data.
type element struct {
	id        uint64
	start     int64 // the element's own first byte (the ID)
	dataStart int64
	dataEnd   int64
	unknown   bool
	// clamped: the declared size runs past the region the element was read in, and dataEnd is
	// that region's end.
	clamped  bool
	declared uint64 // the declared data size
	next     int64
}

func (e element) dataLen() int64 { return e.dataEnd - e.dataStart }

// overruns reports whether the element does not end inside the region it was read in: its size
// runs past it or is unknown. Its data is then whatever follows, not a value.
func (e element) overruns() bool { return e.clamped || e.unknown }

// readElement parses the element header at off within [off, end). ok is false for a header that is
// not there or does not decode; a read that fails fatally ([bits.Fatal]) is the error.
func readElement(src core.ReaderAtSized, off, end, limit int64) (element, bool, error) {
	id, idn, _, ok, err := readVINT(src, off, end, limit, true)
	if !ok {
		return element{}, false, err
	}
	size, szn, unknown, ok, err := readVINT(src, off+idn, end, limit, false)
	if !ok {
		return element{}, false, err
	}
	dataStart := off + idn + szn
	if dataStart > end {
		return element{}, false, nil
	}
	dataEnd, clamped := end, false
	if !unknown {
		dataEnd = dataStart + int64(size)
		// Clamp a declared size that overruns the region (truncated/corrupt input)
		// so the range stays valid and this becomes the last element.
		if dataEnd > end || dataEnd < dataStart {
			dataEnd, clamped = end, true
		}
	}
	return element{id: id, start: off, dataStart: dataStart, dataEnd: dataEnd, unknown: unknown, clamped: clamped, declared: size, next: dataEnd}, true, nil
}

// resolveUnknownEnd returns the end of an unknown-size element whose data begins at
// from. walkSegment uses it so siblings after a streamed Cluster (trailing Tags or
// Attachments) are still visited. A header read that fails fatally is the error.
func resolveUnknownEnd(src core.ReaderAtSized, from, end int64, limit int64) (int64, error) {
	off := from
	for off < end {
		el, ok, err := readElement(src, off, end, limit)
		if err != nil {
			return 0, err
		}
		if !ok {
			return end, nil
		}
		if level1IDs[el.id] {
			return off, nil
		}
		if el.unknown || el.dataEnd <= off {
			return end, nil
		}
		off = el.dataEnd
	}
	return end, nil
}

// resync returns where the element that follows the bytes at off, which head none, starts: at
// the first level-1 ID past off, within [core.ResyncWindow] of it, that an element header
// follows. It is end where no such ID stands ahead of end, and -1 where the window ends first.
// In a Segment whose end is its own (not open: neither cut short nor of unknown size) an element
// that would run past it is no candidate.
func resync(src core.ReaderAtSized, off, end, limit int64, open bool) (int64, error) {
	window, err := bits.ReadSlice(src, off, min(end-off, core.ResyncWindow+maxIDLen), limit)
	if err != nil {
		return -1, bits.Fatal(err)
	}
	for i := 1; i+maxIDLen <= len(window); i++ {
		if !level1IDs[uint64(binary.BigEndian.Uint32(window[i:]))] {
			continue
		}
		el, ok, err := readElement(src, off+int64(i), end, limit)
		if err != nil {
			return -1, err
		}
		if ok && (open || !el.clamped) {
			return off + int64(i), nil
		}
	}
	if int64(len(window)) < end-off {
		return -1, nil
	}
	return end, nil
}

// intVal converts an EBML unsigned integer to an int, returning 0 for a value too large
// to be a sane count or geometry.
func intVal(v uint64) int {
	if v > math.MaxInt32 {
		return 0
	}
	return int(v)
}

// eachChild calls fn for each element in [start, end). It stops without error on a
// header it cannot parse; a header read that fails fatally ([bits.Fatal]) is the error. depth
// bounds nesting.
func eachChild(src core.ReaderAtSized, start, end int64, depth *bits.Depth, limit int64, fn func(element) error) error {
	_, err := eachChildReport(src, start, end, depth, limit, fn)
	return err
}

// eachChildReport is eachChild for a master a rewrite renders again from its children, which
// must know of bytes the walk left unread: it returns where the elements handed to fn end, end
// unless a header there does not parse. The last of them may overrun the master ([element.overruns]).
func eachChildReport(src core.ReaderAtSized, start, end int64, depth *bits.Depth, limit int64, fn func(element) error) (stoppedAt int64, err error) {
	if err := depth.Enter(); err != nil {
		return start, err
	}
	defer depth.Leave()
	off := start
	for off < end {
		el, ok, err := readElement(src, off, end, limit)
		if err != nil || !ok {
			return off, err
		}
		// Count every metadata element walked against the per-parse breadth budget.
		if err := depth.Count(); err != nil {
			return off, err
		}
		if err := fn(el); err != nil {
			return off, err
		}
		off = el.next
	}
	return end, nil
}

// walkSegment iterates the Segment's direct children like eachChildReport, but resolves an
// unknown-size element (a streamed Cluster) to its true end via resolveUnknownEnd, so
// later siblings are still visited and the element's extent is not overstated. Bytes that head
// no element are stepped over, as ffmpeg does: they are handed to fn as an element of id 0 that
// ends at the next level-1 element ([resync]) or, with none, at end. Where more than
// [core.ResyncWindow] of them stand ahead of anything, the walk stops there.
func walkSegment(src core.ReaderAtSized, start, end int64, depth *bits.Depth, limit int64, open bool, fn func(element) error) (stoppedAt int64, err error) {
	if err := depth.Enter(); err != nil {
		return start, err
	}
	defer depth.Leave()
	off := start
	for off < end {
		el, ok, err := readElement(src, off, end, limit)
		if err != nil {
			return off, err
		}
		if !ok {
			next, err := resync(src, off, end, limit, open)
			if err != nil || next < 0 {
				return off, err
			}
			el = element{start: off, dataStart: off, dataEnd: next}
		}
		if el.unknown {
			if el.dataEnd, err = resolveUnknownEnd(src, el.dataStart, end, limit); err != nil {
				return off, err
			}
		}
		if err := fn(el); err != nil {
			return off, err
		}
		off = el.dataEnd
	}
	return end, nil
}

// readUint reads an EBML unsigned integer (big-endian, 1-8 bytes), 0 for one of another width or
// one that overruns its parent; a read that fails fatally ([bits.Fatal]) is the error.
func readUint(src core.ReaderAtSized, el element, limit int64) (uint64, error) {
	n := el.dataLen()
	if el.overruns() || n <= 0 || n > 8 {
		return 0, nil
	}
	b, err := bits.ReadSlice(src, el.dataStart, n, limit)
	if err != nil {
		return 0, bits.Fatal(err)
	}
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v, nil
}

// readFloat reads an EBML float (4 or 8 bytes). A 0-byte float is 0.0 per spec. ok is false for
// another width or one that overruns its parent; a read that fails fatally ([bits.Fatal]) is the
// error.
func readFloat(src core.ReaderAtSized, el element, limit int64) (float64, bool, error) {
	n := el.dataLen()
	if el.overruns() {
		return 0, false, nil
	}
	switch n {
	case 0:
		return 0, true, nil
	case 4:
		b, err := bits.ReadSlice(src, el.dataStart, 4, limit)
		if err != nil {
			return 0, false, bits.Fatal(err)
		}
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b))), true, nil
	case 8:
		b, err := bits.ReadSlice(src, el.dataStart, 8, limit)
		if err != nil {
			return 0, false, bits.Fatal(err)
		}
		return math.Float64frombits(binary.BigEndian.Uint64(b)), true, nil
	default:
		return 0, false, nil
	}
}

// readString reads a UTF-8 string element, capped at maxElement, up to its first NUL. cut
// reports bytes behind that NUL other than the NUL padding some muxers add.
func readString(src core.ReaderAtSized, el element, limit int64) (s string, cut bool, err error) {
	b, err := readBytes(src, el, limit)
	if err != nil {
		return "", false, err
	}
	text, behind, _ := bytes.Cut(b, []byte{0})
	return string(text), len(bytes.TrimRight(behind, "\x00")) > 0, nil
}

// readLabel reads an informational string element (a DocType, CodecID, scope keyword or
// language): a read that fails fatally ([bits.Fatal]) is the error, and any other failure reads
// as "".
func readLabel(src core.ReaderAtSized, el element, limit int64) (string, error) {
	s, _, err := readString(src, el, limit)
	return s, bits.Fatal(err)
}

// maxAudioSpecificConfig bounds the CodecPrivate prefix given to the AAC config decoder,
// which reads at most a few dozen bits past its header; 32 bytes covers every byte it
// can consume.
const maxAudioSpecificConfig = 32

// readBytesPrefix reads at most max bytes of a leaf element's data, none of one that overruns its
// parent. Unlike [readBytes], a longer element is not an error, so a hostile length costs only a
// bounded read.
func readBytesPrefix(src core.ReaderAtSized, el element, max, limit int64) ([]byte, error) {
	n := min(el.dataLen(), max)
	if el.overruns() || n <= 0 {
		return nil, nil
	}
	return bits.ReadSlice(src, el.dataStart, n, limit)
}

// readBytes reads a leaf element's data, none of one that overruns its parent.
func readBytes(src core.ReaderAtSized, el element, limit int64) ([]byte, error) {
	n := el.dataLen()
	if el.overruns() || n <= 0 {
		return nil, nil
	}
	if n > maxElement {
		return nil, fmt.Errorf("%w: metadata element of %d bytes exceeds the %d-byte cap",
			waxerr.ErrSizeTooLarge, n, maxElement)
	}
	return bits.ReadSlice(src, el.dataStart, n, limit)
}
