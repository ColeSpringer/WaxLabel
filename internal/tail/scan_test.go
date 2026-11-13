package tail

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Every tail follows a 4 KiB audio stub whose first 512 bytes stand for the stream header.
const (
	stubLen   = 4 << 10
	stubFloor = 512
	testLimit = 1 << 20
	subject   = "after the test stream"
)

func v1(title string) []byte { return testfile.ID3v1(title, "Artist", "", "2001", "", 255) }

func apeTag(tb testing.TB, header bool, items ...ape.Item) []byte {
	tb.Helper()
	raw, err := ape.Render(items, 2000, header, 0)
	if err != nil {
		tb.Fatal(err)
	}
	return raw
}

func titled(tb testing.TB, header bool, title string) []byte {
	tb.Helper()
	return apeTag(tb, header, ape.Item{Key: "Title", Value: title})
}

func junk(n int) []byte { return bytes.Repeat([]byte{0xA5}, n) }

// put32 returns a copy of b with the little-endian word at off set to v; a negative off counts
// from the end, so -24, -20, -16 and -12 are a footer's version, size, item count and flags.
func put32(b []byte, off int, v uint32) []byte {
	b = slices.Clone(b)
	if off < 0 {
		off += len(b)
	}
	binary.LittleEndian.PutUint32(b[off:], v)
	return b
}

// record is one 32-byte APE header or footer record.
func record(version, size, count, flags uint32) []byte {
	b := make([]byte, 32)
	copy(b, "APETAGEX")
	binary.LittleEndian.PutUint32(b[8:], version)
	binary.LittleEndian.PutUint32(b[12:], size)
	binary.LittleEndian.PutUint32(b[16:], count)
	binary.LittleEndian.PutUint32(b[20:], flags)
	return b
}

// disagreeing is an empty footer-only tag the search takes, then a header record announcing an
// empty tag right after it, then where that tag's footer would sit a footer that announces a header
// but declares size bytes, so it does not answer this one (version 3000 so the search skips it),
// then junk.
func disagreeing(size uint32) []byte {
	return slices.Concat(record(2000, 32, 0, 0), record(2000, 32, 0, 1<<31|1<<29), record(3000, size, 0, 1<<31), junk(8))
}

// withStub is the audio stub followed by pieces.
func withStub(pieces ...[]byte) []byte {
	return slices.Concat(append([][]byte{make([]byte, stubLen)}, pieces...)...)
}

// endAt is a codec that knows its audio ends at e; 0 means it cannot tell.
func endAt(e int64) AudioEnd {
	if e == 0 {
		return nil
	}
	return func(core.ReaderAtSized, int64) (int64, error) { return e, nil }
}

