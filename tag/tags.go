package tag

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// Tags is the typed convenience projection of a [TagSet]. It is lossy by
// design: a struct's zero values cannot distinguish absent from empty from
// cleared, so Tags is authoritative for nothing. Use it to read common fields
// ergonomically ([Project]) and to write them as sugar ([Tags.Patch], which
// only sets non-empty fields - clearing requires an explicit [TagPatch]).
type Tags struct {
	Title       string
	Artists     []string
	Album       string
	AlbumArtist string
	Composers   []string
	Genres      []string

	TrackNumber int
	TrackTotal  int
	DiscNumber  int
	DiscTotal   int

	// Three-way dates, kept as strings to preserve partial precision
	// (year, year-month, full date) rather than forcing time.Time.
	RecordingDate string
	ReleaseDate   string
	OriginalDate  string

	// Comment is multi-valued. AIFF ANNO, Vorbis comments, MP4 cmt atoms, and ID3 COMM
	// frames can all store several comments, so the typed projection keeps the full list.
	Comment   []string
	Lyrics    string
	Grouping  string
	Copyright string

	TitleSort       string
	ArtistSort      string
	AlbumSort       string
	AlbumArtistSort string
	ComposerSort    string

	ISRC          string
	Barcode       string
	CatalogNumber string
	Label         string
	Media         string
	DiscSubtitle  string

	Conductor string
	Remixer   string
	// Performers are the credited performers in PERFORMER order, which is
	// significant and round-trips: an ordered slice rather than a map so
	// Project -> Patch does not reorder a multi-valued PERFORMER. A bare value
	// lands as a PerformerCredit with an empty Role.
	Performers []PerformerCredit
	// EncodedBy is the encoding person; Encoder is the encoding software/tool.
	EncodedBy string
	Encoder   string

	AcoustID            string
	AcoustIDFingerprint string

	Compilation bool

	MusicBrainz MusicBrainzIDs
	ReplayGain  ReplayGain

	Rating    string
	PlayCount int

	// Acquisition provenance: where the file came from and how it was produced.
	SourceURL       string
	SourceID        string
	AcquisitionDate string
	EncodingHistory string

	// Audiobook / spoken-word fields. MediaType is the iTunes stik media-kind code
	// (a numeric string, e.g. "2" for audiobook), distinct from Media (the release
	// medium). Description/LongDescription are the short and full blurbs; Narrator is
	// the reader/performer.
	MediaType       string
	Description     string
	LongDescription string
	Narrator        string
}

// MusicBrainzIDs collects the MusicBrainz identifiers. RecordingID corresponds
// to the canonical key [MBRecordingID] (MUSICBRAINZ_TRACKID).
type MusicBrainzIDs struct {
	ReleaseID      string
	ReleaseGroupID string
	RecordingID    string
	ReleaseTrackID string
	WorkID         string
	DiscID         string
	ArtistID       []string
	AlbumArtistID  []string
}

// ReplayGain holds the loudness-normalization values as their stored strings
// (e.g. "-7.30 dB", "0.988553"). Opus R128 gain is modeled by the Opus codec,
// not here.
type ReplayGain struct {
	TrackGain string
	TrackPeak string
	AlbumGain string
	AlbumPeak string
}

