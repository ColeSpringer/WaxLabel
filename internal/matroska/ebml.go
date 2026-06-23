package matroska

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// Matroska is an EBML document: a tree of elements, each
//
//	[Element ID (VINT)][Element Data Size (VINT)][data]
//
// A VINT's first byte holds a length descriptor - the position of its most
// significant set bit gives the total byte length (0x80 => 1 byte ... 0x01 => 8).
// For element IDs the descriptor bits are kept (the canonical ID form); for
// sizes and values they are stripped. A size whose value field is all ones is
// the "unknown size" form (a streamed element that runs until a higher-level
// element appears) - preserved verbatim on write rather than rewritten. The
// inverse encoders live in encode.go.
//
// Reimplemented from the EBML/Matroska specifications (RFC 8794 / RFC 9559).

// Element IDs this codec reads. The structural elements the write path must
// preserve byte-faithfully - SeekHead/Seek/SeekPosition, Cues/CueClusterPosition,
// Void, and CRC-32 - are enumerated here too. The cluster media payload is still
// never decoded, only its byte range recorded.
const (
	idEBML          = 0x1A45DFA3
	idDocType       = 0x4282
	idSegment       = 0x18538067
	idSeekHead      = 0x114D9B74
	idSeek          = 0x4DBB
	idSeekID        = 0x53AB
	idSeekPosition  = 0x53AC
	idInfo          = 0x1549A966
	idTimestampScl  = 0x2AD7B1
	idDuration      = 0x4489
	idSegTitle      = 0x7BA9
	idTracks        = 0x1654AE6B
	idTrackEntry    = 0xAE
	idTrackNumber   = 0xD7
	idTrackType     = 0x83
	idCodecID       = 0x86
	idAudio         = 0xE1
	idSampFreq      = 0xB5
	idChannels      = 0x9F
	idBitDepth      = 0x6264
	idTags          = 0x1254C367
	idTag           = 0x7373
	idTargets       = 0x63C0
	idTgtTypeVal    = 0x68CA
	idTgtType       = 0x63CA
	idTagTrackUID   = 0x63C5
	idTagEditUID    = 0x63C9
	idTagChapUID    = 0x63C4
	idSimpleTag     = 0x67C8
	idTagName       = 0x45A3
	idTagString     = 0x4487
	idTagBinary     = 0x4485
	idTagLang       = 0x447A
	idAttachments   = 0x1941A469
	idAttached      = 0x61A7
	idFileName      = 0x466E
	idFileMime      = 0x4660
	idFileData      = 0x465C
	idFileDesc      = 0x467E
	idFileUID       = 0x46AE
	idCluster       = 0x1F43B675
	idCues          = 0x1C53BB6B
	idCuePoint      = 0xBB
	idCueTrackPos   = 0xB7
	idCueClusterPos = 0xF1
	idChapters      = 0x1043A770
	idEditionEntry  = 0x45B9
	idEditionUID    = 0x45BC
	idEditionFlagDf = 0x45DB
	idChapterAtom   = 0xB6
	idChapterUID    = 0x73C4
	idChapTimeStart = 0x91
	idChapTimeEnd   = 0x92
	idChapDisplay   = 0x80
	idChapString    = 0x85
	idChapLang      = 0x437C
	idVoid          = 0xEC
	idCRC32         = 0xBF
)

// level1IDs are the Segment's direct children - the elements an unknown-size
// element (in practice a streamed Cluster) ends at, per the EBML rule that an
// unknown-size element runs until the next equal-or-higher-level element. They
// are used to bound such an element by skipping its children by size (never
// scanning payload bytes) until one of these IDs appears.
var level1IDs = map[uint64]bool{
	idSeekHead: true, idInfo: true, idTracks: true, idTags: true,
	idAttachments: true, idCluster: true, idCues: true, idChapters: true,
}

// trackTypeAudio is the TrackType value for an audio stream.
const trackTypeAudio = 2

// maxElement caps how many bytes of a single leaf element this codec reads into
// memory (tag strings, attachment metadata, cover art). The large cluster media
// payloads are never read - only their byte ranges are recorded - so this guards
// the small structural elements against a hostile declared size, alongside the
// user's MaxAllocBytes limit (whichever is smaller wins).
const maxElement = 64 << 20