func mustScan(t *testing.T, file []byte, end AudioEnd, maxElements int) Layout {
	t.Helper()
	l, err := Scan(core.BytesSource(file), int64(len(file)), stubFloor, testLimit, maxElements, end)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func mustScanLegacy(t *testing.T, file []byte, end AudioEnd, maxElements int) Layout {
	t.Helper()
	l, err := ScanLegacy(core.BytesSource(file), int64(len(file)), stubFloor, testLimit, maxElements, end)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// pattern is n bytes of the walked codec's stream from offset from, both multiples of 4: units of
// four bytes numbered from zero, the number little-endian in the first three and 0xC3 last, which no
// run of tag or junk bytes passes for. An audio stub is pattern(0, stubLen), and pattern(stubLen, n)
// is that stream going on.
func pattern(from, n int64) []byte {
	out := make([]byte, n)
	for i := range out {
		k := (from + int64(i)) / 4
		switch (from + int64(i)) % 4 {
		case 0:
			out[i] = byte(k)
		case 1:
			out[i] = byte(k >> 8)
		case 2:
			out[i] = byte(k>>16) ^ 0x5A
		default:
			out[i] = 0xC3
		}
	}
	return out
}

// walked is a codec whose stream, starting at start, is the pattern unit by unit: it walks the source
// from start to the boundary and ends the audio at the first unit off the pattern, as a frame walk
// does. With window > 0 it sees only the window bytes before the boundary, as the MP3 resync does, so
// an end further back is unknown. calls counts how often it is asked.
func walked(start, window int64, calls *int) AudioEnd {
	return func(src core.ReaderAtSized, boundary int64) (int64, error) {
		if calls != nil {
			*calls++
		}
		at := start
	walk:
		for at+4 <= boundary {
			b, err := bits.ReadSlice(src, at, min(1024, boundary-at)&^3, testLimit)
			if err != nil {
				return 0, err
			}
			for i := 0; i+4 <= len(b); i += 4 {
				if !bytes.Equal(b[i:i+4], pattern(at-start, 4)) {
					break walk
				}
				at += 4
			}
		}
		if window > 0 && boundary-at > window {
			return -1, nil
		}
		return at, nil
	}
}

// unknown is a codec that never tells where its audio ends.
func unknown(core.ReaderAtSized, int64) (int64, error) { return -1, nil }

// claiming is a codec whose first measure claims its audio up to e, as a VBR byte count or a frame's
// declared length that runs into a tag does, and whose later ones walk the stream as walked does.
func claiming(e int64) AudioEnd {
	walk, asked := walked(0, 0, nil), false
	return func(src core.ReaderAtSized, b int64) (int64, error) {
		if asked {
			return walk(src, b)
		}
		asked = true
		return e, nil
	}
}

// counted is a codec that, as an MP3 VBR header does, counts n bytes of stream from start and ends the
// audio there, unless two units of the stream follow them, when it walks on as walked does with window.
func counted(start, n, window int64) AudioEnd {
	walk := walked(start, window, nil)
	return func(src core.ReaderAtSized, boundary int64) (int64, error) {
		e := start + n
		b, err := bits.ReadSlice(src, e, min(8, boundary-e), testLimit)
		if err != nil {
			return 0, err
		}
		if !bytes.Equal(b, pattern(n, 8)) {
			return e, nil
		}
		return walk(src, boundary)
	}
}

type shape struct {
	Kind     Kind
	Off, Len int64
	Blocks   int
}

func shapes(rs []Region) []shape {
	out := make([]shape, len(rs))
	for i, r := range rs {
		out[i] = shape{r.Kind, r.Off, r.Len, r.Blocks}
	}
	return out
}

func codes(ws []core.Warning) []core.WarningCode {
	var out []core.WarningCode
	for _, w := range ws {
		out = append(out, w.Code)
	}
	return out
}

// span is an expected region over pieces[first..last] of a tail.
type span struct {
	kind        Kind
	first, last int
	blocks      int
}

func TestScan(t *testing.T) {
	tagA, tagB := titled(t, true, "A"), titled(t, true, "B")
	bare := titled(t, false, "A") // footer only, no header record
	footer := bare[len(bare)-32:]
	lyr1 := testfile.Lyrics3v1("la la la")
	lyr2 := testfile.Lyrics3v2([2]string{"IND", "10"}, [2]string{"LYR", "la la la"})
	run33 := make([][]byte, 33)
	for i := range run33 {
		run33[i] = v1("Stacked")
	}
	// A footer whose declared size starts its tag at offset 100, inside the stream header.
	misplaced := put32(footer, -20, stubLen+32-100)
	// An APETAGEX record over 20 junk bytes that do not parse as the item it declares.
	fake := slices.Concat(junk(20), put32(footer, -20, 52))
	headless := slices.Clone(tagA)
	headless[0] = 'X' // the footer still announces a header record
	// A footer that announces a header record where the previous tag's footer sits.
	lying := put32(titled(t, false, "B"), -12, 1<<31)
	// A header record whose declared size ends on a later tag's footer, which has no header.
	stretched := put32(tagA, 12, uint32(len(tagA)-32+100+len(bare)))
	// A header record over items that would hold up behind it if it were a footer.
	headerOnly := put32(bare, -12, 1<<29)
	// A tag whose footer the search refuses (version 3000) but whose header opens it where it sits.
	odd := put32(tagB, -24, 3000)
	// A footer-only tag the search refuses (version 3000), readable only where a found tag starts.
	oddBare := put32(bare, -24, 3000)
	// A Lyrics3v2 block whose last 128 bytes pass for an ID3v1 tag: TAG and a year where one sits.
	lyrics := []byte(strings.Repeat("x", 200))
	copy(lyrics[87:], "TAG")
	copy(lyrics[180:], "1999")
	posing := testfile.Lyrics3v2([2]string{"LYR", string(lyrics)})
	if !id3.LooksLikeID3v1(posing[len(posing)-128:]) {
		t.Fatal("the posing Lyrics3v2 block does not end in an ID3v1 look-alike")
	}

	const (
		id3v1    = core.WarnTrailingID3v1
		lyrics3  = core.WarnTrailingLyrics3
		trailing = core.WarnTrailingBytes
		dup      = core.WarnDuplicateTagBlock
		bad      = core.WarnMalformedTagEntry
	)
	tests := []struct {
		name    string
		pieces  [][]byte
		end     int64 // where the codec says the audio ends; 0 when it cannot tell
		floor   int64 // 0 for the stub's
		want    []span
		known   bool   // Scan takes the codec's end
		essence []span // junk the essence keeps after the audio
		warns   []core.WarningCode
	}{
		{name: "ID3v1", pieces: [][]byte{v1("A")},
			want: []span{{KindID3v1, 0, 0, 1}}, warns: []core.WarningCode{id3v1}},
		{name: "two stacked ID3v1", pieces: [][]byte{v1("A"), v1("B")},
			want: []span{{KindID3v1, 0, 1, 2}}, warns: []core.WarningCode{id3v1}},
		{name: "APE", pieces: [][]byte{tagA},
			want: []span{{KindAPEv2, 0, 0, 0}}},
		{name: "APE then ID3v1", pieces: [][]byte{tagA, v1("A")},
			want: []span{{KindAPEv2, 0, 0, 0}, {KindID3v1, 1, 1, 1}}, warns: []core.WarningCode{id3v1}},
		{name: "ID3v1 inside the APE tag", pieces: [][]byte{v1("A"), tagA},
			want: []span{{KindID3v1, 0, 0, 1}, {KindAPEv2, 1, 1, 0}}, warns: []core.WarningCode{id3v1}},
		{name: "ID3v1 on both sides of the APE tag", pieces: [][]byte{v1("A"), tagA, v1("B")},
			want: []span{{KindID3v1, 0, 0, 1}, {KindAPEv2, 1, 1, 0}, {KindID3v1, 2, 2, 1}}, warns: []core.WarningCode{id3v1}},
		{name: "APE, Lyrics3v2, ID3v1", pieces: [][]byte{tagA, lyr2, v1("A")},
			want:  []span{{KindAPEv2, 0, 0, 0}, {KindLyrics3v2, 1, 1, 0}, {KindID3v1, 2, 2, 1}},
			warns: []core.WarningCode{lyrics3, id3v1}},
		{name: "Lyrics3v1, ID3v1", pieces: [][]byte{lyr1, v1("A")},
			want: []span{{KindLyrics3v1, 0, 0, 0}, {KindID3v1, 1, 1, 1}}, warns: []core.WarningCode{lyrics3, id3v1}},
		{name: "Lyrics3v2 ending in an ID3v1 look-alike", pieces: [][]byte{posing, v1("A")},
			want: []span{{KindLyrics3v2, 0, 0, 0}, {KindID3v1, 1, 1, 1}}, warns: []core.WarningCode{lyrics3, id3v1}},
		{name: "APE then junk, known end", pieces: [][]byte{tagA, junk(300)}, end: stubLen, known: true,
			want: []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 1, 0}}, warns: []core.WarningCode{trailing}},
		{name: "APE then junk, unknown end", pieces: [][]byte{tagA, junk(300)},
			want:    []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 1, 0}},
			essence: []span{{KindJunk, 1, 1, 0}}, warns: []core.WarningCode{trailing}},
		{name: "junk, unknown end", pieces: [][]byte{junk(300)}},
		{name: "junk, known end", pieces: [][]byte{junk(300)}, end: stubLen, known: true,
			want: []span{{KindJunk, 0, 0, 0}}, warns: []core.WarningCode{trailing}},
		{name: "two APE tags", pieces: [][]byte{tagA, tagB},
			want: []span{{KindAPEv2, 0, 0, 0}, {KindAPEv2, 1, 1, 0}}, warns: []core.WarningCode{dup}},
		{name: "two APE tags then junk, known end", pieces: [][]byte{tagA, tagB, junk(300), v1("A")}, end: stubLen, known: true,
			want:  []span{{KindAPEv2, 0, 0, 0}, {KindAPEv2, 1, 1, 0}, {KindJunk, 2, 2, 0}, {KindID3v1, 3, 3, 1}},
			warns: []core.WarningCode{trailing, id3v1, dup}},
		{name: "two APE tags then junk, unknown end", pieces: [][]byte{tagA, tagB, junk(300), v1("A")},
			want:    []span{{KindAPEv2, 0, 0, 0}, {KindAPEv2, 1, 1, 0}, {KindJunk, 2, 2, 0}, {KindID3v1, 3, 3, 1}},
			essence: []span{{KindJunk, 2, 2, 0}}, warns: []core.WarningCode{trailing, id3v1, dup}},
		{name: "tag, junk, tag, junk, unknown end", pieces: [][]byte{tagA, junk(100), tagB, junk(300)},
			want:    []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 1, 0}, {KindAPEv2, 2, 2, 0}, {KindJunk, 3, 3, 0}},
			essence: []span{{KindJunk, 1, 1, 0}, {KindJunk, 3, 3, 0}}, warns: []core.WarningCode{trailing, trailing, dup}},
		{name: "headed tag at a known end that the search refuses", pieces: [][]byte{odd, junk(300)}, end: stubLen, known: true,
			want: []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 1, 0}}, warns: []core.WarningCode{trailing}},
		{name: "headed tag at a known end beyond the search window", pieces: [][]byte{tagA, junk(70_000)}, end: stubLen, known: true,
			want: []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 1, 0}}, warns: []core.WarningCode{trailing}},
		{name: "stacked tag starting before a known end", pieces: [][]byte{bare[:10], bare[10:], tagB, junk(300)},
			end: stubLen + 10, known: true, want: []span{{KindJunk, 1, 1, 0}, {KindAPEv2, 2, 2, 0}, {KindJunk, 3, 3, 0}},
			warns: []core.WarningCode{trailing, trailing}},
		{name: "tag stacked before a searched tag that the search refuses", pieces: [][]byte{oddBare, tagB, junk(300)},
			want:    []span{{KindAPEv2, 0, 0, 0}, {KindAPEv2, 1, 1, 0}, {KindJunk, 2, 2, 0}},
			essence: []span{{KindJunk, 2, 2, 0}}, warns: []core.WarningCode{trailing, dup}},
		{name: "header after a searched tag whose same-size footer announces no header",
			pieces:  [][]byte{bare, record(2000, 32, 0, 1<<31|1<<29), record(3000, 32, 0, 0), junk(300)},
			want:    []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 3, 0}},
			essence: []span{{KindJunk, 1, 3, 0}}, warns: []core.WarningCode{trailing}},
		{name: "headed tag right after a searched tag", pieces: [][]byte{bare, odd, junk(300)},
			want:    []span{{KindAPEv2, 0, 0, 0}, {KindAPEv2, 1, 1, 0}, {KindJunk, 2, 2, 0}},
			essence: []span{{KindJunk, 2, 2, 0}}, warns: []core.WarningCode{trailing, dup}},
		{name: "tag behind junk past the tag bound", pieces: append([][]byte{tagA, junk(300)}, run33[1:]...),
			want: []span{{KindID3v1, 2, 33, 32}}, warns: []core.WarningCode{id3v1}},
		{name: "33 stacked ID3v1", pieces: run33, end: stubLen, known: true,
			want: []span{{KindJunk, 0, 0, 0}, {KindID3v1, 1, 32, 32}}, warns: []core.WarningCode{trailing, id3v1}},
		{name: "APETAGEX in the junk after a tag", pieces: [][]byte{tagA, junk(100), fake, junk(100)},
			want:    []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 3, 0}},
			essence: []span{{KindJunk, 1, 3, 0}}, warns: []core.WarningCode{trailing}},
		{name: "APETAGEX in junk alone", pieces: [][]byte{junk(100), fake, junk(100)}},
		{name: "footer of a tag starting below the floor", pieces: [][]byte{misplaced},
			warns: []core.WarningCode{bad}},
		{name: "footer of a tag starting below the floor, known end", pieces: [][]byte{misplaced}, end: stubLen, known: true,
			want: []span{{KindJunk, 0, 0, 0}}, warns: []core.WarningCode{bad, trailing}},
		{name: "footer announcing a missing header, just above the floor", pieces: [][]byte{lying}, floor: stubLen - 8,
			want: []span{{KindAPEv2, 0, 0, 0}}},
		{name: "stacked tag announcing a missing header", pieces: [][]byte{lying, tagB, junk(300)}, end: stubLen, known: true,
			want:  []span{{KindAPEv2, 0, 0, 0}, {KindAPEv2, 1, 1, 0}, {KindJunk, 2, 2, 0}},
			warns: []core.WarningCode{trailing, dup}},
		{name: "header-first tag at a known end", pieces: [][]byte{tagA, junk(300), v1("A")}, end: stubLen, known: true,
			want:  []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 1, 0}, {KindID3v1, 2, 2, 1}},
			warns: []core.WarningCode{trailing, id3v1}},
		{name: "footer-only tag at a known end", pieces: [][]byte{bare, junk(300)}, end: stubLen, known: true,
			want: []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 1, 0}}, warns: []core.WarningCode{trailing}},
		{name: "searched tag between junk at a known end", pieces: [][]byte{junk(100), bare, junk(200)}, end: stubLen, known: true,
			want:  []span{{KindJunk, 0, 0, 0}, {KindAPEv2, 1, 1, 0}, {KindJunk, 2, 2, 0}},
			warns: []core.WarningCode{trailing, trailing}},
		{name: "searched tag of version 3000 at a known end", pieces: [][]byte{put32(bare, -24, 3000), junk(300)},
			end: stubLen, known: true, want: []span{{KindJunk, 0, 1, 0}}, warns: []core.WarningCode{trailing}},
		{name: "searched tag starting before a known end", pieces: [][]byte{bare[:10], bare[10:], junk(300)},
			end: stubLen + 10, known: true, want: []span{{KindJunk, 1, 2, 0}}, warns: []core.WarningCode{trailing}},
		{name: "tag beyond the search window at a known end", pieces: [][]byte{bare, junk(70_000)}, end: stubLen, known: true,
			want: []span{{KindJunk, 0, 1, 0}}, warns: []core.WarningCode{trailing}},
		{name: "tag beyond the search window, unknown end", pieces: [][]byte{tagA, junk(70_000)}},
		{name: "tag, junk, tag", pieces: [][]byte{tagA, junk(300), tagB}, end: stubLen, known: true,
			want:  []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 1, 0}, {KindAPEv2, 2, 2, 0}},
			warns: []core.WarningCode{trailing, dup}},
		{name: "header-first tag whose header and footer disagree",
			pieces: [][]byte{stretched, junk(100), bare, junk(50)}, end: stubLen, known: true,
			want:  []span{{KindJunk, 0, 1, 0}, {KindAPEv2, 2, 2, 0}, {KindJunk, 3, 3, 0}},
			warns: []core.WarningCode{trailing, trailing}},
		{name: "end past the boundary is unknown", pieces: [][]byte{tagA, junk(300), v1("A")},
			end:     stubLen + int64(len(tagA)) + 300 + 128,
			want:    []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 1, 0}, {KindID3v1, 2, 2, 1}},
			essence: []span{{KindJunk, 1, 1, 0}}, warns: []core.WarningCode{trailing, id3v1}},
		{name: "end below the floor is unknown", pieces: [][]byte{junk(300)}, end: 100},
		{name: "negative end is unknown", pieces: [][]byte{tagA, junk(300)}, end: -1,
			want:    []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 1, 0}},
			essence: []span{{KindJunk, 1, 1, 0}}, warns: []core.WarningCode{trailing}},
		{name: "searched tag of version 3000", pieces: [][]byte{put32(bare, -24, 3000), junk(300)}},
		{name: "searched tag counting more items than it holds", pieces: [][]byte{put32(bare, -16, 2), junk(300)}},
		{name: "searched tag holding more items than it counts", pieces: [][]byte{put32(bare, -16, 0), junk(300)}},
		{name: "searched tag missing its announced header", pieces: [][]byte{headless, junk(300)}},
		{name: "searched tag whose announced header is another tag's footer", pieces: [][]byte{bare, lying, junk(300)},
			want:    []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 2, 0}},
			essence: []span{{KindJunk, 1, 2, 0}}, warns: []core.WarningCode{trailing}},
		{name: "searched header record with no footer", pieces: [][]byte{headerOnly, junk(300)}},
		{name: "searched tag starting below the floor", pieces: [][]byte{bare, junk(300)}, floor: stubLen + 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file := withStub(tc.pieces...)
			floor := tc.floor
			if floor == 0 {
				floor = stubFloor
			}
			l, err := Scan(core.BytesSource(file), int64(len(file)), floor, testLimit, 0, endAt(tc.end))
			if err != nil {
				t.Fatal(err)
			}
			at := []int64{stubLen}
			for _, p := range tc.pieces {
				at = append(at, at[len(at)-1]+int64(len(p)))
			}
			var want []shape
			for _, s := range tc.want {
				want = append(want, shape{s.kind, at[s.first], at[s.last+1] - at[s.first], s.blocks})
			}
			if got := shapes(l.Regions); !slices.Equal(got, want) {
				t.Errorf("regions = %+v, want %+v", got, want)
			}
			start := int64(len(file))
			if len(want) > 0 {
				start = want[0].Off
			}
			if l.Start != start {
				t.Errorf("Start = %d, want %d", l.Start, start)
			}
			if l.EndKnown != tc.known {
				t.Errorf("EndKnown = %v, want %v", l.EndKnown, tc.known)
			}
			essence := [][2]int64{{0, start}}
			for _, s := range tc.essence {
				essence = append(essence, [2]int64{at[s.first], at[s.last+1]})
			}
			if got := l.Essence(0); !slices.Equal(got, essence) {
				t.Errorf("Essence = %v, want %v", got, essence)
			}
			for _, r := range l.Regions {
				switch r.Kind {
				case KindAPEv2:
					if r.Tag == nil || r.Tag.Offset != r.Off || r.Tag.Size != r.Len {
						t.Errorf("APE region %+v carries tag %+v", shape{r.Kind, r.Off, r.Len, r.Blocks}, r.Tag)
					}
				case KindID3v1:
					if !slices.Equal(r.Raw, file[r.Off:r.Off+r.Len]) {
						t.Errorf("ID3v1 run at %d holds %d bytes that are not the file's", r.Off, len(r.Raw))
					}
				}
			}
			if got := codes(Warnings(l, subject)); !slices.Equal(got, tc.warns) {
				t.Errorf("warnings = %v, want %v", got, tc.warns)
			}
		})
	}
}

