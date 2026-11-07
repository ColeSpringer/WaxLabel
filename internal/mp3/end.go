package mp3

import (
	"bytes"
	"context"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
)

// resyncWindow bounds the search for the frame chain that ends the audio, the bytes right before
// the tags at the end of the file.
const resyncWindow = 64 << 10

// maxFrameLen is the longest MPEG audio frame: Layer II of MPEG-2 or 2.5 at 160 kbps and 8 kHz,
// padded.
const maxFrameLen = 2881

// countSpan bounds the bytes before where a VBR header's byte count ends the stream that show whether
// a frame ends there: a frame a second one confirms, that second one, and the frame they run into.
const countSpan = 3 * maxFrameLen

// audioEnd says where the stream whose first frame f opens at off ends, given boundary, where the
// tags peeled from the end of the file begin, or -1 when it cannot tell. The byte count of f's VBR
// header places the end when the bytes it counts end at boundary, or end by it on the end of a frame
// with no frame of the stream right after them or after the tags a file joined there opens with.
// Otherwise the frames in the resyncWindow bytes before boundary do: a chain of them that runs to
// boundary ends the audio there, and one that stops on a whole frame ends it where it stops, the
// bytes it is picked up past counted; when the count runs past boundary and fewer bytes than a header
// follow that frame, they are the frame the cut left, and the audio runs to boundary. A count past
// the chain's end by less than the shortest frame of the stream counts no frame the file lacks, so
// it cuts nothing. A last frame boundary cuts, no chain, or a window past the end of the file leaves
// the end unknown, the safe direction for an end hook. The ID3 tags between joined streams are
// counted where the count ends and wherever the chain goes on past them. Only that window, the
// countSpan bytes before the counted ones, the headers of the tags after them and the frame after
// those are read, as [bits.ReadWithin] reads: a read over limit is refused, and one that fails inside
// the file is an error.
func audioEnd(ctx context.Context, src core.ReaderAtSized, f mpegInfo, off, boundary, limit int64) (measure, error) {
	if err := ctx.Err(); err != nil {
		return measure{}, err
	}
	var m measure
	lo := max(off, boundary-resyncWindow)
	joinAt, joinTags := int64(-1), 0 // where the count ends at the tags a joined file opens with
	if n := f.byteCount(); n > 0 {
		end := off + n
		m.counted = end
		switch {
		case end > boundary:
			m.past = end - boundary
		case end == boundary:
			return measure{end: end}, nil
		default:
			at, tags, err := id3.SkipTags(src, end, boundary, limit)
			if err != nil {
				return measure{}, err
			}
			next, err := bits.ReadWithin(src, at, min(boundary-at, maxFrameLen+4), limit)
			if err != nil {
				return measure{}, err
			}
			if opensFrame(next, f) {
				m.joined, joinAt, joinTags = true, end, tags
				break
			}
			ends, err := endsFrame(src, f, off, end, limit)
			if err != nil {
				return measure{}, err
			}
			if ends {
				return measure{end: end}, nil
			}
			// The count ends inside a frame or past the frames, so they decide.
		}
	}
	w, err := bits.ReadWithin(src, lo, boundary-lo, limit)
	if err != nil || w == nil {
		m.end, m.tags = -1, joinTags
		return m, err
	}
	c := chainEnd(w, f)
	if m.past > 0 && c.end >= 0 && m.counted-(lo+int64(c.end)) < int64(minFrameLen(f)) {
		m.past = 0 // the count claims less than a frame past the chain: none is missing
	}
	m.gap, m.tags = int64(c.gap), c.tags
	if joinAt >= 0 && joinAt < lo+int64(c.first) {
		m.tags += joinTags // the window's chain starts past them, so it does not count them
	}
	switch {
	case c.end < 0:
		m.end = -1
	case m.past > 0 && boundary-(lo+int64(c.end)) < 4:
		m.end = boundary // the bytes after the last whole frame, too few for a header, are the cut frame's
	default:
		m.end = lo + int64(c.end)
	}
	m.claim, m.last = m.end, lo+int64(c.last)
	if c.over > 0 {
		m.claim = lo + int64(c.over)
	}
	if m.past > 0 && m.end >= 0 {
		m.past += boundary - m.end // the header counts audio bytes, so the cut is measured from the audio
	}
	return m, nil
}

// measure is where an MPEG stream ends, as [audioEnd] finds it: -1 when it cannot tell.
type measure struct {
	end int64
	// past is how far the VBR header's byte count runs past the audio end, or past boundary when the
	// end is unknown, as a truncation leaves it; it is set only when the count runs past boundary and,
	// when the chain ends on a whole frame, by at least the stream's shortest frame past it.
	past    int64
	counted int64 // where the VBR header's byte count ends the stream, 0 when it has none
	// joined: the stream goes on where the count ends, or behind the tags a file joined there opens
	// with, so the header counts only part of it.
	joined bool
	gap    int64 // bytes between frames the chain was picked up past, which stay in the stream
	tags   int   // ID3 tags between joined streams, which stay in the stream
	// claim is where the window's frames claim the stream ends: end, or where a last frame that runs
	// past boundary declares it ends; not positive when the byte count decides or no chain does.
	claim int64
	last  int64 // where the last frame of the window's last chain starts
}

