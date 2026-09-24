package tag

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Tags is a typed projection of [TagSet]. Lossy on presence. Prefer TagSet when
// presence matters. [Project] reads; [Tags.Patch] sets non-empty fields only.
type Tags struct {
	Title       string
	Artists     []string
	Album       string
	AlbumArtist string
	Composers   []string
	Lyricists   []string
	Genres      []string

	TrackNumber int
	TrackTotal  int
	DiscNumber  int
	DiscTotal   int

	// Partial dates as strings (year / year-month / full).
	RecordingDate string
	ReleaseDate   string
	OriginalDate  string

	Comment   []string // multi-valued across formats
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

	ReleaseCountry string
	ReleaseStatus  string
	ReleaseTypes   []string // primary then secondary

	Conductor  string
	Remixer    string
	Performers []PerformerCredit // ordered PERFORMER credits
	// EncodedBy is the encoding person; Encoder is the encoding software.
	EncodedBy string
	Encoder   string

	// Contributor-role credits (multivalued). On ID3 the first five map to the
	// involved-people list (TIPL/IPLS); Writers is a TXXX:Writer user frame.
	Producers []string
	Engineers []string
	Mixers    []string
	Arrangers []string
	Writers   []string
	DJMixers  []string

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

	// iTunes structured fields. ITunesAdvisory, BPM, Movement, and MovementTotal
	// stay strings: their atoms hold unsigned values (and tmpo accepts fractions
	// as text elsewhere), which the signed int projection cannot carry.
	ITunesAdvisory string
	ITunesGapless  bool
	ShowMovement   bool
	BPM            string
	Work           string
	MovementName   string
	Movement       string
	MovementTotal  string
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
		Lyricists:   all(Lyricist),
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

		ReleaseCountry: first(ReleaseCountry),
		ReleaseStatus:  first(ReleaseStatus),
		ReleaseTypes:   all(ReleaseType),

		Conductor: first(Conductor),
		Remixer:   first(Remixer),
		EncodedBy: first(EncodedBy),
		Encoder:   first(Encoder),

		Producers: all(Producer),
		Engineers: all(Engineer),
		Mixers:    all(Mixer),
		Arrangers: all(Arranger),
		Writers:   all(Writer),
		DJMixers:  all(DJMixer),

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

		ITunesAdvisory: first(ITunesAdvisory),
		ITunesGapless:  ParseBool(first(ITunesGapless)),
		ShowMovement:   ParseBool(first(ShowMovement)),
		BPM:            first(BPM),
		Work:           first(Work),
		MovementName:   first(MovementName),
		Movement:       first(Movement),
		MovementTotal:  first(MovementTotal),
	}

	t.TrackNumber, t.TrackTotal = ParseNumPair(first(TrackNumber), first(TrackTotal))
	t.DiscNumber, t.DiscTotal = ParseNumPair(first(DiscNumber), first(DiscTotal))
	// Match ParseNumPair: trim surrounding whitespace, and every error, overflow included,
	// yields 0 rather than strconv.Atoi's partial value.
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
	setMulti(Lyricist, t.Lyricists)
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

	setStr(ReleaseCountry, t.ReleaseCountry)
	setStr(ReleaseStatus, t.ReleaseStatus)
	setMulti(ReleaseType, t.ReleaseTypes)

	setStr(Conductor, t.Conductor)
	setStr(Remixer, t.Remixer)
	setStr(EncodedBy, t.EncodedBy)
	setStr(Encoder, t.Encoder)
	setMulti(Performer, formatPerformers(t.Performers))
	setMulti(Producer, t.Producers)
	setMulti(Engineer, t.Engineers)
	setMulti(Mixer, t.Mixers)
	setMulti(Arranger, t.Arrangers)
	setMulti(Writer, t.Writers)
	setMulti(DJMixer, t.DJMixers)

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

	setStr(ITunesAdvisory, t.ITunesAdvisory)
	if t.ITunesGapless {
		p.Set(ITunesGapless, "1")
	}
	if t.ShowMovement {
		p.Set(ShowMovement, "1")
	}
	setStr(BPM, t.BPM)
	setStr(Work, t.Work)
	setStr(MovementName, t.MovementName)
	setStr(Movement, t.Movement)
	setStr(MovementTotal, t.MovementTotal)

	return p
}