// vintLen returns the byte length a VINT occupies given its first byte, or 0 if
// the byte has no length-descriptor bit set (invalid).
func vintLen(first byte) int {
	for i := 0; i < 8; i++ {
		if first&(0x80>>i) != 0 {
			return i + 1
		}
	}
	return 0
}

// readVINT reads a variable-length integer in [off, end). keepMarker retains the
// length-descriptor bit (element IDs); otherwise it is stripped (sizes/values).
// It reports the value, the bytes consumed, whether the stripped value was the
// all-ones "unknown" form, and ok.
func readVINT(src core.ReaderAtSized, off, end, limit int64, keepMarker bool) (val uint64, n int64, unknown, ok bool) {
	if off < 0 || off >= end {
		return 0, 0, false, false
	}
	// One read of up to 8 bytes (a VINT's max length): the first byte's
	// length-descriptor tells how many of them form the integer, so reading the
	// whole header at once avoids a second read that re-fetches the first byte.
	want := min(end-off, 8)
	buf, err := bits.ReadSlice(src, off, want, limit)
	if err != nil {
		return 0, 0, false, false
	}
	length := vintLen(buf[0])
	if length == 0 || int64(length) > want {
		return 0, 0, false, false
	}
	buf = buf[:length]
	if keepMarker {
		for _, c := range buf {
			val = val<<8 | uint64(c)
		}
		return val, int64(length), false, true
	}
	mask := byte(0x80 >> (length - 1))
	val = uint64(buf[0] &^ mask)
	for i := 1; i < length; i++ {
		val = val<<8 | uint64(buf[i])
	}
	maxVal := (uint64(1) << uint(7*length)) - 1
	return val, int64(length), val == maxVal, true
}

// element is one parsed EBML element header: its ID and the byte range of its
// data. next points at the following sibling. An unknown-size element is clamped
// to the parent's end and stops sibling iteration (its true end cannot be known
// without descending, which this read-only codec does not need to do for the
// cluster media it skips).
type element struct {
	id        uint64
	start     int64 // the element's own first byte (the ID)
	dataStart int64
	dataEnd   int64
	unknown   bool
	next      int64
}

func (e element) dataLen() int64 { return e.dataEnd - e.dataStart }

// readElement parses the element header at off within [off, end).
func readElement(src core.ReaderAtSized, off, end, limit int64) (element, bool) {
	id, idn, _, ok := readVINT(src, off, end, limit, true)
	if !ok {
		return element{}, false
	}
	size, szn, unknown, ok := readVINT(src, off+idn, end, limit, false)
	if !ok {
		return element{}, false
	}
	dataStart := off + idn + szn
	if dataStart > end {
		return element{}, false
	}
	dataEnd := end
	if !unknown {
		dataEnd = dataStart + int64(size)
		// Clamp a declared size that overruns the region (truncated/corrupt input)
		// so the range stays valid and this becomes the last element.
		if dataEnd > end || dataEnd < dataStart {
			dataEnd = end
		}
	}
	return element{id: id, start: off, dataStart: dataStart, dataEnd: dataEnd, unknown: unknown, next: dataEnd}, true
}

// resolveUnknownEnd returns the true end of an unknown-size element whose data
// begins at from, used at the Segment level so siblings placed after a streamed
// (unknown-size) Cluster - trailing Tags or Attachments - are not lost. It skips
// the element's children by their declared size (reading only headers, never the
// media payload) until it meets a Segment-level element ID, which by the EBML
// unknown-size rule is where the element ends. It falls back to end when it
// cannot resolve (a nested unknown-size child, an unparseable header, or no
// level-1 element follows).
func resolveUnknownEnd(src core.ReaderAtSized, from, end int64, limit int64) int64 {
	off := from
	for off < end {
		el, ok := readElement(src, off, end, limit)
		if !ok {
			return end
		}
		if level1IDs[el.id] {
			return off
		}
		if el.unknown || el.dataEnd <= off {
			return end
		}
		off = el.dataEnd
	}
	return end
}

