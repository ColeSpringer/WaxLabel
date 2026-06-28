package id3

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
)

// WriteOpts are the inputs to a frame rebuild. The multi-value policy is the
// shared core type so it can be a public write option without duplication.
type WriteOpts struct {
	Multi        core.ID3MultiValuePolicy
	NumericGenre bool // write TCON as a numeric reference when the genre is standard
}

// RebuildInfo reports facts about a rebuild the caller surfaces in the write
// report.
type RebuildInfo struct {
	// UsedV23Multi is true when a v2.3 tag was written with NUL-separated
	// multi-values (a nonstandard extension whose compatibility impact is flagged).
	UsedV23Multi bool
	// DroppedDates lists year-anchored date keys whose edited value had no extractable
	// numeric year and so rendered no v2.3 frame at all - a silent drop the caller
	// surfaces as a value-dropped warning. Empty on v2.4 (TDRC/TDOR store the full
	// string) and for values that do carry a year (only the sub-year precision is lost,
	// which is not a drop). See detectDroppedDates.
	DroppedDates []tag.Key
	// ReducedDates lists date keys whose v2.3 rendering silently lost precision finer than
	// the rendered frames capture: a month with no full date drops to the year (TDAT needs a
	// full DDMM), and an hour with no minute drops to the date (TIME needs a full HHMM). Each
	// entry pairs the key with the attempted edited value (e.g. "2021-03", "2021-03-15T10")
	// for the warning text and the precision-aware suppression. Scoped to RecordingDate;
	// OriginalDate's v2.3 reductions are reported through the capability-based value-reduced
	// path (its TORY field is AccessPartial), so listing it here would double-warn. See
	// detectReducedDates and reducesDatePrecision.
	ReducedDates []ReducedDate
	// HasDroppedMalformedPicture is set when a picture edit replaced the APIC frames and
	// at least one original APIC could not be decoded (a malformed cover). Those raw
	// bytes are not carried forward, so the loss is surfaced rather than left silent.
	HasDroppedMalformedPicture bool
	// NumericGenres lists the GENRE values this edit set that are a bare number naming a
	// standard genre by index (e.g. "17"). Written verbatim to TCON, such a value reads
	// back as the genre NAME on the pure-ID3 formats, so the caller surfaces it as a
	// write-time numeric-genre warning - symmetric with the read-time one - suppressed
	// where a native container keeps the literal number (WAV/AIFF INFO/text). See
	// detectNumericGenres.
	NumericGenres []string
}

// ReducedDate pairs a date key with the value an edit attempted to store before a
// lower-fidelity v2.3 rendering reduced its precision.
type ReducedDate struct {
	Key   tag.Key
	Value string
}