// ParseNumPair resolves a "number" and "total" pair (e.g. track or disc
// numbering). The number field may use the "n/total" convention; an explicit
// total wins. Surrounding whitespace is ignored. Exported so codecs that store a
// structured pair (MP4 trkn/disk) parse the canonical strings as the projection does.
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

// Fold lowercases s and trims surrounding whitespace, for case- and
// space-insensitive comparison. [core.Fold] delegates to it (core imports tag,
// not the reverse), so every caller in the tree folds identically.
func Fold(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// DistinctValues counts the distinct values in vals after [Fold]. Dump duplicate
// markers and codec family-conflict checks share it.
func DistinctValues(vals []string) int {
	seen := make(map[string]bool, len(vals))
	for _, v := range vals {
		seen[Fold(v)] = true
	}
	return len(seen)
}

// SplitNumberTotal splits a "number/total" value (e.g. "3/12") on the first '/'
// into its trimmed number and total substrings. Unlike [ParseNumPair] it keeps the
// exact substrings, leading zeros included, so an edit-time normalization does not
// rewrite the value. Either side is "" when absent or blank ("3/" -> "3",""; "/12"
// -> "","12"). The ID3 read path and the edit-time pair split share it.
func SplitNumberTotal(v string) (num, total string) {
	num, total, _ = strings.Cut(v, "/")
	return strings.TrimSpace(num), strings.TrimSpace(total)
}

// TotalKey returns the canonical "total" companion for a numbering key:
// [TrackNumber] -> [TrackTotal], [DiscNumber] -> [DiscTotal]. Any other key is
// returned unchanged. The codecs and the edit-time numbering split share it.
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

// NumberTotalSplit splits a track/disc "n/total" for read paths ([SplitNumberTotal]).
// Non-pair or malformed returns the value as num with split=false.
func NumberTotalSplit(k Key, v string) (num, total string, split bool) {
	if (k == TrackNumber || k == DiscNumber) &&
		strings.ContainsRune(v, '/') && ValidNumericValue(k, v) {
		num, total = SplitNumberTotal(v)
		return num, total, true
	}
	return v, "", false
}

// NormalizeNumberPairs splits slashed TRACKNUMBER/DISCNUMBER in a read projection
// into number + total when no total is already present.
func NormalizeNumberPairs(ts *TagSet) {
	for _, numKey := range []Key{TrackNumber, DiscNumber} {
		vals, ok := ts.Get(numKey)
		if !ok || len(vals) != 1 {
			continue // absent, or multi-valued (never lose a value)
		}
		SplitNumberValue(ts, numKey, vals[0], !ts.Has(TotalKey(numKey)))
	}
}

// SplitNumberValue applies [NumberTotalSplit] to one key. setTotal controls whether
// a derived total is written (read pass vs editor patch.Touches).
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

// numericKeys are the canonical keys whose typed [Tags] projection is an int, so a
// non-numeric value reads 0 there: the track and disc number/total, and the play
// count. Rating (a free-form string) and MediaType (vocabulary-only, no typed
// accessor) are excluded. It backs [IsNumericKey] and the set-time malformed-value
// note. It is not [IsMP4CanonicalKey], which omits PlayCount and carries MediaType;
// do not fold the two together.
var numericKeys = map[Key]bool{
	TrackNumber: true,
	TrackTotal:  true,
	DiscNumber:  true,
	DiscTotal:   true,
	PlayCount:   true,
}

// dateKeySet is the canonical partial-date keys. [IsDateKey] reads it for the
// linter's malformed-date rule and the set-time malformed-value note.
var dateKeySet = map[Key]bool{
	RecordingDate:   true,
	ReleaseDate:     true,
	OriginalDate:    true,
	AcquisitionDate: true,
}

// booleanKeys is the canonical keys whose value is a boolean flag: Compilation,
// ITunesGapless, and ShowMovement, whose typed [Tags] projections are bools
// ([ParseBool]). [IsBooleanKey] reads it for the set-time malformed-value note.
var booleanKeys = map[Key]bool{
	Compilation:   true,
	ITunesGapless: true,
	ShowMovement:  true,
}

// IsNumericKey reports whether k canonically holds a numeric value (one with an
// int projection in [Tags]): the track/disc number and total, and play count.
func IsNumericKey(k Key) bool { return numericKeys[k] }

// IsDateKey reports whether k canonically holds an ISO-8601 partial date (YYYY,
// YYYY-MM, or YYYY-MM-DD).
func IsDateKey(k Key) bool { return dateKeySet[k] }

// IsBooleanKey reports whether k canonically holds a boolean flag (one with a
// bool projection in [Tags]): Compilation, ITunesGapless, and ShowMovement.
func IsBooleanKey(k Key) bool { return booleanKeys[k] }

// ValidNumericValue reports whether v is a value the numeric key k accepts
// without loss. It mirrors [ParseNumPair], so it never flags a value that
// round-trips: surrounding whitespace is ignored, TrackNumber and DiscNumber
// accept the "number/total" convention, and the parse is strconv.Atoi (which
// accepts a leading sign). A non-numeric key is reported valid.
func ValidNumericValue(k Key, v string) bool {
	if !numericKeys[k] {
		return true
	}
	// Only the number fields carry "n/total" (ParseNumPair splits only those).
	if k == TrackNumber || k == DiscNumber {
		if num, total, ok := strings.Cut(v, "/"); ok {
			// A bare "/" is malformed: it carries no number, and passing it would let
			// splitNumberPairs delete the key. One blank side ("3/" or "/2") is fine;
			// ParseNumPair reads it as 0.
			if strings.TrimSpace(num) == "" && strings.TrimSpace(total) == "" {
				return false
			}
			return numComponent(num) && numComponent(total)
		}
	}
	return validInt(v)
}

// numComponent reports whether one side of a "number/total" value is acceptable.
// An empty side ("3/" or "/2") is fine: ParseNumPair reads it as 0, so the value
// round-trips. Only a non-empty, non-numeric side is malformed.
func numComponent(s string) bool {
	return strings.TrimSpace(s) == "" || validInt(s)
}

// NegativeNumericValue reports whether numeric key k's value v has a negative
// component. Atoi accepts a leading sign, so such a value round-trips and
// [ValidNumericValue] accepts it, but a negative track/disc number, total, or play
// count is odd, so the CLI advises on it and still writes it. The "n/total" pair
// keys check each side: both -3/10 and 3/-10 are caught. A non-numeric key reports
// false.
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
// DiscNumber with an empty number and a numeric total, such as "/5". The value is writable,
// but the empty number is easy to type by accident, so the CLI reports an advisory. It judges
// only the submitted pair; an explicit total key can override the embedded total.
func EmptyNumberWithTotal(k Key, v string) bool {
	if k != TrackNumber && k != DiscNumber {
		return false
	}
	num, total := SplitNumberTotal(v)
	return num == "" && validInt(total)
}

// IsTrimmableKey reports whether k holds a single-token value whose surrounding whitespace is
// never meaningful: a numeric, date, MP4-integer, BPM, ReplayGain, R128 gain, or release-country
// key. [TrimTokenValue], the editor's per-key trim gate, and the transfer grade all use it, so
// adding a key here updates all three.
func IsTrimmableKey(k Key) bool {
	return numericKeys[k] || dateKeySet[k] || IsMP4IntKey(k) || IsBPMKey(k) ||
		IsReplayGainKey(k) || IsReleaseCountryKey(k) || IsR128GainKey(k)
}

// TrimTokenValue removes surrounding whitespace from a trimmable value (see [IsTrimmableKey]) and
// leaves other values unchanged. The editor and CLI advisories share it, so stored values match
// the forms [ValidNumericValue] and [ValidPartialDate] accept. Internal whitespace (the space
// before "dB") and digits, leading zeros included, are preserved.
func TrimTokenValue(k Key, v string) string {
	if IsTrimmableKey(k) {
		return strings.TrimSpace(v)
	}
	return v
}

// parseIntField parses one trimmed numeric component with the parse [ParseNumPair]
// applies, returning the value and whether it parsed. validInt and negativeInt both
// read it, so the malformed and negative checks share one parse rule.
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

// validInt reports whether s, trimmed, parses as an integer, the same parse
// [ParseNumPair] applies.
func validInt(s string) bool {
	_, ok := parseIntField(s)
	return ok
}

// ValidPartialDate accepts the ISO-8601 reduced precisions YYYY, YYYY-MM, and
// YYYY-MM-DD. time.Parse checks the calendar (month range, days per month, leap
// years), so 2021-02-31 is rejected. The exact length match enforces zero-padded
// form, rejecting "2021-6-1". The linter's malformed-date rule and the set-time
// malformed-value note share it.
func ValidPartialDate(s string) bool {
	// Trim first so incidental surrounding space is tolerated like every other typed value.
	s = strings.TrimSpace(s)
	// time.Parse accepts year 0000, which is not a meaningful year; reject it so lint and
	// set-time validation agree. The year is the leading 4 characters of every layout.
	if strings.HasPrefix(s, "0000") {
		return false
	}
	for _, layout := range partialDateLayouts {
		if len(s) == len(layout) {
			if _, err := time.Parse(layout, s); err == nil {
				return true
			}
		}
	}
	return false
}

// partialDateLayouts are the accepted reduced precisions, longest first.
// [ValidPartialDate] and partialDateShaped both read them.
var partialDateLayouts = []string{"2006-01-02", "2006-01", "2006"}

// The two halves of the malformed-date complaint. A value not shaped like an accepted
// layout gets the shape wording; one that is gets the calendar wording from
// [partialDateDetail]. "2001-13-01" is YYYY-MM-DD shaped; its month does not exist.
const (
	dateShapeLintDetail = "is not YYYY, YYYY-MM, or YYYY-MM-DD"
	dateShapeNoteDetail = "is not YYYY / YYYY-MM / YYYY-MM-DD"
	// One wording serves both surfaces; there are no layouts to spell differently.
	dateCalendarDetail = "is not a real date"
)

// partialDateDetail explains why a date value failed [ValidPartialDate], as the lint tail
// and the set-time note tail. A value with the digit layout of an accepted precision
// failed on the calendar (an out-of-range month, a day past the month's length, or year
// 0000); anything else, non-canonical padding ("2021-6-1") included, failed on shape. It
// backs the date validator's Detail hook, so both surfaces classify a value the same way.
func partialDateDetail(_ Key, v string) (lint, note string) {
	if partialDateShaped(v) {
		return dateCalendarDetail, dateCalendarDetail
	}
	return dateShapeLintDetail, dateShapeNoteDetail
}

// partialDateShaped reports whether v matches one of [partialDateLayouts] positionally: a
// digit wherever the layout has one, a hyphen wherever it has one. It says nothing about
// whether the date exists. Reading the layouts keeps the zero-padded widths, which separate
// a shape complaint from a calendar one, defined once.
func partialDateShaped(v string) bool {
	v = strings.TrimSpace(v)
	for _, layout := range partialDateLayouts {
		if len(v) != len(layout) {
			continue
		}
		ok := true
		for i := 0; i < len(layout) && ok; i++ {
			if layout[i] == '-' {
				ok = v[i] == '-'
			} else {
				ok = isAllASCIIDigits(v[i : i+1])
			}
		}
		if ok {
			return true
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

// CanonicalBoolValue normalizes a recognized boolean spelling to the "1"/"0" a boolean tag
// stores: "1"/"true"/"yes" (the [ParseBool] affirmatives) become "1", and "0"/"false"/"no"
// become "0", case-insensitively and whitespace-trimmed. An unrecognized value ("maybe") is
// returned unchanged, so a codec keeps it as literal text. It matches the "1" [Tags.Patch]
// writes and MP4's cpil canonicalization, so FLAC, ID3, and MP4 store a boolean identically.
func CanonicalBoolValue(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return "1"
	case "0", "false", "no":
		return "0"
	default:
		return v
	}
}

// ValidBooleanValue reports whether v is a recognized boolean spelling for the
// boolean key k: "1"/"true"/"yes" (matching [ParseBool]) or "0"/"false"/"no",
// case-insensitive and whitespace-trimmed. A non-boolean key is reported valid.
// It backs the set-time malformed-value note, so a value that does not round-trip
// through the bool projection ("maybe") is flagged but still written.
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

// replayGainKeys is the canonical ReplayGain gain/peak keys. [IsReplayGainKey]
// reads it for the linter and the set-time malformed-value note.
var replayGainKeys = map[Key]bool{
	ReplayGainTrackGain: true,
	ReplayGainTrackPeak: true,
	ReplayGainAlbumGain: true,
	ReplayGainAlbumPeak: true,
}

// r128GainKeys is the Opus loudness tags RFC 7845 defines. They are ordinary custom keys
// outside the canonical vocabulary, but they describe this file's own audio, so a metadata
// copy must not carry them. They are not ReplayGain keys: the value is a Q7.8 integer, not
// the "-3.50 dB" text the ReplayGain checks expect.
var r128GainKeys = map[Key]bool{
	"R128_TRACK_GAIN": true,
	"R128_ALBUM_GAIN": true,
}

// IsR128GainKey reports whether k is one of the Opus R128 loudness keys.
func IsR128GainKey(k Key) bool { return r128GainKeys[k] }

// IsMediaTypeKey reports whether k is the MEDIATYPE (iTunes stik media-kind) key,
// whose value is a non-negative integer.
func IsMediaTypeKey(k Key) bool { return k == MediaType }

// mp4IntKeyMax maps each canonical key stored as an unsigned MP4 integer atom to
// the largest value its atom holds: one byte for stik and rtng, two for ©mvi and
// ©mvc. [ValidMP4IntValue] and the MP4 encoder both derive from it. BPM (tmpo) is
// absent: its validator accepts fractions.
var mp4IntKeyMax = map[Key]uint64{
	MediaType:      0xFF,
	ITunesAdvisory: 0xFF,
	Movement:       0xFFFF,
	MovementTotal:  0xFFFF,
}

// IsMP4IntKey reports whether k is a canonical key stored as an unsigned MP4
// integer atom: MEDIATYPE (stik), ITUNESADVISORY (rtng), MOVEMENT (©mvi), and
// MOVEMENTTOTAL (©mvc).
func IsMP4IntKey(k Key) bool { _, ok := mp4IntKeyMax[k]; return ok }

// ValidMP4IntValue reports whether v is a value the MP4-integer key k accepts: a
// non-negative integer no greater than the key's atom holds (255 for the one-byte
// stik/rtng, 65535 for the two-byte movement atoms), ignoring surrounding
// whitespace. It mirrors the MP4 encoder's intItem, which rejects a value past the
// atom's width, so a value the encoder drops is flagged here too. The parse is
// ParseUint, which rejects a leading '+': the atom stores an unsigned magnitude
// with no sign to round-trip. A non-MP4-integer key is reported valid.
func ValidMP4IntValue(k Key, v string) bool {
	max, ok := mp4IntKeyMax[k]
	if !ok {
		return true
	}
	n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
	return err == nil && n <= max
}

// IsBPMKey reports whether k is the BPM (beats-per-minute) key, whose value is a
// non-negative decimal number.
func IsBPMKey(k Key) bool { return k == BPM }

// ValidBPMValue reports whether v is a value the BPM key accepts: a non-negative
// decimal number no greater than 65535 (the two-byte tmpo atom's ceiling),
// fractions included ("174.99"; DJ tools write fractional BPM), ignoring
// surrounding whitespace. Like [ValidReplayGainValue], a byte pre-scan for digits
// and at most one '.' precedes ParseFloat, so the scientific, hex, and underscored
// forms ParseFloat alone accepts ("1e3", "0x1p7", "1_0") are rejected. No sign is
// allowed: the atom stores an unsigned magnitude. Text formats store the value
// verbatim; the MP4 encoder's tmpoItem drops exactly what this rejects and rounds
// the rest. A non-BPM key is reported valid.
func ValidBPMValue(k Key, v string) bool {
	if k != BPM {
		return true
	}
	s := strings.TrimSpace(v)
	if s == "" {
		return false
	}
	dots := 0
	for i := 0; i < len(s); i++ {
		switch b := s[i]; {
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
	// The byte-scan already rejects "NaN"/"Inf"; this is a defensive finite check.
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return false
	}
	return f <= maxBPM
}

// maxBPM is the largest BPM the two-byte MP4 tmpo atom holds. [ValidBPMValue] and
// bpmDetail share it.
const maxBPM = 65535

// BPMStoredWhole returns the whole-number decimal form the MP4 tmpo atom stores for a BPM
// value, and whether storing changes the numeric value: a fraction rounds to nearest, while
// a respelling such as "174.0" or "0174" is lossless. ok is false for a value [ValidBPMValue]
// rejects, which the atom drops. The MP4 encoder, its coercion report, and the diff fold all
// derive from it.
func BPMStoredWhole(v string) (stored string, roundChanged, ok bool) {
	s := strings.TrimSpace(v)
	if !ValidBPMValue(BPM, s) {
		return "", false, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", false, false
	}
	r := math.Round(f)
	return strconv.FormatUint(uint64(r), 10), r != f, true
}

// IsMP4CanonicalKey reports whether an MP4 integer atom canonicalizes k's value on
// decode by dropping a leading sign or leading zeros ("01" -> "1") or rounding a
// fraction: the four number slots ([Key.NumberPair], packed into trkn/disk), the
// unsigned integer atoms ([IsMP4IntKey]: stik, rtng, ©mvi, ©mvc), and BPM (tmpo
// rounds to a whole number). The diff command's cross-format numeric fold uses it
// as its key gate, so the fold applies only where an atom normalizes the value. It
// is not [numericKeys], which carries PlayCount and none of the atom-backed keys.
func IsMP4CanonicalKey(k Key) bool { return k.NumberPair() || IsMP4IntKey(k) || IsBPMKey(k) }

// IsReplayGainKey reports whether k is a canonical ReplayGain gain or peak key.
func IsReplayGainKey(k Key) bool { return replayGainKeys[k] }

// ownAudioEncodingKeys describes values tied to this file's encoded audio: encoder stamps,
// encoding history, and sample fingerprints. The ReplayGain and Opus R128 loudness keys are
// included through replayGainKeys and r128GainKeys. ACOUSTID_ID is omitted because it
// identifies the recording rather than this file's samples.
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
	return ownAudioEncodingKeys[k] || replayGainKeys[k] || r128GainKeys[k]
}

// ValidMediaTypeValue reports whether v is a value the MEDIATYPE (iTunes stik media kind) key
// accepts: a non-negative integer no greater than 255, the single byte the stik atom stores
// (the defined iTunes media kinds are 0-14). It wraps [ValidMP4IntValue] and is kept for API
// stability, including its any-other-key-is-valid contract, so it stays a no-op for the other
// MP4-integer keys.
func ValidMediaTypeValue(k Key, v string) bool {
	if k != MediaType {
		return true
	}
	return ValidMP4IntValue(k, v)
}

// IsReleaseCountryKey reports whether k is the RELEASECOUNTRY key, whose value is an
// ISO 3166-1 alpha-2 country code.
func IsReleaseCountryKey(k Key) bool { return k == ReleaseCountry }

// ValidReleaseCountryValue reports whether v is a value RELEASECOUNTRY accepts: exactly
// two ASCII letters, ignoring surrounding whitespace. That is the ISO 3166-1 alpha-2 shape,
// and it admits MusicBrainz's XW (worldwide) and XE (Europe) without a country whitelist.
// Case is not checked: "gb" names the same country as "GB". A non-ReleaseCountry key is
// reported valid.
func ValidReleaseCountryValue(k Key, v string) bool {
	if k != ReleaseCountry {
		return true
	}
	s := strings.TrimSpace(v)
	if len(s) != 2 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if b := s[i]; !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z') {
			return false
		}
	}
	return true
}

// ValidR128GainValue reports whether v is a value the R128 loudness key k accepts. RFC 7845
// section 5.2.1: "an integer from -32768 to 32767, inclusive, represented in ASCII as a base
// 10 number with no whitespace. A leading '+' or '-' character is valid. Leading zeros are
// also permitted, but the value MUST be represented by no more than 6 characters".
// Surrounding whitespace is trimmed first, like every other single-token validator here. A
// non-R128 key is reported valid.
func ValidR128GainValue(k Key, v string) bool {
	if !r128GainKeys[k] {
		return true
	}
	// strconv.Atoi is the RFC's grammar: an optional sign then ASCII digits, with no
	// exponent, hex, or underscore forms. The 6-character cap and the range are checked here.
	s := strings.TrimSpace(v)
	if len(s) > 6 {
		return false
	}
	n, err := strconv.Atoi(s)
	return err == nil && n >= math.MinInt16 && n <= math.MaxInt16
}

// ValidReplayGainValue reports whether v is a value the ReplayGain key k accepts: a
// decimal number with an optional leading sign (a positive gain is conventionally
// "+2.34 dB"), optionally suffixed with a case-insensitive "dB" (a peak is unitless).
// A *_PEAK key also rejects any leading '-', since a peak is an amplitude; a *_GAIN may
// carry either sign. A non-ReplayGain key is reported valid. The linter and the set-time
// note share it.
func ValidReplayGainValue(k Key, v string) bool {
	if !replayGainKeys[k] {
		return true
	}
	s := strings.TrimSpace(v)
	if len(s) >= 2 && strings.EqualFold(s[len(s)-2:], "dB") {
		s = strings.TrimSpace(s[:len(s)-2])
	}
	// strconv.ParseFloat alone accepts scientific (1e3), hex (0x1p-2), and underscored
	// (1_0.5) forms. Pre-scan for the conventional decimal shape (digits, at most one '.',
	// an optional single leading sign), then let ParseFloat finish; a lone sign or '.'
	// passes the scan but ParseFloat rejects it. A leading '+' is allowed because the
	// ReplayGain convention writes a positive gain as "+2.34 dB".
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
	// The byte-scan already rejects "NaN"/"Inf"; this is a defensive finite check.
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return false
	}
	// A peak is an amplitude, never signed: reject any leading '-', so "-0.0" fails too.
	// A *_GAIN may be negative.
	if k == ReplayGainTrackPeak || k == ReplayGainAlbumPeak {
		return !strings.HasPrefix(s, "-")
	}
	return true
}

