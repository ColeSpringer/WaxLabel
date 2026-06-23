package tag

import (
	"math"
	"slices"
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

	Comment   string
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
	// Performers maps a role to the people in it (e.g. "guitar" -> {"Foo"}).
	// The empty role holds unqualified PERFORMER values.
	Performers map[string][]string
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

		Comment:   first(Comment),
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
	}

	t.TrackNumber, t.TrackTotal = ParseNumPair(first(TrackNumber), first(TrackTotal))
	t.DiscNumber, t.DiscTotal = ParseNumPair(first(DiscNumber), first(DiscTotal))
	t.PlayCount, _ = strconv.Atoi(first(PlayCount))
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

	setStr(Comment, t.Comment)
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

	return p
}

// ParseNumPair resolves a "number" and "total" pair (e.g. track or disc
// numbering). The number field may use the "n/total" convention; an explicit
// total field wins if present. Surrounding whitespace is ignored. It is exported
// so codecs that store numbering as a structured pair (e.g. MP4 trkn/disk) parse
// the canonical strings the same way the typed projection does.
func ParseNumPair(num, total string) (n, tot int) {
	if num != "" {
		if i := strings.IndexByte(num, '/'); i >= 0 {
			n, _ = strconv.Atoi(strings.TrimSpace(num[:i]))
			tot, _ = strconv.Atoi(strings.TrimSpace(num[i+1:]))
		} else {
			n, _ = strconv.Atoi(strings.TrimSpace(num))
		}
	}
	if total != "" {
		tot, _ = strconv.Atoi(strings.TrimSpace(total))
	}
	return n, tot
}

// SplitNumberTotal splits a "number/total" value (e.g. "3/12") on the first '/'
// into its trimmed number and total substrings. Unlike [ParseNumPair] it preserves
// the exact substrings - leading zeros and all - rather than renumbering to ints, so
// it suits a write/edit normalization that must not silently rewrite the value. Each
// side is "" when absent or blank ("3/" -> "3","" ; "/12" -> "","12"). It is the
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

// numericKeys are the canonical keys whose typed [Tags] projection is an int, so
// a non-numeric value does not round-trip through that accessor (it reads 0): the
// track and disc number/total, and the play count. Rating is excluded (it is a
// free-form string), and so is MediaType (vocabulary-only, no typed accessor). It
// backs [IsNumericKey] and the set-time malformed-value note.
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

// IsReplayGainKey reports whether k is a canonical ReplayGain gain or peak key.
func IsReplayGainKey(k Key) bool { return replayGainKeys[k] }

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
// decimal number, optionally suffixed with a case-insensitive "dB" (the conventional
// gain unit; a peak is unitless). A *_PEAK key additionally requires a non-negative
// magnitude (a peak is an amplitude, never signed), while a *_GAIN may be negative. A
// non-ReplayGain key is reported valid. It mirrors [ValidPartialDate]'s shape so the
// linter and the set-time note share one definition.
func ValidReplayGainValue(k Key, v string) bool {
	if !replayGainKeys[k] {
		return true
	}
	s := strings.TrimSpace(v)
	if len(s) >= 2 && strings.EqualFold(s[len(s)-2:], "dB") {
		s = strings.TrimSpace(s[:len(s)-2])
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return false
	}
	// ParseFloat accepts "NaN"/"Inf"/"+Inf"/"-Inf" without error, but a ReplayGain
	// value is a finite decibel/amplitude figure, so reject the non-finite spellings.
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return false
	}
	if (k == ReplayGainTrackPeak || k == ReplayGainAlbumPeak) && f < 0 {
		return false
	}
	return true
}

// Validator is the value contract for one category of canonical key - the single
// source the linter ([Document.Lint]) and the CLI's set-time malformed-value note
// both consume, so the "lint and set agree" contract cannot drift. Applies reports
// whether a key falls in the category; Valid reports whether a present, non-empty
// value is acceptable. LintDetail/NoteDetail are the human tails the two surfaces
// append, phrased for each (the linter as "%q <LintDetail>", the note as
// "KEY=VALUE <NoteDetail>; written as-is").
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

// parsePerformers reads PERFORMER values, splitting a trailing "(role)" off
// each. Values without a role land under the empty-string key.
func parsePerformers(vals []string) map[string][]string {
	if len(vals) == 0 {
		return nil
	}
	out := make(map[string][]string)
	for _, v := range vals {
		name, role := v, ""
		if strings.HasSuffix(v, ")") {
			if open := strings.LastIndexByte(v, '('); open >= 0 {
				name = strings.TrimSpace(v[:open])
				role = strings.TrimSpace(v[open+1 : len(v)-1])
			}
		}
		out[role] = append(out[role], name)
	}
	return out
}

// formatPerformers is the inverse of parsePerformers. Roles are emitted in
// sorted order for deterministic output; the empty role emits a bare name.
func formatPerformers(m map[string][]string) []string {
	if len(m) == 0 {
		return nil
	}
	roles := make([]string, 0, len(m))
	for r := range m {
		roles = append(roles, r)
	}
	// Deterministic ordering across runs.
	slices.Sort(roles)
	var out []string
	for _, r := range roles {
		for _, name := range m[r] {
			if r == "" {
				out = append(out, name)
			} else {
				out = append(out, name+" ("+r+")")
			}
		}
	}
	return out
}
