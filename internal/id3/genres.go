package id3

import (
	"strconv"
	"strings"
)

// genres is the ID3v1 / Winamp numeric genre table (0-191). It is reference
// data, not expression: the original 0-79 entries are from the ID3v1
// specification, 80-191 are the de-facto Winamp extensions. Reproduced for
// numeric-genre resolution.
var genres = [...]string{
	"Blues", "Classic Rock", "Country", "Dance", "Disco", "Funk", "Grunge",
	"Hip-Hop", "Jazz", "Metal", "New Age", "Oldies", "Other", "Pop", "R&B",
	"Rap", "Reggae", "Rock", "Techno", "Industrial", "Alternative", "Ska",
	"Death Metal", "Pranks", "Soundtrack", "Euro-Techno", "Ambient", "Trip-Hop",
	"Vocal", "Jazz+Funk", "Fusion", "Trance", "Classical", "Instrumental",
	"Acid", "House", "Game", "Sound Clip", "Gospel", "Noise", "Alt. Rock",
	"Bass", "Soul", "Punk", "Space", "Meditative", "Instrumental Pop",
	"Instrumental Rock", "Ethnic", "Gothic", "Darkwave", "Techno-Industrial",
	"Electronic", "Pop-Folk", "Eurodance", "Dream", "Southern Rock", "Comedy",
	"Cult", "Gangsta Rap", "Top 40", "Christian Rap", "Pop/Funk", "Jungle",
	"Native American", "Cabaret", "New Wave", "Psychedelic", "Rave",
	"Showtunes", "Trailer", "Lo-Fi", "Tribal", "Acid Punk", "Acid Jazz",
	"Polka", "Retro", "Musical", "Rock & Roll", "Hard Rock", "Folk",
	"Folk-Rock", "National Folk", "Swing", "Fast-Fusion", "Bebop", "Latin",
	"Revival", "Celtic", "Bluegrass", "Avantgarde", "Gothic Rock",
	"Progressive Rock", "Psychedelic Rock", "Symphonic Rock", "Slow Rock",
	"Big Band", "Chorus", "Easy Listening", "Acoustic", "Humour", "Speech",
	"Chanson", "Opera", "Chamber Music", "Sonata", "Symphony", "Booty Bass",
	"Primus", "Porn Groove", "Satire", "Slow Jam", "Club", "Tango", "Samba",
	"Folklore", "Ballad", "Power Ballad", "Rhythmic Soul", "Freestyle", "Duet",
	"Punk Rock", "Drum Solo", "A Cappella", "Euro-House", "Dance Hall", "Goa",
	"Drum & Bass", "Club-House", "Hardcore", "Terror", "Indie", "BritPop",
	"Afro-Punk", "Polsk Punk", "Beat", "Christian Gangsta Rap", "Heavy Metal",
	"Black Metal", "Crossover", "Contemporary Christian", "Christian Rock",
	"Merengue", "Salsa", "Thrash Metal", "Anime", "JPop", "Synthpop",
	"Abstract", "Art Rock", "Baroque", "Bhangra", "Big Beat", "Breakbeat",
	"Chillout", "Downtempo", "Dub", "EBM", "Eclectic", "Electro", "Electroclash",
	"Emo", "Experimental", "Garage", "Global", "IDM", "Illbient",
	"Industro-Goth", "Jam Band", "Krautrock", "Leftfield", "Lounge",
	"Math Rock", "New Romantic", "Nu-Breakz", "Post-Punk", "Post-Rock",
	"Psytrance", "Shoegaze", "Space Rock", "Trop Rock", "World Music",
	"Neoclassical", "Audiobook", "Audio Theatre", "Neue Deutsche Welle",
	"Podcast", "Indie Rock", "G-Funk", "Dubstep", "Garage Rock", "Psybient",
}

// genreName returns the name for a numeric genre index, or ("", false) if out
// of range.
func genreName(n int) (string, bool) {
	if n < 0 || n >= len(genres) {
		return "", false
	}
	return genres[n], true
}

