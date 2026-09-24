// Package vorbis: Vorbis comment list codec, FLAC PICTURE codec, and shared
// projection/rebuild for FLAC and Ogg. Internal; from the Vorbis-comment and
// FLAC picture specs.
//
// List core: vendor + LE-length "NAME=value" entries. FLAC wraps in a block;
// Ogg Vorbis adds "\x03vorbis"+framing bit; Opus adds "OpusTags"+optional padding.
package vorbis

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
)

// Comment is one "NAME=value" entry. Name spelling kept for unedited rewrite.
//
// Unseparated: no "=". Well framed but uninterpreted: empty Name, Value is raw
// bytes. [RenderCommentList] writes unchanged; projectors skip it.
type Comment struct {
	Name        string
	Value       string
	Unseparated bool
}

// ParseCommentList decodes LE-length vendor, count, and entries. Returns bytes
// consumed (for framing bit / Opus padding). Entries without '=' are Unseparated.
// maxElements caps accumulation (0 = off); count is attacker-controlled.
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
		// A malformed entry counts toward the cap too: it allocates a descriptor like
		// any other, so a body packed with them still hits ErrSizeTooLarge.
		if capErr := bits.CheckElementCap(len(comments), maxElements, "Vorbis comments"); capErr != nil {
			return vendor, comments, c.Pos(), capErr
		}
		name, value, ok := strings.Cut(string(entry), "=")
		if !ok {
			// No separator: keep the entry bytes verbatim so a rewrite preserves them.
			comments = append(comments, Comment{Value: string(entry), Unseparated: true})
			continue
		}
		comments = append(comments, Comment{Name: name, Value: value})
	}
	if c.Err() != nil {
		return vendor, comments, c.Pos(), fmt.Errorf("vorbis comment: %w", c.Err())
	}
	return vendor, comments, c.Pos(), nil
}

// RenderCommentList encodes vendor+comments (LE lengths, no framing). Deterministic.
func RenderCommentList(vendor string, comments []Comment) []byte {
	var buf bytes.Buffer
	writeU32LE(&buf, uint32(len(vendor)))
	buf.WriteString(vendor)
	writeU32LE(&buf, uint32(len(comments)))
	for _, cm := range comments {
		// An unseparated entry is written back as read; "=" + Value would corrupt it.
		entry := cm.Value
		if !cm.Unseparated {
			entry = cm.Name + "=" + cm.Value
		}
		writeU32LE(&buf, uint32(len(entry)))
		buf.WriteString(entry)
	}
	return buf.Bytes()
}

