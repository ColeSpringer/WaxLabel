package id3

import (
	"cmp"
	"encoding/binary"
	"slices"
	"strings"

	"github.com/colespringer/waxlabel/internal/core"
)

// ID3v2 synchronized lyrics live in the SYLT frame (an upgraded v2.2 SLT becomes SYLT via
// the v2.2 frame-ID table, so this decoder reads both). The frame body is:
//
//	encoding(1) language(3) timestamp-format(1) content-type(1) descriptor(term) [text(term) timestamp(4)]*
//
// WaxLabel models only millisecond timestamps (format 2) and the lyrics content type (1):
// the MPEG-frames format needs the full frame index to map to a time, and a
// non-lyric content type (chord, trivia, image URL) is not lyrics. A SYLT that uses
// either is skipped with a warning and preserved verbatim through an unrelated edit.
//
// The implementation follows the ID3v2 SYLT frame layout; reference implementations were
// used only to check behavior.

const (
	// syltFmtMillis is the SYLT timestamp format for absolute milliseconds (format 2);
	// format 1 (MPEG frames) is not modeled.
	syltFmtMillis byte = 2
	// syltContentLyrics is the SYLT content type for lyrics (1); other content types are
	// not projected as lyrics.
	syltContentLyrics byte = 1
	// syltTimeMax is the SYLT timestamp ceiling. Unlike CHAP, SYLT has no
	// reserved "field unused" sentinel, so the full uint32 range is valid.
	syltTimeMax uint32 = 0xFFFFFFFF
)

// maxSyltLines caps how many timed lines one SYLT frame decodes, a defense-in-depth bound
// against hostile input. The cap is far past any real song's line count.
const maxSyltLines = 1 << 16

// SyncedLyricsCapability is the synced-lyrics capability shared by every ID3-backed codec
// (MP3/AAC/AIFF/WAV). SYLT stores the language, descriptor, per-line millisecond
// timestamps, and text losslessly, and several SYLT frames may coexist, so there is no
// item cap and no loss. The shared helper keeps the four codecs identical by construction,
// mirroring the per-codec chapter capability.
func SyncedLyricsCapability() core.Capability {
	return core.Capability{
		Read:           core.AccessFull,
		Write:          core.AccessFull,
		Representation: "ID3v2 SYLT frame",
		Fidelity:       "lossless",
		Constraints:    []string{"synced-lyric timestamps limited to a 32-bit millisecond field (~49.7 days)"},
		// A line past the 32-bit ms field is clamped on write (see encodeSYLT); expose the
		// ceiling so a transfer grades a clamping copy Lossy rather than a clean carry.
		SyncedLyricsTimeMax: msToDuration(syltTimeMax),
	}
}

// ProjectSyncedLyrics decodes a tag's SYLT frames into synced-lyrics sets plus read
// warnings for unsupported timestamp formats or content types. Each lyrics SYLT becomes
// one set. A non-lyric or MPEG-frames-timestamped SYLT is skipped with a warning. Returns
// nil when the tag carries no projecting SYLT.
func ProjectSyncedLyrics(t *Tag) ([]core.SyncedLyrics, []core.Warning) {
	if t == nil {
		return nil, nil
	}
	var sets []core.SyncedLyrics
	var ws []core.Warning
	for _, f := range t.frames {
		// A compressed or encrypted frame body is uninterpretable here; it is preserved
		// opaque through the rebuild path but not projected.
		if f.Opaque || f.ID != "SYLT" {
			continue
		}
		sl, warns, ok := decodeSYLT(f.Body)
		ws = append(ws, warns...)
		if ok {
			sets = append(sets, sl)
		}
	}
	return sets, ws
}

