// Package vorbis implements the byte-level Vorbis comment list codec, the
// FLAC-style PICTURE block codec, and the canonical projection / minimal-change
// rebuild shared by every format that stores tags as Vorbis comments - FLAC and
// Ogg Vorbis/Opus. It is an internal helper reimplemented from the Vorbis-comment
// and FLAC picture specifications; reference implementations were consulted for
// design only.
//
// A comment list is the format-neutral core: a vendor string and "NAME=value"
// entries with little-endian length prefixes. FLAC wraps it in a metadata
// block; Ogg Vorbis prefixes a "\x03vorbis" signature and appends a framing
// bit; Ogg Opus prefixes "OpusTags" and may append padding. Those wrappers live
// in the respective codecs; the list codec here is shared.
package vorbis

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
)

// Comment is one Vorbis "NAME=value" entry. The original name spelling is kept
// so unedited comments preserve their exact form on rewrite.
type Comment struct {
	Name  string
	Value string
}

// ParseCommentList decodes a comment list (little-endian lengths): a vendor
// string, a count, then that many "NAME=value" entries. It returns the number
// of body bytes consumed so a caller can handle whatever follows the list - the
// Vorbis framing bit, or Opus comment-header padding. Entries without '=' are
// dropped from the result (but still consumed). maxElements caps how many
// comments accumulate (0 disables it): the count field is an attacker-controlled
// uint32 and the body can be large (an Ogg comment packet is bounded only by the
// alloc limit), so without the cap a body packed with minimum entries amplifies
// into one Comment descriptor each to OOM - the same guard FLAC/RIFF/ID3/MP4 use.
func ParseCommentList(body []byte, limit int64, maxElements int) (vendor string, comments []Comment, n int64, err error) {
	c := bits.NewCursor(bytes.NewReader(body), int64(len(body)), limit)
	vlen := int64(c.U32LE())
	vendor = string(c.Bytes(vlen))
	count := c.U32LE()
	for i := uint32(0); i < count; i++ {
		if c.Err() != nil {
			break
		}
		l := int64(c.U32LE())
		entry := c.Bytes(l)
		if c.Err() != nil {
			break
		}
		name, value, ok := strings.Cut(string(entry), "=")
		if !ok {
			continue // malformed entry without '='; drop from projection
		}
		if capErr := bits.CheckElementCap(len(comments), maxElements, "Vorbis comments"); capErr != nil {
			return vendor, comments, c.Pos(), capErr
		}
		comments = append(comments, Comment{Name: name, Value: value})
	}
	if c.Err() != nil {
		return vendor, comments, c.Pos(), fmt.Errorf("vorbis comment: %w", c.Err())
	}
	return vendor, comments, c.Pos(), nil
}

// RenderCommentList encodes a vendor string and comments into a list body
// (little-endian lengths, no signature or framing). Deterministic: same inputs
// produce identical bytes.
func RenderCommentList(vendor string, comments []Comment) []byte {
	var buf bytes.Buffer
	writeU32LE(&buf, uint32(len(vendor)))
	buf.WriteString(vendor)
	writeU32LE(&buf, uint32(len(comments)))
	for _, cm := range comments {
		entry := cm.Name + "=" + cm.Value
		writeU32LE(&buf, uint32(len(entry)))
		buf.WriteString(entry)
	}
	return buf.Bytes()
}