// overRangeDetail names the ceiling for a value of the right shape that exceeds what the
// destination atom holds. "is not a non-negative number" would be false for BPM=70000, so
// the over-range case names the maximum, as partialDateDetail names the calendar.
func overRangeDetail(what string, max uint64) (lint, note string) {
	return fmt.Sprintf("is %s but exceeds the maximum of %d", what, max),
		fmt.Sprintf("is %s but exceeds the maximum of %d", what, max)
}

// mp4IntDetail explains an MP4-integer rejection. The ceiling is per key (a one-byte stik
// or rtng, a two-byte movement index), which is why Detail is given the key.
func mp4IntDetail(k Key, v string) (lint, note string) {
	max, ok := mp4IntKeyMax[k]
	if ok {
		if n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64); err == nil && n > max {
			return overRangeDetail("a non-negative integer", max)
		}
	}
	return "is not a non-negative integer", "does not look like a non-negative integer"
}

// bpmDetail explains a BPM rejection: over the two-byte tmpo ceiling, or not a
// non-negative decimal at all.
func bpmDetail(_ Key, v string) (lint, note string) {
	s := strings.TrimSpace(v)
	if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) &&
		f > maxBPM && !strings.ContainsAny(s, "+-") {
		return overRangeDetail("a non-negative number", maxBPM)
	}
	return "is not a non-negative number", "does not look like a non-negative number"
}