// RebuildFrames produces the new frame list for an edited tag, preserving
// unchanged and unmodelled frames in place and re-rendering only the frames a
// changed canonical key affects. Pictures are reconciled here too, since APIC
// frames are interleaved with the text frames.
func RebuildFrames(orig []Frame, base, edited tag.TagSet, version byte,
	pictures []core.Picture, picturesChanged bool, opts WriteOpts) ([]Frame, RebuildInfo) {

	changed := diffKeys(base, edited)
	dirty := map[string]bool{}
	for k := range changed {
		for _, rid := range keyRenderIDs(k, version) {
			dirty[rid] = true
		}
	}

	// The read path does not expose the COMM/USLT 3-byte language and stores a TXXX
	// description under its uppercased canonical key, so recover both from the original
	// frames when rewriting. Re-rendered comment and lyric frames keep their language,
	// and custom TXXX frames keep their original description casing.
	// frameRenderID marks a COMM/USLT frame managed only when its description is empty,
	// so there is at most one managed COMM and one managed USLT to reuse.
	origLangs := map[string]string{}    // "COMM"/"USLT" -> 3-byte language
	origTXXXDesc := map[string]string{} // TXXX render token -> original description (verbatim casing)
	for _, f := range orig {
		switch f.ID {
		case "COMM", "USLT":
			if rid, managed := frameRenderID(f); managed && len(f.Body) >= 4 {
				origLangs[rid] = string(f.Body[1:4])
			}
		case "TXXX":
			if rid, managed := frameRenderID(f); managed {
				if desc, _, ok := decodeUserText(f.Body); ok {
					origTXXXDesc[rid] = desc
				}
			}
		}
	}

	var out []Frame
	var info RebuildInfo
	emitted := map[string]bool{}
	firstAPIC := -1

	for _, f := range orig {
		if f.ID == "APIC" {
			if !picturesChanged {
				out = append(out, f.Clone())
				continue
			}
			// Picture edits replace the original APIC frames with the edited picture set.
			// If an original APIC has a malformed header, it cannot be projected and will
			// not be carried forward, so surface that loss.
			if !validAPIC(f.Body) {
				info.HasDroppedMalformedPicture = true
			}
			if firstAPIC < 0 {
				firstAPIC = len(out)
			}
			continue // re-emitted from the edited picture set below
		}
		if f.Opaque {
			out = append(out, f.Clone())
			continue
		}
		rid, managed := frameRenderID(f)
		if !managed {
			out = append(out, f.Clone())
			continue
		}
		if dirty[rid] {
			if !emitted[rid] {
				frames, v23multi := renderUnit(rid, edited, version, opts, origLangs, origTXXXDesc)
				out = append(out, frames...)
				info.UsedV23Multi = info.UsedV23Multi || v23multi
				emitted[rid] = true
			}
			continue // a changed key's frame is rendered once; drop duplicates
		}
		// Not the write-version's target for this key. If this frame is a stale
		// alternative representation of a key that changed (e.g. a TXXX:RELEASEDATE
		// or a TDRC left behind when the canonical write target is TDRL or TYER),
		// drop it so the value is not duplicated or the edit silently lost.
		if touchesChangedKey(f, changed) {
			continue
		}
		// A managed text frame carried verbatim can itself hold a v2.3 NUL-separated
		// multi-value (a copy that preserves the destination's existing multi-value field,
		// or an unrelated edit on a v2.3 file that already had one). The re-render path above
		// never sees it, so flag it here too - the caveat is a property of the OUTPUT, which
		// still carries the NUL-separated multi-value some readers do not split. v2.4 always
		// splits cleanly, so only v2.3 applies.
		if version == 3 && len(DecodeText(f)) > 1 {
			info.UsedV23Multi = true
		}
		out = append(out, f.Clone())
	}

	// Append frames for changed keys that had no original frame (newly added),
	// in a deterministic (sorted) order so the same edit always yields the same
	// bytes.
	leftover := make([]string, 0, len(dirty))
	for rid := range dirty {
		if !emitted[rid] {
			leftover = append(leftover, rid)
		}
	}
	slices.Sort(leftover)
	for _, rid := range leftover {
		frames, v23multi := renderUnit(rid, edited, version, opts, origLangs, origTXXXDesc)
		out = append(out, frames...)
		info.UsedV23Multi = info.UsedV23Multi || v23multi
		emitted[rid] = true
	}

	// Place new pictures where the originals were (or at the end if none existed).
	if picturesChanged {
		if firstAPIC < 0 {
			firstAPIC = len(out)
		}
		pics := make([]Frame, 0, len(pictures))
		for _, p := range pictures {
			pics = append(pics, Frame{ID: "APIC", Body: encodeAPIC(p, version)})
		}
		out = slices.Insert(out, firstAPIC, pics...)
	}

	info.DroppedDates = detectDroppedDates(changed, edited, version)
	info.ReducedDates = detectReducedDates(changed, edited, version)
	info.NumericGenres = detectNumericGenres(changed, edited)
	return out, info
}

// FrontTag is the rendered leading ID3v2 tag a front-tag-only codec (MP3, AAC) emits, plus
// the report fragments the emission contributes. Bytes/Tag are nil when the tag is dropped
// (no frame survives) - the caller writes no tag segment and hands its result builder a nil
// tag (so the output re-parses with no front tag, audioStart 0).
type FrontTag struct {
	Bytes      []byte         // rendered tag (header + frames + padding); nil to drop the tag
	Tag        *Tag           // the new tag for the result document; nil when dropped
	Padding    int64          // padding bytes written (0 when dropped)
	Operations []string       // report operation lines this emission adds, in order
	Warnings   []core.Warning // report warnings this emission adds
}

// RenderFrontTag sizes and renders the leading ID3v2 tag for a codec that stores tags only
// as a single front tag (MP3, AAC), centralizing the drop-empty-tag policy so the two cannot
// diverge. It emits the tag only when at least one frame survives: an edit (or a --legacy
// strip) that leaves no frames drops the tag entirely rather than fabricating an empty,
// padding-only container, matching WAV/AIFF's len(frames)>0 chunk guard. hadTag is whether the
// source carried a front tag (so a full clear records an "ID3v2 removal" op instead of a bare
// rewrite); srcTagLen is its byte length for in-place padding reuse; pictureCount is the
// edited picture count for the operation line.
func RenderFrontTag(srcTag *Tag, version byte, newFrames []Frame, info RebuildInfo, pad core.PaddingPolicy,
	srcTagLen int64, hadTag, tagsChanged, picturesChanged bool, pictureCount int) FrontTag {

	if len(newFrames) == 0 {
		var ft FrontTag
		if hadTag {
			// A full clear of a file that had a front tag: record the removal so a contentful
			// write (a clear-all) is not reported as a bare rewrite.
			ft.Operations = []string{"ID3v2 removal"}
		}
		return ft
	}
	// Size the tag and its padding. Reuse the original region in place when the new content
	// fits, so the audio offset (and file size) need not change.
	nonPad := RenderedSize(newFrames)
	padSize := pad.ReuseOrTarget(srcTagLen, nonPad)
	// Clamp at the sizing layer, not only inside Render: Report().PaddingAfter comes from
	// ft.Padding, so a hidden clamp would make the report overstate the written padding.
	// The practical trigger is a reused tag region larger than the ID3v2 size field.
	padSize, clamped := clampPadding(nonPad, padSize)
	ft := FrontTag{
		Bytes:   Render(version, newFrames, int(padSize)),
		Tag:     srcTag.WithFrames(newFrames),
		Padding: padSize,
	}
	if clamped {
		ft.Warnings = core.Warn(ft.Warnings, core.WarnPaddingClamped,
			fmt.Sprintf("requested ID3v2 padding exceeded the 28-bit size field (max %d bytes) and was clamped to it", maxFrameSize))
	}
	if tagsChanged {
		ft.Operations = append(ft.Operations, "ID3v2 frame rewrite")
	}
	if picturesChanged {
		ft.Operations = append(ft.Operations, fmt.Sprintf("pictures: %d", pictureCount))
	}
	if !hadTag {
		ft.Operations = append(ft.Operations, fmt.Sprintf("ID3v2.%d tag creation", version))
	}
	if info.UsedV23Multi {
		ft.Operations = append(ft.Operations, "v2.3 multi-value NUL-separated storage")
		ft.Warnings = core.Warn(ft.Warnings, core.WarnID3MultiValue,
			"a multi-value field was written NUL-separated in ID3v2.3, a de-facto extension some readers do not split")
	}
	return ft
}

