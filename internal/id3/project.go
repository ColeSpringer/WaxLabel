package id3

import (
	"fmt"
	"strings"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
)

// musicBrainzOwner is the UFID owner identifier under which the MusicBrainz
// recording ID is stored.
const musicBrainzOwner = "http://musicbrainz.org"

// Projection is the canonical view decoded from an ID3v2 tag: tags, embedded pictures,
// navigation chapters, family/source entries, and parse facts the caller turns into
// warnings.
type Projection struct {
	Tags         tag.TagSet
	Pictures     []core.Picture
	Chapters     []core.Chapter
	SyncedLyrics []core.SyncedLyrics
	Families     []core.FamilyValue
	NumericGenre bool
	Warnings     []core.Warning
}

// EncoderNoise reports inherited-encoder warnings for the tag's TSSE/TENC frames (ffmpeg
// writes "Lavf..." there), the signature of a transcoded or acquired file. The codecs
// that embed ID3v2 (MP3 and WAV) share it. A nil tag yields no warnings.
func EncoderNoise(t *Tag) []core.Warning {
	if t == nil {
		return nil
	}
	var ws []core.Warning
	for _, f := range t.Frames() {
		if f.ID != "TSSE" && f.ID != "TENC" {
			continue
		}
		for _, v := range DecodeText(f) {
			if core.IsTranscoderStamp(v) {
				ws = core.Warn(ws, core.WarnInheritedEncoder, "inherited encoder stamp: "+core.WarnSnippet(v))
			}
		}
	}
	return ws
}

// RewriteBase selects the diff base for rebuilding an ID3 chunk inside a container that
// also has native metadata (WAV LIST/INFO, AIFF text chunks). WAV and AIFF share it so
// their rewrite rules stay aligned. With no existing ID3 chunk the base is empty, so the
// full promoted tag set is written; when the native metadata is being stripped the base
// is only the parsed ID3 frames, so native values count as additions; otherwise it is the
// merged projection, so unchanged frames are reused. srcTag is read only in the
// stripNative case, which implies id3Present.
func RewriteBase(base tag.TagSet, srcTag *Tag, id3Present, stripNative bool) tag.TagSet {
	switch {
	case !id3Present:
		return tag.NewTagSet()
	case stripNative:
		return Project(srcTag).Tags
	default:
		return base
	}
}