// Project builds TagSet and family/source view. Distinct native names with
// disagreeing values for one key are conflicts (unselected). Same-name repeats
// are multi-value. CHAPTERxxx, SYNCEDLYRICS, METADATA_BLOCK_PICTURE are owned
// elsewhere; Rebuild preserves them unless that edit replaces them.
func Project(comments []Comment) (tag.TagSet, []core.FamilyValue) {
	ts := tag.NewTagSet()
	famIndex := map[tag.Key]int{}
	names := map[tag.Key]map[string]bool{} // distinct native names per key
	var fams []core.FamilyValue
	for _, cm := range comments {
		if cm.Unseparated {
			continue // no name to key off; Rebuild preserves the entry verbatim
		}
		if reservedNamespace(cm.Name) != "" {
			continue // owned by structured metadata projectors, not the custom tag view
		}
		key, valid := canonicalTagKey(cm.Name)
		if !valid {
			// A name that is empty or has characters Key.Valid() rejects has no valid
			// canonical key. Keep it out of the canonical model (no tag, no family entry),
			// or copy would grade it Carried and then abort at write time. The raw comment
			// stays in the native list, so Rebuild still preserves it. InvalidKeyWarnings
			// flags it through the same canonicalTagKey rule.
			continue
		}
		// The spec mandates UTF-8, but a non-conformant file can hold invalid bytes.
		// Sanitize the value into the canonical model as the ID3/MP4/Matroska readers
		// do, so the write-time UTF-8 guard does not reject a copy and --json never
		// emits invalid bytes. The native list keeps the raw bytes, so Rebuild still
		// preserves them on an unrelated edit.
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
	// Split a slashed TRACKNUMBER/DISCNUMBER ("4/9") into number + total, as the
	// ID3/MP4/Matroska projections and the editor do. The native list keeps the raw
	// "4/9", so a read then write stays byte-identical and an unrelated edit
	// re-projects through this same pass, keeping base == result. Only ts is
	// normalized; the family view shows the raw value.
	tag.NormalizeNumberPairs(&ts)
	return ts, fams
}

// Rebuild: minimal change. Unchanged comments keep spelling/position; changed
// keys replace the first occurrence (later dupes/aliases dropped); new keys append.
//
// Existing keys keep file spelling unless a write-preferred Vorbis name applies
// (e.g. RecordingDate → DATE). New keys use the preferred spelling.
//
// CHAPTERxxx/SYNCEDLYRICS/METADATA_BLOCK_PICTURE are owned: chapter/lyrics edits
// replace them; unrelated edits preserve. Opaque picture comments stay verbatim.
func Rebuild(orig []Comment, edited tag.TagSet, changed map[tag.Key]bool, chapters []core.Chapter, chaptersChanged bool, syncedLyrics []core.SyncedLyrics, syncedLyricsChanged bool) ([]Comment, RebuildInfo) {
	var info RebuildInfo
	emitted := map[tag.Key]bool{}
	out := make([]Comment, 0, len(orig))
	emit := func(k tag.Key, name string) {
		vals, _ := edited.Get(k)
		// Canonicalize a recognized boolean word to "1"/"0" (COMPILATION=true -> 1),
		// matching MP4's cpil, so copy (Carried) and diff (no change) agree across
		// formats. CanonicalBoolValue leaves an unrecognized value ("maybe") as text.
		boolean := tag.IsBooleanKey(k)
		for _, v := range vals {
			if boolean {
				v = tag.CanonicalBoolValue(v)
			}
			out = append(out, Comment{Name: name, Value: v})
		}
		emitted[k] = true
	}
	// hasNative marks the canonical keys that own a native comment in orig. The
	// slash-pair rewrite below derives a total only when the total key has no
	// comment of its own; the normal loop preserves or replaces an explicit
	// TRACKTOTAL/TOTALTRACKS in place, so it is neither duplicated nor moved.
	hasNative := map[tag.Key]bool{}
	for _, cm := range orig {
		if cm.Unseparated || isChapterComment(cm.Name) || isSyncedLyricsComment(cm.Name) {
			continue
		}
		hasNative[mapping.CanonicalVorbis(cm.Name)] = true
	}
	for _, cm := range orig {
		if cm.Unseparated {
			out = append(out, cm) // no name to key off; preserve the entry verbatim
			continue
		}
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
			// A valid cover was decoded into the picture set, so only a malformed, opaque
			// comment remains here. Preserve it verbatim and skip the generic key path, so
			// a --set METADATA_BLOCK_PICTURE cannot overwrite it in place; that --set falls
			// through to the reserved-namespace drop below, like chapters and synced lyrics.
			out = append(out, cm)
			continue
		}
		k := mapping.CanonicalVorbis(cm.Name)
		// The read path splits a slashed "4/9" into TRACKNUMBER=4 + TRACKTOTAL=9
		// ([tag.NumberTotalSplit]). When either key changed, rewrite the number from the
		// edited value and drop the slash, or a cleared or edited total would resurface
		// from the preserved "4/9". The derived total gets its own comment only when the
		// total key has no native comment (see hasNative). This matches Matroska's edit
		// decisions. An unrelated edit falls through and preserves the slash comment.
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
			// A new key in a reserved namespace (CHAPTERxxx/CHAPTERxxxNAME, SYNCEDLYRICS,
			// METADATA_BLOCK_PICTURE) cannot be written as a custom field: a structured
			// projector owns it on read, so the comment would re-read as a chapter, synced
			// lyric, or cover and the key would vanish. Record it so the caller warns
			// value-dropped. Users set these through the dedicated paths, not --set.
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

// RebuildInfo reports the codec-ceiling clamps [Rebuild] applied to owned chapter
// and synced-lyrics comments; the caller attaches the write-time warnings, as with
// ID3's RebuildInfo. Without the clamp an over-range value is written past what the
// reader accepts and re-projects to nothing, so the write collapses to a "No
// metadata changes" no-op and the edit is lost.
type RebuildInfo struct {
	// ChapterOverflow is set when a chapter start exceeded the CHAPTERxxx timestamp ceiling
	// and was clamped to it.
	ChapterOverflow bool
	// SyncedLyricsOverflow is set when a synced-lyric line's timestamp exceeded the LRC
	// timestamp ceiling and was clamped to it.
	SyncedLyricsOverflow bool
	// ReservedKeys lists new keys in a reserved namespace (CHAPTERxxx/CHAPTERxxxNAME,
	// SYNCEDLYRICS, METADATA_BLOCK_PICTURE) that were dropped instead of written as
	// custom fields, since a structured projector would consume the comment on read.
	// The caller surfaces a value-dropped warning naming the namespace per key.
	ReservedKeys []tag.Key
}

// RebuildWarnings appends the write-time warnings for what [Rebuild] recorded: the
// codec-ceiling clamps (the same coded warning MP4/ID3 emit) and the reserved-namespace
// drops. FLAC and Ogg share it. The clamp warnings are write-report only, since the
// stored value sits at the ceiling and re-parses cleanly; a reserved-key drop is a
// value loss carried through even a no-op via DowngradeNoOp.
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
		// reservedNamespace matched when Rebuild dropped the key, so ns is non-empty.
		ns := reservedNamespace(mapping.VorbisName(k))
		prior = core.WarnKeyed(prior, core.WarnValueDropped,
			fmt.Sprintf("%s is in the reserved %s namespace and cannot be written as a custom field", k, ns), k)
	}
	return prior
}