// frameRenderID returns a frame's render token and whether the frame is managed
// (modelled by the canonical projection, hence re-rendered when its field
// changes). Unmanaged frames - URLs, POPM, PRIV, described comments, non-MB
// UFIDs, opaque frames - are always preserved verbatim.
func frameRenderID(f Frame) (string, bool) {
	if f.Opaque {
		return "", false
	}
	switch f.ID {
	case "APIC":
		return "", false
	case "TXXX":
		desc, _, ok := decodeUserText(f.Body)
		if !ok {
			return "", false
		}
		return "TXXX\x00" + strings.ToUpper(strings.TrimSpace(desc)), true
	case "UFID":
		owner, _, ok := decodeUFID(f.Body)
		if !ok || owner != musicBrainzOwner {
			return "", false
		}
		return "UFID", true
	case "COMM":
		// Only an empty-description COMM is managed as the canonical Comment; a described
		// COMM, such as iTunNORM or a ReplayGain note, is preserved verbatim. The flat
		// Comment model has no per-comment language, so editing Comment merges multiple
		// empty-description COMM frames in different languages into one frame. The texts
		// are still retained under Comment, and untouched Comment frames are preserved
		// verbatim. Preserving the language split would require a language-aware comment
		// model across codecs.
		desc, _, ok := decodeCommentFrame(f.Body)
		if !ok || desc != "" {
			return "", false
		}
		return "COMM", true
	case "USLT":
		desc, _, ok := decodeLangText(f.Body)
		if !ok || desc != "" {
			return "", false
		}
		return "USLT", true
	case "TCON", "TRCK", "TPOS":
		return f.ID, true
	}
	if isDateFrame(f.ID) {
		return f.ID, true
	}
	if strings.HasPrefix(f.ID, "T") {
		return f.ID, true
	}
	return "", false
}

// frameKeys returns the canonical keys a managed frame contributes to. The
// rebuilder uses it to drop a stale alternative representation of a key that
// changed - the same canonical value can sit under more than one frame across
// versions (TYER vs TDRC, TXXX:RELEASEDATE vs TDRL, TXXX:ISRC vs TSRC), and only
// the write-version's target should survive an edit.
func frameKeys(f Frame) []tag.Key {
	if f.Opaque {
		return nil
	}
	switch f.ID {
	case "APIC":
		return nil
	case "TXXX":
		desc, _, ok := decodeUserText(f.Body)
		if !ok {
			return nil
		}
		if k, ok := mapping.ID3TXXXKey(desc); ok {
			return []tag.Key{k}
		}
		return nil
	case "UFID":
		owner, _, ok := decodeUFID(f.Body)
		if !ok || owner != musicBrainzOwner {
			return nil
		}
		return []tag.Key{tag.MBRecordingID}
	case "COMM":
		desc, _, ok := decodeCommentFrame(f.Body)
		if !ok || desc != "" {
			return nil
		}
		return []tag.Key{tag.Comment}
	case "USLT":
		desc, _, ok := decodeLangText(f.Body)
		if !ok || desc != "" {
			return nil
		}
		return []tag.Key{tag.Lyrics}
	case "TCON":
		return []tag.Key{tag.Genre}
	case "TRCK":
		return []tag.Key{tag.TrackNumber, tag.TrackTotal}
	case "TPOS":
		return []tag.Key{tag.DiscNumber, tag.DiscTotal}
	case "TYER", "TDAT", "TIME", "TDRC":
		return []tag.Key{tag.RecordingDate}
	case "TDRL":
		return []tag.Key{tag.ReleaseDate}
	case "TDOR", "TORY":
		return []tag.Key{tag.OriginalDate}
	}
	if strings.HasPrefix(f.ID, "T") {
		if k, ok := mapping.ID3FrameKey(f.ID); ok {
			return []tag.Key{k}
		}
		if k, err := tag.ParseKey(strings.TrimSpace(f.ID)); err == nil {
			return []tag.Key{k}
		}
	}
	return nil
}

