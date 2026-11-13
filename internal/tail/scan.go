// Package tail reads what follows the audio at the end of a file: stacked ID3v1 tags, Lyrics3
// blocks, APE tags and junk, in any order. [Scan] peels them into a [Layout]; writers that keep
// the tail copy it with [Segments], [Families] projects it into the family view, and [Warnings]
// reports it. A stream whose APE tag is its tag store reads and rewrites its tail as a [Trailer].
package tail

import (
	"errors"
	"io"
	"slices"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
)

// Scan takes at most maxRegions tags, each ID3v1 block counting as one, and searches the
// searchWindow bytes before the tags it peels from the end of the file for APE tags behind junk.
const (
	maxRegions   = 32
	searchWindow = 64 << 10
)

// Kind names what a [Region] holds.
type Kind uint8

const (
	KindID3v1 Kind = iota + 1
	KindLyrics3v1
	KindLyrics3v2
	KindAPEv2 // an APE tag of either version
	KindJunk  // bytes that belong to neither the audio nor a tag
)

func (k Kind) String() string {
	switch k {
	case KindID3v1:
		return "ID3v1"
	case KindLyrics3v1:
		return "Lyrics3v1"
	case KindLyrics3v2:
		return "Lyrics3v2"
	case KindAPEv2:
		return "APEv2"
	case KindJunk:
		return "junk"
	default:
		return "unknown"
	}
}

// Region is one stretch of the tail: a tag, a run of stacked ID3v1 tags, or junk.
type Region struct {
	Kind     Kind
	Off, Len int64
	// Tag is the parsed tag of a KindAPEv2 region.
	Tag *ape.Tag
	// Blocks counts the stacked 128-byte tags of a KindID3v1 run; Raw holds the run's bytes.
	Blocks int
	Raw    []byte

	// audio marks junk the essence keeps: every junk region while the audio end is unknown, and
	// the junk the audio runs into when [ScanLegacy] measures the end without the tags.
	audio bool
}

// Layout is the tail of a file, its regions in file order. Start is where the audio ends: the
// first region's offset, or the file size when there is none. EndKnown records that the codec's
// audio end was taken. [Layout.Essence] keeps the junk marked as audio: every junk region when the
// end is unknown, as such junk is known only by the tags around it and may be audio, and, from
// [ScanLegacy], the junk the stream goes on with across a tag once its end is measured without them.
type Layout struct {
	Regions  []Region
	Start    int64
	EndKnown bool

	// misplaced is an APE footer where the peel stopped that places its tag's start below the
	// floor: Off is that start.
	misplaced Region
}

// AudioEnd reports where a codec's audio ends in src, given boundary, the offset where the regions
// Scan peeled from the end of the file begin. A negative end means the codec cannot tell. src is the
// file, or the view of it without its tags that [ScanLegacy] asks over: every read goes through it.
type AudioEnd func(src core.ReaderAtSized, boundary int64) (int64, error)

// Scan reads the tail of src, size bytes long. floor is the end of the first audio unit or header,
// and no region starts before it. Scan peels tags off the end of the file in any order, reading an
// APE footer as leniently as [ape.ParseAt] does. A footer whose tag would start below floor stops
// the peel, and [Warnings] reports it. end, nil when the codec cannot tell, says where the audio
// ends. The stretch from there to the peeled tags holds APE tags and junk. Without an end, or with
// one below floor or past the peeled tags, the stretch starts at floor and the bytes before its
// first tag stay audio. In the stretch, tags are found by [ape.Search], by a header record where a
// known end or a found tag ends, and by a footer where a found tag starts. Every read is refused
// over limit with [waxerr.ErrSizeTooLarge], and so is a search whose candidates' items pass it
// together; a read past the end of the file finds nothing, and one that fails inside it is an
// error.
func Scan(src core.ReaderAtSized, size, floor, limit int64, maxElements int, end AudioEnd) (Layout, error) {
	return scan(src, size, floor, limit, maxElements, end, false)
}