// Project builds the canonical TagSet and the family/source view from a comment
// list, preserving order. A canonical key fed by two or more distinct native
// field names with disagreeing values (e.g. DATE=2020 and YEAR=2019, both
// mapping to RecordingDate) is a genuine conflict and is marked unselected so it
// surfaces in the family view and Lint. Repeats of the same native name
// (ARTIST=A, ARTIST=B) are an ordinary multi-value, not a conflict.
//
// CHAPTERxxx and SYNCEDLYRICS comments are structured chapters and synced lyrics.
// METADATA_BLOCK_PICTURE is cover art. These entries are owned by their dedicated
// projectors, not by the custom tag view; Rebuild preserves them unless the matching edit
// replaces the set.
func Project(comments []Comment) (tag.TagSet, []core.FamilyValue) {
	ts := tag.NewTagSet()
	famIndex := map[tag.Key]int{}
	names := map[tag.Key]map[string]bool{} // distinct native names per key
	var fams []core.FamilyValue
	for _, cm := range comments {
		if reservedNamespace(cm.Name) != "" {
			continue // owned by structured metadata projectors, not the custom tag view
		}
		key, valid := canonicalTagKey(cm.Name)
		if !valid {
			// A non-conformant native name (empty, or with characters the writer's Key.Valid()
			// gate rejects) has no valid canonical key. Keep it out of the canonical model
			// entirely - no tag, no family entry - so copy does not grade it Carried and then
			// abort at write time. The raw comment is untouched in the native list, so an
			// unrelated edit still preserves it verbatim (Rebuild copies it as-is);
			// InvalidKeyWarnings surfaces it at the parse sites via the same canonicalTagKey rule.
			continue
		}
		// The Vorbis reader stores values as raw bytes; a non-conformant file can hold invalid
		// UTF-8 (the spec mandates UTF-8, but WaxLabel parses best-effort). Sanitize it into the
		// canonical model the way the ID3/MP4/Matroska readers do, so the model never carries raw
		// invalid sequences: a copy of such a value is not spuriously rejected by the write-time
		// UTF-8 guard, and --json never emits invalid bytes. The native comment list keeps its
		// raw bytes, so an unrelated edit still preserves them verbatim (Rebuild copies unchanged
		// comments as-is).
		val := core.SanitizeUTF8(cm.Value)
		ts.Add(key, val)
		if i, ok := famIndex[key]; ok {
			fams[i].Values = append(fams[i].Values, val)
		} else {
			famIndex[key] = len(fams)
			names[key] = map[string]bool{}
			fams = append(fams, core.FamilyValue{
				Key: key, Family: core.FamilyVorbis, Scope: core.ScopeTrack,
				Values: []string{val}, Selected: true,
			})
		}
		names[key][strings.ToUpper(cm.Name)] = true
	}
	for key, i := range famIndex {
		if len(names[key]) > 1 && distinctValues(fams[i].Values) > 1 {
			fams[i].Selected = false
		}
	}
	// Split a slashed TRACKNUMBER/DISCNUMBER ("4/9") into number + total so this read path
	// agrees with the ID3/MP4/Matroska projections and the editor. Vorbis stores the pair
	// verbatim, so without this the canonical layer would disagree with itself. The native
	// comment list keeps its raw "4/9" (Rebuild copies unchanged comments as-is), so a plain
	// read then write stays byte-identical and an unrelated edit re-projects through the same
	// post-pass, keeping base == result. Only ts is normalized; the family view still shows the
	// raw value by design.
	tag.NormalizeNumberPairs(&ts)
	return ts, fams
}

