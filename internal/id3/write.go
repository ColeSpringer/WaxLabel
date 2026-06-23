package id3

import (
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

	// The read path discards the COMM/USLT 3-byte language, so recover it from the
	// original frames at write time: a re-rendered comment/lyric keeps its language
	// (e.g. "deu") instead of being reset to "eng". frameRenderID marks a COMM/USLT
	// frame managed only when its description is empty, so there is at most one managed
	// COMM and one managed USLT to reuse.
	origLangs := map[string]string{} // "COMM"/"USLT" -> 3-byte language
	for _, f := range orig {
		if f.ID == "COMM" || f.ID == "USLT" {
			if rid, managed := frameRenderID(f); managed && len(f.Body) >= 4 {
				origLangs[rid] = string(f.Body[1:4])
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
				frames, v23multi := renderUnit(rid, edited, version, opts, origLangs)
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
		frames, v23multi := renderUnit(rid, edited, version, opts, origLangs)
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

	return out, info
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
func renderUnit(token string, edited tag.TagSet, version byte, opts WriteOpts, origLangs map[string]string) ([]Frame, bool) {
	switch {
	case strings.HasPrefix(token, "TXXX\x00"):
		key := txxxKeyForToken(token[len("TXXX\x00"):])
		vals, ok := edited.Get(key)
		if !ok || len(vals) == 0 {
			return nil, false
		}
		desc := mapping.ID3TXXXDesc(key)
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
		if len(iso) >= 16 && iso[10] == 'T' && iso[13] == ':' {
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
