package musepack

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// SV8 stores chapters as CT packets inside the stream: a varlen start sample, a 16-bit
// gain and peak, then an APEv2 tag without its "APETAGEX" preamble - the 24-byte header
// record and the items, no footer - whose Title item names the chapter. mpcchap, the
// reference chapter editor, is the only writer of them, and the reference decoder reads
// them from two places only: right after the seek table an SO packet in the header
// region points at, or else the run of consecutive CT packets that ends at the SE end
// marker. A run anywhere else is invisible to every libmpcdec-based player, so it is not
// read here either.
//
// The packets are metadata inside the audio extent. A rewrite copies the stream
// verbatim, so they are preserved but never edited: the chapters capability is read-only.

// Packet keys this reader acts on.
const (
	keyAudio     = "AP"
	keyChapter   = "CT"
	keySeekOff   = "SO"
	keySeekTable = "ST"
	keyEnd       = "SE"
)

// chapterTagHeaderLen is the APEv2 header record less its 8-byte preamble: version,
// size, item count, flags, and the reserved word. The item count sits at byte 8.
const chapterTagHeaderLen = 24

// maxSizeBytes is the longest varlen number readSize accepts, so a packet header is at
// most the two-byte key plus this.
const maxSizeBytes = 9

// packet is one decoded SV8 packet header.
type packet struct {
	key    string
	hdrLen int   // the key and size field
	size   int64 // the whole packet, header included
}

// parsePacket decodes the packet header at the front of b, for a packet that must fit
// within room bytes. A key outside A-Z, a size that does not cover its own header, or
// one running past the room is not a packet.
func parsePacket(b []byte, room int64) (packet, bool) {
	if len(b) < 3 || !isKeyByte(b[0]) || !isKeyByte(b[1]) {
		return packet{}, false
	}
	size, n, ok := readSize(b[2:])
	if !ok || size < uint64(2+n) || size > uint64(room) {
		return packet{}, false
	}
	return packet{key: string(b[:2]), hdrLen: 2 + n, size: int64(size)}, true
}

func isKeyByte(c byte) bool { return c >= 'A' && c <= 'Z' }

// readPacket decodes the packet header at off in a stream ending at end.
func readPacket(src core.ReaderAtSized, off, end, limit int64) (packet, bool) {
	n := min(int64(2+maxSizeBytes), end-off)
	if n < 3 {
		return packet{}, false
	}
	b, err := bits.ReadSlice(src, off, n, limit)
	if err != nil {
		return packet{}, false
	}
	return parsePacket(b, end-off)
}

// chapterRun returns the offset of the CT run the reference decoder reads, or -1 when
// there is none where it looks. Header packets are hopped until the first audio packet,
// and an SO packet there whose pointer lands on an ST packet settles the answer as the
// packet after that table. Otherwise the walk continues to the end marker, keeping the
// offset of the last run of consecutive CT packets, which any other packet resets; the
// run must end at the marker to count. A malformed packet header or a stream with no
// end marker yields none, as the reference's search fails there too.
//
// The walk is one small read per packet, like the Matroska segment walk, and every
// mpcenc file settles at its SO packet. A stream without one is walked whole, so the
// element cap bounds the packets visited (reported when it trips) and the context is
// checked along the way.
func chapterRun(ctx context.Context, src core.ReaderAtSized, start, end, limit int64, maxElements int) (int64, []core.Warning, error) {
	pos := start + int64(len(sv8Magic))
	run := int64(-1)
	audio := false
	for n := 0; pos < end; n++ {
		if err := ctx.Err(); err != nil {
			return -1, nil, err
		}
		if bits.CheckElementCap(n, maxElements, "SV8 packets") != nil {
			return -1, core.Warn(nil, core.WarnElementCap,
				"the stream has more packets than the element limit allows; no chapters are read"), nil
		}
		p, ok := readPacket(src, pos, end, limit)
		if !ok {
			return -1, nil, nil
		}
		switch p.key {
		case keyEnd:
			return run, nil, nil
		case keyChapter:
			if run < 0 {
				run = pos
			}
		case keySeekOff:
			run = -1
			if !audio {
				if at, ok := afterSeekTable(src, pos, p, end, limit); ok {
					return at, nil, nil
				}
			}
		default:
			run = -1
			audio = audio || p.key == keyAudio
		}
		pos += p.size
	}
	return -1, nil, nil
}

// afterSeekTable resolves an SO packet at pos: its payload is a varlen offset, relative
// to the packet's own start, of the seek table. When an ST packet is there, the chapter
// run begins right after it. Anything else there leaves the search to the walk. Only
// the pointer's own bytes are read, whatever size the packet declares.
func afterSeekTable(src core.ReaderAtSized, pos int64, p packet, end, limit int64) (int64, bool) {
	payload, err := bits.ReadSlice(src, pos+int64(p.hdrLen), min(p.size-int64(p.hdrLen), maxSizeBytes), limit)
	if err != nil {
		return 0, false
	}
	ptr, _, ok := readSize(payload)
	if !ok || ptr >= uint64(end-pos) {
		return 0, false
	}
	at := pos + int64(ptr)
	st, ok := readPacket(src, at, end, limit)
	if !ok || st.key != keySeekTable {
		return 0, false
	}
	return at + st.size, true
}

