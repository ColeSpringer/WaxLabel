package matroska

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// parse reads a Matroska/WebM file into a neutral Media: the scoped tag tree from
// Segment.Tags, the segment title from Segment.Info, cover art from
// Segment.Attachments, and the audio geometry from Segment.Tracks. The cluster
// media is never read - only the audio byte range is recorded. It also captures a
// writeBase (the Segment header, the ordered top-level children, and the raw
// bytes of the SeekHead/Cues/Info/Attachments/Tags) so the write path can
// re-render and patch without the source (see rewrite_read.go).
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes
	depth := bits.NewDepth(opts.Limits.MaxDepth)

	d := &doc{}
	var pics []core.Picture
	var segStart, segEnd int64 = -1, -1
	wb := &writeBase{size: size}

	// Top level: the EBML header (for DocType) then the Segment.
	err := eachChild(src, 0, size, depth, limit, func(el element) error {
		switch el.id {
		case idEBML:
			d.docType = readDocType(src, el, depth, limit)
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
		return nil
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
	err = walkSegment(src, segStart, segEnd, depth, limit, func(el element) error {
		wb.children = append(wb.children, l1elem{id: el.id, start: el.start, dataStart: el.dataStart, dataEnd: el.dataEnd})
		switch el.id {
		case idSeekHead:
			wb.seek = captureSeekHead(src, el, depth, limit)
		case idCues:
			wb.cues = captureCues(src, el, depth, limit)
		case idInfo:
			wb.info = captureInfo(src, el, depth, limit)
			ns, err := parseInfo(src, el, depth, limit, d)
			if err != nil {
				return err
			}
			durationNs = ns
		case idTracks:
			parseTracks(src, el, depth, limit, d)
		case idTags:
			wb.tagsCRC = firstChildIsCRC(src, el, limit)
			return parseTags(src, el, depth, limit, d)
		case idAttachments:
			wb.attach = &attachBlock{start: el.start, end: el.dataEnd, hasCRC: firstChildIsCRC(src, el, limit)}
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
		case idCluster:
			if audioStart < 0 {
				audioStart = el.start
			}
			if el.dataEnd > audioEnd {
				audioEnd = el.dataEnd
			}
		}
		return nil
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

	// Matroska carries no average-bitrate element. For a single audio-only file
	// (one audio track, no video/subtitle track) derive it from the cluster byte
	// span over the segment duration - the same audioBytes/secs every other codec
	// uses. Skip multi-track or video-bearing files, where the cluster bytes are
	// shared across streams and would inflate an audio figure. Attachments (cover
	// art) sit outside the cluster bounds, so a cover-art + single-audio file still
	// computes cleanly. The audioStart/audioEnd guard matches the AudioStart/AudioEnd
	// guard below, so a clusterless file yields no bogus bitrate.
	if !d.sawNonAudio && len(d.tracks) == 1 && audioStart >= 0 && audioEnd > audioStart {
		t := &d.tracks[0]
		t.Bitrate = core.AverageBitrate(audioEnd-audioStart, t.Duration.Seconds())
	}

	media := &core.Media{Format: core.FormatMatroska, Native: d}
	tags, families := project(d)
	media.Tags = tags
	media.Families = families
	media.Pictures = pics
	media.Chapters = chapters
	media.Warnings = mediaWarnings(tags, families)
	media.Properties = core.Properties{Container: containerName(d.docType), Tracks: d.tracks}
	if audioStart >= 0 && audioEnd > audioStart {
		media.AudioStart = audioStart
		media.AudioEnd = audioEnd
	}
	media.Identity = core.Identity{Size: size}
	media.Identity.Fingerprint, media.Identity.HasFinger = core.Fingerprint(src, media, limit)
	return media, nil
}

// readDocType returns the EBML header's DocType ("matroska" or "webm"). It is an
// informational container label, so a read error degrades to "" rather than
// failing the parse.
func readDocType(src core.ReaderAtSized, ebml element, depth *bits.Depth, limit int64) string {
	var dt string
	_ = eachChild(src, ebml.dataStart, ebml.dataEnd, depth, limit, func(c element) error {
		if c.id == idDocType {
			dt, _ = readString(src, c, limit)
		}
		return nil
	})
	return dt
}

// parseInfo reads the timestamp scale, duration, and segment title. It returns
// the duration in nanoseconds (Duration is in TimestampScale units).
func parseInfo(src core.ReaderAtSized, info element, depth *bits.Depth, limit int64, d *doc) (int64, error) {
	var scale uint64 = 1000000 // spec default: 1 ms
	var duration float64
	err := eachChild(src, info.dataStart, info.dataEnd, depth, limit, func(el element) error {
		switch el.id {
		case idTimestampScl:
			if v := readUint(src, el, limit); v > 0 {
				scale = v
			}
		case idDuration:
			if f, ok := readFloat(src, el, limit); ok {
				duration = f
			}
		case idSegTitle:
			title, err := readString(src, el, limit)
			if err != nil {
				return err
			}
			d.segTitle = title
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	// Guard NaN/Inf explicitly: comparisons against NaN are all false, and a
	// float->int64 conversion of an out-of-range or NaN value is implementation-
	// defined in Go.
	ns := duration * float64(scale)
	if math.IsNaN(ns) || ns <= 0 || ns >= float64(1<<62) {
		return 0, nil
	}
	return int64(ns), nil
}

// parseTracks reads each TrackEntry, keeping the audio tracks.
func parseTracks(src core.ReaderAtSized, tracks element, depth *bits.Depth, limit int64, d *doc) {
	_ = eachChild(src, tracks.dataStart, tracks.dataEnd, depth, limit, func(el element) error {
		if el.id == idTrackEntry {
			parseTrackEntry(src, el, depth, limit, d)
		}
		return nil
	})
}

// parseTrackEntry reads one track; non-audio tracks (video, subtitles) are
// skipped for the properties view. The first audio track also supplies the
// essence-digest config.
func parseTrackEntry(src core.ReaderAtSized, entry element, depth *bits.Depth, limit int64, d *doc) {
	var tt uint64
	var codecID string
	var t core.AudioTrack
	_ = eachChild(src, entry.dataStart, entry.dataEnd, depth, limit, func(el element) error {
		switch el.id {
		case idTrackType:
			tt = readUint(src, el, limit)
		case idCodecID:
			codecID, _ = readString(src, el, limit) // technical/display string; degrade gracefully
		case idTrackNumber:
			t.Index = intVal(readUint(src, el, limit))
		case idAudio:
			parseAudio(src, el, depth, limit, &t)
		}
		return nil
	})
	if tt != trackTypeAudio {
		d.sawNonAudio = true
		return
	}
	t.Codec = codecName(codecID)
	d.tracks = append(d.tracks, t)
	if len(d.tracks) == 1 {
		d.codecID = codecID
		d.sampleRate = t.SampleRate
		d.channels = t.Channels
		d.bitDepth = t.BitsPerSample
	}
}

// parseAudio reads the Audio sub-element's sampling frequency, channel count, and
// bit depth.
func parseAudio(src core.ReaderAtSized, audio element, depth *bits.Depth, limit int64, t *core.AudioTrack) {
	_ = eachChild(src, audio.dataStart, audio.dataEnd, depth, limit, func(el element) error {
		switch el.id {
		case idSampFreq:
			// Guard NaN/Inf and absurd magnitudes: int(NaN)/int(+Inf) is
			// implementation-defined, and a wild rate poisons the essence config.
			if f, ok := readFloat(src, el, limit); ok && f > 0 && f < math.MaxInt32 {
				t.SampleRate = int(f)
			}
		case idChannels:
			t.Channels = intVal(readUint(src, el, limit))
		case idBitDepth:
			t.BitsPerSample = intVal(readUint(src, el, limit))
		}
		return nil
	})
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
	g := tagGroup{raw: captureRaw(src, tagEl, limit)}
	err := eachChild(src, tagEl.dataStart, tagEl.dataEnd, depth, limit, func(el element) error {
		switch el.id {
		case idCRC32:
			g.hasCRC = true
		case idTargets:
			g.targetsRaw = captureRaw(src, el, limit)
			parseTargets(src, el, depth, limit, &g)
		case idSimpleTag:
			st, err := parseSimpleTag(src, el, depth, limit)
			if err != nil {
				return err
			}
			g.tags = append(g.tags, st)
		}
		return nil
	})
	g.scope = resolveScope(g)
	return g, err
}

// parseTargets reads a Targets element: the logical level (TargetTypeValue /
// TargetType) and whether the tag is narrowed to a track, edition, or chapter.
func parseTargets(src core.ReaderAtSized, targets element, depth *bits.Depth, limit int64, g *tagGroup) {
	_ = eachChild(src, targets.dataStart, targets.dataEnd, depth, limit, func(el element) error {
		switch el.id {
		case idTgtTypeVal:
			g.targetTypeValue = readUint(src, el, limit)
		case idTgtType:
			g.targetType, _ = readString(src, el, limit) // scope keyword; numeric value is primary
		case idTagTrackUID:
			g.trackUID = true
		case idTagEditUID:
			g.editionUID = true
		case idTagChapUID:
			g.chapterUID = true
		}
		return nil
	})
}

// parseSimpleTag reads a SimpleTag (name/value/language) and recurses into any
// nested sub-tags. eachChild's depth guard bounds the recursion.
func parseSimpleTag(src core.ReaderAtSized, st element, depth *bits.Depth, limit int64) (simpleTag, error) {
	s := simpleTag{raw: captureRaw(src, st, limit)}
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
		case idTagLang:
			s.lang, _ = readString(src, el, limit) // informational; degrade gracefully
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

// parseAttached reads one AttachedFile, returning its summary and, when it is an
// image, a decoded Picture. The attachment fields and cover bytes propagate read
// errors so a truncated or over-limit cover fails the parse rather than yielding
// a partial picture.
func parseAttached(src core.ReaderAtSized, af element, depth *bits.Depth, limit int64) (attachment, *core.Picture, error) {
	a := attachment{raw: captureRaw(src, af, limit)}
	var dataEl element
	var haveData bool
	err := eachChild(src, af.dataStart, af.dataEnd, depth, limit, func(el element) error {
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
	a.image = strings.HasPrefix(strings.ToLower(a.mime), "image/")
	if !a.image || !haveData {
		return a, nil, nil
	}
	data, err := readBytes(src, dataEl, limit)
	if err != nil {
		return a, nil, err
	}
	pic := core.Picture{
		Type:        pictureType(a.name),
		MIME:        a.mime,
		Description: a.description,
		Data:        data,
	}
	pic.SniffInto()
	return a, &pic, nil
}

// resolveScope maps a tag group's targets to a canonical scope. A chapter or
// edition UID, or a high TargetTypeValue, widens or narrows the level; the
// default (empty targets => TargetTypeValue 50) is album level. A track UID
// narrows an album/part-level group to the track. When the numeric
// TargetTypeValue is absent, the informational TargetType string is consulted
// (Picard and some hand-authored files set only the string).
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

// level resolves a tag group's TargetTypeValue: the explicit numeric value, else
// the informational TargetType keyword (Picard and some hand-authored files set
// only the string), else the spec default of 50 (album/movie). It is the single
// level resolver shared by resolveScope and the native dump view.
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

// scopedContribution is one canonical value with the scope it was tagged at.
type scopedContribution struct {
	key   tag.Key
	value string
	scope core.Scope
}

// project builds the authoritative TagSet and the scope-aware family view from
// the parsed groups and the segment title. Only top-level SimpleTags project to
// the canonical set; nested sub-tags and unmapped names stay in the native tree.
func project(d *doc) (tag.TagSet, []core.FamilyValue) {
	var contribs []scopedContribution
	if d.segTitle != "" {
		contribs = append(contribs, scopedContribution{tag.Title, d.segTitle, core.ScopeAlbum})
	}
	for _, g := range d.groups {
		for _, st := range g.tags {
			contribs = append(contribs, projectTag(st.name, st.value, g.scope)...)
		}
	}

	ts := tag.NewTagSet()
	seen := map[string]bool{}
	for _, c := range contribs {
		sig := string(c.key) + "\x00" + core.Fold(c.value)
		if seen[sig] {
			continue
		}
		seen[sig] = true
		ts.Add(c.key, c.value)
	}
	return ts, buildFamilies(contribs)
}

// projectTag maps one SimpleTag name/value to canonical contributions, splitting
// a "n/total" track or disc number into its two canonical keys.
func projectTag(name, value string, scope core.Scope) []scopedContribution {
	key, ok := mapping.MatroskaTagKey(name)
	if !ok || value == "" {
		return nil
	}
	if (key == tag.TrackNumber || key == tag.DiscNumber) && strings.ContainsRune(value, '/') {
		n, total := tag.ParseNumPair(value, "")
		var out []scopedContribution
		if n > 0 {
			out = append(out, scopedContribution{key, strconv.Itoa(n), scope})
		}
		if total > 0 {
			out = append(out, scopedContribution{totalKey(key), strconv.Itoa(total), scope})
		}
		if len(out) > 0 {
			return out
		}
	}
	return []scopedContribution{{key, value, scope}}
}

// totalKey returns the "total" companion key for a numbering key.
func totalKey(k tag.Key) tag.Key {
	if k == tag.DiscNumber {
		return tag.DiscTotal
	}
	return tag.TrackTotal
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
				ws = core.Warn(ws, core.WarnInheritedEncoder, "inherited encoder stamp: "+v)
			}
		}
	}
	seen := map[tag.Key]bool{}
	for _, f := range fams {
		if !f.Selected && !seen[f.Key] {
			seen[f.Key] = true
			ws = core.Warn(ws, core.WarnConflictingFamilies,
				"conflicting values across targets for "+string(f.Key))
		}
	}
	return ws
}

// pictureType maps an attachment file name to a picture role using the Matroska
// cover-art naming convention (matroska.org): the front cover is "cover.<ext>"
// and a small thumbnail is "small_cover.<ext>". Matroska defines no standard
// back-cover name, so anything that is not a recognized cover is Other rather
// than guessed at from substrings (which would misfire on e.g. "background.jpg").
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
		return "AAC"
	case strings.HasPrefix(id, "A_MPEG/L3"):
		return "MP3"
	case strings.HasPrefix(id, "A_MPEG/L2"):
		return "MP2"
	case strings.HasPrefix(id, "A_AC3"):
		return "AC-3"
	case strings.HasPrefix(id, "A_PCM"):
		return "PCM"
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