// Project reads a TagSet into the typed struct. Unknown and custom keys are
// ignored by the projection (they remain available through the TagSet).
func Project(ts TagSet) Tags {
	first := func(k Key) string { v, _ := ts.First(k); return v }
	all := func(k Key) []string { v, _ := ts.Get(k); return v }

	t := Tags{
		Title:       first(Title),
		Artists:     all(Artist),
		Album:       first(Album),
		AlbumArtist: first(AlbumArtist),
		Composers:   all(Composer),
		Genres:      all(Genre),

		RecordingDate: first(RecordingDate),
		ReleaseDate:   first(ReleaseDate),
		OriginalDate:  first(OriginalDate),

		Comment:   all(Comment),
		Lyrics:    first(Lyrics),
		Grouping:  first(Grouping),
		Copyright: first(Copyright),

		TitleSort:       first(TitleSort),
		ArtistSort:      first(ArtistSort),
		AlbumSort:       first(AlbumSort),
		AlbumArtistSort: first(AlbumArtistSort),
		ComposerSort:    first(ComposerSort),

		ISRC:          first(ISRC),
		Barcode:       first(Barcode),
		CatalogNumber: first(CatalogNumber),
		Label:         first(Label),
		Media:         first(Media),
		DiscSubtitle:  first(DiscSubtitle),

		Conductor: first(Conductor),
		Remixer:   first(Remixer),
		EncodedBy: first(EncodedBy),
		Encoder:   first(Encoder),

		AcoustID:            first(AcoustID),
		AcoustIDFingerprint: first(AcoustIDFingerprint),

		Compilation: ParseBool(first(Compilation)),

		MusicBrainz: MusicBrainzIDs{
			ReleaseID:      first(MBReleaseID),
			ReleaseGroupID: first(MBReleaseGroupID),
			RecordingID:    first(MBRecordingID),
			ReleaseTrackID: first(MBReleaseTrackID),
			WorkID:         first(MBWorkID),
			DiscID:         first(MBDiscID),
			ArtistID:       all(MBArtistID),
			AlbumArtistID:  all(MBAlbumArtistID),
		},
		ReplayGain: ReplayGain{
			TrackGain: first(ReplayGainTrackGain),
			TrackPeak: first(ReplayGainTrackPeak),
			AlbumGain: first(ReplayGainAlbumGain),
			AlbumPeak: first(ReplayGainAlbumPeak),
		},
		Rating: first(Rating),

		SourceURL:       first(SourceURL),
		SourceID:        first(SourceID),
		AcquisitionDate: first(AcquisitionDate),
		EncodingHistory: first(EncodingHistory),

		MediaType:       first(MediaType),
		Description:     first(Description),
		LongDescription: first(LongDescription),
		Narrator:        first(Narrator),
	}

	t.TrackNumber, t.TrackTotal = ParseNumPair(first(TrackNumber), first(TrackTotal))
	t.DiscNumber, t.DiscTotal = ParseNumPair(first(DiscNumber), first(DiscTotal))
	// Match ParseNumPair's convention (trim surrounding whitespace, every error including
	// overflow yields 0) rather than leaving PlayCount at strconv.Atoi's partial 0 on error.
	if pc, err := strconv.Atoi(strings.TrimSpace(first(PlayCount))); err == nil {
		t.PlayCount = pc
	} else {
		t.PlayCount = 0
	}
	t.Performers = parsePerformers(all(Performer))
	return t
}

// Patch compiles the non-empty fields of t into a TagPatch of Set operations.
// Empty fields are left untouched (not cleared); use a [TagPatch] directly to
// clear keys.
func (t Tags) Patch() TagPatch {
	var p TagPatch
	setStr := func(k Key, v string) {
		if v != "" {
			p.Set(k, v)
		}
	}
	setMulti := func(k Key, v []string) {
		if len(v) > 0 {
			p.Set(k, v...)
		}
	}
	setNum := func(k Key, v int) {
		if v != 0 {
			p.Set(k, strconv.Itoa(v))
		}
	}

	setStr(Title, t.Title)
	setMulti(Artist, t.Artists)
	setStr(Album, t.Album)
	setStr(AlbumArtist, t.AlbumArtist)
	setMulti(Composer, t.Composers)
	setMulti(Genre, t.Genres)

	setNum(TrackNumber, t.TrackNumber)
	setNum(TrackTotal, t.TrackTotal)
	setNum(DiscNumber, t.DiscNumber)
	setNum(DiscTotal, t.DiscTotal)

	setStr(RecordingDate, t.RecordingDate)
	setStr(ReleaseDate, t.ReleaseDate)
	setStr(OriginalDate, t.OriginalDate)

	setMulti(Comment, t.Comment)
	setStr(Lyrics, t.Lyrics)
	setStr(Grouping, t.Grouping)
	setStr(Copyright, t.Copyright)

	setStr(TitleSort, t.TitleSort)
	setStr(ArtistSort, t.ArtistSort)
	setStr(AlbumSort, t.AlbumSort)
	setStr(AlbumArtistSort, t.AlbumArtistSort)
	setStr(ComposerSort, t.ComposerSort)

	setStr(ISRC, t.ISRC)
	setStr(Barcode, t.Barcode)
	setStr(CatalogNumber, t.CatalogNumber)
	setStr(Label, t.Label)
	setStr(Media, t.Media)
	setStr(DiscSubtitle, t.DiscSubtitle)

	setStr(Conductor, t.Conductor)
	setStr(Remixer, t.Remixer)
	setStr(EncodedBy, t.EncodedBy)
	setStr(Encoder, t.Encoder)
	setMulti(Performer, formatPerformers(t.Performers))

	setStr(AcoustID, t.AcoustID)
	setStr(AcoustIDFingerprint, t.AcoustIDFingerprint)

	if t.Compilation {
		p.Set(Compilation, "1")
	}

	setStr(MBReleaseID, t.MusicBrainz.ReleaseID)
	setStr(MBReleaseGroupID, t.MusicBrainz.ReleaseGroupID)
	setStr(MBRecordingID, t.MusicBrainz.RecordingID)
	setStr(MBReleaseTrackID, t.MusicBrainz.ReleaseTrackID)
	setStr(MBWorkID, t.MusicBrainz.WorkID)
	setStr(MBDiscID, t.MusicBrainz.DiscID)
	setMulti(MBArtistID, t.MusicBrainz.ArtistID)
	setMulti(MBAlbumArtistID, t.MusicBrainz.AlbumArtistID)

	setStr(ReplayGainTrackGain, t.ReplayGain.TrackGain)
	setStr(ReplayGainTrackPeak, t.ReplayGain.TrackPeak)
	setStr(ReplayGainAlbumGain, t.ReplayGain.AlbumGain)
	setStr(ReplayGainAlbumPeak, t.ReplayGain.AlbumPeak)

	setStr(Rating, t.Rating)
	setNum(PlayCount, t.PlayCount)

	setStr(SourceURL, t.SourceURL)
	setStr(SourceID, t.SourceID)
	setStr(AcquisitionDate, t.AcquisitionDate)
	setStr(EncodingHistory, t.EncodingHistory)

	setStr(MediaType, t.MediaType)
	setStr(Description, t.Description)
	setStr(LongDescription, t.LongDescription)
	setStr(Narrator, t.Narrator)

	return p
}