// GenreName exposes the numeric-genre table to the other codecs that share it
// (MP4's legacy "gnre" atom resolves a 1-based ID3v1 genre number to a name). It
// keeps the vendored 192-entry list in one place rather than duplicated per
// codec; n is the 0-based index.
func GenreName(n int) (string, bool) { return genreName(n) }

// genreByName maps a lowercased genre name to its index, built once so the
// WithNumericGenre write path is an O(1) lookup rather than a 192-entry scan.
var genreByName = func() map[string]int {
	m := make(map[string]int, len(genres))
	for i, g := range genres {
		m[strings.ToLower(g)] = i
	}
	return m
}()

// genreIndex returns the numeric index of a genre name (case-insensitive), or
// (-1) if it is not a standard genre. Used for the WithNumericGenre write option.
func genreIndex(name string) int {
	if i, ok := genreByName[strings.ToLower(name)]; ok {
		return i
	}
	return -1
}

// GenreIndex exposes genreIndex to other codecs (the MP4 numeric "gnre" atom),
// mirroring the exported GenreName. It returns the 0-based ID3v1 genre index of a
// name (case-insensitive), or -1 if the name is not a standard genre.
func GenreIndex(name string) int { return genreIndex(name) }

// specialGenre maps the non-numeric TCON references defined by ID3: RX (Remix)
// and CR (Cover). They resolve the same whether bare or parenthesized, so both
// resolveGenres branches use this table.
func specialGenre(ref string) (string, bool) {
	switch ref {
	case "RX":
		return "Remix", true
	case "CR":
		return "Cover", true
	}
	return "", false
}

// resolveGenres turns a raw TCON value into one or more display names, expanding
// every numeric/special reference. It reports whether any numeric reference was
// seen so the parser can surface a numeric-genre warning. A single ID3v2.3 TCON
// can carry several references plus a trailing refinement. Forms handled:
//
//	"17"         -> ["Rock"]                 (bare number, v2.4)
//	"(17)"       -> ["Rock"]                 (parenthesised reference, v2.3)
//	"(51)(39)"   -> ["Techno-Industrial","Noise"]  (multiple references)
//	"(17)Hard"   -> ["Rock","Hard"]          (reference + textual refinement)
//	"(RX)"/"RX"  -> ["Remix"]                (special reference, parenthesised or bare)
//	"(CR)"/"CR"  -> ["Cover"]
//	"Rock"       -> ["Rock"]                 (already a name)
//
// Bare RX/CR values are reference tokens, not literal genre names. They resolve
// on read like bare numeric references; lowercase values and other text stay literal.
func resolveGenres(v string) (names []string, numeric bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, false
	}
	// No parentheses: a bare special reference (RX/CR) resolves like its parenthesized form,
	// a bare number is a v2.4 reference, and anything else is a literal name.
	if !strings.HasPrefix(v, "(") {
		if name, ok := specialGenre(v); ok {
			return []string{name}, true
		}
		if n, err := strconv.Atoi(v); err == nil {
			if g, ok := genreName(n); ok {
				return []string{g}, true
			}
		}
		return []string{v}, false
	}
	// One or more "(ref)" references, optionally followed by a refinement.
	for strings.HasPrefix(v, "(") {
		if strings.HasPrefix(v, "((") {
			break // "((" escapes a literal "(" that begins the refinement text
		}
		end := strings.IndexByte(v, ')')
		if end < 0 {
			break // unterminated reference; treat the remainder as text
		}
		ref := v[1:end]
		if name, ok := specialGenre(ref); ok {
			names = append(names, name)
			numeric = true
		} else if n, err := strconv.Atoi(ref); err == nil {
			numeric = true
			if g, ok := genreName(n); ok {
				names = append(names, g)
			} else {
				names = append(names, "("+ref+")") // out of range: keep the literal
			}
		} else {
			names = append(names, ref)
		}
		v = v[end+1:]
	}
	if rest := strings.TrimSpace(strings.TrimPrefix(v, "(")); rest != "" {
		names = append(names, rest)
	}
	if len(names) == 0 {
		names = []string{strings.TrimSpace(v)}
	}
	return names, numeric
}