// The codec is asked for the audio end once the tags at the end of the file are peeled, given
// where they begin; an error from it stops the scan.
func TestScanAudioEnd(t *testing.T) {
	tagA := titled(t, true, "A")
	file := withStub(junk(300), tagA, v1("A"))
	boundary := int64(-1)
	l := mustScan(t, file, func(_ core.ReaderAtSized, b int64) (int64, error) {
		boundary = b
		return stubLen, nil
	}, 0)
	if boundary != stubLen+300 {
		t.Errorf("boundary = %d, want %d", boundary, stubLen+300)
	}
	want := []shape{
		{KindJunk, stubLen, 300, 0},
		{KindAPEv2, stubLen + 300, int64(len(tagA)), 0},
		{KindID3v1, stubLen + 300 + int64(len(tagA)), 128, 1},
	}
	if got := shapes(l.Regions); !slices.Equal(got, want) {
		t.Errorf("regions = %+v, want %+v", got, want)
	}

	walk := errors.New("walk failed")
	_, err := Scan(core.BytesSource(file), int64(len(file)), stubFloor, testLimit, 0,
		func(core.ReaderAtSized, int64) (int64, error) { return 0, walk })
	if !errors.Is(err, walk) {
		t.Errorf("err = %v, want the audio end's error", err)
	}
}