// ParseNumPair resolves a "number" and "total" pair (e.g. track or disc
// numbering). The number field may use the "n/total" convention; an explicit
// total field wins if present. Surrounding whitespace is ignored. It is exported
// so codecs that store numbering as a structured pair (e.g. MP4 trkn/disk) parse
// the canonical strings the same way the typed projection does.
func ParseNumPair(num, total string) (n, tot int) {
	// atoi parses a trimmed int and treats every error as 0, including
	// out-of-range overflow.
	atoi := func(s string) int {
		v, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return 0
		}
		return v
	}
	if num != "" {
		if i := strings.IndexByte(num, '/'); i >= 0 {
			n = atoi(num[:i])
			tot = atoi(num[i+1:])
		} else {
			n = atoi(num)
		}
	}
	if total != "" {
		tot = atoi(total)
	}
	return n, tot
}

// Fold normalizes a string for case- and space-insensitive comparison
// (lowercased, surrounding whitespace trimmed). It is the canonical fold rule for
// the whole tree: [core.Fold] delegates to it (core imports tag, not the reverse),
// so codecs that import core and the tag package's own callers fold identically.
func Fold(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// DistinctValues counts the case- and space-insensitive distinct values in vals,
// folding each through [Fold]. Dump duplicate markers and codec family-conflict
// checks both use this rule, so they agree on what counts as the same value.
func DistinctValues(vals []string) int {
	seen := make(map[string]bool, len(vals))
	for _, v := range vals {
		seen[Fold(v)] = true
	}
	return len(seen)
}

// SplitNumberTotal splits a "number/total" value (e.g. "3/12") on the first '/'
// into its trimmed number and total substrings. Unlike [ParseNumPair] it preserves
// the exact substrings - leading zeros and all - rather than renumbering to ints, so
// it suits a write/edit normalization that must not silently rewrite the value. Each
// side is "" when absent or blank ("3/" -> "3",""; "/12" -> "","12"). It is the
// single substring split shared by the ID3 read path and the edit-time pair split.
func SplitNumberTotal(v string) (num, total string) {
	num, total, _ = strings.Cut(v, "/")
	return strings.TrimSpace(num), strings.TrimSpace(total)
}

// TotalKey returns the canonical "total" companion for a numbering key:
// [TrackNumber] -> [TrackTotal], [DiscNumber] -> [DiscTotal]. Any other key is
// returned unchanged. It is the single number->total mapping shared by the codecs
// and the edit-time numbering split so the sites cannot drift.
func TotalKey(k Key) Key {
	switch k {
	case TrackNumber:
		return TrackTotal
	case DiscNumber:
		return DiscTotal
	default:
		return k
	}
}

// NumberTotalSplit splits a track/disc number value into its number and total substrings for
// the read paths, so every codec that stores numbering as text splits a slashed value the same
// way and agrees with the edit-time split (editor.splitNumberPairs). It is the single split
// decision shared by ID3's emitNumTotal, Matroska's projectTag, and [NormalizeNumberPairs] (the
// FLAC/Ogg and WAV post-pass), so those sites cannot drift.
//
// A genuine numeric pair ("4/9", "04/09", "0/12", "/2", "3/") splits on the first '/' via
// [SplitNumberTotal], preserving the exact substrings (leading zeros and a literal 0 included),
// each side "" when absent, and reports split=true. A value with no '/', a malformed pair whose
// number or total side is non-numeric ("abc/1", "1/2/3"), or a bare "/" comes back whole as num
// with an empty total and split=false, so it stays verbatim on the number key instead of
// fabricating a total, matching what the editor leaves alone.
//
// A non-pair key returns split=false unchanged. That guard is not dead defense: Matroska's
// projectTag and WAV's infoFamilies pass arbitrary mapped keys through here and rely on it, so
// dropping it would split an Album or Title value that happens to contain a '/'. The validity
// gate reuses [ValidNumericValue], so it cannot disagree with the linter on what a well-formed
// pair is.
func NumberTotalSplit(k Key, v string) (num, total string, split bool) {
	if (k == TrackNumber || k == DiscNumber) &&
		strings.ContainsRune(v, '/') && ValidNumericValue(k, v) {
		num, total = SplitNumberTotal(v)
		return num, total, true
	}
	return v, "", false
}

// NormalizeNumberPairs splits a slashed TRACKNUMBER/DISCNUMBER carried in a read projection into
// a number key plus a derived total key, so the FLAC/Ogg and WAV read paths agree with the
// ID3/MP4/Matroska projections and with the editor. Without it Tags() would show ["4/9"] while
// the typed Fields() fabricated a total, and dump, copy, and diff would disagree on the same
// file. It runs as a post-pass over a codec's projected [TagSet] and has no edit context, so its
// total guard is "no total already present" rather than the editor's patch.Touches.
//
// An absent or multi-valued key is left alone; splitting a multi-value would invent a total no
// writer can store and churn the file. A single value goes through [SplitNumberValue], which
// sets the derived total only when the key has no value yet, so an explicit or present-empty
// TRACKTOTAL wins over the slash's own total (matching [ParseNumPair]).
func NormalizeNumberPairs(ts *TagSet) {
	for _, numKey := range []Key{TrackNumber, DiscNumber} {
		vals, ok := ts.Get(numKey)
		if !ok || len(vals) != 1 {
			continue // absent, or multi-valued (out of scope - never lose a value)
		}
		SplitNumberValue(ts, numKey, vals[0], !ts.Has(TotalKey(numKey)))
	}
}

// SplitNumberValue applies a slashed track/disc number split to one key in ts. When value is a
// genuine pair (per [NumberTotalSplit]) the number substring replaces numKey, or deletes it when
// the number side is empty ("/12"), and the total is written to the companion total key when it
// is non-empty and setTotal is true. A non-pair or malformed value is left untouched.
//
// This split-and-assign body is shared by [NormalizeNumberPairs] and the editor's edit-time
// split. The two differ only in setTotal: the read pass passes "no total already present" (an
// explicit total wins), the editor passes "the patch does not also touch the total key" (a slash
// updates a base-carried total, but an explicit set wins). Keeping the body in one place stops
// the number set/delete and leading-zero handling from drifting between them.
func SplitNumberValue(ts *TagSet, numKey Key, value string, setTotal bool) {
	num, total, split := NumberTotalSplit(numKey, value)
	if !split {
		return
	}
	if num != "" {
		ts.Set(numKey, num)
	} else {
		ts.Delete(numKey) // "/12": no number survives
	}
	if total != "" && setTotal {
		ts.Set(TotalKey(numKey), total)
	}
}

// numericKeys are the canonical keys whose typed [Tags] projection is an int, so
// a non-numeric value does not round-trip through that accessor (it reads 0): the
// track and disc number/total, and the play count. Rating is excluded (it is a
// free-form string), and so is MediaType (vocabulary-only, no typed accessor). It
// backs [IsNumericKey] and the set-time malformed-value note.
//
// It is deliberately not [IsMP4CanonicalKey]: this set carries PlayCount and omits MediaType,
// the reverse of what a 16-bit MP4 atom normalizes, so do not fold the two together.
var numericKeys = map[Key]bool{
	TrackNumber: true,
	TrackTotal:  true,
	DiscNumber:  true,
	DiscTotal:   true,
	PlayCount:   true,
}

// dateKeySet is the canonical partial-date keys, kept as a set so [IsDateKey] is
// the single date-key definition shared by the linter's malformed-date rule and
// the set-time malformed-value note.
var dateKeySet = map[Key]bool{
	RecordingDate:   true,
	ReleaseDate:     true,
	OriginalDate:    true,
	AcquisitionDate: true,
}

// booleanKeys is the canonical keys whose value is a boolean flag - today only
// Compilation, whose typed [Tags] projection is a bool ([ParseBool]). Kept as a
// set so [IsBooleanKey] is the single boolean-key definition the set-time
// malformed-value note reads, mirroring numericKeys/dateKeySet.
var booleanKeys = map[Key]bool{
	Compilation: true,
}

// IsNumericKey reports whether k canonically holds a numeric value (one with an
// int projection in [Tags]): the track/disc number and total, and play count.
func IsNumericKey(k Key) bool { return numericKeys[k] }

// IsDateKey reports whether k canonically holds an ISO-8601 partial date (YYYY,
// YYYY-MM, or YYYY-MM-DD).
func IsDateKey(k Key) bool { return dateKeySet[k] }

// IsBooleanKey reports whether k canonically holds a boolean flag (one with a
// bool projection in [Tags]): today only Compilation.
func IsBooleanKey(k Key) bool { return booleanKeys[k] }

// ValidNumericValue reports whether v is a value the numeric key k accepts
// without loss. It mirrors [ParseNumPair] exactly so it never flags a value that
// round-trips: surrounding whitespace is ignored, the pair keys (TrackNumber and
// DiscNumber) accept the "number/total" convention, and the parse is
// strconv.Atoi (which accepts a leading sign). A key that is not numeric is
// reported valid - there is nothing to check.
func ValidNumericValue(k Key, v string) bool {
	if !numericKeys[k] {
		return true
	}
	// Only the number fields carry "n/total"; the standalone totals and play count
	// do not (ParseNumPair splits only the number field).
	if k == TrackNumber || k == DiscNumber {
		if num, total, ok := strings.Cut(v, "/"); ok {
			// A bare "/" (both sides blank) is malformed: a lone slash carries no number,
			// so it must not pass the validator and let splitNumberPairs delete the key.
			// One blank side ("3/" or "/2") is still fine - ParseNumPair reads it as 0.
			if strings.TrimSpace(num) == "" && strings.TrimSpace(total) == "" {
				return false
			}
			return numComponent(num) && numComponent(total)
		}
	}
	return validInt(v)
}

// numComponent reports whether one side of a "number/total" value is acceptable.
// An empty side ("3/" or "/2") is fine: ParseNumPair runs each side through Atoi
// and ignores the error, so an empty side parses to 0 and the value round-trips -
// only a non-empty, non-numeric side is malformed.
func numComponent(s string) bool {
	return strings.TrimSpace(s) == "" || validInt(s)
}

// NegativeNumericValue reports whether numeric key k's value v has a negative
// component. Atoi accepts a leading sign, so such a value round-trips and
// [ValidNumericValue] accepts it - but a negative track/disc number, total, or play
// count is semantically odd, so the CLI advises on it (the value is still written).
// It mirrors ValidNumericValue's structure so the "n/total" pair keys check each
// side independently: both -3/10 and 3/-10 are caught. A non-numeric key, or a value
// with no negative component, reports false.
func NegativeNumericValue(k Key, v string) bool {
	if !numericKeys[k] {
		return false
	}
	if k == TrackNumber || k == DiscNumber {
		if num, total, ok := strings.Cut(v, "/"); ok {
			return negativeInt(num) || negativeInt(total)
		}
	}
	return negativeInt(v)
}

// EmptyNumberWithTotal reports whether v is a valid "number/total" value for TrackNumber or
// DiscNumber with an empty number side and a numeric total, such as "/5". The value is valid
// and can be written, but the empty number is easy to type by accident, so the CLI reports an
// advisory. Explicit total keys can override the embedded total; this helper only describes
// the submitted pair value.
func EmptyNumberWithTotal(k Key, v string) bool {
	if k != TrackNumber && k != DiscNumber {
		return false
	}
	num, total := SplitNumberTotal(v)
	return num == "" && validInt(total)
}

// IsTrimmableKey reports whether a value stored under k is a single-token value whose surrounding
// whitespace is never meaningful - a numeric, date, media-type, or ReplayGain key. [TrimTokenValue],
// the editor's per-key trim gate, and the transfer grade all key off this one predicate, so the
// stored form, the write, and the copy report cannot disagree on which keys trim; adding a
// trim-eligible key here updates all three at once.
func IsTrimmableKey(k Key) bool {
	return numericKeys[k] || dateKeySet[k] || IsMediaTypeKey(k) || IsReplayGainKey(k)
}

// TrimTokenValue removes surrounding whitespace from a trimmable value (see [IsTrimmableKey]) and
// leaves other values unchanged. The editor and CLI advisories share this helper so stored values
// match the forms [ValidNumericValue] and [ValidPartialDate] accept. MEDIATYPE and the REPLAYGAIN_*
// keys are single-token values ("2", "-7.30 dB") where a stray leading or trailing space is never
// meaningful, so they trim the same way; internal whitespace (the space before "dB") and digits,
// including leading zeros, are preserved.
func TrimTokenValue(k Key, v string) string {
	if IsTrimmableKey(k) {
		return strings.TrimSpace(v)
	}
	return v
}

// parseIntField parses one numeric component (trimmed of surrounding whitespace),
// the same parse [ParseNumPair] applies, returning the value and whether it parsed.
// It is the single place validInt and negativeInt read, so a parse-rule change cannot
// make the malformed and negative checks drift apart.
func parseIntField(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	return n, err == nil
}

// negativeInt reports whether s parses (trimmed) as a negative integer. An empty or
// non-integer side is not negative; ValidNumericValue judges malformedness.
func negativeInt(s string) bool {
	n, ok := parseIntField(s)
	return ok && n < 0
}

// validInt reports whether s, after trimming surrounding whitespace, parses as an
// integer - the same parse [ParseNumPair] applies.
func validInt(s string) bool {
	_, ok := parseIntField(s)
	return ok
}

// ValidPartialDate accepts the ISO-8601 reduced precisions YYYY, YYYY-MM, and
// YYYY-MM-DD. It uses time.Parse so the calendar is checked properly - month
// range, days per month, and leap years - rejecting e.g. 2021-02-31. The exact
// length match enforces zero-padded canonical form (rejecting "2021-6-1"). It is
// shared by the linter's malformed-date rule and the set-time malformed-value
// note, so the two cannot disagree on what a valid date is.
func ValidPartialDate(s string) bool {
	// Trim first so incidental surrounding space is tolerated like every other typed value.
	s = strings.TrimSpace(s)
	// Year 0000 is not a meaningful recording/release year, but time.Parse accepts it; reject it
	// here so the linter's malformed-date rule and set-time validation agree. The year is always
	// the leading 4 characters in each canonical layout below.
	if strings.HasPrefix(s, "0000") {
		return false
	}
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if len(s) == len(layout) {
			if _, err := time.Parse(layout, s); err == nil {
				return true
			}
		}
	}
	return false
}