// chain is what [chainEnd] finds in a window.
type chain struct {
	// end is where the last chain ends: the window's length when it runs to the end, where it stops
	// on a whole frame otherwise, and -1 when its last frame runs past the end or no chain starts.
	end   int
	over  int // where a last frame that runs past the end declares it ends, 0 for none
	last  int // where the last chain's last frame starts
	gap   int // the bytes between a break and the frame the chain is picked up at
	tags  int // the ID3 tags the chain goes on past
	first int // where the first chain starts, the window's length when none does
}

// endsFrame reports whether a frame of f's stream ends at end, where its VBR header's byte count ends
// the stream: the frames in the countSpan bytes before it, walked from f's own frame at off when they
// reach back to it, run to exactly end, picked up past a gap or a tag at the next frame a second one
// confirms or that ends at end. A free-format stream's frames cannot be followed, so its count stands.
func endsFrame(src core.ReaderAtSized, f mpegInfo, off, end, limit int64) (bool, error) {
	if frameLength(f) == 0 {
		return true, nil
	}
	from := max(off, end-countSpan)
	w, err := bits.ReadWithin(src, from, end-from, limit)
	if err != nil || w == nil {
		return false, err
	}
	resync := func(i int) (int, bool) {
		j, _, ok := firstFrame(w[i:], func(w []byte, k int, h mpegInfo) bool {
			return sameStream(f, h) && (k+frameLength(h) == len(w) || nextFrameConfirms(w, k, h))
		})
		return i + j, ok
	}
	i, ok := 0, true
	if from > off {
		i, ok = resync(0)
	}
	for ok && i < len(w) {
		if h, frame := decodeHeader(w[i:]); frame && sameStream(f, h) && frameLength(h) > 0 {
			i += frameLength(h)
			continue
		}
		i, ok = resync(i + 1)
	}
	return ok && i == len(w), nil
}

// minFrameLen is the shortest frame f's stream can hold: its version, layer and rate at the lowest
// bitrate its channel mode allows, unpadded. MPEG-1 Layer II allows 32 to 56 kbps in mono only.
func minFrameLen(f mpegInfo) int {
	group, index := 1, 1
	if f.version == 1 {
		group = 0
		if f.layer == 2 && f.channels == 2 {
			index = 4
		}
	}
	f.frameBitrate = bitrateTable[group][f.layer-1][index] * 1000
	f.header[2] &^= 2
	return frameLength(f)
}

// confirmedIn accepts a frame of f's stream that a second frame confirms.
func confirmedIn(f mpegInfo) func(w []byte, i int, h mpegInfo) bool {
	return func(w []byte, i int, h mpegInfo) bool { return sameStream(f, h) && nextFrameConfirms(w, i, h) }
}

// chainEnd follows the frames of w that agree with f, from the first one a second frame confirms,
// goes on past the ID3 tags a file joined there opens with where a confirmed frame follows them, and
// picks the chain up again at the next confirmed frame wherever else it breaks.
func chainEnd(w []byte, f mpegInfo) chain {
	in := bytes.NewReader(w)
	c := chain{end: -1, first: len(w)}
	for pos := 0; ; {
		i, h, ok := firstFrame(w[pos:], confirmedIn(f))
		if !ok {
			return c
		}
		if pos == 0 {
			c.first = i
		}
		if c.end >= 0 {
			c.gap += i
		}
		q := pos + i
		for {
			c.last = q
			q += frameLength(h)
			if q > len(w) {
				c.end, c.over = -1, q
				return c
			}
			if q == len(w) {
				c.end = q
				return c
			}
			next, ok := decodeHeader(w[q:])
			if !ok || !sameStream(f, next) || frameLength(next) == 0 {
				// w is in memory, so a read of it is never over the limit of its length.
				at, n, _ := id3.SkipTags(in, int64(q), int64(len(w)), int64(len(w)))
				if n == 0 || !opensFrame(w[at:], f) {
					break
				}
				q, c.tags = int(at), c.tags+n
				next, _ = decodeHeader(w[q:])
			}
			h = next
		}
		c.end, pos = q, q
	}
}

// opensFrame reports whether b opens with a frame of f's stream that a second frame confirms.
func opensFrame(b []byte, f mpegInfo) bool {
	h, ok := decodeHeader(b)
	return ok && sameStream(f, h) && nextFrameConfirms(b, 0, h)
}