// Rebuild produces the new comment list with minimal change: unchanged comments
// keep their exact spelling and position; a changed key's new values replace its
// first original occurrence (later duplicates and aliases of that key are
// dropped, deduping inherited noise); newly added keys are appended in edited
// order.
//
// An edited key that already existed keeps the file's own spelling for that key (so a
// lowercase "artist" stays "artist" on an unrelated-value edit), except when the key
// has a write-preferred Vorbis spelling distinct from its canonical name - an alias
// like RecordingDate, whose preferred tag is DATE - in which case it canonicalizes to
// that. A newly-added key uses the preferred Vorbis spelling.
//
// CHAPTERxxx and SYNCEDLYRICS comments are owned by the chapter and synced-lyrics models,
// not by the generic tag-key diff. A chapter or synced-lyrics edit drops the source owned
// comments and appends the edited set; unrelated edits preserve them verbatim. A
// METADATA_BLOCK_PICTURE comment is likewise never treated as a custom key: a malformed/opaque one
// is preserved verbatim (a valid cover was decoded out into the picture set and is re-rendered by
// the codec), and a --set on that reserved key drops-with-warning instead of overwriting it.
func Rebuild(orig []Comment, edited tag.TagSet, changed map[tag.Key]bool, chapters []core.Chapter, chaptersChanged bool, syncedLyrics []core.SyncedLyrics, syncedLyricsChanged bool) ([]Comment, RebuildInfo) {
	var info RebuildInfo
	emitted := map[tag.Key]bool{}
	out := make([]Comment, 0, len(orig))
	emit := func(k tag.Key, name string) {
		vals, _ := edited.Get(k)
		for _, v := range vals {
			out = append(out, Comment{Name: name, Value: v})
		}
		emitted[k] = true
	}
	// hasNative marks the canonical keys that already own a native comment in orig. The slash-pair
	// rewrite below re-derives a total from the slash only when its total key has no comment of
	// its own; an explicit TRACKTOTAL/TOTALTRACKS is left to the normal loop, which preserves or
	// replaces it in place. Re-deriving it there too would duplicate an explicit-total-first
	// ordering, or relabel and relocate an untouched one.
	hasNative := map[tag.Key]bool{}
	for _, cm := range orig {
		if isChapterComment(cm.Name) || isSyncedLyricsComment(cm.Name) {
			continue
		}
		hasNative[mapping.CanonicalVorbis(cm.Name)] = true
	}
	for _, cm := range orig {
		if isChapterComment(cm.Name) {
			if !chaptersChanged {
				out = append(out, cm) // preserve verbatim on an unrelated edit
			}
			continue // dropped on a chapter edit; re-emitted from the edited set below
		}
		if isSyncedLyricsComment(cm.Name) {
			if !syncedLyricsChanged {
				out = append(out, cm) // preserve verbatim on an unrelated edit
			}
			continue // dropped on a synced-lyrics edit; re-emitted below
		}
		if IsPictureComment(cm.Name) {
			// Cover art belongs to the picture model and is re-rendered by the codec, never edited
			// as a custom tag: a valid cover was already decoded out into the picture set, so only
			// a malformed, opaque comment lingers here. Preserve it verbatim and skip the generic
			// key path, so a --set METADATA_BLOCK_PICTURE on a file that already holds a picture
			// comment cannot overwrite it in place. It falls through to the reserved-namespace drop
			// below instead, the same as chapters and synced lyrics; without this branch that --set
			// would quietly reopen the side channel the guard closes.
			out = append(out, cm)
			continue
		}
		k := mapping.CanonicalVorbis(cm.Name)
		// A slash-backed TRACKNUMBER/DISCNUMBER comment natively holds both the number and a
		// derived total; the read path splits "4/9" into TRACKNUMBER=4 + TRACKTOTAL=9. When either
		// canonical key changed, rewrite the number from the edited value and drop the slash, or a
		// cleared or edited total would resurface when the preserved "4/9" is re-projected. The
		// derived total gets its own comment only when the total key has no native comment;
		// otherwise the explicit TRACKTOTAL/TOTALTRACKS is left to the normal loop (kept verbatim
		// if untouched, replaced in place if changed) so it is neither duplicated nor moved. This
		// tracks the read-path split ([tag.NumberTotalSplit]) and Matroska's droppedByEdit so write
		// and projection agree. An unrelated edit leaves the pair alone, so the fall-through below
		// preserves the slash comment verbatim.
		if k == tag.TrackNumber || k == tag.DiscNumber {
			if _, _, split := tag.NumberTotalSplit(k, cm.Value); split {
				totKey := tag.TotalKey(k)
				if changed[k] || changed[totKey] {
					if !emitted[k] {
						emit(k, cm.Name) // number only, keeping the file's spelling (no slash)
					}
					if !hasNative[totKey] && !emitted[totKey] {
						emit(totKey, mapping.VorbisName(totKey)) // derived total with no comment of its own
					}
					continue
				}
			}
		}
		if changed[k] {
			if !emitted[k] {
				// Reuse the original comment's casing, unless the key has a write-preferred
				// spelling (e.g. an alias canonicalizing to DATE), which wins.
				name := cm.Name
				if pref := mapping.VorbisName(k); pref != string(k) {
					name = pref
				}
				emit(k, name) // replace in place; nothing emitted if the key was cleared
			}
			continue
		}
		if emitted[k] {
			continue // a later duplicate of a key already emitted by the slash-pair rewrite above
		}
		out = append(out, cm)
	}
	for _, k := range edited.Keys() {
		if changed[k] && !emitted[k] {
			// A newly-added key in a reserved namespace - CHAPTERxxx/CHAPTERxxxNAME chapters,
			// SYNCEDLYRICS synced lyrics, or METADATA_BLOCK_PICTURE cover art - cannot be written
			// as a custom field: on read each is owned by its structured projector, not the tag
			// view, so writing it would leave a stray comment the reader consumes as structured
			// data and the key vanishes silently. Record it so the caller warns value-dropped, and
			// skip it rather than emit a comment that re-reads as something other than a custom
			// field. All three behave like CHAPTERxxx (always drop-with-warning), rather than the
			// surprising "invalid payload lost, valid payload silently becomes a chapter / synced
			// lyric / cover"; users set these through the dedicated paths, not --set.
			name := mapping.VorbisName(k)
			if reservedNamespace(name) != "" {
				info.ReservedKeys = append(info.ReservedKeys, k)
				continue
			}
			emit(k, name) // newly-added key: the preferred Vorbis spelling
		}
	}
	if chaptersChanged {
		cc, overflow := chapterComments(chapters)
		out = append(out, cc...)
		info.ChapterOverflow = overflow
	}
	if syncedLyricsChanged {
		sc, overflow := syncedLyricsComments(syncedLyrics)
		out = append(out, sc...)
		info.SyncedLyricsOverflow = overflow
	}
	return out, info
}