// ParseBool reads a canonical boolean tag value, accepting "1"/"true"/"yes"
// case-insensitively (with surrounding whitespace) as true. Shared so every codec
// interprets a boolean field (e.g. MP4 cpil) identically.
func ParseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// ValidBooleanValue reports whether v is a recognized boolean spelling for the
// boolean key k - "1"/"true"/"yes" or "0"/"false"/"no", case-insensitive and
// whitespace-trimmed, the affirmatives matching [ParseBool] exactly plus their
// negatives. A key that is not boolean is reported valid - there is nothing to
// check. It backs the set-time malformed-value note, so a value that does not
// round-trip through the bool projection ("maybe") can be flagged while still
// being written faithfully.
func ValidBooleanValue(k Key, v string) bool {
	if !booleanKeys[k] {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "0", "false", "no":
		return true
	default:
		return false
	}
}

// replayGainKeys is the canonical ReplayGain gain/peak keys, kept as a set so
// [IsReplayGainKey] is the single definition shared by the linter and the set-time
// malformed-value note, mirroring numericKeys/dateKeySet/booleanKeys.
var replayGainKeys = map[Key]bool{
	ReplayGainTrackGain: true,
	ReplayGainTrackPeak: true,
	ReplayGainAlbumGain: true,
	ReplayGainAlbumPeak: true,
}