// Every read is refused over the allocation limit, a tag included when it holds up as far as it can be
// read without its items; a candidate failing a check that reads no items, and a footer whose tag would
// start below the floor, are no tags and read nothing more. A read past the end of the file finds
// nothing.
func TestScanLimits(t *testing.T) {
	cover := apeTag(t, true, ape.Item{Key: "Cover Art (Front)", Data: make([]byte, 100_000), Flags: 2})
	coverHeadless := slices.Clone(cover)
	coverHeadless[0] = 'X'
	bare := titled(t, false, "A")
	misplaced := put32(bare[len(bare)-32:], -20, stubLen+32-100)
	tests := []struct {
		name   string
		pieces [][]byte
		end    int64 // a known end narrows the search window to the bytes after it
		limit  int64
		err    error
		warns  []core.WarningCode
	}{
		{"APE tag over the limit", [][]byte{cover}, 0, 64 << 10, waxerr.ErrSizeTooLarge, nil},
		{"APE tag over the limit, header-first at a known end", [][]byte{put32(cover, -24, 3000), junk(300)}, stubLen, 64 << 10, waxerr.ErrSizeTooLarge, nil},
		{"APE tag over the limit, found by the search", [][]byte{cover, junk(300)}, 0, 64 << 10, waxerr.ErrSizeTooLarge, nil},
		{"searched candidate over the limit missing its announced header", [][]byte{coverHeadless, junk(300)}, 0, 64 << 10, nil, nil},
		{"footer below the floor declaring more than the limit", [][]byte{misplaced}, stubLen, 2 << 10, nil,
			[]core.WarningCode{core.WarnMalformedTagEntry, core.WarnTrailingBytes}},
		{"header whose footer disagrees, declaring more than the limit", [][]byte{make([]byte, 2<<20), disagreeing(2 << 20)}, 0, 1 << 20, nil,
			[]core.WarningCode{core.WarnTrailingBytes}},
		{"header whose footer disagrees, declaring less than the limit", [][]byte{make([]byte, 2<<20), disagreeing(2 << 20)}, 0, 4 << 20, nil,
			[]core.WarningCode{core.WarnTrailingBytes}},
		{"ID3v1 block over the limit", [][]byte{v1("A")}, 0, 100, waxerr.ErrSizeTooLarge, nil},
		{"ID3v1 run over the limit", [][]byte{v1("A"), v1("B")}, stubLen, 200, waxerr.ErrSizeTooLarge, nil},
		{"Lyrics3v1 window over the limit", [][]byte{testfile.Lyrics3v1("la"), v1("A")}, 0, 2 << 10, waxerr.ErrSizeTooLarge, nil},
		{"search window over the limit", [][]byte{titled(t, true, "A"), junk(300)}, 0, 1 << 10, waxerr.ErrSizeTooLarge, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file := withStub(tc.pieces...)
			l, err := Scan(core.BytesSource(file), int64(len(file)), stubFloor, tc.limit, 0, endAt(tc.end))
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Errorf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := codes(Warnings(l, subject)); !slices.Equal(got, tc.warns) {
				t.Errorf("warnings = %v, want %v", got, tc.warns)
			}
		})
	}

	file := withStub(v1("A"))
	size := int64(len(file)) + 1000 // the size claims bytes the source does not hold
	l, err := Scan(core.BytesSource(file), size, stubFloor, testLimit, 0, nil)
	if err != nil || len(l.Regions) != 0 || l.Start != size {
		t.Errorf("short source: regions %+v, Start %d, err %v; want none, %d, nil", shapes(l.Regions), l.Start, err, size)
	}
}

// The scanner reads only inside the file, so each of its reads that fails is an I/O fault the scan
// returns, not a tag absent: the probes of the tags that end the file, a Lyrics3v1 block's window, an
// APE footer and the items it declares, the search window, and a header record at a known end.
func TestScanFailedRead(t *testing.T) {
	headed := titled(t, true, "AB") // its items are 16 bytes, a size no other read here has
	for _, tc := range []struct {
		name  string
		file  []byte
		end   int64
		fails func(off, n int64) bool
	}{
		{"the ID3v1 probe", withStub(v1("A")), 0, func(_, n int64) bool { return n == 128 }},
		{"the Lyrics3v2 probe", withStub(v1("A")), 0, func(_, n int64) bool { return n == lyrics2Footer }},
		{"a Lyrics3v1 block's window", withStub(testfile.Lyrics3v1("la"), v1("A")), 0, func(off, _ int64) bool { return off == stubFloor }},
		{"an APE footer", withStub(headed), 0, func(off, n int64) bool { return n == 32 && off == stubLen+int64(len(headed))-32 }},
		{"an APE tag's items", withStub(headed), 0, func(_, n int64) bool { return n == 16 }},
		{"the search window", withStub(headed, junk(300)), 0, func(_, n int64) bool { return n >= 1<<10 }},
		{"a header record at a known end", withStub(headed, junk(300)), stubLen, func(off, n int64) bool { return off == stubLen && n == 32 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &testfile.Faulty{Src: core.BytesSource(tc.file), Fails: tc.fails}
			if _, err := Scan(src, int64(len(tc.file)), stubFloor, testLimit, 0, endAt(tc.end)); !errors.Is(err, syscall.EIO) {
				t.Errorf("err = %v, want the read's own failure", err)
			}
		})
	}
}

// A writer keeping the tail copies it in file order; a strip keeps only the junk.
func TestSegments(t *testing.T) {
	tagA := titled(t, true, "A")
	file := withStub(tagA, junk(300), testfile.Lyrics3v2([2]string{"LYR", "la"}), v1("A"))
	l := mustScan(t, file, endAt(stubLen), 0)
	if got, want := Segments(l, false), []bits.Segment{bits.Copy(stubLen, int64(len(file))-stubLen)}; !reflect.DeepEqual(got, want) {
		t.Errorf("kept tail = %+v, want %+v", got, want)
	}
	if got, want := Segments(l, true), []bits.Segment{bits.Copy(stubLen+int64(len(tagA)), 300)}; !reflect.DeepEqual(got, want) {
		t.Errorf("stripped tail = %+v, want %+v", got, want)
	}
}

// The family view holds every ID3v1 block of a run once and the text items of each APE tag, never
// a Lyrics3 block; a codec that reads the APE tag as its own store projects the layout without it.
func TestFamilies(t *testing.T) {
	file := withStub(titled(t, true, "From APE"), testfile.Lyrics3v2([2]string{"LYR", "la"}), v1("Old"), v1("Older"))
	l := mustScan(t, file, nil, 0)
	auth := tag.NewTagSet()
	auth.Set(tag.Title, "Old")

	type entry struct {
		family   core.Family
		key      tag.Key
		value    string
		selected bool
	}
	entries := func(fs []core.FamilyValue) []entry {
		var out []entry
		for _, f := range fs {
			if !f.Legacy || len(f.Values) != 1 {
				t.Errorf("family %+v: want one legacy value", f)
			}
			out = append(out, entry{f.Family, f.Key, f.Values[0], f.Selected})
		}
		return out
	}
	want := []entry{
		{core.FamilyAPEv2, tag.Title, "From APE", false},
		{core.FamilyID3v1, tag.Title, "Old", true},
		{core.FamilyID3v1, tag.Artist, "Artist", true},
		{core.FamilyID3v1, tag.RecordingDate, "2001", true},
		{core.FamilyID3v1, tag.Title, "Older", false},
		{core.FamilyID3v1, tag.Artist, "Artist", true},
		{core.FamilyID3v1, tag.RecordingDate, "2001", true},
	}
	if got := entries(Families(auth, l)); !slices.Equal(got, want) {
		t.Errorf("families = %+v, want %+v", got, want)
	}
	if got := entries(Families(auth, l.Without(KindAPEv2))); !slices.Equal(got, want[1:]) {
		t.Errorf("families without the APE tag = %+v, want %+v", got, want[1:])
	}
}

// A Lyrics3 block, or an APE tag holding items the family view does not carry, is content a legacy
// strip cannot prove redundant; without its APE regions a layout reports only the Lyrics3 block.
func TestOpaque(t *testing.T) {
	cover := apeTag(t, true, ape.Item{Key: "Cover Art (Front)", Data: testfile.PNG(), Flags: 2})
	pair := apeTag(t, true, ape.Item{Key: "Title", Value: "A"}, ape.Item{Key: "Artist", Value: "B"})
	tests := []struct {
		name           string
		pieces         [][]byte
		maxElements    int
		opaque, native bool
	}{
		{"text tags", [][]byte{titled(t, true, "A"), v1("A")}, 0, false, false},
		{"Lyrics3", [][]byte{titled(t, true, "A"), testfile.Lyrics3v2([2]string{"LYR", "la"}), v1("A")}, 0, true, true},
		{"binary APE item", [][]byte{cover, v1("A")}, 0, true, false},
		{"APE items past the element cap", [][]byte{pair}, 1, true, false},
		{"APE items past the footer's count", [][]byte{put32(put32(pair, 16, 1), -16, 1)}, 0, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := mustScan(t, withStub(tc.pieces...), nil, tc.maxElements)
			if got := l.Opaque(); got != tc.opaque {
				t.Errorf("Opaque = %v, want %v", got, tc.opaque)
			}
			if got := l.Without(KindAPEv2).Opaque(); got != tc.native {
				t.Errorf("Opaque without the APE tag = %v, want %v", got, tc.native)
			}
		})
	}
}

