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

// SV8 chapters are CT packets: varlen start sample, 16-bit gain/peak, then an
// APEv2 tag without APETAGEX preamble (24-byte header + items, no footer); Title
// names the chapter. Read only where the reference decoder does: after the seek
// table an SO packet points at, or the consecutive CT run ending at SE. Elsewhere
// is invisible to libmpcdec players.
//
// Packets sit inside the audio extent; rewrite copies the stream verbatim, so
// chapters are preserved but never edited (read-only capability).
const (
	keyAudio     = "AP"
	keyChapter   = "CT"
	keySeekOff   = "SO"
	keySeekTable = "ST"
	keyEnd       = "SE"
)

// chapterTagHeaderLen: APEv2 header without 8-byte preamble; item count at byte 8.
const chapterTagHeaderLen = 24

// maxSizeBytes: longest varlen readSize accepts (packet header = 2-byte key + this).
const maxSizeBytes = 9

// packet is one decoded SV8 packet header.
type packet struct {
	key    string
	hdrLen int   // key + size field
	size   int64 // whole packet including header
}

// parsePacket at the front of b; must fit in room. Invalid key/size is not a packet.
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

// readPacket reads the packet header at off in a stream ending at end, as [bits.ReadWithin] reads:
// one past the end of the file is no packet, and a failed read or one over limit is an error.
func readPacket(src core.ReaderAtSized, off, end, limit int64) (packet, bool, error) {
	n := min(int64(2+maxSizeBytes), end-off)
	if n < 3 {
		return packet{}, false, nil
	}
	b, err := bits.ReadWithin(src, off, n, limit)
	if err != nil || b == nil {
		return packet{}, false, err
	}
	p, ok := parsePacket(b, end-off)
	return p, ok, nil
}

// walk is what [walkPackets] finds.
type walk struct {
	end      int64 // past the SE packet, -1 when unknown
	chapters int64 // where the CT run the reference decoder reads starts, -1 for none
	capAt    int64 // where the walk stopped at the packet cap with the chapters unplaced, -1 for none
}

// walkPackets walks the packet headers of the SV8 stream at start once, for where it ends and where
// the chapters the reference decoder reads start. An SO packet among the stream headers that points at
// a seek table places them right after it and skips the audio; without one, they are the last run of
// consecutive CT packets that ends at the SE packet. The stream ends past its SE packet. A malformed
// packet, boundary (where the tags begin), or more than maxPackets headers before SE leave the end
// unknown, and the chapters unplaced unless the seek table placed them; a read that fails is an error.
func walkPackets(ctx context.Context, src core.ReaderAtSized, start, boundary, limit int64, maxPackets int) (walk, error) {
	w := walk{end: -1, chapters: -1, capAt: -1}
	pos := start + int64(len(sv8Magic))
	run := int64(-1) // the CT run in progress
	// audio: past the stream headers, where no SO counts; placed: a seek table placed the chapters.
	audio, placed := false, false
	for n := 0; pos < boundary; n++ {
		if err := ctx.Err(); err != nil {
			return walk{}, err
		}
		if bits.CheckElementCap(n, maxPackets, "SV8 packets") != nil {
			if !placed {
				w.capAt = pos
			}
			return w, nil
		}
		p, ok, err := readPacket(src, pos, boundary, limit)
		if err != nil {
			return walk{}, err
		}
		if !ok {
			return w, nil
		}
		switch p.key {
		case keyEnd:
			w.end = pos + p.size
			if !placed {
				w.chapters = run
			}
			return w, nil
		case keyChapter:
			if run < 0 {
				run = pos
			}
		case keySeekOff:
			run = -1
			if !audio {
				at, ok, err := afterSeekTable(src, pos, p, boundary, limit)
				if err != nil {
					return walk{}, err
				}
				if ok {
					pos, audio, placed, w.chapters = at, true, true, at
					continue
				}
			}
		default:
			run = -1
			audio = audio || p.key == keyAudio
		}
		pos += p.size
	}
	return w, nil
}

// afterSeekTable: SO payload is a varlen offset (from packet start) to the seek
// table; chapters begin right after an ST there. Only the pointer bytes are read,
// as [bits.ReadWithin] reads them.
func afterSeekTable(src core.ReaderAtSized, pos int64, p packet, end, limit int64) (int64, bool, error) {
	payload, err := bits.ReadWithin(src, pos+int64(p.hdrLen), min(p.size-int64(p.hdrLen), maxSizeBytes), limit)
	if err != nil || payload == nil {
		return 0, false, err
	}
	ptr, _, ok := readSize(payload)
	if !ok || ptr >= uint64(end-pos) {
		return 0, false, nil
	}
	at := pos + int64(ptr)
	st, ok, err := readPacket(src, at, end, limit)
	if err != nil || !ok || st.key != keySeekTable {
		return 0, false, err
	}
	return at + st.size, true, nil
}

// chapterFault classifies what decodeChapter could not read.
type chapterFault uint8

const (
	faultNone        chapterFault = iota
	faultTruncated                // payload past the end of the file or short of start/gain/peak
	faultUnplaceable              // start sample past any duration
	faultOversized                // larger than allocation limit
	faultShortTag                 // tag shorter than header; chapter listed untitled
	faultCappedTag                // items past element cap unread; title may be among them
)

// faultReports: wording and whether the chapter is still listed.
// Capped-tag is WarnElementCap; others are malformed-entry.
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

// readChapters projects CT packets at at into chapters (start order) and returns
// where the read ended. Every packet counts against the element cap. Each fault
// is reported once for the run; framing after a faulty packet stays intact. The
// packets are read as [bits.ReadWithin] reads: a payload past the end of the file
// is a truncated chapter, and a read that fails is an error.
func readChapters(src core.ReaderAtSized, at, end int64, rate int, limit int64, maxElements int) ([]core.Chapter, int64, []core.Warning, error) {
	var chs []core.Chapter
	var warnings []core.Warning
	var first, count [len(faultReports)]int
	pos := at
	for n := 0; pos < end; n++ {
		p, ok, err := readPacket(src, pos, end, limit)
		if err != nil {
			return nil, 0, nil, err
		}
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
		payload, err := bits.ReadWithin(src, payloadAt, p.size-int64(p.hdrLen), limit)
		switch {
		case errors.Is(err, waxerr.ErrSizeTooLarge):
			fault = faultOversized
		case err != nil:
			return nil, 0, nil, err
		case payload == nil:
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
	return chs, pos, warnings, nil
}

// decodeChapter: start sample, gain/peak (unmodeled; stay in copied bytes), then
// tag. Short/capped tag still yields a chapter (possibly untitled) with a fault.
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
			// Multi-valued items are NUL-joined; a chapter has one title.
			ch.Title, _, _ = strings.Cut(it.Value, "\x00")
			break
		}
	}
	if capped {
		return ch, faultCappedTag
	}
	return ch, faultNone
}