// replayGainDetail separates a negative peak from a value that is not a ReplayGain figure.
// A peak is an amplitude, so "-0.5" is well-formed but cannot be negative; the generic
// wording would send the user looking for a syntax error.
func replayGainDetail(k Key, v string) (lint, note string) {
	if k == ReplayGainTrackPeak || k == ReplayGainAlbumPeak {
		s := strings.TrimSpace(v)
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(s, "dB"), "DB"))
		if strings.HasPrefix(s, "-") {
			if _, err := strconv.ParseFloat(s, 64); err == nil {
				return "is negative; a peak is an amplitude, never signed",
					"is negative; a peak is an amplitude, never signed"
			}
		}
	}
	return "is not a ReplayGain value (e.g. -7.30 dB)", "does not look like a ReplayGain value (e.g. -7.30 dB)"
}

// Validator is the value contract for one category of canonical key. The linter
// ([Document.Lint]) and the CLI's set-time malformed-value note both consume it.
// Applies reports whether a key falls in the category; Valid reports whether a
// present, non-empty value is acceptable. LintDetail/NoteDetail are the tails the
// two surfaces append: the linter as "%q <LintDetail>", the note as "KEY=VALUE
// <NoteDetail>; kept as text where the format supports it".
type Validator struct {
	Applies    func(Key) bool
	Valid      func(Key, string) bool
	LintCode   string
	LintDetail string
	NoteDetail string
	// Detail refines LintDetail/NoteDetail for one rejected value, for a category with
	// more than one failure mode. It takes the key because a category can be range-checked
	// per key (the MP4 integer atoms differ in width). Nil for a single failure mode;
	// [Validator.Details] falls back to the fixed pair.
	Detail func(k Key, value string) (lint, note string)
}