// IsMediaTypeKey reports whether k is the MEDIATYPE (iTunes stik media-kind) key,
// whose value is a non-negative integer.
func IsMediaTypeKey(k Key) bool { return k == MediaType }

// IsMP4CanonicalKey reports whether a 16-bit MP4 integer atom canonicalizes k's value on
// decode - dropping a leading sign or leading zeros ("01" -> "1"). Those are the four number
// slots ([Key.NumberPair]: the track/disc number and total, packed into trkn/disk) plus
// MEDIATYPE (the stik media-kind atom). It is the key gate the diff command's cross-format
// numeric fold uses, so the fold applies only where an MP4 atom genuinely normalizes the value,
// not to every numeric key. It is deliberately not [numericKeys] (which carries PlayCount, not
// MediaType); keep the two sets apart.
func IsMP4CanonicalKey(k Key) bool { return k.NumberPair() || IsMediaTypeKey(k) }

// IsReplayGainKey reports whether k is a canonical ReplayGain gain or peak key.
func IsReplayGainKey(k Key) bool { return replayGainKeys[k] }

// ownAudioEncodingKeys describes values tied to this file's encoded audio: encoder stamps,
// encoding history, and sample fingerprints. ReplayGain keys are included through
// replayGainKeys. ACOUSTID_ID is omitted because it identifies the recording rather than
// this file's samples.
var ownAudioEncodingKeys = map[Key]bool{
	Encoder:             true,
	EncodedBy:           true,
	EncodingHistory:     true,
	AcoustIDFingerprint: true,
}