// InvalidKeyWarnings reports a WarnInvalidTagKey for each comment whose name has no
// valid canonical key (empty, or with characters Key.Valid() rejects). Project drops
// the key but the raw comment survives a write, so the warning says the key is not
// carried, not that it was removed. Called at the parse sites like EncoderNoise.
// Owned structured comments (chapters, synced lyrics, pictures) are skipped, as in
// Project.
func InvalidKeyWarnings(comments []Comment) []core.Warning {
	var ws []core.Warning
	unseparated, first := 0, ""
	for _, cm := range comments {
		if cm.Unseparated {
			// Preserved in the file, but unreadable as a key and value. Counted, not
			// warned per entry, so a list full of them is one warning.
			if unseparated++; unseparated == 1 {
				first = cm.Value
			}
			continue
		}
		if reservedNamespace(cm.Name) != "" {
			continue
		}
		if _, valid := canonicalTagKey(cm.Name); valid {
			continue
		}
		ws = core.WarnInvalidKey(ws, cm.Name)
	}
	return core.WarnUnseparatedEntry(ws, first, unseparated)
}

// canonicalTagKey resolves a Vorbis comment name to its canonical key and reports
// whether that key is representable in the canonical tag model. [Project] (which
// drops an invalid key) and [InvalidKeyWarnings] (which flags it) share the decision.
func canonicalTagKey(name string) (tag.Key, bool) {
	k := mapping.CanonicalVorbis(name)
	return k, k.Valid()
}

// reservedNamespace returns the label of the structured projector that owns a Vorbis
// comment name ("chapter", "synced lyrics", or "cover art"), or "" for an ordinary
// custom field. [Project], [Rebuild], [RebuildWarnings], and [InvalidKeyWarnings] all
// resolve it here, so the reserved test (label != "") and the value-dropped warning
// text agree, and a new namespace is added in one place.
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