// Details returns the tails to append for key k's rejected value: the per-value
// refinement when the category has one, otherwise the fixed pair. Both surfaces call it,
// so the linter's finding and the set-time note describe a rejected value the same way.
func (v Validator) Details(k Key, value string) (lint, note string) {
	if v.Detail != nil {
		return v.Detail(k, value)
	}
	return v.LintDetail, v.NoteDetail
}

// validators is the category registry. The key sets are disjoint, so a key matches at
// most one. RATING is absent: it is free-form across formats.
var validators = []Validator{
	{IsNumericKey, ValidNumericValue, "malformed-number",
		"is not a number", "does not look like a number", nil},
	{IsDateKey, func(_ Key, v string) bool { return ValidPartialDate(v) }, "malformed-date",
		dateShapeLintDetail, dateShapeNoteDetail, partialDateDetail},
	{IsBooleanKey, ValidBooleanValue, "malformed-boolean",
		"is not a boolean (1/true/yes/0/false/no)", "does not look like a boolean (1/true/yes/0/false/no)", nil},
	{IsMP4IntKey, ValidMP4IntValue, "malformed-number",
		"is not a non-negative integer", "does not look like a non-negative integer", mp4IntDetail},
	{IsBPMKey, ValidBPMValue, "malformed-number",
		"is not a non-negative number", "does not look like a non-negative number", bpmDetail},
	{IsReleaseCountryKey, ValidReleaseCountryValue, "malformed-country",
		"is not a two-letter country code (ISO 3166-1 alpha-2, e.g. GB)",
		"does not look like a two-letter country code (ISO 3166-1 alpha-2, e.g. GB)", nil},
	{IsReplayGainKey, ValidReplayGainValue, "malformed-number",
		"is not a ReplayGain value (e.g. -7.30 dB)", "does not look like a ReplayGain value (e.g. -7.30 dB)",
		replayGainDetail},
	{IsR128GainKey, ValidR128GainValue, "malformed-number",
		"is not an R128 gain (a signed integer, at most 6 characters, e.g. -573)",
		"does not look like an R128 gain (a signed integer, at most 6 characters, e.g. -573)", nil},
}