// DescribesOwnAudio reports whether the key's value describes this file's own audio rather
// than portable metadata about the work. Metadata-only transfers exclude such values so
// destination files keep their own encoder, gain, and fingerprint data.
func (k Key) DescribesOwnAudio() bool {
	return ownAudioEncodingKeys[k] || replayGainKeys[k]
}

// ValidMediaTypeValue reports whether v is a value the MEDIATYPE (iTunes stik media
// kind) key accepts: a non-negative integer in the uint32 range the atom stores. It
// mirrors the MP4 encoder's strconv.ParseUint(...,32) so a value the encoder would
// drop is flagged while one it stores (including a large 70000) passes. A non-MediaType
// key is reported valid - there is nothing to check.
func ValidMediaTypeValue(k Key, v string) bool {
	if k != MediaType {
		return true
	}
	_, err := strconv.ParseUint(strings.TrimSpace(v), 10, 32)
	return err == nil
}

// ValidReplayGainValue reports whether v is a value the ReplayGain key k accepts: a
// decimal number with an optional leading sign (a positive gain is conventionally written
// "+2.34 dB"), optionally suffixed with a case-insensitive "dB" (the conventional gain
// unit; a peak is unitless). A *_PEAK key additionally rejects any leading '-' (a peak is
// an amplitude, never signed), while a *_GAIN may carry either sign. A non-ReplayGain key
// is reported valid. It mirrors [ValidPartialDate]'s shape so the linter and the set-time
// note share one definition.
func ValidReplayGainValue(k Key, v string) bool {
	if !replayGainKeys[k] {
		return true
	}
	s := strings.TrimSpace(v)
	if len(s) >= 2 && strings.EqualFold(s[len(s)-2:], "dB") {
		s = strings.TrimSpace(s[:len(s)-2])
	}
	// strconv.ParseFloat is too permissive for a ReplayGain figure: it accepts scientific
	// (1e3), hex (0x1p-2), and underscored (1_0.5) forms. Pre-scan for the conventional
	// decimal shape - digits, at most one '.', an optional single leading sign - then let
	// ParseFloat finish the job (a lone sign or '.' passes this scan but ParseFloat rejects
	// it, so the two compose). A leading '+' is allowed: the ReplayGain convention writes a
	// positive gain with an explicit sign (e.g. "+2.34 dB"), so rejecting it would
	// false-flag legitimate values.
	if s == "" {
		return false
	}
	dots := 0
	for i := 0; i < len(s); i++ {
		switch b := s[i]; {
		case b == '+' || b == '-':
			if i != 0 {
				return false
			}
		case b == '.':
			if dots++; dots > 1 {
				return false
			}
		case b < '0' || b > '9':
			return false
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return false
	}
	// The byte-scan already rejects "NaN"/"Inf" (their letters), so this is a defensive
	// finite check.
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return false
	}
	// A peak is an amplitude, never signed: reject any leading '-' (so "-0.0" fails too,
	// not just a negative magnitude). A *_GAIN may be negative.
	if k == ReplayGainTrackPeak || k == ReplayGainAlbumPeak {
		return !strings.HasPrefix(s, "-")
	}
	return true
}