// The authoritative APE tag is the one nearest the end of the file, whether the tags sit right before
// the end or behind junk.
func TestLayoutTag(t *testing.T) {
	tagA, tagB := titled(t, true, "A"), titled(t, true, "B")
	for _, tc := range []struct {
		name string
		file []byte
		end  int64
	}{
		{"stacked at the end", withStub(tagA, tagB, v1("A")), 0},
		{"stacked behind junk, known end", withStub(tagA, tagB, junk(300), v1("A")), stubLen},
		{"stacked behind junk, unknown end", withStub(tagA, tagB, junk(300), v1("A")), 0},
	} {
		if got := mustScan(t, tc.file, endAt(tc.end), 0).Tag(); got == nil || len(got.Items) != 1 || got.Items[0].Value != "B" {
			t.Errorf("%s: Tag = %+v, want the tag titled B", tc.name, got)
		}
	}
	if got := mustScan(t, withStub(v1("A")), nil, 0).Tag(); got != nil {
		t.Errorf("Tag = %+v, want none", got)
	}
}

// A layout holds tags when any region is more than junk.
func TestLayoutHasTags(t *testing.T) {
	for _, tc := range []struct {
		name string
		file []byte
		end  int64
		want bool
	}{
		{"no tail", withStub(), 0, false},
		{"junk alone", withStub(junk(300)), stubLen, false},
		{"an ID3v1 tag", withStub(v1("A")), 0, true},
		{"a Lyrics3 block behind junk", withStub(junk(300), testfile.Lyrics3v1("la")), stubLen, true},
	} {
		if got := mustScan(t, tc.file, endAt(tc.end), 0).HasTags(); got != tc.want {
			t.Errorf("%s: HasTags = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A clone shares no bytes or tags with the layout it copies.
func TestLayoutClone(t *testing.T) {
	l := mustScan(t, withStub(titled(t, true, "A"), v1("A"), v1("B")), nil, 0)
	if len(l.Regions) != 2 {
		t.Fatalf("regions = %+v, want an APE tag and an ID3v1 run", shapes(l.Regions))
	}
	c := l.Clone()
	if !reflect.DeepEqual(c, l) {
		t.Fatalf("clone = %+v, want %+v", c, l)
	}
	c.Regions[0].Tag.Items[0].Value = "changed"
	c.Regions[1].Raw[3] = 'X'
	c.Regions[1].Kind = KindJunk
	if l.Regions[0].Tag.Items[0].Value != "A" || l.Regions[1].Raw[3] != 'A' || l.Regions[1].Kind != KindID3v1 {
		t.Errorf("mutating the clone changed the layout: %+v", l.Regions)
	}
}

// The warnings say what they report: the junk's size and place, the Lyrics3 version, a run's block
// count, or in one warning the tags of runs apart, how many APE tags there are, and the tag a footer
// would start below the floor. Junk after a searched tag stays in the essence when the audio end is
// unknown, and its warning says so instead of placing it after the stream.
func TestWarningMessages(t *testing.T) {
	bare := titled(t, false, "A")
	tests := []struct {
		name string
		file []byte
		end  AudioEnd
		want map[core.WarningCode]string
		not  string
	}{
		{"known end", withStub(junk(300), titled(t, true, "A"), titled(t, true, "B"),
			testfile.Lyrics3v2([2]string{"LYR", "la"}), v1("A"), v1("B")), endAt(stubLen),
			map[core.WarningCode]string{
				core.WarnTrailingBytes:     "300 byte(s) " + subject + " belong to no tag",
				core.WarnTrailingLyrics3:   "Lyrics3v2",
				core.WarnTrailingID3v1:     "2 stacked",
				core.WarnDuplicateTagBlock: "2 APE tags follow the audio; APE readers see only the one nearest the end of the file",
			}, ""},
		{"ID3v1 tags apart", withStub(v1("A"), v1("B"), titled(t, true, "A"), v1("C")), nil,
			map[core.WarningCode]string{core.WarnTrailingID3v1: "3 legacy ID3v1 tags follow the audio in 2 places; preserved"}, ""},
		{"footer below the floor", withStub(put32(bare[len(bare)-32:], -20, stubLen+32-100)), nil,
			map[core.WarningCode]string{core.WarnMalformedTagEntry: "places its tag at offset 100"}, ""},
		{"footer announcing a header before the file", withStub(put32(put32(bare[len(bare)-32:], -20, stubLen+32), -12, 1<<31)), nil,
			map[core.WarningCode]string{core.WarnMalformedTagEntry: "places its tag at offset 0,"}, "offset -"},
		{"unknown end", withStub(titled(t, true, "A"), junk(300)), nil,
			map[core.WarningCode]string{core.WarnTrailingBytes: "300 byte(s) after an APE tag belong to no tag and stay in the audio essence"},
			subject},
		{"the stream going on after the tag", slices.Concat(pattern(0, stubLen), titled(t, true, "A"), pattern(stubLen, 600), v1("A")), walked(0, 0, nil),
			map[core.WarningCode]string{
				core.WarnTrailingBytes: "600 byte(s) after an APE tag are audio the stream goes on with across the tag, kept in the audio essence",
				core.WarnTrailingID3v1: "legacy ID3v1 tag",
			}, "belong to no tag"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, w := range Warnings(mustScanLegacy(t, tc.file, tc.end, 0), subject) {
				if want, ok := tc.want[w.Code]; !ok || !strings.Contains(w.Message, want) {
					t.Errorf("%s: %q does not say %q", w.Code, w.Message, want)
				}
				if tc.not != "" && strings.Contains(w.Message, tc.not) {
					t.Errorf("%s: %q places the bytes %s", w.Code, w.Message, tc.not)
				}
				delete(tc.want, w.Code)
			}
			for code := range tc.want {
				t.Errorf("no %s warning", code)
			}
		})
	}
}

// The junk ScanLegacy keeps in the essence is worded for where it sits: before the APE tag the stream
// goes on across, or after it, whether the end is known over the file without the tags or not.
func TestKeptJunkWording(t *testing.T) {
	// One unit of the stream after the counted bytes, the tag, then the stream going on: over the file
	// the count holds, as the unit is not two, and without the tag the stream goes on from the count.
	file := func(after ...[]byte) []byte {
		return slices.Concat(append([][]byte{pattern(0, stubLen), pattern(stubLen, 4), titled(t, true, "A"), pattern(stubLen+4, 600)}, after...)...)
	}
	for _, tc := range []struct {
		name string
		file []byte
		end  AudioEnd
		want []string // the trailing-bytes warnings, in order
	}{
		{"the end known without the tag", file(v1("A")), counted(0, stubLen, 0), []string{
			"4 byte(s) before an APE tag are audio the stream goes on with across the tag, kept in the audio essence",
			"600 byte(s) after an APE tag are audio the stream goes on with across the tag, kept in the audio essence",
		}},
		{"the end unknown without the tag", file(junk(2000)), counted(0, stubLen, 1024), []string{
			"4 byte(s) before an APE tag belong to no tag and stay in the audio essence, since the audio end is unknown",
			"2600 byte(s) after an APE tag belong to no tag and stay in the audio essence, since the audio end is unknown",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, w := range Warnings(mustScanLegacy(t, tc.file, tc.end, 0), subject) {
				if w.Code == core.WarnTrailingBytes {
					got = append(got, w.Message)
				}
			}
			if len(got) != len(tc.want) {
				t.Fatalf("trailing-bytes warnings = %q, want %d", got, len(tc.want))
			}
			for i, want := range tc.want {
				if !strings.HasPrefix(got[i], want) {
					t.Errorf("warning %d = %q, want it to begin %q", i, got[i], want)
				}
			}
		})
	}
}

// With an unknown end the junk after a searched tag stays in the essence, so the files a writer
// makes from the layout, with the junk moved ahead of a rewritten tag or the tag in front of it
// stripped, hold the same essence bytes; with a known end the junk stays out of all three.
func TestEssenceSurvivesRewrites(t *testing.T) {
	audio := make([]byte, stubLen)
	for i := range audio {
		audio[i] = byte(i*7 + 1)
	}
	tagA, tagB := titled(t, true, "A"), titled(t, true, "B")
	essence := func(b []byte, end int64) []byte {
		var out []byte
		for _, r := range mustScan(t, b, endAt(end), 0).Essence(0) {
			out = append(out, b[r[0]:r[1]]...)
		}
		return out
	}
	tests := []struct {
		name string
		tail [][]byte
		end  int64
		want []byte
	}{
		{"one tag, unknown end", [][]byte{tagA, junk(300), v1("A")}, 0, slices.Concat(audio, junk(300))},
		{"one tag, known end", [][]byte{tagA, junk(300), v1("A")}, stubLen, audio},
		{"stacked tags, unknown end", [][]byte{tagA, tagB, junk(300), v1("A")}, 0, slices.Concat(audio, junk(300))},
		{"stacked tags, known end", [][]byte{tagA, tagB, junk(300), v1("A")}, stubLen, audio},
		{"tags apart, unknown end", [][]byte{tagA, junk(100), tagB, junk(300), v1("A")}, 0, slices.Concat(audio, junk(100), junk(300))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file := slices.Concat(append([][]byte{audio}, tc.tail...)...)
			render := func(segs ...[]bits.Segment) []byte {
				var out bytes.Buffer
				if _, err := bits.Write(t.Context(), &out, bytes.NewReader(file), slices.Concat(segs...), nil); err != nil {
					t.Fatal(err)
				}
				return out.Bytes()
			}
			l := mustScan(t, file, endAt(tc.end), 0)
			audioCopy := []bits.Segment{bits.Copy(0, l.Start)}
			outputs := map[string][]byte{
				"source": file,
				"junk moved ahead of a rewritten tag": render(audioCopy,
					Segments(l.Without(KindAPEv2, KindLyrics3v1, KindLyrics3v2, KindID3v1), false),
					[]bits.Segment{bits.Lit(titled(t, true, "C"))},
					Segments(l.Without(KindAPEv2, KindJunk), false)),
				"tag stripped": render(audioCopy, Segments(l, true)),
			}
			for name, b := range outputs {
				if got := essence(b, tc.end); !bytes.Equal(got, tc.want) {
					t.Errorf("%s: essence of %d bytes differs from the %d expected", name, len(got), len(tc.want))
				}
			}
		})
	}
}

// A writer that keeps every tag after the audio as legacy moves the audio behind a new prefix and
// copies the tail with Segments. SetMovedEssence predicts, range for range, the essence a legacy scan
// of what it writes sets, with or without the strip, whether the codec's end is unknown, fixed, walked
// to the boundary as a frame walk is, or walked within a window as the MP3 resync is, and whether the
// stream goes on after a tag or junk follows it.
func TestMovedEssence(t *testing.T) {
	audio := pattern(0, stubLen)
	const at = 100
	goesOn := pattern(stubLen, 600)
	lyr := testfile.Lyrics3v2([2]string{"LYR", "la"})
	long := apeTag(t, true, ape.Item{Key: "Comment", Value: strings.Repeat("x", 2000)})
	tails := []struct {
		name string
		tail [][]byte
	}{
		{"tag then junk", [][]byte{titled(t, false, "A"), junk(300), v1("A")}},
		{"long tag then junk", [][]byte{long, junk(300), v1("A")}},
		{"tag then the stream going on", [][]byte{titled(t, false, "A"), goesOn, v1("A")}},
		{"tag, the stream going on, then junk", [][]byte{titled(t, false, "A"), goesOn, junk(300), v1("A")}},
		{"tags apart", [][]byte{titled(t, false, "A"), junk(100), titled(t, true, "B"), junk(300), lyr, v1("A"), v1("B")}},
		{"the stream going on between tags", [][]byte{titled(t, false, "A"), goesOn, titled(t, true, "B"), junk(300), lyr, v1("A"), v1("B")}},
		{"junk then a tag", [][]byte{junk(200), titled(t, true, "B"), v1("A")}},
		{"junk, a tag, then the stream going on", [][]byte{junk(200), titled(t, true, "B"), goesOn, v1("A")}},
		{"junk, a long tag, then junk", [][]byte{junk(10), long, junk(10)}},
		{"no tag", nil},
	}
	ends := []struct {
		name string
		end  func(start int64) AudioEnd
	}{
		{"unknown", func(int64) AudioEnd { return nil }},
		{"fixed", func(start int64) AudioEnd { return endAt(start + stubLen) }},
		{"walked", func(start int64) AudioEnd { return walked(start, 0, nil) }},
		{"walked in a window", func(start int64) AudioEnd { return walked(start, 1024, nil) }},
	}
	for _, tc := range tails {
		for _, e := range ends {
			for _, strip := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s, end %s, strip %v", tc.name, e.name, strip), func(t *testing.T) {
					file := slices.Concat(append([][]byte{audio}, tc.tail...)...)
					l := mustScanLegacy(t, file, e.end(0), 0)
					var out bytes.Buffer
					segs := slices.Concat([]bits.Segment{bits.Lit(make([]byte, at)), bits.Copy(0, l.Start)}, Segments(l, strip))
					if _, err := bits.Write(t.Context(), &out, bytes.NewReader(file), segs, nil); err != nil {
						t.Fatal(err)
					}
					var got, want core.Media
					l.SetMovedEssence(&got, 0, at, strip)
					mustScanLegacy(t, out.Bytes(), e.end(at), 0).SetEssence(&want, at)
					if !slices.Equal(got.EssenceRanges(), want.EssenceRanges()) || got.AudioEnd != want.AudioEnd {
						t.Errorf("predicted %v ending audio at %d, scanned %v ending it at %d",
							got.EssenceRanges(), got.AudioEnd, want.EssenceRanges(), want.AudioEnd)
					}
				})
			}
		}
	}
}