// decodeSYLT decodes a SYLT frame body into one synced-lyrics set. It returns ok == false
// (with a warning) for a frame it cannot model: a bad header, an MPEG-frames timestamp
// format, a non-lyric content type, or a body with no decodable line. The per-line text
// has its conventional leading line-break marker stripped so the modeled text is the clean
// line content.
func decodeSYLT(body []byte) (core.SyncedLyrics, []core.Warning, bool) {
	// encoding(1) + language(3) + tsfmt(1) + content(1) = 6 header bytes before the
	// terminated descriptor.
	if len(body) < 6 {
		return core.SyncedLyrics{}, nil, false
	}
	enc := body[0]
	if !validEncoding(enc) {
		return core.SyncedLyrics{}, nil, false
	}
	lang := syltLanguage(string(body[1:4]))
	tsFmt := body[4]
	content := body[5]
	// Share byte-order state across the descriptor and every line. SYLT stores all strings
	// under one encoding byte, and some files put a UTF-16 BOM only on the first string.
	order := &utf16Order{}
	desc, rest, ok := cutEncodedTracked(enc, body[6:], order)
	if !ok {
		return core.SyncedLyrics{}, nil, false
	}
	if tsFmt != syltFmtMillis {
		return core.SyncedLyrics{}, []core.Warning{{Code: core.WarnSyncedLyricsTimestampFormat,
			Message: "a SYLT frame uses a non-millisecond timestamp format, which is not modeled; it was skipped"}}, false
	}
	if content != syltContentLyrics {
		return core.SyncedLyrics{}, []core.Warning{{Code: core.WarnSyncedLyricsContentType,
			Message: "a SYLT frame carries a non-lyric content type, which is not modeled; it was skipped"}}, false
	}
	var lines []core.SyncedLine
	for len(rest) > 0 && len(lines) < maxSyltLines {
		text, after, tok := cutEncodedTracked(enc, rest, order)
		if !tok || len(after) < 4 {
			break // truncated entry: keep what parsed
		}
		ms := binary.BigEndian.Uint32(after[0:4])
		rest = after[4:]
		lines = append(lines, core.SyncedLine{
			Time: msToDuration(ms),
			Text: stripLeadingLineBreak(core.SanitizeUTF8(text)),
		})
	}
	if len(lines) == 0 {
		return core.SyncedLyrics{}, nil, false
	}
	// Project in chronological order, matching the model contract and the LRC store's
	// ParseLRC. That keeps the public view consistent even when a SYLT frame lists entries
	// out of order. encodeSYLT preserves slice order, so re-rendering a sorted model set is
	// byte-stable.
	slices.SortStableFunc(lines, func(a, b core.SyncedLine) int { return cmp.Compare(a.Time, b.Time) })
	return core.SyncedLyrics{Language: lang, Description: core.SanitizeUTF8(desc), Lines: lines}, nil, true
}

// syltProjectsLyrics reports whether a SYLT frame projects into the synced-lyrics model (a
// lyrics, millisecond-timestamped frame with at least one line). The rebuild path uses it
// to drop only the SYLT frames a synced-lyrics edit replaces, preserving a non-projecting
// SYLT (a chord or trivia track) verbatim. It checks the header and that one line entry
// decodes, matching decodeSYLT's ok result, without decoding, sanitizing, and sorting every
// line only to compute the boolean.
func syltProjectsLyrics(body []byte) bool {
	if len(body) < 6 || !validEncoding(body[0]) || body[4] != syltFmtMillis || body[5] != syltContentLyrics {
		return false
	}
	enc := body[0]
	_, rest, ok := cutEncoded(enc, body[6:]) // the content descriptor
	if !ok {
		return false
	}
	// At least one line entry (text terminator + 4-byte timestamp) must decode, matching the
	// len(lines) > 0 condition in decodeSYLT.
	_, after, lineOK := cutEncoded(enc, rest)
	return lineOK && len(after) >= 4
}

// syltFrameLanguage returns the raw 3-byte language of a SYLT frame body, used as the
// empty-language fallback when re-rendering an edited set whose language is unset.
func syltFrameLanguage(body []byte) (string, bool) {
	if len(body) < 4 {
		return "", false
	}
	return string(body[1:4]), true
}

// syltFrameDescriptor returns the content descriptor of a SYLT frame body, decoded per its encoding
// byte (Latin-1/UTF-16/UTF-8) exactly as decodeSYLT and syltProjectsLyrics read it. It is the
// empty-descriptor fallback when re-rendering an edited set whose modeled descriptor is unset, so a
// line-only edit keeps the file's existing descriptor rather than blanking it. Plain cutEncoded (not
// the tracked variant) is correct: the descriptor is the first string, so the shared UTF-16-BOM
// state the tracked variant threads across later lines does not matter here. The descriptor is not
// fixed-width, so it is decoded through cutEncoded rather than sliced by hand.
func syltFrameDescriptor(body []byte) (string, bool) {
	if len(body) < 6 || !validEncoding(body[0]) {
		return "", false
	}
	desc, _, ok := cutEncoded(body[0], body[6:])
	if !ok {
		return "", false
	}
	return core.SanitizeUTF8(desc), true
}

// syltFrames renders synced-lyrics sets as SYLT frames (one per set, in order). fallbackLang and
// fallbackDesc are the raw 3-byte language and content descriptor of the first original projecting
// SYLT, used for a set whose modeled language or descriptor is empty so a line-only edit keeps the
// file's existing values (a CLI-authored set carries neither). A set with no lines emits no frame: a
// line-less SYLT projects to nothing on re-read, so writing one would create a frame with no model
// value. This matches the Vorbis LRC store's syncedLyricsComments. It reports whether any line's
// timestamp was clamped to the 32-bit millisecond field.
func syltFrames(sets []core.SyncedLyrics, version byte, fallbackLang, fallbackDesc string) (frames []Frame, overflow, invalidNUL bool) {
	frames = make([]Frame, 0, len(sets))
	for _, sl := range sets {
		if len(sl.Lines) == 0 {
			continue
		}
		// Defense-in-depth: a NUL in the modeled line text or authored descriptor would silently
		// truncate the NUL-terminated SYLT text/descriptor field. The editor already rejects an
		// authored NUL; flag one here too so a library caller that bypasses the editor surfaces
		// waxerr.ErrInvalidData (via RebuildError) rather than writing a truncated frame. Only these
		// values need checking: fallbackDesc/fallbackLang come from decoded SYLT strings, which
		// cannot contain a NUL (it is the field terminator on read).
		if strings.IndexByte(sl.Description, 0) >= 0 {
			invalidNUL = true
		}
		for _, ln := range sl.Lines {
			if strings.IndexByte(ln.Text, 0) >= 0 {
				invalidNUL = true
			}
		}
		body, ov := encodeSYLT(sl, version, fallbackLang, fallbackDesc)
		overflow = overflow || ov
		frames = append(frames, Frame{ID: "SYLT", Body: body})
	}
	return frames, overflow, invalidNUL
}