// Project decodes an ID3v2 tag into the canonical model. A nil tag projects to the
// empty Projection: a write that drops the front ID3v2 entirely (an edit clearing every
// frame, or a --legacy strip on a tagless file) passes nil here for the result document,
// which must equal a fresh parse of the now-tagless output.
func Project(t *Tag) Projection {
	if t == nil {
		return Projection{}
	}
	var contribs []core.Contribution
	var pics []core.Picture
	var dp dateParts
	var warnings []core.Warning
	numeric := false

	// A frame whose declared size overran the tag stopped the walk, so everything from its
	// header on is unread. Every ID3-backed codec projects through here and inherits the
	// diagnostic.
	if id, n := t.MalformedTail(); id != "" {
		warnings = core.Warn(warnings, core.WarnMalformedTagEntry,
			fmt.Sprintf("the %s frame declares more bytes than the tag holds; the %d byte(s) from it to the end of the tag could not be read",
				core.WarnSnippet(id), n))
	}
	if r := t.Ignored(); r != "" {
		warnings = core.Warn(warnings, core.WarnMalformedTagEntry, r)
	}

	emit := func(key tag.Key, val, src string) {
		contribs = append(contribs, core.Contribution{Key: key, Value: val, Source: src})
	}

	for _, f := range t.frames {
		if f.Opaque {
			continue
		}
		switch {
		case f.ID == "APIC":
			if p, ok := decodeAPIC(f.Body); ok {
				pics = append(pics, p)
			} else {
				// A malformed APIC is dropped from the picture set; warn so dump and lint
				// report the bad cover, matching the FLAC PICTURE-block warning. decodeAPIC
				// returns only ok, so the message is static.
				warnings = core.Warn(warnings, core.WarnInvalidPicture, "APIC: invalid picture data")
			}
		case f.ID == "TXXX":
			if desc, vals, ok := decodeUserText(f.Body); ok {
				key, kok := mapping.ID3TXXXKey(desc)
				if !kok {
					// The description is not representable as a canonical key (lowercase-only
					// or punctuation-bearing), so the frame is preserved but its value never
					// reaches the tag set. Warn so it does not vanish from every view.
					warnings = core.WarnInvalidKey(warnings, desc)
					continue
				}
				src := "TXXX\x00" + strings.ToUpper(strings.TrimSpace(desc))
				for _, v := range vals {
					emit(key, v, src)
				}
			}
		case f.ID == "UFID":
			if owner, id, ok := decodeUFID(f.Body); ok && owner == musicBrainzOwner {
				emit(tag.MBRecordingID, id, "UFID")
			}
		case f.ID == "COMM":
			// A described comment is still a comment: Windows Explorer and CDDB-era taggers
			// write one, and leaving it unprojected hid it from dump, lint and diff and let
			// copy report a lossless carry that left it behind. Only a machine description
			// (iTunes normalization, ReplayGain) stays out, through the predicate the
			// writer's management gates also use. The source label is the literal "COMM",
			// not the description: core.BuildFamilies marks a key unselected when distinct
			// sources supply distinct values, so a per-description source would turn one
			// plain plus one described comment into a spurious conflicting-families
			// finding. Different TXXX descriptions land on different keys and never
			// collide, so that precedent does not apply.
			if desc, vals, ok := decodeCommentFrame(f.Body); ok && !mapping.ID3TechnicalCommentDesc(desc) {
				for _, v := range vals {
					emit(tag.Comment, v, "COMM")
				}
			}
		case f.ID == "USLT":
			// Only an empty-description USLT projects, unlike COMM above. tag.Lyrics is not
			// multivalued (tag/keys.go) and renderUnit's USLT branch takes
			// edited.First(tag.Lyrics), so projecting a described USLT beside a plain one
			// would raise a single-valued-multi finding on read and collapse to the first on
			// write. A described USLT usually marks an alternate or translated set, which is
			// not "the lyrics".
			if desc, text, ok := decodeLangText(f.Body); ok && desc == "" {
				emit(tag.Lyrics, text, "USLT")
			}
		case f.ID == "TCON":
			for _, v := range decodeTextFrame(f.Body) {
				names, wasNum := resolveGenres(v)
				numeric = numeric || wasNum
				for _, name := range names {
					if name != "" {
						emit(tag.Genre, name, "TCON")
					}
				}
			}
		case f.ID == "TRCK":
			emitNumTotal(emit, decodeTextFrame(f.Body), tag.TrackNumber, tag.TrackTotal, "TRCK")
		case f.ID == "TPOS":
			emitNumTotal(emit, decodeTextFrame(f.Body), tag.DiscNumber, tag.DiscTotal, "TPOS")
		case isDateFrame(f.ID):
			dp.add(f.ID, decodeTextFrame(f.Body))
		case f.ID == "TIPL" || f.ID == "IPLS":
			// The involved-people list holds one function/name pair per credit. Project only
			// the modelled functions (folding case and read-only aliases); unknown
			// involvements stay unprojected and are preserved on write. decodeInvolvedPeople
			// drops nameless pairs. TIPL must precede the T-prefix tail.
			for _, p := range decodeInvolvedPeople(f.Body) {
				if k, ok := mapping.ID3InvolvedRoleKey(p.Function); ok {
					emit(k, p.Name, f.ID)
				}
			}
		case f.ID == "MVIN":
			// Apple's movement number/total frame, an "n/total" pair like TRCK. Not
			// T-prefixed, so the generic text tail below never reaches it.
			for _, v := range decodeTextFrame(f.Body) {
				num, total, _ := movementSplit(v)
				if num != "" {
					emit(tag.Movement, num, "MVIN")
				}
				if total != "" {
					emit(tag.MovementTotal, total, "MVIN")
				}
			}
		case f.ID == "MVNM":
			// Apple's movement-name frame; also not T-prefixed, so without this case
			// the frame would stay preserved but invisible.
			for _, v := range decodeTextFrame(f.Body) {
				emit(tag.MovementName, v, "MVNM")
			}
		case strings.HasPrefix(f.ID, "T"):
			key, ok := mapping.ID3FrameKey(f.ID)
			if !ok {
				k, err := tag.ParseKey(strings.TrimSpace(f.ID))
				if err != nil {
					continue
				}
				key = k
			}
			for _, v := range decodeTextFrame(f.Body) {
				emit(key, v, f.ID)
			}
		}
		// Other frames (W***, POPM, PRIV, RVA2, GEOB, ...) are preserved in the
		// native document but not canonically projected.
	}

	// Compose the date frames gathered above into canonical date keys.
	dp.emit(emit)

	// Decode CHAP/CTOC navigation chapters. See chapters.go for ordering rules.
	chapters, chapterWarnings := ProjectChapters(t)
	// Decode SYLT synchronized lyrics. See synced_lyrics.go.
	syncedLyrics, syncedWarnings := ProjectSyncedLyrics(t)

	return Projection{
		Tags:         core.BuildTagSet(contribs),
		Pictures:     pics,
		Chapters:     chapters,
		SyncedLyrics: syncedLyrics,
		Families:     core.BuildFamilies(contribs, core.FamilyID3v2),
		NumericGenre: numeric,
		Warnings:     append(append(chapterWarnings, syncedWarnings...), warnings...),
	}
}

