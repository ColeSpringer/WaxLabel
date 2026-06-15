package tag

import (
	"slices"
	"strconv"
	"strings"
)

// Tags is the typed convenience projection of a [TagSet]. It is lossy by
// design: a struct's zero values cannot distinguish absent from empty from
// cleared, so Tags is authoritative for nothing. Use it to read common fields
// ergonomically ([Project]) and to write them as sugar ([Tags.Patch], which
// only sets non-empty fields — clearing requires an explicit [TagPatch]).
type Tags struct {
	Title       string
	Artists     []string
	Album       string
	AlbumArtist string
	Composer    []string
	Genre       []string

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
	EncodedBy  string

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
		Composer:    all(Composer),
		Genre:       all(Genre),

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

		AcoustID:            first(AcoustID),
		AcoustIDFingerprint: first(AcoustIDFingerprint),

		Compilation: parseBool(first(Compilation)),

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

	t.TrackNumber, t.TrackTotal = parseNumPair(first(TrackNumber), first(TrackTotal))
	t.DiscNumber, t.DiscTotal = parseNumPair(first(DiscNumber), first(DiscTotal))
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
	setMulti(Composer, t.Composer)
	setMulti(Genre, t.Genre)

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

// parseNumPair resolves a "number" and "total" pair. The number field may use
// the "n/total" convention; an explicit total field wins if present.
func parseNumPair(num, total string) (n, tot int) {
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

func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
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