// ScanLegacy scans as [Scan] does for a stream whose every tail tag is legacy, which a strip removes
// leaving the audio and the junk. Its search starts at floor even when end is known, and a tag it
// finds below that end wins over the audio the codec claims there: the audio ends at the tag, as
// with an unknown end. When the stretch before the peeled tags holds a tag, end is asked again over
// the file without the tags, as a strip leaves it, and the junk the audio then runs into stays in the
// essence: the essence does not depend on the tags being there, so a strip keeps the digest and the
// plan of one predicts what the stripped file reads.
func ScanLegacy(src core.ReaderAtSized, size, floor, limit int64, maxElements int, end AudioEnd) (Layout, error) {
	return scan(src, size, floor, limit, maxElements, end, true)
}

// Peeled returns where the tags [Scan] peels off the end of src begin, size when there are none,
// placing each APE tag by its footer with no item read. A stream whose units are checksummed asks
// it before it walks them, to tell a unit running past that offset from one cut short that a tag
// only seems to complete.
func Peeled(src core.ReaderAtSized, size, floor, limit int64) (int64, error) {
	s := &scanner{src: src, floor: floor, limit: limit, steps: maxRegions, footersOnly: true}
	_, b, err := s.peel(size)
	return b, err
}

func scan(src core.ReaderAtSized, size, floor, limit int64, maxElements int, end AudioEnd, legacy bool) (Layout, error) {
	s := &scanner{src: src, floor: floor, limit: limit, maxElements: maxElements, steps: maxRegions}
	rs, b, err := s.peel(size)
	if err != nil {
		return Layout{}, err
	}
	before, known, err := s.before(b, end, legacy)
	if err != nil {
		return Layout{}, err
	}
	rs = append(rs, before...)
	slices.Reverse(rs)
	l := Layout{Regions: rs, Start: size, EndKnown: known, misplaced: s.misplaced}
	if len(rs) > 0 {
		l.Start = rs[0].Off
	}
	l.place(known, l.Start)
	if legacy && end != nil && slices.ContainsFunc(before, func(r Region) bool { return r.Kind != KindJunk }) {
		v := l.stripped(src)
		e, err := end(v, v.size)
		if err != nil {
			return Layout{}, err
		}
		l.place(e >= floor && e <= v.size, e)
	}
	return l, nil
}

// place marks the junk the essence keeps, given the audio end e, known or not, measured over
// [Layout.stripped]: with an unknown end every junk region; with a known one the junk before it,
// the junk region it falls in split there, and junk ahead of the first region when it falls before
// Start, which then moves back to it. Over the file itself a known end is Start, so no junk is kept.
func (l *Layout) place(known bool, e int64) {
	l.EndKnown = known
	if !known {
		for i := range l.Regions {
			l.Regions[i].audio = l.Regions[i].Kind == KindJunk
		}
		return
	}
	if e < l.Start {
		l.Regions = append([]Region{{Kind: KindJunk, Off: e, Len: l.Start - e}}, l.Regions...)
		l.Start = e
	}
	out := make([]Region, 0, len(l.Regions)+1)
	at := l.Start // where the next region sits once the tags are cut
	for _, r := range l.Regions {
		if r.Kind != KindJunk {
			out = append(out, r)
			continue
		}
		from := at
		at += r.Len
		switch {
		case at <= e:
			r.audio = true
		case from >= e:
			r.audio = false
		default:
			n := e - from
			out = append(out, Region{Kind: KindJunk, Off: r.Off, Len: n, audio: true})
			r.Off, r.Len, r.audio = r.Off+n, r.Len-n, false
		}
		out = append(out, r)
	}
	l.Regions = out
}

// Essence returns the essence ranges of a stream that starts at start: the audio up to Start and the
// junk the layout keeps as audio, every junk region when the end is unknown. Kept there, such junk
// hashes the same once a rewrite moves it ahead of the tags or a strip removes them.
func (l Layout) Essence(start int64) [][2]int64 {
	out := [][2]int64{{start, l.Start}}
	for _, r := range l.Regions {
		if r.audio {
			out = append(out, [2]int64{r.Off, r.Off + r.Len})
		}
	}
	return out
}

// stripped is src as a legacy strip leaves it: the bytes up to Start, then each junk region in file
// order, the tags cut out.
func (l Layout) stripped(src core.ReaderAtSized) *stripped {
	v := &stripped{src: src, keep: [][2]int64{{0, l.Start}}, size: l.Start}
	for _, r := range l.Regions {
		if r.Kind == KindJunk {
			v.keep = append(v.keep, [2]int64{r.Off, r.Off + r.Len})
			v.size += r.Len
		}
	}
	return v
}