// RebuildInfo reports the codec-ceiling clamps [Rebuild] applied while rendering owned
// chapter and synced-lyrics comments, so the caller can attach the matching write-time
// warnings. Rebuild stays otherwise pure - it records the clamp here rather than emitting a
// warning itself - mirroring ID3's RebuildInfo. The clamp is not cosmetic: without it the
// over-range value is written past what the reader accepts and re-projects to nothing, so
// the write collapses to a "No metadata changes" no-op and the edit is silently lost.
type RebuildInfo struct {
	// ChapterOverflow is set when a chapter start exceeded the CHAPTERxxx timestamp ceiling
	// and was clamped to it.
	ChapterOverflow bool
	// SyncedLyricsOverflow is set when a synced-lyric line's timestamp exceeded the LRC
	// timestamp ceiling and was clamped to it.
	SyncedLyricsOverflow bool
	// ReservedKeys lists newly-added keys in a reserved namespace - CHAPTERxxx/CHAPTERxxxNAME
	// chapters, SYNCEDLYRICS synced lyrics, or METADATA_BLOCK_PICTURE cover art - that cannot be
	// written as custom fields: on read they are owned by a structured projector, not the tag
	// view, so writing one would leave a stray comment the reader silently consumes as structured
	// data. They are dropped rather than written, and the caller surfaces a value-dropped warning
	// naming the specific namespace per key.
	ReservedKeys []tag.Key
}

// RebuildWarnings appends the write-time warnings for what [Rebuild] recorded in RebuildInfo:
// the codec-ceiling clamps (an over-range chapter or synced-lyric timestamp, surfacing the same
// coded warning MP4/ID3 emit) and the reserved-namespace drops (a custom key in a reserved
// namespace - CHAPTERxxx chapters, SYNCEDLYRICS synced lyrics, or METADATA_BLOCK_PICTURE cover
// art - that cannot be written as a tag). FLAC and Ogg share it, keeping their two write paths
// aligned. The clamp warnings are write-report only - the stored value sits at the codec ceiling
// and re-parses cleanly, so a fresh parse emits neither - while a reserved-key drop is a real
// value loss carried through even a no-op (via DowngradeNoOp) so it is never silent.
func RebuildWarnings(prior []core.Warning, info RebuildInfo) []core.Warning {
	if info.ChapterOverflow {
		prior = core.Warn(prior, core.WarnChapterStartOverflow,
			"a chapter start exceeded the CHAPTERxxx timestamp limit and was clamped")
	}
	if info.SyncedLyricsOverflow {
		prior = core.Warn(prior, core.WarnSyncedLyricsTimestampClamped,
			"a synced-lyric timestamp exceeded the LRC timestamp limit and was clamped")
	}
	for _, k := range info.ReservedKeys {
		// The key reached ReservedKeys only because reservedNamespace matched, so the label here is
		// the same non-empty classification the drop decision made.
		ns := reservedNamespace(mapping.VorbisName(k))
		prior = core.WarnKeyed(prior, core.WarnValueDropped,
			fmt.Sprintf("%s is in the reserved %s namespace and cannot be written as a custom field", k, ns), k)
	}
	return prior
}

