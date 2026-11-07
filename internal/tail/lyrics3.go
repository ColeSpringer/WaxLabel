package tail

import "bytes"

// Lyrics3 markers. A v2.00 block ends in a six-digit size and LYRICS200; a v1.00 block ends in
// LYRICSEND, with LYRICSBEGIN at most 5100 bytes of lyrics before it.
const (
	lyricsBegin   = "LYRICSBEGIN"
	lyricsEnd     = "LYRICSEND"
	lyrics200     = "LYRICS200"
	lyrics2Footer = int64(6 + len(lyrics200))
	lyrics1Window = int64(5100 + len(lyricsBegin))
)

// lyrics3v2 reads a Lyrics3 v2.00 block ending at pos. Its size counts the bytes from
// LYRICSBEGIN up to the size field.
func (s *scanner) lyrics3v2(pos int64) (Region, bool, error) {
	foot, err := s.read(pos-lyrics2Footer, lyrics2Footer)
	if err != nil || foot == nil || string(foot[6:]) != lyrics200 {
		return Region{}, false, err
	}
	var n int64
	for _, c := range foot[:6] {
		if c < '0' || c > '9' {
			return Region{}, false, nil
		}
		n = n*10 + int64(c-'0')
	}
	if n < int64(len(lyricsBegin)) {
		return Region{}, false, nil
	}
	start := pos - lyrics2Footer - n
	begin, err := s.read(start, int64(len(lyricsBegin)))
	if err != nil || begin == nil || string(begin) != lyricsBegin {
		return Region{}, false, err
	}
	return Region{Kind: KindLyrics3v2, Off: start, Len: pos - start}, true, nil
}

// lyrics3v1 reads a Lyrics3 v1.00 block ending at pos, from the first LYRICSBEGIN in the window
// before its LYRICSEND.
func (s *scanner) lyrics3v1(pos int64) (Region, bool, error) {
	at := pos - int64(len(lyricsEnd))
	foot, err := s.read(at, int64(len(lyricsEnd)))
	if err != nil || foot == nil || string(foot) != lyricsEnd {
		return Region{}, false, err
	}
	lo := max(s.floor, at-lyrics1Window)
	win, err := s.read(lo, at-lo)
	if err != nil || win == nil {
		return Region{}, false, err
	}
	i := bytes.Index(win, []byte(lyricsBegin))
	if i < 0 {
		return Region{}, false, nil
	}
	start := lo + int64(i)
	return Region{Kind: KindLyrics3v1, Off: start, Len: pos - start}, true, nil
}