// stripped reads the kept ranges of a file, in order, as one.
type stripped struct {
	src  core.ReaderAtSized
	keep [][2]int64
	size int64
}

func (v *stripped) Size() int64 { return v.size }

// ReadAt fills p from off across the kept ranges; a read past the end returns what it got and
// io.EOF, as [io.ReaderAt] asks.
func (v *stripped) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("tail: negative offset")
	}
	n := 0
	at := int64(0) // where the current range sits in the view
	for _, k := range v.keep {
		kl := k[1] - k[0]
		if n < len(p) && off+int64(n) < at+kl {
			skip := off + int64(n) - at
			want := min(len(p)-n, int(kl-skip))
			m, err := v.src.ReadAt(p[n:n+want], k[0]+skip)
			n += m
			if m < want {
				if err == nil {
					err = io.ErrUnexpectedEOF
				}
				return n, err
			}
			if err != nil && !errors.Is(err, io.EOF) {
				return n, err
			}
		}
		at += kl
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// SetEssence sets m's essence to [Layout.Essence] of a stream that starts at start, AudioStart and
// AudioEnd bounding the audio up to Start, and returns how many bytes it covers, the figure an
// average bitrate counts.
func (l Layout) SetEssence(m *core.Media, start int64) int64 {
	ranges := l.Essence(start)
	m.AudioStart, m.AudioEnd = start, l.Start
	if len(ranges) > 1 {
		m.AudioRanges = ranges
	}
	return covered(ranges)
}

// SetMovedEssence sets m's essence to what a fresh read finds after a rewrite that moves the stream
// at start to at and copies what follows the audio as [Segments] does with strip. The ranges of
// [Layout.Essence] move with the stream. A strip leaves only junk after the audio, so the junk the
// essence keeps, which comes first among the junk, then follows the audio and the ranges join into
// one.
func (l Layout) SetMovedEssence(m *core.Media, start, at int64, strip bool) {
	ranges := l.Essence(start)
	if strip {
		ranges = [][2]int64{{start, start + covered(ranges)}}
	}
	for i := range ranges {
		ranges[i][0] += at - start
		ranges[i][1] += at - start
	}
	m.AudioStart, m.AudioEnd = ranges[0][0], ranges[0][1]
	if len(ranges) > 1 {
		m.AudioRanges = ranges
	}
}

// covered is how many bytes ranges cover.
func covered(ranges [][2]int64) int64 {
	var n int64
	for _, r := range ranges {
		n += r[1] - r[0]
	}
	return n
}

// Clone deep-copies l so a Document holding it stays detached.
func (l Layout) Clone() Layout {
	l.Regions = slices.Clone(l.Regions)
	for i := range l.Regions {
		l.Regions[i].Tag = l.Regions[i].Tag.Clone()
		l.Regions[i].Raw = slices.Clone(l.Regions[i].Raw)
	}
	return l
}

// Tag returns the authoritative APE tag, the one nearest the end of the file, or nil.
func (l Layout) Tag() *ape.Tag {
	for _, r := range slices.Backward(l.Regions) {
		if r.Kind == KindAPEv2 {
			return r.Tag
		}
	}
	return nil
}

// HasTags reports whether l holds a tag: any region but junk.
func (l Layout) HasTags() bool {
	return slices.ContainsFunc(l.Regions, func(r Region) bool { return r.Kind != KindJunk })
}

// Opaque reports whether l holds legacy content [Families] cannot carry, which a legacy strip
// would destroy unproven: a Lyrics3 block, or an APE tag with a non-text item, more items than
// the element limit let it read, or item bytes no item was read from. As there, every region
// counts as legacy.
func (l Layout) Opaque() bool {
	for _, r := range l.Regions {
		switch r.Kind {
		case KindLyrics3v1, KindLyrics3v2:
			return true
		case KindAPEv2:
			if r.Tag.Truncated || r.Tag.UnreadTail > 0 || slices.ContainsFunc(r.Tag.Items, ape.Item.NonText) {
				return true
			}
		}
	}
	return false
}

// Without returns l without its regions of the given kinds; Start stays where it was.
func (l Layout) Without(kinds ...Kind) Layout {
	l.Regions = slices.DeleteFunc(slices.Clone(l.Regions), func(r Region) bool {
		return slices.Contains(kinds, r.Kind)
	})
	return l
}

type scanner struct {
	src         core.ReaderAtSized
	floor       int64
	limit       int64
	maxElements int
	steps       int // tags still to take
	misplaced   Region
	footersOnly bool // an APE tag is placed by its footer, its items unread
}

// peel takes tags off the end of the file, size bytes long, and returns them nearest the end
// first with the offset where they begin. Consecutive ID3v1 tags join one run.
func (s *scanner) peel(size int64) ([]Region, int64, error) {
	var rs []Region
	pos := size
	for ; s.steps > 0; s.steps-- {
		r, ok, err := s.next(pos)
		if err != nil {
			return nil, 0, err
		}
		if !ok {
			break
		}
		if n := len(rs); n > 0 && r.Kind == KindID3v1 && rs[n-1].Kind == KindID3v1 {
			run := &rs[n-1]
			if err := bits.CheckAlloc(run.Len+r.Len, s.limit); err != nil {
				return nil, 0, err
			}
			run.Off, run.Len, run.Blocks, run.Raw = r.Off, run.Len+r.Len, run.Blocks+1, append(r.Raw, run.Raw...)
		} else {
			rs = append(rs, r)
		}
		pos = r.Off
	}
	return rs, pos, nil
}

// before reads the stretch between the audio and the tags peeled at b, against the audio end the
// codec places, and reports whether it took that end; regions come nearest b first. For a legacy
// tail the search starts at floor all the same, and a tag it finds below that end wins over the audio
// the codec claims there: the end is not taken, and the bytes before the tag stay audio.
func (s *scanner) before(b int64, end AudioEnd, legacy bool) (rs []Region, known bool, err error) {
	e := int64(-1)
	if end != nil {
		if e, err = end(s.src, b); err != nil {
			return nil, false, err
		}
	}
	if e < s.floor || e > b {
		rs, err = s.stretch(s.floor, b, false)
		return rs, false, err
	}
	if !legacy {
		rs, err = s.stretch(e, b, true)
		return rs, true, err
	}
	if rs, err = s.stretch(s.floor, b, false); err != nil {
		return nil, false, err
	}
	low := b
	if n := len(rs); n > 0 {
		low = rs[n-1].Off
	}
	if low < e {
		return rs, false, nil
	}
	// No tag below the end: from it come the tags that open there, then junk up to the lowest found.
	lead, err := s.headed(e, low)
	if err != nil {
		return nil, false, err
	}
	lo := e
	if n := len(lead); n > 0 {
		lo = lead[n-1].Off + lead[n-1].Len
	}
	rs = append(rs, junkSpan(lo, low)...)
	slices.Reverse(lead)
	return append(rs, lead...), true, nil
}

// stretch finds the APE tags in [lo, b). From a known end lo come the tags that open with their
// header records there, one after another. Then, nearest b first, come each tag the search finds,
// the tags opening with their headers right after it, and the tags stacked right before it. The
// bytes between tags are junk, and so are those from a known end to the first tag; with an
// unknown end the bytes before the first tag stay audio. Regions come nearest b first.
func (s *scanner) stretch(lo, b int64, known bool) ([]Region, error) {
	var lead []Region
	if known {
		var err error
		if lead, err = s.headed(lo, b); err != nil {
			return nil, err
		}
		if n := len(lead); n > 0 {
			lo = lead[n-1].Off + lead[n-1].Len
		}
	}
	var rs []Region
	hi := b
	if s.steps > 0 {
		search, err := ape.NewSearch(s.src, max(lo, b-searchWindow), b, s.limit, s.maxElements)
		if err != nil {
			return nil, err
		}
		for s.steps > 0 {
			t, ok, err := search.Before(hi, lo)
			if err != nil {
				return nil, err
			}
			if !ok {
				break
			}
			s.steps--
			after, err := s.headed(t.Offset+t.Size, hi)
			if err != nil {
				return nil, err
			}
			top := t.Offset + t.Size
			if n := len(after); n > 0 {
				top = after[n-1].Off + after[n-1].Len
			}
			rs = append(rs, junkSpan(top, hi)...)
			slices.Reverse(after)
			rs = append(append(rs, after...), apeRegion(t))
			stacked, err := s.stacked(t.Offset, lo)
			if err != nil {
				return nil, err
			}
			rs = append(rs, stacked...)
			hi = rs[len(rs)-1].Off
		}
	}
	if known {
		rs = append(rs, junkSpan(lo, hi)...)
		slices.Reverse(lead)
		rs = append(rs, lead...)
	}
	return rs, nil
}

// headed reads the tags that open with their header records at at, one right after another, ending
// by end; they come in file order.
func (s *scanner) headed(at, end int64) ([]Region, error) {
	var rs []Region
	for ; s.steps > 0; s.steps-- {
		t, ok, err := ape.ParseHeadAt(s.src, at, end, s.limit, s.maxElements)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		rs = append(rs, apeRegion(t))
		at = t.Offset + t.Size
	}
	return rs, nil
}

// stacked reads the tags stacked right before pos, each footer ending where the next tag starts, as
// leniently as the peel reads a footer; a footer that places its tag below lo stops them. They come
// nearest pos first.
func (s *scanner) stacked(pos, lo int64) ([]Region, error) {
	var rs []Region
	for ; s.steps > 0; s.steps-- {
		start, ok, err := ape.DeclaredStart(s.src, pos, s.limit)
		if err != nil || !ok || start < lo {
			return rs, err
		}
		t, ok, err := ape.ParseAt(s.src, pos, s.limit, s.maxElements)
		if err != nil || !ok {
			return rs, err
		}
		rs = append(rs, apeRegion(t))
		pos = t.Offset
	}
	return rs, nil
}

// next returns the region that ends at pos, if any. The Lyrics3 markers are tried before an
// ID3v1 block's shape, and APE last, so a misplaced footer stops the peel.
func (s *scanner) next(pos int64) (Region, bool, error) {
	for _, at := range []func(int64) (Region, bool, error){s.lyrics3v2, s.lyrics3v1, s.id3v1, s.apeTag} {
		if r, ok, err := at(pos); err != nil || ok {
			return r, ok, err
		}
	}
	return Region{}, false, nil
}

// id3v1 reads an ID3v1 tag ending at pos.
func (s *scanner) id3v1(pos int64) (Region, bool, error) {
	b, err := s.read(pos-128, 128)
	if err != nil || b == nil || !id3.LooksLikeID3v1(b) {
		return Region{}, false, err
	}
	return Region{Kind: KindID3v1, Off: pos - 128, Len: 128, Blocks: 1, Raw: b}, true, nil
}

// apeTag reads an APE tag whose footer ends at pos. A footer that places its tag's start below the
// floor is not a region: no item is read, and it is kept for [Warnings].
func (s *scanner) apeTag(pos int64) (Region, bool, error) {
	start, ok, err := ape.DeclaredStart(s.src, pos, s.limit)
	if err != nil || !ok {
		return Region{}, false, err
	}
	if start < s.floor {
		s.misplaced = Region{Kind: KindAPEv2, Off: start, Len: pos - start}
		return Region{}, false, nil
	}
	if s.footersOnly {
		return Region{Kind: KindAPEv2, Off: start, Len: pos - start}, true, nil
	}
	t, ok, err := ape.ParseAt(s.src, pos, s.limit, s.maxElements)
	if err != nil || !ok {
		return Region{}, false, err
	}
	return apeRegion(t), true, nil
}

func apeRegion(t *ape.Tag) Region {
	return Region{Kind: KindAPEv2, Off: t.Offset, Len: t.Size, Tag: t}
}

// junkSpan is the junk region [from, to), if it is not empty.
func junkSpan(from, to int64) []Region {
	if from >= to {
		return nil
	}
	return []Region{{Kind: KindJunk, Off: from, Len: to - from}}
}

// read returns the n bytes at off, or nil when they lie below the floor or past the end of the file,
// read as [bits.ReadWithin] reads: one over the limit is refused, and one that fails is an error.
func (s *scanner) read(off, n int64) ([]byte, error) {
	if off < s.floor {
		return nil, nil
	}
	return bits.ReadWithin(s.src, off, n, s.limit)
}