// InvalidKeyWarnings reports a WarnInvalidTagKey for each comment whose native name does
// not map to a valid canonical tag key (an empty name, or one with characters the writer's
// Key.Valid() gate rejects). Project drops such a key from the canonical model, but the raw
// comment is preserved verbatim on write, so the warning says the key is not represented in
// canonical tags / not carried - not that it was removed. Emitted at the parse sites like
// EncoderNoise; owned structured comments (chapters, synced lyrics, pictures) are skipped,
// exactly as Project skips them.
func InvalidKeyWarnings(comments []Comment) []core.Warning {
	var ws []core.Warning
	for _, cm := range comments {
		if reservedNamespace(cm.Name) != "" {
			continue
		}
		if _, valid := canonicalTagKey(cm.Name); valid {
			continue
		}
		ws = core.Warn(ws, core.WarnInvalidTagKey,
			"tag key not represented in canonical tags (not carried): "+tag.SanitizeLine(cm.Name))
	}
	return ws
}

// canonicalTagKey resolves a Vorbis comment name to its canonical key and reports whether that
// key is valid, meaning representable in the canonical tag model. [Project] (which drops an
// invalid key) and [InvalidKeyWarnings] (which flags it) share this one decision so the two
// cannot drift out of sync.
func canonicalTagKey(name string) (tag.Key, bool) {
	k := mapping.CanonicalVorbis(name)
	return k, k.Valid()
}

// reservedNamespace classifies a Vorbis comment name that a structured projector owns rather than
// the custom tag view, returning its label ("chapter", "synced lyrics", or "cover art") or "" for
// an ordinary custom field. [Project], [Rebuild], [RebuildWarnings], and [InvalidKeyWarnings] all
// resolve it here, so the "is this reserved?" test (label != "") and the value-dropped warning
// text stay in step: a new namespace is added in one place, and [RebuildWarnings] can never name a
// namespace the drop decision did not make.
func reservedNamespace(name string) string {
	switch {
	case isChapterComment(name):
		return "chapter"
	case isSyncedLyricsComment(name):
		return "synced lyrics"
	case IsPictureComment(name):
		return "cover art"
	}
	return ""
}

// TransferClassifier grades the fields whose Vorbis transfer fate the format-level
// capability cannot express: a custom key whose native Vorbis name falls in a reserved
// namespace (CHAPTERxxx chapters, SYNCEDLYRICS synced lyrics, METADATA_BLOCK_PICTURE cover
// art). A Vorbis writer drops such a key rather than emit a stray comment a reader would
// silently consume as structured data (see [RebuildWarnings]), so a copy that carries one -
// e.g. a Matroska CHAPTER050NAME custom tag copied to FLAC/Ogg - must report it Dropped
// rather than a clean carry. It reuses the same reservedNamespace decision the writer makes,
// keeping the copy report and the write drop in step; FLAC and Ogg share it. Every ordinary
// key is left to the format-level grade.
//
// The direction inverts the writer: the writer classifies the stored comment name, while
// this classifies the native name of a canonical key (mapping.VorbisName). The two align
// only while VorbisName(customKey) equals the raw stored name, which a negative transfer
// test guards. It is a plain [core.FieldClassifier] (registered by value, not called), so it
// captures nothing and allocates no closure.
func TransferClassifier(key tag.Key, _ []string, _ tag.TagSet) (core.Disposition, string, bool) {
	if ns := reservedNamespace(mapping.VorbisName(key)); ns != "" {
		return core.Dropped, fmt.Sprintf("the %s namespace is reserved for structured data, so it cannot be written as a Vorbis custom field", ns), true
	}
	return core.Carried, "", false
}