// TransferClassifier grades a custom key whose native Vorbis name falls in a reserved
// namespace (CHAPTERxxx, SYNCEDLYRICS, METADATA_BLOCK_PICTURE), which the format-level
// capability cannot express. The writer drops such a key (see [RebuildWarnings]), so a
// copy that carries one, e.g. a Matroska CHAPTER050NAME custom tag copied to FLAC/Ogg,
// reports it Dropped. It reuses the writer's reservedNamespace decision; FLAC and Ogg
// share it. Every ordinary key is left to the format-level grade.
//
// The writer classifies the stored comment name; this classifies the native name of a
// canonical key (mapping.VorbisName). The two align only while VorbisName(customKey)
// equals the raw stored name, which a negative transfer test checks. It is a plain
// [core.FieldClassifier] registered by value, so it captures nothing.
func TransferClassifier(key tag.Key, _ []string, _ tag.TagSet) (core.Disposition, string, bool) {
	if ns := reservedNamespace(mapping.VorbisName(key)); ns != "" {
		return core.Dropped, fmt.Sprintf("the %s namespace is reserved for structured data, so it cannot be written as a Vorbis custom field", ns), true
	}
	return core.Carried, "", false
}

// DiffKeys returns the canonical keys whose values differ between base and
// edited (added, removed, or modified).
func DiffKeys(base, edited tag.TagSet) map[tag.Key]bool { return core.DiffKeys(base, edited) }

// EncoderNoise flags inherited transcoder stamps (e.g. ffmpeg's "encoder=Lavf..."
// comment or vendor string). When the vendor string and an ENCODER comment carry
// the same stamp, as ffmpeg writes, they collapse into one warning.
func EncoderNoise(vendor string, comments []Comment) []core.Warning {
	var ws []core.Warning
	vendorStamp := core.IsTranscoderStamp(vendor)
	// Does an ENCODER comment repeat the vendor stamp verbatim?
	vendorEchoed := false
	if vendorStamp {
		for _, cm := range comments {
			if cm.Unseparated {
				continue // no name, so not ENCODER
			}
			// Match case-insensitively so a casing difference still collapses to one note.
			if strings.EqualFold(cm.Name, "ENCODER") && strings.EqualFold(cm.Value, vendor) {
				vendorEchoed = true
				break
			}
		}
	}
	switch {
	case vendorStamp && vendorEchoed:
		ws = core.Warn(ws, core.WarnInheritedEncoder,
			"transcoder stamp in vendor string and encoder comment: "+core.WarnSnippet(vendor))
	case vendorStamp:
		// Name the field: dump shows the ENCODER tag (e.g. "Lavc..."), while this stamp
		// is the container vendor string, not a tag, so without the distinction the
		// warning reads as contradicting the displayed ENCODER.
		ws = core.Warn(ws, core.WarnInheritedEncoder,
			"container vendor string (distinct from the ENCODER tag) is a transcoder stamp: "+core.WarnSnippet(vendor))
	}
	for _, cm := range comments {
		if cm.Unseparated {
			continue // no name, so not ENCODER
		}
		if !strings.EqualFold(cm.Name, "ENCODER") || !core.IsTranscoderStamp(cm.Value) {
			continue
		}
		// Skip the comment already folded into the combined warning above.
		if vendorEchoed && strings.EqualFold(cm.Value, vendor) {
			continue
		}
		ws = core.Warn(ws, core.WarnInheritedEncoder, "inherited encoder comment: "+core.WarnSnippet(cm.Value))
	}
	return ws
}

// WaxLabelVendor is the neutral vendor string written when --strip-encoder replaces an
// inherited transcoder stamp in a FLAC/Ogg Vorbis comment block.
const WaxLabelVendor = "WaxLabel"

// NeutralizeVendor returns the vendor string to write under the strip flag and reports
// whether it changed. A changed vendor is an edit in itself: no canonical tag key can
// reach the comment-header vendor field.
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