// Validator is the value contract for one category of canonical key - the single
// source the linter ([Document.Lint]) and the CLI's set-time malformed-value note
// both consume, so the "lint and set agree" contract cannot drift. Applies reports
// whether a key falls in the category; Valid reports whether a present, non-empty
// value is acceptable. LintDetail/NoteDetail are the human tails the two surfaces
// append, phrased for each (the linter as "%q <LintDetail>", the note as
// "KEY=VALUE <NoteDetail>; kept as text where the format supports it").
type Validator struct {
	Applies    func(Key) bool
	Valid      func(Key, string) bool
	LintCode   string
	LintDetail string
	NoteDetail string
}

// validators is the category registry. The category key-sets are disjoint, so a key
// matches at most one. RATING is deliberately absent: it is free-form across formats
// with no canonical numeric contract.
var validators = []Validator{
	{IsNumericKey, ValidNumericValue, "malformed-number",
		"is not a number", "does not look like a number"},
	{IsDateKey, func(_ Key, v string) bool { return ValidPartialDate(v) }, "malformed-date",
		"is not YYYY, YYYY-MM, or YYYY-MM-DD", "is not YYYY / YYYY-MM / YYYY-MM-DD"},
	{IsBooleanKey, ValidBooleanValue, "malformed-boolean",
		"is not a boolean (1/true/yes/0/false/no)", "does not look like a boolean (1/true/yes/0/false/no)"},
	{IsMediaTypeKey, ValidMediaTypeValue, "malformed-number",
		"is not a non-negative integer", "does not look like a non-negative integer"},
	{IsReplayGainKey, ValidReplayGainValue, "malformed-number",
		"is not a ReplayGain value (e.g. -7.30 dB)", "does not look like a ReplayGain value (e.g. -7.30 dB)"},
}