// intVal converts an EBML unsigned integer to an int, returning 0 for a value too
// large to be a sane count or geometry. A direct uint64->int cast of a hostile
// 8-byte value would otherwise produce a negative property and wrap in the
// essence-digest config.
func intVal(v uint64) int {
	if v > math.MaxInt32 {
		return 0
	}
	return int(v)
}

// eachChild iterates the elements in [start, end), invoking fn for each. It stops
// without error on a header it cannot parse or on an element with no forward
// progress (never panicking on malformed input). depth bounds nesting.
func eachChild(src core.ReaderAtSized, start, end int64, depth *bits.Depth, limit int64, fn func(element) error) error {
	if err := depth.Enter(); err != nil {
		return err
	}
	defer depth.Leave()
	off := start
	for off < end {
		el, ok := readElement(src, off, end, limit)
		if !ok {
			return nil
		}
		if err := fn(el); err != nil {
			return err
		}
		if el.next <= off {
			return nil
		}
		off = el.next
	}
	return nil
}

// walkSegment iterates the Segment's direct children like eachChild, but resolves
// an unknown-size element (a streamed Cluster) to its true end via
// resolveUnknownEnd, so siblings placed after it - trailing Tags or Attachments -
// are still visited and the element's recorded extent is accurate rather than
// overstated to the Segment end.
func walkSegment(src core.ReaderAtSized, start, end int64, depth *bits.Depth, limit int64, fn func(element) error) error {
	if err := depth.Enter(); err != nil {
		return err
	}
	defer depth.Leave()
	off := start
	for off < end {
		el, ok := readElement(src, off, end, limit)
		if !ok {
			return nil
		}
		if el.unknown {
			el.dataEnd = resolveUnknownEnd(src, el.dataStart, end, limit)
		}
		if err := fn(el); err != nil {
			return err
		}
		if el.dataEnd <= off {
			return nil
		}
		off = el.dataEnd
	}
	return nil
}

// readUint reads an EBML unsigned integer (big-endian, 1-8 bytes).
func readUint(src core.ReaderAtSized, el element, limit int64) uint64 {
	n := el.dataLen()
	if n <= 0 || n > 8 {
		return 0
	}
	b, err := bits.ReadSlice(src, el.dataStart, n, limit)
	if err != nil {
		return 0
	}
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

// readFloat reads an EBML float (4 or 8 bytes). A 0-byte float is 0.0 per spec.
func readFloat(src core.ReaderAtSized, el element, limit int64) (float64, bool) {
	n := el.dataLen()
	switch n {
	case 0:
		return 0, true
	case 4:
		b, err := bits.ReadSlice(src, el.dataStart, 4, limit)
		if err != nil {
			return 0, false
		}
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b))), true
	case 8:
		b, err := bits.ReadSlice(src, el.dataStart, 8, limit)
		if err != nil {
			return 0, false
		}
		return math.Float64frombits(binary.BigEndian.Uint64(b)), true
	default:
		return 0, false
	}
}

// readString reads a UTF-8 string element, capped at maxElement, with trailing
// NUL padding (which some muxers add) trimmed. It propagates the read error so a
// metadata value that is truncated or exceeds the alloc limit fails the parse
// rather than being silently dropped.
func readString(src core.ReaderAtSized, el element, limit int64) (string, error) {
	b, err := readBytes(src, el, limit)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\x00"), nil
}

// readBytes reads a leaf element's data. A length beyond the metadata cap or the
// user's alloc limit yields waxerr.ErrSizeTooLarge rather than a silently
// truncated value (a truncated cover handed to the image sniffer would be a
// corrupt picture). An empty element is (nil, nil).
func readBytes(src core.ReaderAtSized, el element, limit int64) ([]byte, error) {
	n := el.dataLen()
	if n <= 0 {
		return nil, nil
	}
	if n > maxElement {
		return nil, fmt.Errorf("%w: metadata element of %d bytes exceeds the %d-byte cap",
			waxerr.ErrSizeTooLarge, n, maxElement)
	}
	return bits.ReadSlice(src, el.dataStart, n, limit)
}
