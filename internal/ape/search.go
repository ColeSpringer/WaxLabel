package ape

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// Search finds APE tags by their footers in a window read once, the footer nearest the window's
// end first. APE tags carry no checksum, so a candidate counts only when it holds together:
// version 1000 or 2000, a footer record, a size that fits, a start at or after the caller's bound,
// the header record its flags announce, agreeing on size and count, and items that walk to exactly
// the declared length and count. Every check that reads no items comes first, and the items
// candidates read or walk share one budget of the allocation limit; items the window holds are walked
// there, not read again.
type Search struct {
	src         core.ReaderAtSized
	lo          int64
	win         []byte
	next        int   // the window before next is still to search
	budget      int64 // item bytes candidates may still read or walk
	limit       int64
	maxElements int
}

// NewSearch reads the window [lo, hi) as [bits.ReadWithin] reads: refused over limit with
// [waxerr.ErrSizeTooLarge], an error when the read fails, and with no candidates when the window runs
// past the end of the file.
func NewSearch(src core.ReaderAtSized, lo, hi, limit int64, maxElements int) (*Search, error) {
	s := &Search{src: src, lo: lo, budget: limit, limit: limit, maxElements: maxElements}
	if hi-lo < footerLen {
		return s, nil
	}
	win, err := bits.ReadWithin(src, lo, hi-lo, limit)
	if err != nil {
		return nil, err
	}
	s.win, s.next = win, len(win)
	return s, nil
}

// Before returns the tag whose footer ends nearest hi, at or before it, that holds together and
// starts at or after minStart, resuming below the candidates earlier calls examined. A candidate
// that passes every check reading no items but declares items over the limit, or over what the
// budget has left, is a tag too large to read, refused with [waxerr.ErrSizeTooLarge].
func (s *Search) Before(hi, minStart int64) (*Tag, bool, error) {
	marker := []byte(preamble)
	for {
		i := bytes.LastIndex(s.win[:min(s.next, max(int(hi-s.lo), 0))], marker)
		if i < 0 {
			break
		}
		s.next = i
		if t, ok, err := s.candidate(i, hi, minStart); err != nil || ok {
			return t, ok, err
		}
	}
	return nil, false, nil
}

// candidate reads the tag whose footer sits at win[i:], if it ends by hi and holds together.
func (s *Search) candidate(i int, hi, minStart int64) (*Tag, bool, error) {
	end := s.lo + int64(i) + footerLen
	foot, ok := decodeRecord(s.win[i:])
	if !ok || end > hi || foot.isHeader() || (foot.version != 1000 && foot.version != 2000) ||
		foot.size < footerLen || foot.size > end || foot.start(end) < minStart {
		return nil, false, nil
	}
	var head record
	if foot.headed() {
		var ok bool
		var err error
		head, ok, err = s.record(end - foot.size - footerLen)
		if err != nil || !ok || !head.isHeader() || head.size != foot.size || head.count != foot.count {
			return nil, false, err
		}
	}
	itemsLen := foot.size - footerLen
	if err := bits.CheckAlloc(itemsLen, s.limit); err != nil {
		return nil, false, err
	}
	if s.budget -= itemsLen; s.budget < 0 {
		return nil, false, fmt.Errorf("%w: APE tag candidates declare more item bytes than limit %d", waxerr.ErrSizeTooLarge, s.limit)
	}
	raw, shared, ok, err := s.items(foot, end)
	if err != nil || !ok {
		return nil, false, err
	}
	if _, whole := walkItems(raw, foot.count, nil); !whole {
		return nil, false, nil
	}
	if shared {
		raw = slices.Clone(raw)
	}
	return tagFrom(foot, head, end, raw, foot.headed(), s.maxElements), true, nil
}

// items returns the items of the tag whose footer ends at end: the window's own bytes when it holds
// them (shared, so a tag that keeps them copies them), else read as [readItems] reads.
func (s *Search) items(foot record, end int64) (raw []byte, shared, ok bool, err error) {
	i, j := end-foot.size-s.lo, end-footerLen-s.lo
	if foot.size > footerLen && i >= 0 && j <= int64(len(s.win)) {
		return s.win[i:j], true, true, nil
	}
	raw, ok, err = readItems(s.src, foot, end, s.limit)
	return raw, false, ok, err
}

// record decodes the record at off, from the window when it lies inside.
func (s *Search) record(off int64) (record, bool, error) {
	if i := off - s.lo; i >= 0 && i+footerLen <= int64(len(s.win)) {
		r, ok := decodeRecord(s.win[i:])
		return r, ok, nil
	}
	return readRecord(s.src, off, s.limit)
}