// ValidatorFor returns the value contract for key k, and whether k has one. A key in
// no category (RATING, or any custom key) returns false, so its values are never flagged.
func ValidatorFor(k Key) (Validator, bool) {
	for _, v := range validators {
		if v.Applies(k) {
			return v, true
		}
	}
	return Validator{}, false
}

// PerformerCredit is one credited performer: a Name and an optional Role (the part
// or instrument, e.g. "guitar"). It models one PERFORMER value, stored as
// "Name (Role)", or a bare "Name" when Role is empty. It is comparable with ==.
//
// An empty Name with a non-empty Role re-emits as "(Role)", which re-parses as
// {Name: "(Role)"}; that shape is not round-trip-stable, so construct credits
// with a non-empty Name.
type PerformerCredit struct {
	Name string
	Role string
}

// parsePerformers reads PERFORMER values in order, splitting a trailing "(role)"
// off each into a Role. The split happens only when both the name before the paren
// and the role text are non-empty after trimming; otherwise the whole value is the
// Name, so "(note)", "()", and "Name ()" round-trip verbatim.
//
// Each value is trimmed first so surrounding whitespace ("Name (role) ") does not
// hide the "(role)" suffix. The typed projection is lossy, so dropping that
// whitespace is acceptable; the native bytes are preserved regardless.
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
// performer in order (PERFORMER order is significant, so no sort). A performer with
// a role emits "Name (Role)"; a bare name emits the name; an empty-name performer
// with a role emits "(Role)", reachable only from a directly constructed credit and
// not round-trip-stable (see [PerformerCredit]).
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