// ScanLegacy asks the codec's end again over the file without the tags of the stretch, as a strip
// leaves it, so the end cannot move with them: the junk the audio then runs into is kept in the
// essence, a known end splits the junk it falls in, and one before the first tag leaves junk ahead of
// it. The second measure is skipped when the stretch holds no tag. A tag in the search window that
// starts below the end the codec first claims ends the audio there.
func TestScanLegacy(t *testing.T) {
	audio := pattern(0, stubLen)
	tagA := titled(t, true, "A")
	long := apeTag(t, true, ape.Item{Key: "Comment", Value: strings.Repeat("x", 2000)})
	goesOn := pattern(stubLen, 600)
	const (
		id3v1    = core.WarnTrailingID3v1
		trailing = core.WarnTrailingBytes
	)
	tests := []struct {
		name    string
		pieces  [][]byte
		window  int64 // the walked codec's window, 0 for none
		end     AudioEnd
		want    []span
		known   bool
		essence []span // the junk the essence keeps
		calls   int
		warns   []core.WarningCode
	}{
		{name: "the stream going on after the tag", pieces: [][]byte{tagA, goesOn, v1("A")},
			want: []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 1, 0}, {KindID3v1, 2, 2, 1}}, known: true,
			essence: []span{{KindJunk, 1, 1, 0}}, calls: 2, warns: []core.WarningCode{trailing, id3v1}},
		{name: "the stream going on, then junk", pieces: [][]byte{tagA, goesOn, junk(300), v1("A")},
			want: []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 1, 0}, {KindJunk, 2, 2, 0}, {KindID3v1, 3, 3, 1}}, known: true,
			essence: []span{{KindJunk, 1, 1, 0}}, calls: 2, warns: []core.WarningCode{trailing, trailing, id3v1}},
		{name: "junk before the tag the stream goes on past", pieces: [][]byte{junk(100), tagA, goesOn, v1("A")},
			want: []span{{KindJunk, 0, 0, 0}, {KindAPEv2, 1, 1, 0}, {KindJunk, 2, 2, 0}, {KindID3v1, 3, 3, 1}}, known: true,
			calls: 2, warns: []core.WarningCode{trailing, trailing, id3v1}},
		{name: "a tag filling the window, then junk", pieces: [][]byte{long, junk(10)}, window: 1024,
			want: []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 1, 0}}, known: true, calls: 2, warns: []core.WarningCode{trailing}},
		{name: "end before the first tag", pieces: [][]byte{junk(10), long, junk(10)}, window: 1024,
			want: []span{{KindJunk, 0, 0, 0}, {KindAPEv2, 1, 1, 0}, {KindJunk, 2, 2, 0}}, known: true,
			calls: 2, warns: []core.WarningCode{trailing, trailing}},
		{name: "unknown over the file and without the tags", pieces: [][]byte{tagA, goesOn, v1("A")}, end: unknown,
			want:    []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 1, 0}, {KindID3v1, 2, 2, 1}},
			essence: []span{{KindJunk, 1, 1, 0}}, calls: 2, warns: []core.WarningCode{trailing, id3v1}},
		{name: "no tag in the stretch", pieces: [][]byte{junk(300), v1("A")},
			want: []span{{KindJunk, 0, 0, 0}, {KindID3v1, 1, 1, 1}}, known: true, calls: 1, warns: []core.WarningCode{trailing, id3v1}},
		{name: "peeled tags only", pieces: [][]byte{tagA, v1("A")},
			want: []span{{KindAPEv2, 0, 0, 0}, {KindID3v1, 1, 1, 1}}, known: true, calls: 1, warns: []core.WarningCode{id3v1}},
		{name: "a claim ending inside a tag", pieces: [][]byte{long, junk(10)}, end: claiming(stubLen + 100),
			want: []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 1, 0}}, known: true, calls: 2, warns: []core.WarningCode{trailing}},
		{name: "a claim running past a whole tag", pieces: [][]byte{tagA, junk(300)}, end: claiming(stubLen + int64(len(tagA)) + 100),
			want: []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 1, 0}}, known: true, calls: 2, warns: []core.WarningCode{trailing}},
		{name: "a claim ending where a tag starts", pieces: [][]byte{tagA, junk(10)}, end: claiming(stubLen),
			want: []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 1, 0}}, known: true, calls: 2, warns: []core.WarningCode{trailing}},
		{name: "a tag at the end, then more junk than the window", pieces: [][]byte{tagA, junk(70000)},
			want: []span{{KindAPEv2, 0, 0, 0}, {KindJunk, 1, 1, 0}}, known: true, calls: 2, warns: []core.WarningCode{trailing}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file := slices.Concat(append([][]byte{audio}, tc.pieces...)...)
			calls := 0
			end := tc.end
			if end == nil {
				end = walked(0, tc.window, &calls)
			} else {
				e := end
				end = func(src core.ReaderAtSized, b int64) (int64, error) { calls++; return e(src, b) }
			}
			l := mustScanLegacy(t, file, end, 0)
			offsets := []int64{stubLen}
			for _, p := range tc.pieces {
				offsets = append(offsets, offsets[len(offsets)-1]+int64(len(p)))
			}
			spanOf := func(sp span) shape {
				return shape{sp.kind, offsets[sp.first], offsets[sp.last+1] - offsets[sp.first], sp.blocks}
			}
			var want []shape
			for _, sp := range tc.want {
				want = append(want, spanOf(sp))
			}
			if got := shapes(l.Regions); !slices.Equal(got, want) {
				t.Errorf("regions = %+v, want %+v", got, want)
			}
			if l.EndKnown != tc.known || calls != tc.calls {
				t.Errorf("EndKnown %v after %d calls, want %v after %d", l.EndKnown, calls, tc.known, tc.calls)
			}
			essence := [][2]int64{{0, l.Start}}
			for _, sp := range tc.essence {
				e := spanOf(sp)
				essence = append(essence, [2]int64{e.Off, e.Off + e.Len})
			}
			if got := l.Essence(0); !slices.Equal(got, essence) {
				t.Errorf("Essence = %v, want %v", got, essence)
			}
			if got := codes(Warnings(l, subject)); !slices.Equal(got, tc.warns) {
				t.Errorf("warnings = %v, want %v", got, tc.warns)
			}
		})
	}
}