// encodeSYLT renders a SYLT frame body for the write version: always the millisecond
// timestamp format and the lyrics content type. Each line's text is prefixed with the
// conventional line-break marker (a newline), which decodeSYLT strips on read, so the
// modeled text round-trips. The encoding is chosen across the descriptor and every line so
// a non-Latin-1 lyric upgrades the whole frame consistently. An empty modeled descriptor
// falls back to fallbackDesc (the first original SYLT's descriptor) so an authored line-only
// edit keeps it, mirroring the language fallback; the chosen descriptor is used for both the
// encoding decision and the written bytes so the two cannot disagree. It reports whether any
// line's timestamp was clamped to the 32-bit millisecond field (~49.7 days).
func encodeSYLT(sl core.SyncedLyrics, version byte, fallbackLang, fallbackDesc string) (body []byte, overflow bool) {
	desc := sl.Description
	if desc == "" {
		desc = fallbackDesc // keep the file's existing descriptor on a line-only edit
	}
	values := make([]string, 0, len(sl.Lines)+1)
	values = append(values, desc)
	for _, ln := range sl.Lines {
		values = append(values, "\n"+ln.Text)
	}
	enc := chooseEncoding(version, values)

	lang := sl.Language
	if lang == "" {
		lang = fallbackLang // keep the file's existing language on a line-only edit
	}
	out := []byte{enc}
	out = append(out, syltLangBytes(lang)...)
	out = append(out, syltFmtMillis, syltContentLyrics)
	out = append(out, encodeString(enc, desc)...)
	out = append(out, term(enc)...)
	for _, ln := range sl.Lines {
		out = append(out, encodeString(enc, "\n"+ln.Text)...)
		out = append(out, term(enc)...)
		var ts [4]byte
		ms, ov := durationToMs(ln.Time, syltTimeMax) // clamps only a line past the full 32-bit ms field
		overflow = overflow || ov
		binary.BigEndian.PutUint32(ts[:], ms)
		out = append(out, ts[:]...)
	}
	return out, overflow
}

// syltLangBytes renders a modeled language into the SYLT frame's fixed 3-byte field. An
// empty language writes the spec's "XXX" undefined marker. A 1-2 byte code is NUL-padded
// because some encoders store short codes that way; using "X" padding would read back as a
// different language such as "enX". A longer value is truncated to the field's hard limit.
// ISO-639-2 codes are conventionally lowercase. Fold uppercase ASCII here so
// callers that bypass the CLI still write canonical bytes. This avoids Unicode
// case folding before the fixed-width pad/truncate below.
func syltLangBytes(lang string) []byte {
	// Recognize every ISO "undefined" form (empty, NUL/space-padded, or "xxx") as the canonical
	// "XXX" marker, using the exact rule the read applies (syltLanguage). Otherwise a model
	// language of "xxx" - which the CLI accepts and the model may carry - would be stored
	// verbatim yet read back empty, the asymmetry; now write and read agree that "xxx" is
	// the undefined marker (the model's unspecified = empty convention).
	if syltLanguage(lang) == "" {
		return []byte{'X', 'X', 'X'}
	}
	b := []byte(lang)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	for len(b) < 3 {
		b = append(b, 0)
	}
	return b[:3]
}

// syltLanguage normalizes a SYLT 3-byte language field to the modeled language: the
// spec's "XXX" (and an empty or NUL-padded field) means undefined and maps to "".
func syltLanguage(lang string) string {
	lang = strings.TrimRight(lang, "\x00 ")
	if lang == "" || strings.EqualFold(lang, "xxx") {
		return ""
	}
	return core.SanitizeUTF8(lang)
}

// stripLeadingLineBreak removes a single leading line break (CRLF, LF, or CR) from a SYLT
// text fragment. That marker is the conventional line separator; the model stores only the
// line content. encodeSYLT re-adds it on write.
func stripLeadingLineBreak(s string) string {
	switch {
	case strings.HasPrefix(s, "\r\n"):
		return s[2:]
	case strings.HasPrefix(s, "\n"), strings.HasPrefix(s, "\r"):
		return s[1:]
	}
	return s
}