// touchesChangedKey reports whether any canonical key the frame contributes to
// is in the changed set.
func touchesChangedKey(f Frame, changed map[tag.Key]bool) bool {
	for _, k := range frameKeys(f) {
		if changed[k] {
			return true
		}
	}
	return false
}

// keyRenderIDs returns the render tokens a change to key dirties under the write
// version.
func keyRenderIDs(key tag.Key, version byte) []string {
	switch key {
	case tag.TrackNumber, tag.TrackTotal:
		return []string{"TRCK"}
	case tag.DiscNumber, tag.DiscTotal:
		return []string{"TPOS"}
	case tag.Genre:
		return []string{"TCON"}
	case tag.MBRecordingID:
		return []string{"UFID"}
	case tag.Comment:
		return []string{"COMM"}
	case tag.Lyrics:
		return []string{"USLT"}
	case tag.RecordingDate:
		if version >= 4 {
			return []string{"TDRC"}
		}
		return []string{"TYER", "TDAT", "TIME"}
	case tag.ReleaseDate:
		if version >= 4 {
			return []string{"TDRL"}
		}
		return []string{"TXXX\x00RELEASEDATE"}
	case tag.OriginalDate:
		if version >= 4 {
			return []string{"TDOR"}
		}
		return []string{"TORY"}
	}
	if id, ok := mapping.ID3KeyFrame(key); ok {
		return []string{id}
	}
	if rawFrameIDKey(key) {
		return []string{string(key)}
	}
	return []string{"TXXX\x00" + strings.ToUpper(mapping.ID3TXXXDesc(key))}
}

// rawFrameIDKey reports whether a canonical key is itself a plain ID3 text-frame
// identifier (four characters beginning with T), so an otherwise-unmapped text
// frame round-trips under its own identifier instead of via TXXX.
func rawFrameIDKey(key tag.Key) bool {
	s := string(key)
	if len(s) != 4 || s[0] != 'T' {
		return false
	}
	for i := 0; i < 4; i++ {
		if !(s[i] >= 'A' && s[i] <= 'Z' || s[i] >= '0' && s[i] <= '9') {
			return false
		}
	}
	return true
}

// renderUnit renders the frame(s) for a render token from the edited tag set,
// returning an empty slice when the underlying field is now absent (the frame is
// dropped). It also reports whether a v2.3 NUL-separated multi-value was emitted.
func renderUnit(token string, edited tag.TagSet, version byte, opts WriteOpts, origLangs, origTXXXDesc map[string]string) ([]Frame, bool) {
	switch {
	case strings.HasPrefix(token, "TXXX\x00"):
		key := txxxKeyForToken(token[len("TXXX\x00"):])
		vals, ok := edited.Get(key)
		if !ok || len(vals) == 0 {
			return nil, false
		}
		// Use the preferred Picard spelling for an aliased key. For custom keys, reuse
		// the original TXXX description casing when available, matching the Vorbis rebuild.
		desc := mapping.ID3TXXXDesc(key)
		if orig, ok := origTXXXDesc[token]; ok && desc == string(key) {
			desc = orig
		}
		return []Frame{{ID: "TXXX", Body: encodeUserText(version, desc, vals)}}, false
	case token == "UFID":
		id, ok := edited.First(tag.MBRecordingID)
		if !ok || id == "" {
			return nil, false
		}
		return []Frame{{ID: "UFID", Body: encodeUFID(musicBrainzOwner, id)}}, false
	case token == "COMM":
		vals, ok := edited.Get(tag.Comment)
		if !ok || len(vals) == 0 {
			return nil, false
		}
		return []Frame{{ID: "COMM", Body: encodeComment(version, unitLang(origLangs, "COMM"), "", vals)}}, false
	case token == "USLT":
		text, ok := edited.First(tag.Lyrics)
		if !ok {
			return nil, false
		}
		return []Frame{{ID: "USLT", Body: encodeLangText(version, unitLang(origLangs, "USLT"), "", text)}}, false
	case token == "TCON":
		vals, ok := edited.Get(tag.Genre)
		if !ok || len(vals) == 0 {
			return nil, false
		}
		return renderText(version, "TCON", genreValues(vals, version, opts.NumericGenre), opts.Multi)
	case token == "TRCK":
		return renderNumTotal(version, "TRCK", edited, tag.TrackNumber, tag.TrackTotal)
	case token == "TPOS":
		return renderNumTotal(version, "TPOS", edited, tag.DiscNumber, tag.DiscTotal)
	case token == "TDRC":
		return renderDate(version, "TDRC", edited, tag.RecordingDate)
	case token == "TDRL":
		return renderDate(version, "TDRL", edited, tag.ReleaseDate)
	case token == "TDOR":
		return renderDate(version, "TDOR", edited, tag.OriginalDate)
	case token == "TYER":
		return renderDatePart(version, "TYER", edited, tag.RecordingDate, partYear)
	case token == "TDAT":
		return renderDatePart(version, "TDAT", edited, tag.RecordingDate, partDayMonth)
	case token == "TIME":
		return renderDatePart(version, "TIME", edited, tag.RecordingDate, partHourMin)
	case token == "TORY":
		return renderDatePart(version, "TORY", edited, tag.OriginalDate, partYear)
	default: // simple or pass-through text frame
		key, ok := mapping.ID3FrameKey(token)
		if !ok {
			key = tag.Key(token)
		}
		vals, has := edited.Get(key)
		if !has || len(vals) == 0 {
			return nil, false
		}
		return renderText(version, token, vals, opts.Multi)
	}
}