// Scan, for the APE-tagged formats, keeps the end the codec places: the bytes of a tag that starts
// below it stay audio, and the rest of the tag is junk.
func TestScanKeepsTheEndOverATag(t *testing.T) {
	long := apeTag(t, true, ape.Item{Key: "Comment", Value: strings.Repeat("x", 2000)})
	l := mustScan(t, withStub(long, junk(10)), endAt(stubLen+100), 0)
	want := []shape{{KindJunk, stubLen + 100, int64(len(long)) - 100 + 10, 0}}
	if got := shapes(l.Regions); !slices.Equal(got, want) || !l.EndKnown {
		t.Errorf("regions %+v, EndKnown %v; want %+v from the known end", got, l.EndKnown, want)
	}
}

// Operations names each kind of tag a legacy write keeps or strips, with how many there are, and
// Entries lists every region for the native view as legacy.
func TestOperationsAndEntries(t *testing.T) {
	file := withStub(titled(t, false, "A"), junk(100), titled(t, true, "B"), junk(300),
		testfile.Lyrics3v2([2]string{"LYR", "la"}), v1("A"), v1("B"))
	l := mustScan(t, file, endAt(stubLen), 0)
	for _, strip := range []bool{false, true} {
		verb := map[bool]string{false: "preservation", true: "strip"}[strip]
		want := []string{"trailing APEv2 " + verb + " (2 tags)", "trailing Lyrics3v2 " + verb, "trailing ID3v1 " + verb + " (2 stacked tags)"}
		if got := Operations(l, strip); !slices.Equal(got, want) {
			t.Errorf("strip %v: operations = %q, want %q", strip, got, want)
		}
	}
	if got := Operations(l.Without(KindAPEv2, KindLyrics3v2), false); !slices.Equal(got, []string{"trailing ID3v1 preservation (2 stacked tags)"}) {
		t.Errorf("ID3v1 run alone: operations = %q", got)
	}
	var kinds, notes []string
	for _, e := range l.Entries("test audio") {
		kinds, notes = append(kinds, e.Kind), append(notes, e.Note)
	}
	if want := []string{"APEv2", "trailing bytes", "APEv2", "trailing bytes", "Lyrics3v2", "ID3v1"}; !slices.Equal(kinds, want) {
		t.Errorf("entry kinds = %q, want %q", kinds, want)
	}
	if want := []string{"1 items, legacy, preserved", "preserved", "1 items, legacy, preserved", "preserved",
		"legacy, preserved, not read", "2 stacked tags, legacy, preserved"}; !slices.Equal(notes, want) {
		t.Errorf("entry notes = %q, want %q", notes, want)
	}
	// The stream going on across a tag is audio, named as the codec names its audio; junk kept in the
	// essence only because the end is unknown stays trailing bytes.
	goesOn := slices.Concat(pattern(0, stubLen), titled(t, true, "A"), pattern(stubLen, 600), junk(300), v1("A"))
	for _, c := range []struct {
		end  AudioEnd
		want []string
	}{
		{walked(0, 0, nil), []string{"APEv2", "test audio", "trailing bytes", "ID3v1"}},
		{unknown, []string{"APEv2", "trailing bytes", "ID3v1"}},
	} {
		kinds = nil
		for _, e := range mustScanLegacy(t, goesOn, c.end, 0).Entries("test audio") {
			kinds = append(kinds, e.Kind)
			if e.Kind == "test audio" && !strings.Contains(e.Note, "essence") {
				t.Errorf("the stream going on across the tag is noted %q, not as kept in the essence", e.Note)
			}
		}
		if !slices.Equal(kinds, c.want) {
			t.Errorf("entry kinds = %q, want %q", kinds, c.want)
		}
	}
}

// counting counts the bytes read from a source.
type counting struct {
	core.ReaderAtSized
	n *int64
}

func (c counting) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.ReaderAtSized.ReadAt(p, off)
	*c.n += int64(n)
	return n, err
}

// However many footers the search window holds that pass every check reading no items, their item
// reads share one budget of the allocation limit, so the scan reads a bounded number of bytes: the
// first such candidate the budget cannot cover is a tag too large to read, refused.
func TestSearchBudget(t *testing.T) {
	const limit = 1 << 20
	// Footers 24 bytes apart, the next preamble standing in for each one's reserved bytes, each
	// declaring 512 KiB of items that do not hold up.
	var tail []byte
	for len(tail)+32 <= searchWindow-16 {
		rec := make([]byte, 24)
		copy(rec, "APETAGEX")
		binary.LittleEndian.PutUint32(rec[8:], 2000)
		binary.LittleEndian.PutUint32(rec[12:], 512<<10+32)
		binary.LittleEndian.PutUint32(rec[16:], 1)
		tail = append(tail, rec...)
	}
	file := slices.Concat(make([]byte, 1<<20), tail, make([]byte, 8), junk(8))
	var n int64
	_, err := Scan(counting{core.BytesSource(file), &n}, int64(len(file)), stubFloor, limit, 0, nil)
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("err %v; want the candidate past the budget refused as too large", err)
	}
	if bound := int64(searchWindow + limit + 64<<10); n > bound {
		t.Errorf("read %d bytes for %d candidates, want at most %d", n, bytes.Count(tail, []byte("APETAGEX")), bound)
	}
}