// emitNumTotal splits "n/total" text values into a number key and a total key via the
// shared [tag.NumberTotalSplit], so the split and its validity gate match the edit-time
// pair normalization and the other read paths. A malformed pair ("abc/1", "1/2/3") stays
// verbatim on the number key; a well-formed "4/9" splits and keeps leading zeros.
func emitNumTotal(emit func(tag.Key, string, string), vals []string, numKey, totKey tag.Key, src string) {
	for _, v := range vals {
		num, total, _ := tag.NumberTotalSplit(numKey, v)
		if num != "" {
			emit(numKey, num, src)
		}
		if total != "" {
			emit(totKey, total, src)
		}
	}
}

// movementSplit splits an MVIN "number/total" value into its number and total, the
// movement analog of [tag.NumberTotalSplit], which is gated to the track/disc keys and
// whose [tag.ValidNumericValue] gate passes anything for other keys. A value splits only
// when it contains a '/', each side is empty or a valid movement integer
// ([tag.ValidMP4IntValue]: unsigned, within uint16), and the sides are not both empty;
// anything else (no slash, "abc/1", a bare "/") stays whole on the number side with
// split=false, matching NumberTotalSplit's malformed-stays-verbatim contract. The MVIN
// read case and composeMovementPair share it.
func movementSplit(v string) (num, total string, split bool) {
	if !strings.ContainsRune(v, '/') {
		return v, "", false
	}
	n, t := tag.SplitNumberTotal(v)
	if n == "" && t == "" {
		return v, "", false
	}
	valid := func(s string) bool { return s == "" || tag.ValidMP4IntValue(tag.Movement, s) }
	if !valid(n) || !valid(t) {
		return v, "", false
	}
	return n, t, true
}

// LegacyV1Families projects a preserved ID3v1 tag into legacy family entries. MP3,
// FLAC, and the APEv2-native containers all carry one as a non-authoritative trailer,
// so the projection lives here rather than being written out per codec.
func LegacyV1Families(auth tag.TagSet, raw []byte) []core.FamilyValue {
	v1, ok := ParseV1(raw)
	if !ok {
		return nil
	}
	contribs := make([]core.Contribution, 0, 8)
	for _, p := range v1.Pairs() {
		contribs = append(contribs, core.Contribution{Key: p.Key, Value: p.Value})
	}
	return core.LegacyFamilies(auth, core.FamilyID3v1, contribs)
}

// LegacyV2Families projects a preserved, non-authoritative ID3v2 tag into legacy family
// entries, and reports whether it also carries content the canonical set does not fold
// in (pictures, chapters, or synced lyrics), which a legacy strip cannot prove redundant.
// FLAC's stray leading tag and Musepack's both take this path. An unreadable tag, or one
// parsed but ignored whole ([Tag.Ignored]), reports opaque with no entries.
func LegacyV2Families(auth tag.TagSet, raw []byte, maxElements int) ([]core.FamilyValue, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	t, err := ParseTag(raw, maxElements)
	if err != nil {
		return nil, true
	}
	proj := Project(t)
	var contribs []core.Contribution
	for _, k := range proj.Tags.Keys() {
		vals, _ := proj.Tags.Get(k)
		for _, v := range vals {
			contribs = append(contribs, core.Contribution{Key: k, Value: v})
		}
	}
	// A region the frame walk could not read, or a tag ignored whole (a v2.2 compression
	// flag), is content nothing can prove redundant; a strip would destroy it, so the
	// container is opaque. Without this an ignored tag would project nothing and read as
	// provably empty.
	malformed, _ := t.MalformedTail()
	opaque := malformed != "" || t.Ignored() != "" ||
		len(proj.Pictures) > 0 || len(proj.Chapters) > 0 || len(proj.SyncedLyrics) > 0
	return core.LegacyFamilies(auth, core.FamilyID3v2, contribs), opaque
}