// chapterFault classifies what decodeChapter could not read of one packet.
type chapterFault uint8

const (
	faultNone        chapterFault = iota
	faultTruncated                // the payload cannot be read, or ends before the start sample, gain, and peak
	faultUnplaceable              // the start sample is past any duration
	faultOversized                // the packet is larger than the allocation limit
	faultShortTag                 // a tag shorter than its header record; the chapter is listed untitled
	faultCappedTag                // items past the element cap were not read; the title may be among them
)

// faultReports words each fault and says whether the chapter is still listed. The
// capped-tag fault is an element-cap report, the rest describe a malformed entry.
var faultReports = [...]struct {
	code   core.WarningCode
	text   string
	listed bool
}{
	faultTruncated:   {core.WarnMalformedTagEntry, "is truncated before its start sample; skipped", false},
	faultUnplaceable: {core.WarnMalformedTagEntry, "starts past any duration; skipped", false},
	faultOversized:   {core.WarnMalformedTagEntry, "is larger than the allocation limit; not read", false},
	faultShortTag:    {core.WarnMalformedTagEntry, "carries an unreadable tag; the chapter is listed untitled", true},
	faultCappedTag:   {core.WarnElementCap, "has more tag items than the element limit allows; items past it are not read", true},
}

// readChapters projects the run of CT packets at `at` into chapters, in start order,
// and returns where the packets it read end. Every packet counts against the element
// cap whether or not it yields a chapter, and the cap ends the walk, so a run of
// malformed packets cannot spend unbounded work. Each fault is reported once for the
// run, naming the first packet it hit and how many more shared it. The packets after a
// faulty one are still read, since the packet framing around it is intact.
func readChapters(src core.ReaderAtSized, at, end int64, rate int, limit int64, maxElements int) ([]core.Chapter, int64, []core.Warning) {
	var chs []core.Chapter
	var warnings []core.Warning
	var first, count [len(faultReports)]int
	pos := at
	for n := 0; pos < end; n++ {
		p, ok := readPacket(src, pos, end, limit)
		if !ok || p.key != keyChapter {
			break
		}
		if bits.CheckElementCap(n, maxElements, "chapter packets") != nil {
			warnings = core.Warn(warnings, core.WarnElementCap,
				"the stream has more chapter packets than the element limit allows; the rest are not read")
			break
		}
		payloadAt := pos + int64(p.hdrLen)
		pos += p.size
		var ch core.Chapter
		var fault chapterFault
		payload, err := bits.ReadSlice(src, payloadAt, p.size-int64(p.hdrLen), limit)
		switch {
		case errors.Is(err, waxerr.ErrSizeTooLarge):
			fault = faultOversized
		case err != nil:
			fault = faultTruncated
		default:
			ch, fault = decodeChapter(payload, rate, maxElements)
		}
		if fault != faultNone {
			if count[fault] == 0 {
				first[fault] = n + 1
			}
			count[fault]++
			if !faultReports[fault].listed {
				continue
			}
		}
		chs = append(chs, ch)
	}
	for f, r := range faultReports {
		if count[f] == 0 {
			continue
		}
		msg := fmt.Sprintf("chapter packet %d %s", first[f], r.text)
		if count[f] > 1 {
			msg += fmt.Sprintf(" (and %d more)", count[f]-1)
		}
		warnings = core.Warn(warnings, r.code, msg)
	}
	core.SortChaptersByStart(chs)
	return chs, pos, warnings
}

// decodeChapter decodes one CT payload: the start sample, the gain and peak (not
// modeled; they stay in the bytes the rewrite copies), then the tag. A payload ending
// before those fields, or a start sample no duration can hold, yields no chapter. A tag
// shorter than its header record, or one whose items run past the element cap, still
// yields the chapter, possibly untitled, with the fault to report.
func decodeChapter(p []byte, rate int, maxElements int) (core.Chapter, chapterFault) {
	sample, n, ok := readSize(p)
	if !ok || len(p) < n+4 {
		return core.Chapter{}, faultTruncated
	}
	ch := core.Chapter{Start: core.SamplesToDuration(sample, rate)}
	if ch.Start == 0 && sample > 0 {
		return core.Chapter{}, faultUnplaceable
	}
	tagBytes := p[n+4:]
	if len(tagBytes) == 0 {
		return ch, faultNone
	}
	if len(tagBytes) < chapterTagHeaderLen {
		return ch, faultShortTag
	}
	count := binary.LittleEndian.Uint32(tagBytes[8:12])
	items, capped := ape.ParseItemRun(tagBytes[chapterTagHeaderLen:], count, maxElements)
	for _, it := range items {
		if it.NonText() {
			continue
		}
		if k, ok := mapping.CanonicalAPE(it.Key); ok && k == tag.Title {
			// A multi-valued item is NUL-joined; a chapter has one title.
			ch.Title, _, _ = strings.Cut(it.Value, "\x00")
			break
		}
	}
	if capped {
		return ch, faultCappedTag
	}
	return ch, faultNone
}