// DiffKeys returns the canonical keys whose values differ between base and
// edited (added, removed, or modified).
func DiffKeys(base, edited tag.TagSet) map[tag.Key]bool {
	changed := map[tag.Key]bool{}
	for _, k := range base.Keys() {
		bv, _ := base.Get(k)
		ev, has := edited.Get(k)
		if !has || !slices.Equal(bv, ev) {
			changed[k] = true
		}
	}
	for _, k := range edited.Keys() {
		if !base.Has(k) {
			changed[k] = true
		}
	}
	return changed
}

// EncoderNoise flags inherited transcoder stamps (e.g. ffmpeg's
// "encoder=Lavf..." comment or vendor string), the typical signature of a file
// acquired by transcoding. When the vendor string and an ENCODER comment carry the
// identical stamp - ffmpeg writes the same "Lavf..." into both - they collapse
// into one warning rather than reporting the same value twice.
func EncoderNoise(vendor string, comments []Comment) []core.Warning {
	var ws []core.Warning
	vendorStamp := core.IsTranscoderStamp(vendor)
	// Does an ENCODER comment repeat the vendor stamp verbatim?
	vendorEchoed := false
	if vendorStamp {
		for _, cm := range comments {
			// Match case-insensitively: a transcoder writes the same stamp into both,
			// and a casing difference between the two should still collapse to one note.
			if strings.EqualFold(cm.Name, "ENCODER") && strings.EqualFold(cm.Value, vendor) {
				vendorEchoed = true
				break
			}
		}
	}
	switch {
	case vendorStamp && vendorEchoed:
		ws = core.Warn(ws, core.WarnInheritedEncoder,
			"transcoder stamp in vendor string and encoder comment: "+vendor)
	case vendorStamp:
		// Name the field explicitly: dump shows the ENCODER *tag* (e.g. "Lavc..."),
		// while this stamp is the container *vendor string* (never a tag), so without
		// the distinction the warning reads as contradicting the displayed ENCODER.
		ws = core.Warn(ws, core.WarnInheritedEncoder,
			"container vendor string (distinct from the ENCODER tag) is a transcoder stamp: "+vendor)
	}
	for _, cm := range comments {
		if !strings.EqualFold(cm.Name, "ENCODER") || !core.IsTranscoderStamp(cm.Value) {
			continue
		}
		// Skip the comment already folded into the combined warning above.
		if vendorEchoed && strings.EqualFold(cm.Value, vendor) {
			continue
		}
		ws = core.Warn(ws, core.WarnInheritedEncoder, "inherited encoder comment: "+cm.Value)
	}
	return ws
}

// WaxLabelVendor is the neutral vendor string written when --strip-encoder replaces an
// inherited transcoder stamp in a FLAC/Ogg Vorbis comment block.
const WaxLabelVendor = "WaxLabel"

// NeutralizeVendor returns the vendor string to write under the strip flag and reports
// whether it changed. A changed vendor is a real edit because no canonical tag key can reach
// the comment-header vendor field.
func NeutralizeVendor(vendor string, strip bool) (string, bool) {
	if strip && core.IsTranscoderStamp(vendor) {
		return WaxLabelVendor, true
	}
	return vendor, false
}

// CarryEncoderWarnings recomputes inherited-encoder warnings from the vendor and comments
// that were written, preserving every other warning in prior. Post-write documents use this
// to match a fresh parse of the output.
func CarryEncoderWarnings(prior []core.Warning, vendor string, comments []Comment) []core.Warning {
	// Filter first, then deep-clone only the survivors. WarningsWithoutCode already returns a
	// fresh slice, but its structs still share Keys with prior.
	out := core.CloneWarnings(core.WarningsWithoutCode(prior, core.WarnInheritedEncoder))
	return append(out, EncoderNoise(vendor, comments)...)
}

// distinctValues counts the distinct case- and space-insensitive values using
// the same fold rule as dump duplicate markers.
func distinctValues(vals []string) int { return tag.DistinctValues(vals) }

func writeU32LE(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}