// A tag behind footers whose items do not hold up is refused once their item reads spend the budget,
// not hashed as junk, and found when the budget covers them all.
func TestSearchBudgetRefusesTheTagBehind(t *testing.T) {
	decoy := make([]byte, 32)
	copy(decoy, "APETAGEX")
	binary.LittleEndian.PutUint32(decoy[8:], 2000)
	binary.LittleEndian.PutUint32(decoy[12:], 40<<10+32)
	binary.LittleEndian.PutUint32(decoy[16:], 1)
	const at = 100 << 10
	file := slices.Concat(make([]byte, at), titled(t, false, "Behind"), junk(100), decoy, junk(100), decoy, junk(8))
	if _, err := Scan(core.BytesSource(file), int64(len(file)), stubFloor, 64<<10, 0, nil); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Errorf("limit 64 KiB: err %v; want the tag behind the spent budget refused", err)
	}
	l, err := Scan(core.BytesSource(file), int64(len(file)), stubFloor, testLimit, 0, nil)
	if err != nil || !slices.ContainsFunc(l.Regions, func(r Region) bool { return r.Kind == KindAPEv2 && r.Off == at }) {
		t.Errorf("limit 1 MiB: regions %+v, err %v; want the tag at %d", shapes(l.Regions), err, at)
	}
}

// A candidate whose items the search window holds walks them there and reads nothing more.
func TestSearchReadsItemsInTheWindow(t *testing.T) {
	file := slices.Concat(make([]byte, 100<<10), apeTag(t, false, ape.Item{Key: "Title", Value: strings.Repeat("x", 8<<10)}), junk(100))
	var n int64
	l, err := Scan(counting{core.BytesSource(file), &n}, int64(len(file)), stubFloor, testLimit, 0, nil)
	if err != nil || !slices.ContainsFunc(l.Regions, func(r Region) bool { return r.Kind == KindAPEv2 }) {
		t.Fatalf("regions %+v, err %v; want the tag", shapes(l.Regions), err)
	}
	if bound := int64(searchWindow + 4<<10); n > bound {
		t.Errorf("read %d bytes, want at most %d", n, bound)
	}
}

// A header record right after a found tag whose footer does not answer it costs the two records,
// however many such headers follow found tags and however much their footers declare.
func TestHeaderFirstReads(t *testing.T) {
	const limit = 1 << 20
	audio := make([]byte, limit+4096)
	var units []byte
	for range 32 {
		units = append(units, disagreeing(limit)...)
	}
	file := slices.Concat(audio, units, junk(8))
	for _, end := range []int64{0, int64(len(audio))} {
		var n int64
		l, err := Scan(counting{core.BytesSource(file), &n}, int64(len(file)), stubFloor, limit, 0, endAt(end))
		if err != nil || len(l.Regions) != 64 {
			t.Fatalf("end %d: %d regions, err %v; want 32 tags and their junk", end, len(l.Regions), err)
		}
		if bound := int64(searchWindow + 16<<10); n > bound {
			t.Errorf("end %d: read %d bytes, want at most %d", end, n, bound)
		}
	}
}

// FuzzScan checks that every layout tiles the tail and keeps whole junk regions, in order, in the
// essence: all of them with an unknown end, none from a plain scan with a known one. ScanLegacy runs
// over the stream the walked codec reads, its window picked by the fuzzer, so its end depends on the
// bytes before the tags: what a rewrite that keeps the tail writes, and what a strip leaves unless the
// strip leaves a tag behind, read back with the essence the layout predicts, the same bytes.
func FuzzScan(f *testing.F) {
	tagA := titled(f, true, "A")
	f.Add(slices.Concat(tagA, testfile.Lyrics3v2([2]string{"LYR", "la"}), v1("A")), int64(0))
	f.Add(slices.Concat(tagA, junk(300)), int64(0))
	f.Add(slices.Concat(tagA, junk(300), v1("A"), v1("B")), int64(stubLen))
	f.Add(slices.Concat(testfile.Lyrics3v1("la"), v1("A")), int64(-1))
	f.Add(slices.Concat(tagA, pattern(stubLen, 600), v1("A")), int64(0))
	f.Add(slices.Concat(pattern(stubLen, 4), tagA, pattern(stubLen+4, 600), junk(300), v1("A")), int64(1024))
	f.Add(slices.Concat(apeTag(f, true, ape.Item{Key: "Comment", Value: strings.Repeat("x", 2000)}), junk(10)), int64(1024))
	f.Fuzz(func(t *testing.T, tail []byte, end int64) {
		file := withStub(tail)
		l, err := Scan(core.BytesSource(file), int64(len(file)), stubFloor, testLimit, 0, endAt(end))
		if err != nil {
			t.Fatal(err)
		}
		checkLayout(t, "Scan", l, int64(len(file)), true)

		window := max(end, 0) % 4096
		file = slices.Concat(pattern(0, stubLen), tail)
		l, err = ScanLegacy(core.BytesSource(file), int64(len(file)), stubFloor, testLimit, 0, walked(0, window, nil))
		if err != nil {
			t.Fatal(err)
		}
		checkLayout(t, "ScanLegacy", l, int64(len(file)), false)
		const at = 64
		for _, strip := range []bool{false, true} {
			var out bytes.Buffer
			segs := slices.Concat([]bits.Segment{bits.Lit(make([]byte, at)), bits.Copy(0, l.Start)}, Segments(l, strip))
			if _, err := bits.Write(t.Context(), &out, bytes.NewReader(file), segs, nil); err != nil {
				t.Fatal(err)
			}
			re, err := ScanLegacy(core.BytesSource(out.Bytes()), int64(out.Len()), stubFloor+at, testLimit, 0, walked(at, window, nil))
			if err != nil {
				t.Fatal(err)
			}
			if strip && slices.ContainsFunc(re.Regions, func(r Region) bool { return r.Kind != KindJunk }) {
				continue // the strip left a tag behind, which the search had not reached
			}
			var got, want core.Media
			l.SetMovedEssence(&got, 0, at, strip)
			re.SetEssence(&want, at)
			if !slices.Equal(got.EssenceRanges(), want.EssenceRanges()) {
				t.Fatalf("strip %v: predicted essence %v, read back %v", strip, got.EssenceRanges(), want.EssenceRanges())
			}
			if !bytes.Equal(essenceBytes(file, l.Essence(0)), essenceBytes(out.Bytes(), re.Essence(at))) {
				t.Fatalf("strip %v: the essence read back holds other bytes", strip)
			}
		}
	})
}

// checkLayout fails unless l tiles the tail of a file size bytes long from its Start and its essence
// keeps whole junk regions in order: every one with an unknown end, and none with a known end when
// plain is set, as Scan keeps none then.
func checkLayout(t *testing.T, name string, l Layout, size int64, plain bool) {
	t.Helper()
	at := l.Start
	if at < stubFloor {
		t.Fatalf("%s: Start %d is below the floor", name, at)
	}
	for _, r := range l.Regions {
		if r.Off != at || r.Len <= 0 {
			t.Fatalf("%s: regions %+v do not tile the tail from %d", name, shapes(l.Regions), l.Start)
		}
		at += r.Len
	}
	if at != size {
		t.Fatalf("%s: regions %+v end at %d, not at %d", name, shapes(l.Regions), at, size)
	}
	essence := l.Essence(0)
	if essence[0] != [2]int64{0, l.Start} {
		t.Fatalf("%s: essence %v does not open with the audio up to %d", name, essence, l.Start)
	}
	var junk []Region
	for _, r := range l.Regions {
		if r.Kind == KindJunk {
			junk = append(junk, r)
		}
	}
	kept := 0
	for _, e := range essence[1:] {
		for kept < len(junk) && (junk[kept].Off != e[0] || junk[kept].Off+junk[kept].Len != e[1]) {
			kept++
		}
		if kept == len(junk) {
			t.Fatalf("%s: essence %v keeps %v, which is not a junk region in order", name, essence, e)
		}
		kept++
	}
	if n := len(essence) - 1; !l.EndKnown && n != len(junk) || plain && l.EndKnown && n != 0 {
		t.Fatalf("%s: essence %v keeps %d junk regions of %d (known %v)", name, essence, n, len(junk), l.EndKnown)
	}
}

// essenceBytes is the bytes of b the ranges cover, in order.
func essenceBytes(b []byte, ranges [][2]int64) []byte {
	var out []byte
	for _, r := range ranges {
		out = append(out, b[r[0]:r[1]]...)
	}
	return out
}
