package matroska

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/internal/mpeg4audio"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// parse reads a Matroska/WebM file into a neutral Media: the scoped tag tree from
// Segment.Tags, the segment title from Segment.Info, cover art from
// Segment.Attachments, and the audio geometry from Segment.Tracks.
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes
	// Element headers are read in windows of at most min(size, 8) bytes, the first at offset 0,
	// so a limit that cannot hold that window refuses the file here.
	if err := bits.CheckAlloc(min(size, 8), limit); err != nil {
		return nil, err
	}
	// The depth guard also carries the per-parse breadth budget: eachChild counts every
	// metadata element against MaxElements, so a flat run of empty EBML elements cannot
	// amplify into descriptors and OOM. Clusters, walked by walkSegment, are exempt; see
	// eachChild and TestMatroska*Clusters.
	depth := bits.NewDepth(opts.Limits.MaxDepth).WithElementCap(opts.Limits.MaxElements, "Matroska metadata elements")

	d := &doc{}
	var pics []core.Picture
	var segStart, segEnd int64 = -1, -1
	wb := &writeBase{size: size}

	// Top level: the EBML header (for DocType) then the Segment.
	err := eachChild(src, 0, size, depth, limit, func(el element) error {
		var err error
		switch el.id {
		case idEBML:
			d.docType, err = readDocType(src, el, depth, limit)
		case idSegment:
			if segStart < 0 {
				segStart, segEnd = el.dataStart, el.dataEnd
				idn := int64(len(idBytes(el.id)))
				wb.segStart = el.start
				wb.segSizeOff = el.start + idn
				wb.segSizeLen = el.dataStart - (el.start + idn)
				wb.segUnknown = el.unknown
				wb.segDataStart = el.dataStart
				wb.segDataEnd = el.dataEnd
			}
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if segStart < 0 {
		return nil, fmt.Errorf("%w: no Matroska Segment", waxerr.ErrInvalidData)
	}

	var audioStart int64 = -1
	var audioEnd, durationNs int64
	var chapters []core.Chapter
	var tagsSeen bool // capture the Tags CRC framing from the first master only (later masters merge)
	err = walkSegment(src, segStart, segEnd, depth, limit, func(el element) error {
		if el.id == idCluster {
			// Coalesce a contiguous run of Clusters into one descriptor.
			if n := len(wb.children); n > 0 && wb.children[n-1].id == idCluster {
				wb.children[n-1].dataEnd = el.dataEnd
			} else {
				wb.children = append(wb.children, l1elem{id: el.id, start: el.start, dataStart: el.dataStart, dataEnd: el.dataEnd})
			}
			if audioStart < 0 {
				audioStart = el.start
			}
			if el.dataEnd > audioEnd {
				audioEnd = el.dataEnd
			}
			return nil
		}
		// Charge every non-cluster level-1 child against the element budget.
		if err := depth.Count(); err != nil {
			return err
		}
		wb.children = append(wb.children, l1elem{id: el.id, start: el.start, dataStart: el.dataStart, dataEnd: el.dataEnd})
		var err error
		switch el.id {
		case idCRC32:
			// A CRC-32 directly under the Segment covers the whole segment body, so any edit
			// leaves it stale.
			var raw []byte
			if raw, err = captureRaw(src, el, limit); len(raw) > 0 {
				raw[0] = idVoid
				wb.segVoidFromCRC = raw
			}
		case idSeekHead:
			wb.seek, err = captureSeekHead(src, el, depth, limit)
		case idCues:
			wb.cues, err = captureCues(src, el, depth, limit)
		case idInfo:
			if wb.info, err = captureInfo(src, el, depth, limit); err == nil {
				durationNs, err = parseInfo(src, el, depth, limit, d)
			}
		case idTracks:
			err = parseTracks(src, el, depth, limit, d)
		case idTags:
			// The layout keeps the first Tags master and absorbs the rest, so capture the CRC
			// flag from the first only. Every master is still parsed so its groups merge.
			if !tagsSeen {
				if wb.tagsCRC, err = firstChildIsCRC(src, el, limit); err != nil {
					return err
				}
				tagsSeen = true
			}
			return parseTags(src, el, depth, limit, d)
		case idAttachments:
			// Keep the first Attachments master's framing (the one the layout preserves);
			// still parse every master so later files merge.
			if wb.attach == nil {
				hasCRC, err := firstChildIsCRC(src, el, limit)
				if err != nil {
					return err
				}
				wb.attach = &attachBlock{start: el.start, end: el.dataEnd, hasCRC: hasCRC}
			}
			ps, err := parseAttachments(src, el, depth, limit, d)
			if err != nil {
				return err
			}
			pics = append(pics, ps...)
		case idChapters:
			if d.chapters != nil {
				return nil // at most one Chapters element is valid; ignore a malformed duplicate
			}
			chs, err := parseChapters(src, el, depth, limit, d)
			if err != nil {
				return err
			}
			chapters = chs
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	wb.clusterStart = audioStart
	if wb.clusterStart < 0 {
		wb.clusterStart = wb.segDataEnd
	}
	d.wb = wb

	// Matroska has no per-track duration element; the segment Duration is the
	// container's playable length, so it is the best value for every audio track.
	if durationNs > 0 {
		for i := range d.tracks {
			d.tracks[i].Duration = time.Duration(durationNs)
		}
	}

	// Matroska carries no average-bitrate element. Skip multi-track or video-bearing
	// files, where the cluster bytes are shared across streams and would inflate an audio
	// figure.
	if !d.sawNonAudio && len(d.tracks) == 1 && audioStart >= 0 && audioEnd > audioStart {
		t := &d.tracks[0]
		t.Bitrate = core.AverageBitrate(audioEnd-audioStart, t.Duration.Seconds())
	}

	media := &core.Media{Format: core.FormatMatroska, Native: d}
	tags, families := project(d, media)
	media.Pictures = pics
	media.Chapters = chapters
	media.Families = families
	media.Warnings = mediaWarnings(tags, families)
	media.Warnings = append(media.Warnings, invalidKeyWarnings(d)...)
	media.Properties = core.Properties{Container: containerName(d.docType), Tracks: d.tracks}
	if audioStart >= 0 && audioEnd > audioStart {
		media.AudioStart = audioStart
		media.AudioEnd = audioEnd
		// Essence digests hash only Cluster byte runs. Non-cluster level-1 elements between
		// clusters are excluded so tag-only edits there do not change the audio digest.
		// AudioStart and AudioEnd remain for the save-back fingerprint.
		media.AudioRanges = clusterRuns(wb.children)
	}
	if err := core.SetIdentity(src, media); err != nil {
		return nil, err
	}
	return media, nil
}

// readDocType returns the EBML header's DocType ("matroska" or "webm"), or "" on a
// read error that is not fatal ([bits.Fatal]): it is only an informational container label.
func readDocType(src core.ReaderAtSized, ebml element, depth *bits.Depth, limit int64) (string, error) {
	var dt string
	err := eachChild(src, ebml.dataStart, ebml.dataEnd, depth, limit, func(c element) error {
		var err error
		if c.id == idDocType {
			dt, err = readLabel(src, c, limit)
		}
		return err
	})
	if err := bits.Fatal(err); err != nil {
		return "", err
	}
	return dt, nil
}

// parseInfo reads the timestamp scale, duration, and segment title. It returns
// the duration in nanoseconds (Duration is in TimestampScale units).
func parseInfo(src core.ReaderAtSized, info element, depth *bits.Depth, limit int64, d *doc) (int64, error) {
	var scale uint64 = 1000000 // spec default: 1 ms
	var duration float64
	err := eachChild(src, info.dataStart, info.dataEnd, depth, limit, func(el element) error {
		switch el.id {
		case idTimestampScl:
			v, err := readUint(src, el, limit)
			if err != nil {
				return err
			}
			if v > 0 {
				scale = v
			}
		case idDuration:
			f, ok, err := readFloat(src, el, limit)
			if err != nil {
				return err
			}
			if ok {
				duration = f
			}
		case idSegTitle:
			title, err := readString(src, el, limit)
			if err != nil {
				return err
			}
			d.segTitle = title
			d.hasSegTitle = true // record presence so a "" title round-trips distinct from none
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	// Check NaN explicitly: comparisons against NaN are all false, and converting an
	// out-of-range or NaN float to int64 is implementation-defined in Go.
	ns := duration * float64(scale)
	if math.IsNaN(ns) || ns <= 0 || ns >= float64(1<<62) {
		return 0, nil
	}
	return int64(ns), nil
}

// parseTracks reads each TrackEntry, keeping the audio tracks. Only a fatal error ([bits.Fatal])
// fails the parse.
func parseTracks(src core.ReaderAtSized, tracks element, depth *bits.Depth, limit int64, d *doc) error {
	err := eachChild(src, tracks.dataStart, tracks.dataEnd, depth, limit, func(el element) error {
		if el.id == idTrackEntry {
			return parseTrackEntry(src, el, depth, limit, d)
		}
		return nil
	})
	return bits.Fatal(err)
}

// parseTrackEntry reads one track; non-audio tracks (video, subtitles) are
// skipped for the properties view. The first audio track also supplies the
// essence-digest config. Only a fatal error ([bits.Fatal]) fails the parse.
func parseTrackEntry(src core.ReaderAtSized, entry element, depth *bits.Depth, limit int64, d *doc) error {
	var tt uint64
	var codecID string
	var outputRate int
	var private element
	var t core.AudioTrack
	err := eachChild(src, entry.dataStart, entry.dataEnd, depth, limit, func(el element) error {
		var err error
		switch el.id {
		case idTrackType:
			tt, err = readUint(src, el, limit)
		case idCodecID:
			codecID, err = readLabel(src, el, limit) // technical/display string
		case idTrackNumber:
			var v uint64
			v, err = readUint(src, el, limit)
			t.Index = intVal(v)
		case idAudio:
			// Only overwrite when this Audio child declared one, so a malformed second,
			// empty Audio child does not erase the first.
			var r int
			if r, err = parseAudio(src, el, depth, limit, &t); r > 0 {
				outputRate = r
			}
		case idCodecPrivate:
			private = el // read below, and only for the codec whose config this parser decodes
		}
		return err
	})
	if err := bits.Fatal(err); err != nil {
		return err
	}
	if tt != trackTypeAudio {
		d.sawNonAudio = true
		return nil
	}
	t.Codec = codecName(codecID)
	// The digest salt is SamplingFrequency, so copy it before the output rate replaces it.
	if len(d.tracks) == 0 {
		d.codecID = codecID
		d.sampleRate = t.SampleRate
		d.channels = t.Channels
		d.bitDepth = t.BitsPerSample
	}
	// OutputSamplingFrequency is what a player produces; SamplingFrequency is the core
	// coder's, half of it for an SBR stream.
	if outputRate > 0 {
		t.SampleRate = outputRate
	}
	if strings.HasPrefix(codecID, "A_AAC") {
		// Read CodecPrivate only for AAC and only the prefix the decoder consumes: Vorbis
		// and FLAC private data hold whole setup headers, and an AAC one that declares
		// megabytes must not be allocated to read 24 bytes.
		b, err := readBytesPrefix(src, private, maxAudioSpecificConfig, limit)
		if err := bits.Fatal(err); err != nil {
			return err
		}
		applyAACCodecPrivate(&t, codecID, b, outputRate > 0)
	}
	d.tracks = append(d.tracks, t)
	return nil
}

// applyAACCodecPrivate names an AAC track's profile and, when the container did not
// declare an output rate, takes the played rate from the AudioSpecificConfig the
// CodecPrivate holds.
func applyAACCodecPrivate(t *core.AudioTrack, codecID string, private []byte, haveOutputRate bool) {
	fromID := aacCodecIDProfile(codecID)
	asc, ok := mpeg4audio.ParseConfig(private)
	if !ok {
		t.Codec = fromID
		return
	}
	switch {
	case asc.SBRSignalled || asc.SBR:
		// The config addressed SBR, so it outranks a CodecID suffix the muxer may have left
		// stale.
		t.Codec = asc.ProfileName()
	case fromID != "AAC":
		// The config said nothing about SBR, so the suffix stands. Letting the config win
		// would turn A_AAC/MPEG4/LC/SBR into "AAC LC", while the same file with no
		// CodecPrivate reads HE-AAC.
		t.Codec = fromID
	default:
		t.Codec = asc.ProfileName()
	}
	if haveOutputRate || !asc.SBR {
		return
	}
	// The container under-declared, so the config supplies the played rate and channel
	// count; a parametric-stereo track would otherwise read as one channel.
	if r := asc.OutputSampleRate(); r > 0 {
		t.SampleRate = r
	}
	if ch := asc.OutputChannels(); ch > 0 && t.Channels == asc.Channels {
		t.Channels = ch
	}
}

// aacCodecIDProfile names an AAC profile from the CodecID suffix: A_AAC/MPEG4/LC/SBR and
// its MPEG2 sibling spell the object type where no CodecPrivate does.
func aacCodecIDProfile(codecID string) string {
	if strings.HasSuffix(codecID, "/SBR") {
		return "HE-AAC"
	}
	switch {
	case strings.HasSuffix(codecID, "/MAIN"):
		return "AAC Main"
	case strings.HasSuffix(codecID, "/LC"):
		return "AAC LC"
	case strings.HasSuffix(codecID, "/SSR"):
		return "AAC SSR"
	case strings.HasSuffix(codecID, "/LTP"):
		return "AAC LTP"
	}
	return "AAC"
}

// parseAudio reads the Audio sub-element's sampling frequency, channel count, and bit
// depth onto t, returning OutputSamplingFrequency separately (0 when absent or
// unusable). Only a fatal error ([bits.Fatal]) fails the parse.
func parseAudio(src core.ReaderAtSized, audio element, depth *bits.Depth, limit int64, t *core.AudioTrack) (int, error) {
	// Guard NaN/Inf and absurd magnitudes: int(NaN)/int(+Inf) is implementation-defined,
	// and a wild rate poisons the essence config.
	rate := func(el element) (int, bool, error) {
		f, ok, err := readFloat(src, el, limit)
		if err != nil || !ok || !(f > 0) || f >= math.MaxInt32 {
			return 0, false, err
		}
		return int(f), true, nil
	}
	outputRate := 0
	err := eachChild(src, audio.dataStart, audio.dataEnd, depth, limit, func(el element) error {
		switch el.id {
		case idSampFreq:
			r, ok, err := rate(el)
			if err != nil {
				return err
			}
			if ok {
				t.SampleRate = r
			}
		case idOutSampFreq:
			r, ok, err := rate(el)
			if err != nil {
				return err
			}
			if ok {
				outputRate = r
			}
		case idChannels:
			v, err := readUint(src, el, limit)
			t.Channels = intVal(v)
			return err
		case idBitDepth:
			v, err := readUint(src, el, limit)
			t.BitsPerSample = intVal(v)
			return err
		}
		return nil
	})
	if err := bits.Fatal(err); err != nil {
		return 0, err
	}
	return outputRate, nil
}

// parseTags reads every Tag (target group) into the native doc.
func parseTags(src core.ReaderAtSized, tags element, depth *bits.Depth, limit int64, d *doc) error {
	return eachChild(src, tags.dataStart, tags.dataEnd, depth, limit, func(el element) error {
		if el.id != idTag {
			return nil
		}
		g, err := parseTag(src, el, depth, limit)
		if err != nil {
			return err
		}
		d.groups = append(d.groups, g)
		return nil
	})
}

// parseTag reads one Tag: its Targets scope and its SimpleTags.
func parseTag(src core.ReaderAtSized, tagEl element, depth *bits.Depth, limit int64) (tagGroup, error) {
	var g tagGroup
	var err error
	if g.raw, err = captureRaw(src, tagEl, limit); err != nil {
		return g, err
	}
	err = eachChild(src, tagEl.dataStart, tagEl.dataEnd, depth, limit, func(el element) error {
		var err error
		switch el.id {
		case idCRC32:
			g.hasCRC = true
		case idTargets:
			if g.targetsRaw, err = captureRaw(src, el, limit); err == nil {
				err = parseTargets(src, el, depth, limit, &g)
			}
		case idSimpleTag:
			var st simpleTag
			if st, err = parseSimpleTag(src, el, depth, limit); err != nil {
				return err
			}
			// Capture raw only for the top-level SimpleTag: its bytes span the whole nested
			// subtree, and the write path reads no nested tag's raw.
			if st.raw, err = captureRaw(src, el, limit); err != nil {
				return err
			}
			g.tags = append(g.tags, st)
		}
		return err
	})
	g.scope = resolveScope(g)
	return g, err
}

// parseTargets reads a Targets element: the logical level (TargetTypeValue /
// TargetType) and whether the tag is narrowed to a track, edition, or chapter. Only a
// fatal error ([bits.Fatal]) fails the parse.
func parseTargets(src core.ReaderAtSized, targets element, depth *bits.Depth, limit int64, g *tagGroup) error {
	err := eachChild(src, targets.dataStart, targets.dataEnd, depth, limit, func(el element) error {
		var err error
		switch el.id {
		case idTgtTypeVal:
			g.targetTypeValue, err = readUint(src, el, limit)
		case idTgtType:
			g.targetType, err = readLabel(src, el, limit) // scope keyword; numeric value is primary
		case idTagTrackUID:
			g.trackUID = true
		case idTagEditUID:
			g.editionUID = true
		case idTagChapUID:
			g.chapterUID = true
		}
		return err
	})
	return bits.Fatal(err)
}

// parseSimpleTag reads a SimpleTag (name/value/language) and recurses into any
// nested sub-tags. eachChild's depth guard bounds the recursion.
func parseSimpleTag(src core.ReaderAtSized, st element, depth *bits.Depth, limit int64) (simpleTag, error) {
	// raw stays nil here; parseTag sets it on the top-level tag only.
	s := simpleTag{}
	err := eachChild(src, st.dataStart, st.dataEnd, depth, limit, func(el element) error {
		switch el.id {
		case idTagName:
			name, err := readString(src, el, limit)
			if err != nil {
				return err
			}
			s.name = name
		case idTagString:
			value, err := readString(src, el, limit)
			if err != nil {
				return err
			}
			s.value = value
			s.hasValue = true // an empty TagString is a present empty value, distinct from none
		case idTagLang:
			lang, err := readLabel(src, el, limit) // informational
			if err != nil {
				return err
			}
			s.lang = lang
		case idTagLangBCP47:
			s.bcp47 = true
		case idTagBinary:
			s.binary = int(min(el.dataLen(), maxElement))
		case idSimpleTag:
			sub, err := parseSimpleTag(src, el, depth, limit)
			if err != nil {
				return err
			}
			s.sub = append(s.sub, sub)
		}
		return nil
	})
	return s, err
}

// parseAttachments reads each AttachedFile into the native doc and returns the
// pictures decoded from image attachments (cover art).
func parseAttachments(src core.ReaderAtSized, att element, depth *bits.Depth, limit int64, d *doc) ([]core.Picture, error) {
	var pics []core.Picture
	err := eachChild(src, att.dataStart, att.dataEnd, depth, limit, func(el element) error {
		if el.id != idAttached {
			return nil
		}
		a, pic, err := parseAttached(src, el, depth, limit)
		if err != nil {
			return err
		}
		d.attachments = append(d.attachments, a)
		if pic != nil {
			pics = append(pics, *pic)
		}
		return nil
	})
	return pics, err
}

// parseAttached reads one AttachedFile, returning its summary and, when it is an image,
// a decoded Picture.
func parseAttached(src core.ReaderAtSized, af element, depth *bits.Depth, limit int64) (attachment, *core.Picture, error) {
	raw, err := captureRaw(src, af, limit)
	if err != nil {
		return attachment{}, nil, err
	}
	a := attachment{raw: raw}
	var dataEl element
	var haveData bool
	err = eachChild(src, af.dataStart, af.dataEnd, depth, limit, func(el element) error {
		var err error
		switch el.id {
		case idFileName:
			a.name, err = readString(src, el, limit)
		case idFileMime:
			a.mime, err = readString(src, el, limit)
		case idFileDesc:
			a.description, err = readString(src, el, limit)
		case idFileData:
			dataEl, haveData = el, true
			a.size = int(min(el.dataLen(), maxElement))
		}
		return err
	})
	if err != nil {
		return a, nil, err
	}
	// A cover is an image MIME, or a --force octet-stream stored under the cover-art
	// file name. The latter reprojects as an Unrecognized() picture (removable, rebuilt
	// on write) rather than accumulating a new cover_<n> attachment on every re-add.
	a.image = isCoverAttachment(a.mime, a.name)
	if !a.image || !haveData {
		return a, nil, nil
	}
	data, err := readBytes(src, dataEl, limit)
	if err != nil {
		return a, nil, err
	}
	pic := core.Picture{
		Type: pictureType(a.name),
		MIME: a.mime,
		// Sanitize the description like the tag-value read path, so a transfer that re-adds
		// this cover passes the write-time UTF-8 check. The native attachment keeps the
		// raw description.
		Description: core.SanitizeUTF8(a.description),
		Data:        data,
	}
	// Recognizable bytes win over the declared MIME, as in the ID3/MP4 read paths, so a
	// mislabeled cover reads as its true type.
	pic.SniffAuthoritative()
	return a, &pic, nil
}

// resolveScope maps a tag group's targets to a canonical scope.
func resolveScope(g tagGroup) core.Scope {
	switch {
	case g.chapterUID:
		return core.ScopeChapter
	case g.editionUID:
		return core.ScopeEdition
	}
	switch ttv := g.level(); {
	case ttv >= 60:
		return core.ScopeEdition
	case ttv >= 40:
		if g.trackUID {
			return core.ScopeTrack
		}
		return core.ScopeAlbum
	default:
		return core.ScopeTrack
	}
}

// level resolves a tag group's TargetTypeValue: the explicit numeric value, else the
// informational TargetType keyword (Picard and some hand-authored files set only the
// string), else the spec default of 50 (album/movie).
func (g tagGroup) level() uint64 {
	ttv := g.targetTypeValue
	if ttv == 0 {
		ttv = targetTypeLevel(g.targetType)
	}
	if ttv == 0 {
		ttv = 50
	}
	return ttv
}

// targetTypeLevel maps a Matroska TargetType keyword to its numeric level, or 0
// when unknown. The keywords are the spec's per-level synonyms.
func targetTypeLevel(s string) uint64 {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "COLLECTION":
		return 70
	case "EDITION", "ISSUE", "VOLUME", "OPUS", "SEASON", "SEQUEL":
		return 60
	case "ALBUM", "OPERA", "CONCERT", "MOVIE", "EPISODE":
		return 50
	case "PART", "SESSION":
		return 40
	case "TRACK", "SONG", "CHAPTER":
		return 30
	case "SUBTRACK", "PART_BIS", "MOVEMENT", "SCENE":
		return 20
	case "SHOT":
		return 10
	default:
		return 0
	}
}

// scopedContribution is one canonical value with the scope it was tagged at and the modeled
// language of its SimpleTag ([tagLanguage]).
type scopedContribution struct {
	key   tag.Key
	value string
	scope core.Scope
	lang  string
}

// language is the modeled language of a SimpleTag: its TagLanguage, which a TagLanguageBCP47
// beside it supersedes, leaving none the model holds.
func (st simpleTag) language() string {
	if st.bcp47 {
		return ""
	}
	return tagLanguage(st.lang)
}

// tagLanguage is the modeled form of a TagLanguage, read up to its first NUL and in lowercase:
// empty for none and for Matroska's "und".
func tagLanguage(lang string) string {
	lang, _, _ = strings.Cut(lang, "\x00")
	if !meaningfulLang(lang) {
		return ""
	}
	return core.LowerASCII(core.SanitizeUTF8(lang))
}

// project fills m's tags and its comment and lyrics items, and returns the tags with the
// scope-aware family view, from the parsed groups and the segment title. Only top-level
// SimpleTags project to the canonical set; nested sub-tags and unmapped names stay in the
// native tree.
func project(d *doc, m *core.Media) (tag.TagSet, []core.FamilyValue) {
	// Matroska text is stored as raw bytes; a non-conformant file can hold invalid UTF-8
	// in a TagString or the Info.Title.
	var contribs []scopedContribution
	if d.hasSegTitle {
		contribs = append(contribs, scopedContribution{key: tag.Title, value: core.SanitizeUTF8(d.segTitle), scope: core.ScopeAlbum})
	}
	for _, g := range d.groups {
		for _, st := range g.tags {
			if !st.hasValue {
				continue // a binary- or nested-only SimpleTag has no string value to project
			}
			contribs = append(contribs, projectTag(st, g.scope)...)
		}
	}
	m.Tags, m.Comments, m.Lyrics = projectFlat(contribs)
	core.SyncTextItems(m)
	return m.Tags, buildFamilies(contribs)
}

// projectFlat builds the flat TagSet from scoped Matroska contributions, and the comment and
// lyrics items of the COMMENT and LYRICS values it emits. For each key it picks a primary
// scope, preferring album scope when present, and emits that scope's values verbatim and in
// order.
func projectFlat(contribs []scopedContribution) (ts tag.TagSet, comments, lyrics []core.TextItem) {
	byKey := map[tag.Key][]scopedContribution{}
	var keyOrder []tag.Key
	for _, c := range contribs {
		if len(byKey[c.key]) == 0 {
			keyOrder = append(keyOrder, c.key)
		}
		byKey[c.key] = append(byKey[c.key], c)
	}
	ts = tag.NewTagSet()
	for _, key := range keyOrder {
		ck := byKey[key]
		for _, e := range projectionOrder(key, ck) {
			if !e.emitted {
				continue
			}
			c := ck[e.index]
			ts.Add(key, c.value)
			switch key {
			case tag.Comment:
				comments = append(comments, core.TextItem{Text: c.value, Language: c.lang})
			case tag.Lyrics:
				lyrics = append(lyrics, core.TextItem{Text: c.value, Language: c.lang})
			}
		}
	}
	return ts, comments, lyrics
}

// contributionEmit is one contribution's fate in the flat projection: its index in
// the key's contribution slice, and whether the projection emits it or suppresses
// it as a cross-scope echo.
type contributionEmit struct {
	index   int
	emitted bool
}

// projectionOrder resolves one canonical key's contributions (in the order the parse
// walk produced them) into the order the flat projection visits them, each flagged
// emitted or suppressed.
func projectionOrder(key tag.Key, contribs []scopedContribution) []contributionEmit {
	if len(contribs) == 0 {
		return nil
	}
	var scopes []core.Scope
	seen := map[core.Scope]bool{}
	for _, c := range contribs {
		if !seen[c.scope] {
			seen[c.scope] = true
			scopes = append(scopes, c.scope)
		}
	}
	primary := scopes[0]
	if slices.Contains(scopes, core.ScopeAlbum) {
		primary = core.ScopeAlbum // album is the canonical scope when present
	}
	present := map[string]bool{}
	out := make([]contributionEmit, 0, len(contribs))
	visit := func(scope core.Scope, dedup bool) {
		for i, c := range contribs {
			if c.scope != scope {
				continue
			}
			f := core.Fold(c.value)
			if dedup && present[f] {
				out = append(out, contributionEmit{index: i})
				continue
			}
			present[f] = true
			out = append(out, contributionEmit{index: i, emitted: true})
		}
	}
	visit(primary, key == tag.Title) // primary scope verbatim (TITLE suppresses its own echo)
	for _, s := range scopes {
		if s != primary {
			visit(s, true) // other scopes: cross-scope echoes suppressed, distinct values kept
		}
	}
	return out
}

// invalidKeyWarnings reports the SimpleTag names the canonical vocabulary cannot
// represent: the native tree preserves their values but the tag set never sees them.
func invalidKeyWarnings(d *doc) []core.Warning {
	var ws []core.Warning
	seen := map[string]bool{}
	for _, g := range d.groups {
		for _, st := range g.tags {
			if !st.hasValue || seen[st.name] || !unprojectableTagName(st.name) {
				continue
			}
			seen[st.name] = true
			ws = core.WarnInvalidKey(ws, st.name)
		}
	}
	return ws
}

// unprojectableTagName reports whether a SimpleTag name is dropped because the
// canonical vocabulary cannot represent it, rather than because it is a reserved
// technical name.
func unprojectableTagName(name string) bool {
	if _, ok := mapping.MatroskaTagKey(name); ok {
		return false
	}
	return strings.TrimSpace(name) != "" && !mapping.MatroskaTechnicalName(name)
}

// projectTag maps one SimpleTag to canonical contributions, splitting a "n/total" track or disc
// number into its two canonical keys.
func projectTag(st simpleTag, scope core.Scope) []scopedContribution {
	key, ok := mapping.MatroskaTagKey(st.name)
	if !ok {
		return nil
	}
	// Sanitized as the canonical TagSet holds it.
	value, lang := core.SanitizeUTF8(st.value), st.language()
	// The shared helper keeps the exact substrings ("04/09" -> "04"/"09", "0/12" ->
	// "0"/"12") and leaves a malformed pair verbatim, so Matroska agrees with every
	// other read path.
	num, total, split := tag.NumberTotalSplit(key, value)
	if !split {
		return []scopedContribution{{key, value, scope, lang}}
	}
	var out []scopedContribution
	if num != "" {
		out = append(out, scopedContribution{key, num, scope, lang})
	}
	if total != "" {
		out = append(out, scopedContribution{tag.TotalKey(key), total, scope, lang})
	}
	return out
}

// buildFamilies groups contributions into one entry per (key, scope), preserving
// first-seen order. An entry is unselected when the same key carries conflicting
// values across more than one scope.
func buildFamilies(contribs []scopedContribution) []core.FamilyValue {
	type fkey struct {
		key   tag.Key
		scope core.Scope
	}
	var order []fkey
	vals := map[fkey][]string{}
	scopesByKey := map[tag.Key]map[core.Scope]bool{}
	valsByKey := map[tag.Key]map[string]bool{}
	for _, c := range contribs {
		fk := fkey{c.key, c.scope}
		if _, ok := vals[fk]; !ok {
			order = append(order, fk)
		}
		vals[fk] = append(vals[fk], c.value)
		if scopesByKey[c.key] == nil {
			scopesByKey[c.key] = map[core.Scope]bool{}
			valsByKey[c.key] = map[string]bool{}
		}
		scopesByKey[c.key][c.scope] = true
		valsByKey[c.key][core.Fold(c.value)] = true
	}
	var fams []core.FamilyValue
	for _, fk := range order {
		conflict := len(scopesByKey[fk.key]) > 1 && len(valsByKey[fk.key]) > 1
		fams = append(fams, core.FamilyValue{
			Key: fk.key, Family: core.FamilyMatroska, Scope: fk.scope,
			Values: vals[fk], Selected: !conflict,
		})
	}
	return fams
}

// mediaWarnings reports the inherited transcoder stamp (ffmpeg writes "Lavf..."
// into the ENCODER tag of an acquired file) and any cross-scope conflict.
func mediaWarnings(ts tag.TagSet, fams []core.FamilyValue) []core.Warning {
	var ws []core.Warning
	if vs, ok := ts.Get(tag.Encoder); ok {
		for _, v := range vs {
			if core.IsTranscoderStamp(v) {
				ws = core.Warn(ws, core.WarnInheritedEncoder, "inherited encoder stamp: "+core.WarnSnippet(v))
			}
		}
	}
	seen := map[tag.Key]bool{}
	for _, f := range fams {
		if !f.Selected && !seen[f.Key] {
			seen[f.Key] = true
			// A warning has no key field, so the key is appended as " (KEY)", the suffix
			// Finding.String renders from lintFamilies' Key field; dump and lint then read
			// identically.
			ws = core.Warn(ws, core.WarnConflictingFamilies, core.ConflictingFamiliesMessage()+" ("+string(f.Key)+")")
		}
	}
	return ws
}

// pictureType maps an attachment file name to a picture role using the Matroska
// cover-art naming convention (matroska.org): the front cover is "cover.<ext>" and a
// small thumbnail is "small_cover.<ext>".
func pictureType(name string) core.PictureType {
	n := strings.ToLower(name)
	switch {
	case strings.HasPrefix(n, "small_cover"):
		return core.PicOther
	case strings.HasPrefix(n, "cover"):
		return core.PicFrontCover
	default:
		return core.PicOther
	}
}

// isImageMIME reports whether a MIME labels an image. isCoverAttachment, the shared
// cover-art gate, accepts it or a cover-named application/octet-stream (a --force cover).
func isImageMIME(mime string) bool {
	return strings.HasPrefix(strings.ToLower(mime), "image/")
}

// isCoverName reports whether an attachment file name follows the cover-art naming
// convention: a base name of "cover" or "small_cover", optionally with WaxLabel's
// numeric suffix ("cover_1", "small_cover_2"), and any or no extension.
func isCoverName(name string) bool {
	base := name
	if i := strings.LastIndexByte(base, '.'); i >= 0 {
		base = base[:i] // strip a single trailing extension
	}
	base = strings.ToLower(base)
	for _, stem := range []string{"small_cover", "cover"} {
		if base == stem {
			return true
		}
		// A numeric suffix is WaxLabel's uniquifier ("cover_1"); an empty or non-numeric one
		// (the "cover_letter" case) is not, so it stays a plain attachment.
		if suffix, ok := strings.CutPrefix(base, stem+"_"); ok && core.AllASCIIDigits(suffix) {
			return true
		}
	}
	return false
}

// isCoverAttachment reports whether an AttachedFile projects as a cover picture. The
// read gate, the write refusal and the result reprojection all use it.
func isCoverAttachment(mime, name string) bool {
	return isImageMIME(mime) || (mime == core.UnrecognizedMIME && isCoverName(name))
}

// codecName maps a Matroska CodecID to a short display codec name.
func codecName(id string) string {
	switch {
	case id == "A_FLAC":
		return "FLAC"
	case id == "A_OPUS":
		return "Opus"
	case id == "A_VORBIS":
		return "Vorbis"
	case strings.HasPrefix(id, "A_AAC"):
		// The object type, when the track carries one, comes from the CodecPrivate or the
		// CodecID suffix; this is the fallback for neither.
		return "AAC"
	case strings.HasPrefix(id, "A_MPEG/L3"):
		return "MP3"
	case strings.HasPrefix(id, "A_MPEG/L2"):
		return "MP2"
	case strings.HasPrefix(id, "A_AC3"):
		return "AC-3"
	case strings.HasPrefix(id, "A_PCM"):
		return "PCM"
	case id == "A_TRUEHD":
		return "TrueHD"
	case id == "A_DTS/EXPRESS":
		return "DTS Express"
	case id == "A_DTS/LOSSLESS":
		return "DTS-HD Master Audio"
	case strings.HasPrefix(id, "A_"):
		return id[2:]
	default:
		return id
	}
}

// containerName returns the Properties container label for an EBML DocType.
func containerName(docType string) string {
	if strings.EqualFold(docType, "webm") {
		return "WebM"
	}
	return "Matroska"
}