// unitLang returns the 3-byte language for a managed COMM/USLT frame: the
// original frame's language recovered in RebuildFrames, or "eng" for a newly
// added comment or lyric with no original frame. A garbage 3-byte language
// round-trips verbatim because langBytes neither normalizes nor rejects it.
func unitLang(origLangs map[string]string, token string) string {
	if l, ok := origLangs[token]; ok {
		return l
	}
	return "eng"
}

// txxxKeyForToken resolves a TXXX render token (an uppercased description) back
// to its canonical key.
func txxxKeyForToken(upperDesc string) tag.Key {
	if k, ok := mapping.ID3TXXXKey(upperDesc); ok {
		return k
	}
	return tag.Key(upperDesc)
}

// renderText renders a text frame's value(s), applying the multi-value policy in
// v2.3. v2.4 always NUL-separates.
func renderText(version byte, id string, values []string, pol core.ID3MultiValuePolicy) ([]Frame, bool) {
	if len(values) <= 1 || version >= 4 {
		enc := chooseEncoding(version, values)
		return []Frame{{ID: id, Body: encodeTextFrame(enc, values)}}, false
	}
	switch pol {
	case core.ID3MultiRepeatFrame:
		var frames []Frame
		for _, v := range values {
			enc := chooseEncoding(version, []string{v})
			frames = append(frames, Frame{ID: id, Body: encodeTextFrame(enc, []string{v})})
		}
		return frames, false
	case core.ID3MultiSlash:
		joined := strings.Join(values, " / ")
		enc := chooseEncoding(version, []string{joined})
		return []Frame{{ID: id, Body: encodeTextFrame(enc, []string{joined})}}, false
	default: // ID3MultiNullSep - a v2.3 extension
		enc := chooseEncoding(version, values)
		return []Frame{{ID: id, Body: encodeTextFrame(enc, values)}}, true
	}
}

// renderNumTotal renders a TRCK/TPOS frame from a number key and an optional
// total key as "n/total".
func renderNumTotal(version byte, id string, edited tag.TagSet, numKey, totKey tag.Key) ([]Frame, bool) {
	num, _ := edited.First(numKey)
	total, _ := edited.First(totKey)
	if num == "" && total == "" {
		return nil, false
	}
	// Peel any embedded "n/total" out of the number so TRACKNUMBER="5/12" plus an
	// explicit TRACKTOTAL never composes "5/12/20" (which re-reads as TRACKTOTAL="12/20").
	// An explicit total wins; otherwise the embedded one is used. SplitNumberTotal keeps
	// the exact digit strings, including leading zeros, unlike tag.ParseNumPair.
	nPart, embeddedTotal := tag.SplitNumberTotal(num)
	value := nPart
	finalTotal := total
	if finalTotal == "" {
		finalTotal = embeddedTotal
	}
	if finalTotal != "" {
		value = nPart + "/" + finalTotal
	}
	enc := chooseEncoding(version, []string{value})
	return []Frame{{ID: id, Body: encodeTextFrame(enc, []string{value})}}, false
}

// renderDate renders a v2.4 date frame directly from an ISO date key.
func renderDate(version byte, id string, edited tag.TagSet, key tag.Key) ([]Frame, bool) {
	v, ok := edited.First(key)
	if !ok || v == "" {
		return nil, false
	}
	enc := chooseEncoding(version, []string{v})
	return []Frame{{ID: id, Body: encodeTextFrame(enc, []string{v})}}, false
}

type datePart uint8

const (
	partYear datePart = iota
	partDayMonth
	partHourMin
)