// ValidatorFor returns the value contract for key k, and whether k has one. A key in
// no category - a free-form key like RATING, or any custom key - returns false, so
// its values are never flagged as malformed.
func ValidatorFor(k Key) (Validator, bool) {
	for _, v := range validators {
		if v.Applies(k) {
			return v, true
		}
	}
	return Validator{}, false
}

// Performer is one credited performer: a Name and an optional Role (the part or
// instrument, e.g. "guitar"). It models a single PERFORMER value, stored as
// "Name (Role)" or a bare "Name" when Role is empty. Performer is a comparable
// struct, so two performers can be compared with ==.
//
// A Performer with an empty Name and a non-empty Role re-emits as "(Role)", which
// re-parses as {Name: "(Role)"} - the one shape that is not round-trip-stable;
// construct performers with a non-empty Name.
type PerformerCredit struct {
	Name string
	Role string
}

// parsePerformers reads PERFORMER values in order, splitting a trailing "(role)"
// off each into a Role. The split happens only when both the pre-paren name and the
// role text are non-empty after trimming; otherwise the whole value is kept as the
// Name, so a fully-parenthesized value ("(note)", "()", "Name ()") round-trips
// verbatim instead of losing its parentheses.
//
// Each value is trimmed once up front so incidental surrounding whitespace ("Name
// (role) ") does not hide the "(role)" suffix and leave it stuck on the name. The
// typed projection is a convenience view (lossy by design), so dropping that
// whitespace here is acceptable; the native bytes are preserved regardless.
func parsePerformers(vals []string) []PerformerCredit {
	if len(vals) == 0 {
		return nil
	}
	out := make([]PerformerCredit, 0, len(vals))
	for _, v := range vals {
		v = strings.TrimSpace(v)
		name, role := v, ""
		if strings.HasSuffix(v, ")") {
			if open := strings.LastIndexByte(v, '('); open >= 0 {
				n := strings.TrimSpace(v[:open])
				r := strings.TrimSpace(v[open+1 : len(v)-1])
				if n != "" && r != "" {
					name, role = n, r
				}
			}
		}
		out = append(out, PerformerCredit{Name: name, Role: role})
	}
	return out
}

// formatPerformers is the inverse of parsePerformers, emitting one value per
// performer IN ORDER (PERFORMER order is significant, so no sort). A performer with
// a role emits "Name (Role)"; a bare name emits the name; an empty-name performer
// with a role emits "(Role)" with no leading space (reachable only from a directly
// constructed Performer, and not round-trip-stable - see Performer).
func formatPerformers(ps []PerformerCredit) []string {
	if len(ps) == 0 {
		return nil
	}
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		switch {
		case p.Role == "":
			out = append(out, p.Name)
		case p.Name == "":
			out = append(out, "("+p.Role+")")
		default:
			out = append(out, p.Name+" ("+p.Role+")")
		}
	}
	return out
}