// detectNumericGenres returns the GENRE values this edit set that are a bare integer
// naming a standard genre by index (e.g. "17" -> "Rock"). Written verbatim to TCON, such a
// value is resolved back to the genre NAME by the read path, so GENRE=17 round-trips as
// "Rock" - a surprising change the caller surfaces (see AppendRebuildWarnings), suppressed
// where a native container keeps the literal number. Only a value the edit actually changed
// is reported, so an untouched pre-existing numeric genre does not warn.
func detectNumericGenres(changed map[tag.Key]bool, edited tag.TagSet) []string {
	if !changed[tag.Genre] {
		return nil
	}
	vals, _ := edited.Get(tag.Genre)
	var out []string
	seen := map[string]bool{}
	for _, v := range vals {
		// De-duplicate by value so a repeated reference (GENRE=17,17) warns once, not once
		// per occurrence - and so the same single warning is what DowngradeNoOp carries onto
		// a no-op report.
		if isNumericGenreRef(v) && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// isNumericGenreRef reports whether v carries a numeric or special genre reference the
// read path resolves to a genre name - a bare number ("17"), a parenthesised reference
// ("(17)", "(51)(39)"), a special code ("(RX)", "(CR)"), or a reference with a refinement
// ("(17)Hard"). It delegates to resolveGenres, the SAME resolver the parser uses, so the
// write-time warning cannot drift from what actually round-trips: a value that resolves to a
// name reads back as that name, a non-verbatim round-trip on the pure-ID3 formats. An
// out-of-range or non-resolving value (kept verbatim) reports false here, and the retained-
// value suppression in AppendRebuildWarnings catches any remaining no-change case.
func isNumericGenreRef(v string) bool {
	_, numeric := resolveGenres(v)
	return numeric
}

// detectDroppedDates finds the year-anchored date keys whose edited value cannot be
// represented at all on a v2.3 tag, so the caller can warn rather than drop silently.
// TYER (RecordingDate) and TORY (OriginalDate) need a numeric year: a touched value
// with none (e.g. "Unknown Date") renders no frame, a silent loss. ReleaseDate is
// excluded - on v2.3 it maps to TXXX:RELEASEDATE, which stores the string verbatim,
// so it never drops. v2.4 stores the full string (TDRC/TDOR) and returns nothing here.
//
// The check is per key, not per render token: a single RecordingDate dirties three
// tokens (TYER/TDAT/TIME), and a common "2021"/"2021-05" legitimately renders only
// TYER while TDAT/TIME yield nothing - so a per-token "no frame" test would falsely
// flag a perfectly stored date. A date is dropped iff the key has a non-empty value
// but no extractable year, which also preserves a shaped-but-invalid "2021-13-45"
// (its year extracts, so only the day/month is lost, not the whole value).
func detectDroppedDates(changed map[tag.Key]bool, edited tag.TagSet, version byte) []tag.Key {
	if version >= 4 {
		return nil
	}
	var dropped []tag.Key
	for _, k := range []tag.Key{tag.RecordingDate, tag.OriginalDate} {
		if !changed[k] {
			continue
		}
		if v, _ := edited.First(k); v != "" && extractDatePart(v, partYear) == "" {
			dropped = append(dropped, k)
		}
	}
	return dropped
}

// detectReducedDates finds RecordingDate edits whose finer precision a v2.3 tag drops:
// a month with no full date, or an hour with no minute. OriginalDate is intentionally
// excluded because the capability path reports its v2.3 TORY reduction and uses different
// cross-container suppression. Listing it here too would double-warn.
func detectReducedDates(changed map[tag.Key]bool, edited tag.TagSet, version byte) []ReducedDate {
	if version >= 4 {
		return nil
	}
	var reduced []ReducedDate
	k := tag.RecordingDate
	if changed[k] {
		if v, _ := edited.First(k); reducesDatePrecision(v) {
			reduced = append(reduced, ReducedDate{Key: k, Value: v})
		}
	}
	return reduced
}

// reducesDatePrecision reports whether a v2.3 tag would store iso with less precision
// than it carries. v2.3 splits a date-time across TYER (year), TDAT (DDMM, needs a full
// YYYY-MM-DD) and TIME (HHMM, needs YYYY-MM-DDTHH:MM), each all-or-nothing. A value
// carrying a component the rendered frames cannot capture loses it:
//   - a month with no full date ("2021-03", non-canonical "2021-3"/"2021-03-1") -> only TYER,
//     month/day dropped;
//   - an hour with no minute ("2021-03-15T10") -> TYER+TDAT only, the hour dropped;
//   - seconds past a full minute ("2021-03-15T10:30:45") -> TIME stores only HHMM, the
//     seconds dropped.
//
// A bare year, a full date, or a date-time to the minute render losslessly and are excluded.
// The tool stores values verbatim (no normalization), so the non-canonical forms are reachable
// too. A value with no extractable year drops entirely and is handled by detectDroppedDates
// instead.
func reducesDatePrecision(iso string) bool {
	if extractDatePart(iso, partYear) == "" {
		return false
	}
	monthLost := hasSubYearPart(iso) && extractDatePart(iso, partDayMonth) == ""
	hourLost := hasSubDayPart(iso) && extractDatePart(iso, partHourMin) == ""
	// HHMM is stored (a full minute renders) yet the value carries seconds: v2.3 TIME has
	// no seconds field, so they are dropped.
	secondsLost := hasSubMinutePart(iso) && extractDatePart(iso, partHourMin) != ""
	return monthLost || hourLost || secondsLost
}

// hasSubYearPart reports whether iso carries a month-or-finer component after its 4-digit
// year: a '-' then at least one digit (so "2021-3", "2021-03", "2021-03-15" do, but a bare
// "2021" does not). It separates a reducible partial date from a lossless year-only value.
func hasSubYearPart(iso string) bool {
	return len(iso) >= 6 && iso[4] == '-' && iso[5] >= '0' && iso[5] <= '9'
}

// hasSubDayPart reports whether iso carries an hour-or-finer component after a full date: a
// 'T' (or space) date-time separator then at least one digit past the 10-char YYYY-MM-DD (so
// "2021-03-15T10" does, but a bare date does not). It separates a reducible partial
// date-time from a lossless full date.
func hasSubDayPart(iso string) bool {
	return len(iso) >= 12 && (iso[10] == 'T' || iso[10] == ' ') && iso[11] >= '0' && iso[11] <= '9'
}

// hasSubMinutePart reports whether iso carries a seconds-or-finer component after a full
// minute: the ':ss' at the YYYY-MM-DDThh:mm boundary - a ':' at index 16 then a digit at 17
// (so "2021-03-15T10:30:45" does, but a minute-precision "2021-03-15T10:30", or one with only
// a trailing zone like "2021-03-15T10:30+05:00", does not). It separates a value v2.3's HHMM
// TIME stores losslessly from one whose seconds it drops. The trailing-digit check mirrors
// hasSubYearPart/hasSubDayPart and avoids flagging a malformed trailing-colon value.
func hasSubMinutePart(iso string) bool {
	return len(iso) >= 18 && iso[16] == ':' && iso[17] >= '0' && iso[17] <= '9'
}

// AppendRebuildWarnings appends warnings for losses found while rebuilding ID3 frames:
// dates that render no v2.3 frame at all, dates whose v2.3 TDAT/TIME rendering drops
// precision, and malformed pictures dropped during a picture edit. Date warnings are
// suppressed when the re-projected output still retains the attempted value in another
// container, such as WAV's LIST/INFO ICRD. The keyed warnings let --strict name the field
// and keep the four ID3-backed codecs' wording and suppression rules aligned.
func AppendRebuildWarnings(ws []core.Warning, info RebuildInfo, retained tag.TagSet) []core.Warning {
	for _, k := range info.DroppedDates {
		if v, _ := retained.First(k); v != "" {
			continue // retained in another container (e.g. WAV's ICRD); not actually dropped
		}
		ws = core.WarnKeyed(ws, core.WarnValueDropped,
			fmt.Sprintf("%s value cannot be represented in ID3v2.3 (it has no numeric year) and was dropped", k), k)
	}
	for _, rd := range info.ReducedDates {
		// Suppress only when another container still carries the attempted precision:
		// a retained "2021" must not suppress an attempted "2021-03".
		if v, _ := retained.First(rd.Key); v == rd.Value {
			continue
		}
		ws = core.WarnKeyed(ws, core.WarnValueReduced,
			fmt.Sprintf("%s value %q carries finer precision than ID3v2.3 date frames can store (TDAT needs a full day, TIME a full minute) and was reduced", rd.Key, rd.Value), rd.Key)
	}
	for _, gv := range info.NumericGenres {
		// Suppress only where a native container still carries the literal number: on the
		// pure-ID3 formats (MP3/AAC) the retained GENRE reads back as the genre name, so gv is
		// absent and the warning fires; on WAV/AIFF the INFO/text slot keeps "17" verbatim, so
		// it is retained and the round-trip did not change - no warning. This mirrors the
		// date-warning suppression and keeps the write-time note aligned with the read-time one.
		if genres, _ := retained.Get(tag.Genre); slices.Contains(genres, gv) {
			continue
		}
		ws = core.WarnKeyed(ws, core.WarnNumericGenre,
			fmt.Sprintf("GENRE %q is a numeric reference that reads back as its genre name on ID3-based formats", gv), tag.Genre)
	}
	if info.HasDroppedMalformedPicture {
		ws = core.Warn(ws, core.WarnInvalidPicture,
			"a malformed embedded picture could not be decoded and was dropped during a picture edit")
	}
	return ws
}

// PerFieldCapabilities builds the per-key capability overrides shared by every
// ID3-backed codec (MP3/AAC/AIFF/WAV). The two value-mutating ID3 cases are declared
// once here rather than copied into each codec:
//
//   - ORIGINALDATE is AccessPartial when the codec writes ID3v2.3 (writeVersion == 3),
//     whose TORY frame keeps only the year. This drives both the transfer grade and the
//     value-reduced edit warning (editor.appendValueReducedWarnings), the latter confirming
//     the value actually changed before warning.
//   - GENRE is AccessPartial when --numeric-genre is set and the ID3 tag is the authoritative
//     genre store for this codec (genreViaID3). That holds always for MP3/AAC/AIFF (no native
//     genre slot wins over ID3), but for WAV only when an id3 chunk is present - a bare WAV's
//     native LIST/INFO IGNR keeps the genre as text, losslessly.
//
// Returns nil when neither applies, so a codec with a lossless write passes no overrides.
func PerFieldCapabilities(writeVersion byte, numericGenre, genreViaID3 bool) map[tag.Key]core.Capability {
	var perField map[tag.Key]core.Capability
	add := func(k tag.Key, c core.Capability) {
		if perField == nil {
			perField = map[tag.Key]core.Capability{}
		}
		perField[k] = c
	}
	if writeVersion == 3 {
		add(tag.OriginalDate, core.OriginalDateV23Capability())
	}
	if numericGenre && genreViaID3 {
		add(tag.Genre, core.NumericGenreCapability("numeric ID3 TCON reference"))
	}
	return perField
}

// renderDatePart renders a v2.3 date component (TYER/TDAT/TIME/TORY) extracted
// from an ISO date key.
func renderDatePart(version byte, id string, edited tag.TagSet, key tag.Key, part datePart) ([]Frame, bool) {
	iso, ok := edited.First(key)
	if !ok {
		return nil, false
	}
	v := extractDatePart(iso, part)
	if v == "" {
		return nil, false
	}
	enc := chooseEncoding(version, []string{v})
	return []Frame{{ID: id, Body: encodeTextFrame(enc, []string{v})}}, false
}

// extractDatePart pulls a component out of an ISO-8601 date "YYYY[-MM-DD[THH:MM]]".
func extractDatePart(iso string, part datePart) string {
	switch part {
	case partYear:
		if len(iso) >= 4 && allDigits(iso[:4]) {
			return iso[:4]
		}
	case partDayMonth:
		if len(iso) >= 10 && iso[4] == '-' && iso[7] == '-' {
			return iso[8:10] + iso[5:7] // DDMM
		}
	case partHourMin:
		// Accept either ISO date-time separator: 'T' (the canonical form) or a space (a
		// common variant). hasSubDayPart accepts both when deciding a value carries a time,
		// so this must too - else "2021-03-15 10:30" would be judged reducible yet yield no
		// TIME frame, spuriously firing [value-reduced] while the 'T' form keeps the time.
		if len(iso) >= 16 && (iso[10] == 'T' || iso[10] == ' ') && iso[13] == ':' {
			return iso[11:13] + iso[14:16] // HHMM
		}
	}
	return ""
}

// genreValues converts genre names to numeric references when WithNumericGenre
// is set and the name is a standard genre; otherwise the names pass through.
func genreValues(names []string, version byte, numeric bool) []string {
	if !numeric {
		return names
	}
	out := make([]string, len(names))
	for i, n := range names {
		if idx := genreIndex(n); idx >= 0 {
			if version >= 4 {
				out[i] = strconv.Itoa(idx)
			} else {
				out[i] = "(" + strconv.Itoa(idx) + ")"
			}
			continue
		}
		out[i] = n
	}
	return out
}

// encodeUserText renders a TXXX body: encoding, description, then the value(s).
func encodeUserText(version byte, desc string, values []string) []byte {
	enc := chooseEncoding(version, append([]string{desc}, values...))
	return encodeDescValues(enc, "", desc, values)
}

// encodeUFID renders a UFID body: the owner identifier, a NUL, then the raw id.
func encodeUFID(owner, id string) []byte {
	out := append(encodeLatin1(owner), 0)
	return append(out, []byte(id)...)
}

// encodeComment renders a COMM body: encoding, language, description, value(s).
func encodeComment(version byte, lang, desc string, values []string) []byte {
	enc := chooseEncoding(version, append([]string{desc}, values...))
	return encodeDescValues(enc, lang, desc, values)
}

// encodeLangText renders a USLT body: encoding, language, descriptor, text.
func encodeLangText(version byte, lang, desc, text string) []byte {
	enc := chooseEncoding(version, []string{desc, text})
	return encodeDescValues(enc, lang, desc, []string{text})
}

// encodeDescValues builds the common frame body shared by TXXX, COMM, and USLT:
// the encoding byte, an optional 3-byte language code, a terminated description,
// then the values separated by the encoding's terminator.
func encodeDescValues(enc byte, lang, desc string, values []string) []byte {
	out := []byte{enc}
	if lang != "" {
		out = append(out, langBytes(lang)...)
	}
	out = append(out, encodeString(enc, desc)...)
	out = append(out, term(enc)...)
	return appendValues(out, enc, values)
}

// appendValues appends values to out, separated by the encoding's terminator
// (no trailing terminator). Shared by the description frames and the plain
// text-frame encoder.
func appendValues(out []byte, enc byte, values []string) []byte {
	t := term(enc)
	for i, v := range values {
		if i > 0 {
			out = append(out, t...)
		}
		out = append(out, encodeString(enc, v)...)
	}
	return out
}

// langBytes returns a 3-byte language code, padding or truncating to fit.
func langBytes(lang string) []byte {
	b := []byte(lang)
	for len(b) < 3 {
		b = append(b, 'X')
	}
	return b[:3]
}

// diffKeys returns the canonical keys whose values differ between base and
// edited.
func diffKeys(base, edited tag.TagSet) map[tag.Key]bool {
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
