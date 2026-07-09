package id3

import (
	"encoding/binary"
	"errors"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// syltSet is a sample synced-lyrics set reused across the SYLT tests.
func syltSet() core.SyncedLyrics {
	return core.SyncedLyrics{
		Language:    "eng",
		Description: "Main",
		Lines: []core.SyncedLine{
			{Time: 0, Text: "Intro"},
			{Time: 12 * time.Second, Text: "Verse one"},
			{Time: 17500 * time.Millisecond, Text: "Refrain café"}, // non-Latin-1 forces UTF
			{Time: 30 * time.Second, Text: ""},                     // empty clear marker
		},
	}
}

// TestSYLTRoundTrip checks decode(encode(x)) == x at both write versions, including the
// language, descriptor, millisecond timestamps, per-line text, and an empty clear marker.
func TestSYLTRoundTrip(t *testing.T) {
	in := syltSet()
	for _, version := range []byte{3, 4} {
		frames, _, _ := syltFrames([]core.SyncedLyrics{in}, version, "", "")
		got, ws := ProjectSyncedLyrics(tagWith(version, frames))
		if len(ws) != 0 {
			t.Errorf("v%d: unexpected warnings %v", version, ws)
		}
		if len(got) != 1 {
			t.Fatalf("v%d: got %d sets, want 1", version, len(got))
		}
		if got[0].Language != in.Language || got[0].Description != in.Description {
			t.Errorf("v%d: lang/desc = %q/%q, want %q/%q", version, got[0].Language, got[0].Description, in.Language, in.Description)
		}
		if len(got[0].Lines) != len(in.Lines) {
			t.Fatalf("v%d: %d lines, want %d", version, len(got[0].Lines), len(in.Lines))
		}
		for i := range in.Lines {
			if got[0].Lines[i] != in.Lines[i] {
				t.Errorf("v%d line %d = %+v, want %+v", version, i, got[0].Lines[i], in.Lines[i])
			}
		}
	}
}

// TestSYLTLanguageNormalization checks the "XXX" undefined marker and an empty/NUL language
// both project to an empty modeled language, while a real code is kept.
func TestSYLTLanguageNormalization(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"eng", "eng"},
		{"XXX", ""},
		{"\x00\x00\x00", ""},
	}
	for _, c := range cases {
		body := buildSYLT(encLatin1, c.raw, syltFmtMillis, syltContentLyrics, "", []core.SyncedLine{{Time: time.Second, Text: "x"}})
		got, _, ok := decodeSYLT(body)
		if !ok {
			t.Fatalf("decodeSYLT(%q) not ok", c.raw)
		}
		if got.Language != c.want {
			t.Errorf("language %q -> %q, want %q", c.raw, got.Language, c.want)
		}
	}
}

// TestSYLTLanguageWriteMatchesRead is a regression: a modeled language of "xxx" (which the
// CLI accepts) is the ISO undefined marker, so the write must store it as the canonical "XXX" and
// it must read back empty - the same value an empty model language round-trips to - rather than
// being stored verbatim yet dropped to empty on read.
func TestSYLTLanguageWriteMatchesRead(t *testing.T) {
	for _, lang := range []string{"xxx", "XXX", ""} {
		frames, _, _ := syltFrames([]core.SyncedLyrics{{Language: lang, Lines: []core.SyncedLine{{Time: 0, Text: "x"}}}}, 4, "", "")
		if len(frames) != 1 {
			t.Fatalf("language %q: got %d frames, want 1", lang, len(frames))
		}
		if l, _ := syltFrameLanguage(frames[0].Body); l != "XXX" {
			t.Errorf("language %q written as %q, want XXX (canonical undefined marker)", lang, l)
		}
		if got, _, ok := decodeSYLT(frames[0].Body); !ok || got.Language != "" {
			t.Errorf("language %q read back as %q (ok=%v), want empty (consistent with the write)", lang, got.Language, ok)
		}
	}
}

// TestSYLTEmptyLanguageFallback checks a re-rendered set whose modeled language is empty
// falls back to the supplied original language, so a line-only edit keeps it.
func TestSYLTEmptyLanguageFallback(t *testing.T) {
	frames, _, _ := syltFrames([]core.SyncedLyrics{{Lines: []core.SyncedLine{{Time: 0, Text: "x"}}}}, 4, "deu", "")
	got, _, ok := decodeSYLT(frames[0].Body)
	if !ok || got.Language != "deu" {
		t.Fatalf("fallback language = %q (ok=%v), want deu", got.Language, ok)
	}
}

// TestSYLTTimestampFormatSkipped checks an MPEG-frames (format 1) SYLT is skipped with a
// timestamp-format warning rather than projected at a wrong offset.
func TestSYLTTimestampFormatSkipped(t *testing.T) {
	body := buildSYLT(encLatin1, "eng", 1 /* MPEG frames */, syltContentLyrics, "", []core.SyncedLine{{Time: time.Second, Text: "x"}})
	_, ws, ok := decodeSYLT(body)
	if ok {
		t.Fatal("format-1 SYLT should be skipped")
	}
	if len(ws) != 1 || ws[0].Code != core.WarnSyncedLyricsTimestampFormat {
		t.Errorf("warnings = %v, want one synced-lyrics-timestamp-format", ws)
	}
}

// TestSYLTContentTypeSkipped checks a non-lyric content type is skipped with a content-type
// warning rather than mismodeled as lyrics.
func TestSYLTContentTypeSkipped(t *testing.T) {
	body := buildSYLT(encLatin1, "eng", syltFmtMillis, 5 /* chord */, "", []core.SyncedLine{{Time: time.Second, Text: "Am"}})
	_, ws, ok := decodeSYLT(body)
	if ok {
		t.Fatal("non-lyric SYLT should be skipped")
	}
	if len(ws) != 1 || ws[0].Code != core.WarnSyncedLyricsContentType {
		t.Errorf("warnings = %v, want one synced-lyrics-content-type", ws)
	}
}

// TestSYLTLeadingNewlineStripped checks the conventional leading line-break marker is
// stripped on read (and re-added on write), so the modeled text is the clean line.
func TestSYLTLeadingNewlineStripped(t *testing.T) {
	// A fragment carrying the CRLF marker decodes to the clean text.
	body := buildSYLT(encLatin1, "eng", syltFmtMillis, syltContentLyrics, "", []core.SyncedLine{{Time: 0, Text: "\r\nLine"}})
	got, _, ok := decodeSYLT(body)
	if !ok || got.Lines[0].Text != "Line" {
		t.Fatalf("leading CRLF not stripped: %q", got.Lines[0].Text)
	}
}

// TestSYLTMultipleSets checks two SYLT frames project as two independent sets, in order.
func TestSYLTMultipleSets(t *testing.T) {
	a := core.SyncedLyrics{Language: "eng", Lines: []core.SyncedLine{{Time: 0, Text: "english"}}}
	b := core.SyncedLyrics{Language: "spa", Lines: []core.SyncedLine{{Time: 0, Text: "espanol"}}}
	frames, _, _ := syltFrames([]core.SyncedLyrics{a, b}, 4, "", "")
	got, _ := ProjectSyncedLyrics(tagWith(4, frames))
	if len(got) != 2 || got[0].Language != "eng" || got[1].Language != "spa" {
		t.Fatalf("got %+v, want eng then spa", got)
	}
}

// TestSYLTv22Upgrade checks a v2.2 SLT frame is upgraded to SYLT and projected, so a legacy
// tag's synchronized lyrics read without a special case.
func TestSYLTv22Upgrade(t *testing.T) {
	// A v2.2 SLT body shares the SYLT layout.
	sltBody := buildSYLT(encLatin1, "eng", syltFmtMillis, syltContentLyrics, "Main",
		[]core.SyncedLine{{Time: time.Second, Text: "\nHello"}})
	// Hand-build a v2.2 tag: "ID3" 02 00 00 <size>, then SLT + size(3) + body.
	frame := append([]byte("SLT"), byte(len(sltBody)>>16), byte(len(sltBody)>>8), byte(len(sltBody)))
	frame = append(frame, sltBody...)
	var sz [4]byte
	putSyncSafe(sz[:], int64(len(frame)))
	data := append([]byte{'I', 'D', '3', 2, 0, 0}, sz[:]...)
	data = append(data, frame...)

	tg, err := ParseTag(data, 0)
	if err != nil {
		t.Fatalf("ParseTag: %v", err)
	}
	got, _ := ProjectSyncedLyrics(tg)
	if len(got) != 1 || len(got[0].Lines) != 1 || got[0].Lines[0].Text != "Hello" {
		t.Fatalf("v2.2 SLT upgrade projected %+v, want one Hello line", got)
	}
	if got[0].Lines[0].Time != time.Second {
		t.Errorf("upgraded line time = %v, want 1s", got[0].Lines[0].Time)
	}
}

// TestSYLTSkipsEmptySet checks a set with no lines renders no SYLT frame: a line-less SYLT
// projects to nothing on re-read, so writing one would be a phantom frame.
func TestSYLTSkipsEmptySet(t *testing.T) {
	sets := []core.SyncedLyrics{
		{Language: "eng"}, // no lines, must be skipped
		{Language: "spa", Lines: []core.SyncedLine{{Time: time.Second, Text: "x"}}},
	}
	frames, _, _ := syltFrames(sets, 4, "", "")
	if len(frames) != 1 {
		t.Fatalf("got %d SYLT frames, want 1 (the empty set skipped)", len(frames))
	}
	got, _, ok := decodeSYLT(frames[0].Body)
	if !ok || got.Language != "spa" {
		t.Errorf("surviving frame = %+v (ok=%v), want the spa set", got, ok)
	}
}

// TestSYLTNonLyricLanguageNotInherited checks an empty-language synced-lyrics edit does
// not inherit the language of a leading non-lyric chord SYLT. The origLangs fallback must
// be gated on a projecting frame.
func TestSYLTNonLyricLanguageNotInherited(t *testing.T) {
	// A leading chord SYLT (content-type != lyrics) in German.
	chord := buildSYLT(encLatin1, "deu", syltFmtMillis, 5 /* chord */, "", []core.SyncedLine{{Time: 0, Text: "Am"}})
	orig := []Frame{{ID: "SYLT", Body: chord}}
	se := StructuredEdit{
		SyncedLyrics:        []core.SyncedLyrics{{Lines: []core.SyncedLine{{Time: time.Second, Text: "lyric"}}}},
		SyncedLyricsChanged: true,
	}
	out, _ := RebuildFrames(orig, tag.NewTagSet(), tag.NewTagSet(), 4, se, WriteOpts{})
	var lyricLang string
	found := false
	for _, f := range out {
		if f.ID == "SYLT" && syltProjectsLyrics(f.Body) {
			sl, _, _ := decodeSYLT(f.Body)
			lyricLang, found = sl.Language, true
		}
	}
	if !found {
		t.Fatal("no projecting lyrics SYLT in the rebuilt frames")
	}
	if lyricLang != "" {
		t.Errorf("lyrics SYLT language = %q, want empty (not inherited from the chord frame's deu)", lyricLang)
	}
}

// TestSYLTCarriedLanguageNotInherited is a regression: a faithful carry of a
// no-language synced-lyrics set must not inherit the destination's existing SYLT language.
// An authored line-only edit still keeps it (the documented CLI convenience), so the two
// dispositions are asserted side by side to pin the SyncedLyricsCarried gate.
func TestSYLTCarriedLanguageNotInherited(t *testing.T) {
	// A leading projecting lyrics SYLT already in the destination, in English.
	engLyrics := buildSYLT(encLatin1, "eng", syltFmtMillis, syltContentLyrics, "", []core.SyncedLine{{Time: 0, Text: "old"}})
	orig := []Frame{{ID: "SYLT", Body: engLyrics}}
	noLangSet := []core.SyncedLyrics{{Lines: []core.SyncedLine{{Time: time.Second, Text: "lyric"}}}}

	lyricLang := func(se StructuredEdit) string {
		out, _ := RebuildFrames(orig, tag.NewTagSet(), tag.NewTagSet(), 4, se, WriteOpts{})
		for _, f := range out {
			if f.ID == "SYLT" && syltProjectsLyrics(f.Body) {
				sl, _, _ := decodeSYLT(f.Body)
				return sl.Language
			}
		}
		t.Fatal("no projecting lyrics SYLT in the rebuilt frames")
		return ""
	}

	// Carried: the destination's "eng" must not leak onto the no-language source set.
	if got := lyricLang(StructuredEdit{SyncedLyrics: noLangSet, SyncedLyricsChanged: true, SyncedLyricsCarried: true}); got != "" {
		t.Errorf("carried lyrics SYLT language = %q, want empty (must not inherit the destination's eng)", got)
	}
	// Authored (not carried): the documented convenience still inherits "eng".
	if got := lyricLang(StructuredEdit{SyncedLyrics: noLangSet, SyncedLyricsChanged: true}); got != "eng" {
		t.Errorf("authored line-only edit language = %q, want eng (the documented fallback)", got)
	}
}

// TestSYLTDescriptorPreservedOnAuthoring checks that authoring lyric lines over a SYLT carrying a
// content descriptor preserves that descriptor. The language fallback already kept the language, but
// the descriptor was blanked, because an authored set carries Description=="". A faithful
// cross-format carry must not inherit the descriptor, so the two cases are asserted side by side to
// pin the SyncedLyricsCarried guard, the same way the language fallback is gated.
func TestSYLTDescriptorPreservedOnAuthoring(t *testing.T) {
	// A projecting lyrics SYLT already in the destination, with a content descriptor.
	described := buildSYLT(encLatin1, "eng", syltFmtMillis, syltContentLyrics, "Karaoke",
		[]core.SyncedLine{{Time: 0, Text: "old"}})
	orig := []Frame{{ID: "SYLT", Body: described}}
	// An authored edit sets lines but no descriptor (Description == "").
	noDescSet := []core.SyncedLyrics{{Lines: []core.SyncedLine{{Time: time.Second, Text: "lyric"}}}}

	descriptor := func(se StructuredEdit) string {
		out, _ := RebuildFrames(orig, tag.NewTagSet(), tag.NewTagSet(), 4, se, WriteOpts{})
		for _, f := range out {
			if f.ID == "SYLT" && syltProjectsLyrics(f.Body) {
				sl, _, _ := decodeSYLT(f.Body)
				return sl.Description
			}
		}
		t.Fatal("no projecting lyrics SYLT in the rebuilt frames")
		return ""
	}

	// Authored (not carried): the file's descriptor is preserved.
	if got := descriptor(StructuredEdit{SyncedLyrics: noDescSet, SyncedLyricsChanged: true}); got != "Karaoke" {
		t.Errorf("authored line-only edit descriptor = %q, want \"Karaoke\" (preserved)", got)
	}
	// Carried: a faithful carry of a descriptor-less set must not inherit the destination's.
	if got := descriptor(StructuredEdit{SyncedLyrics: noDescSet, SyncedLyricsChanged: true, SyncedLyricsCarried: true}); got != "" {
		t.Errorf("carried lyrics SYLT descriptor = %q, want empty (must not inherit the destination's Karaoke)", got)
	}
}

// TestSYLTTimestampOverflow checks a line past the SYLT 32-bit millisecond field (~49.7
// days) is reported as clamped, so the codec can surface a warning instead of silently
// moving the lyric.
func TestSYLTTimestampOverflow(t *testing.T) {
	// ~60 days, past the 32-bit millisecond ceiling.
	set := core.SyncedLyrics{Lines: []core.SyncedLine{{Time: 60 * 24 * time.Hour, Text: "way out"}}}
	_, overflow, _ := syltFrames([]core.SyncedLyrics{set}, 4, "", "")
	if !overflow {
		t.Error("expected overflow for a line past the 32-bit ms field")
	}
	// A normal line does not report overflow.
	if _, ov, _ := syltFrames([]core.SyncedLyrics{syltSet()}, 4, "", ""); ov {
		t.Error("a normal set should not report overflow")
	}
}

// TestSyncedLyricsNULFlaggedAndErrored checks the shared NUL-guard mechanism: RebuildFrames flags
// an embedded NUL in a modeled line's text or in an authored descriptor via RebuildInfo, and
// RebuildError turns that flag into a waxerr.ErrInvalidData, while a clean set flags neither and
// errors not at all. Each codec calls RebuildError next to CheckSize; the end-to-end wiring is
// covered by the root package's TestSyncedLyricsNULRejectedAtCodec.
func TestSyncedLyricsNULFlaggedAndErrored(t *testing.T) {
	infoFor := func(sl core.SyncedLyrics) RebuildInfo {
		_, info := RebuildFrames(nil, tag.NewTagSet(), tag.NewTagSet(), 4, StructuredEdit{
			SyncedLyrics: []core.SyncedLyrics{sl}, SyncedLyricsChanged: true,
		}, WriteOpts{})
		return info
	}

	// A NUL in the line text is flagged and yields a hard error.
	nulLine := infoFor(core.SyncedLyrics{Lines: []core.SyncedLine{{Text: "before\x00after"}}})
	if !nulLine.SyncedLyricsInvalidNUL {
		t.Error("a NUL in line text should set SyncedLyricsInvalidNUL")
	}
	if err := RebuildError(nulLine); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("RebuildError for a NUL line = %v, want waxerr.ErrInvalidData", err)
	}

	// A NUL in the descriptor is flagged too (the line here is clean, so only the descriptor trips it).
	nulDesc := infoFor(core.SyncedLyrics{Description: "d\x00e", Lines: []core.SyncedLine{{Text: "ok"}}})
	if !nulDesc.SyncedLyricsInvalidNUL {
		t.Error("a NUL in the descriptor should set SyncedLyricsInvalidNUL")
	}

	// A clean set flags nothing and errors not at all.
	clean := infoFor(core.SyncedLyrics{Description: "chorus", Lines: []core.SyncedLine{{Text: "clean"}}})
	if clean.SyncedLyricsInvalidNUL {
		t.Error("a clean synced-lyrics set must not set SyncedLyricsInvalidNUL")
	}
	if err := RebuildError(clean); err != nil {
		t.Errorf("RebuildError on a clean rebuild = %v, want nil", err)
	}
}

// TestSYLTTimestampFullRange checks that SYLT accepts the full uint32
// millisecond range. A line at 0xFFFFFFFF round-trips without overflow; CHAP's
// lower chapTimeMax ceiling does not apply.
func TestSYLTTimestampFullRange(t *testing.T) {
	const maxMs = 0xFFFFFFFF
	wantD := time.Duration(maxMs) * time.Millisecond
	set := core.SyncedLyrics{Lines: []core.SyncedLine{{Time: wantD, Text: "edge"}}}
	frames, overflow, _ := syltFrames([]core.SyncedLyrics{set}, 4, "", "")
	if overflow {
		t.Error("a line at exactly 0xFFFFFFFF ms should not report overflow")
	}
	got, _ := ProjectSyncedLyrics(tagWith(4, frames))
	if len(got) != 1 || len(got[0].Lines) != 1 {
		t.Fatalf("decoded %v, want one set with one line", got)
	}
	if got[0].Lines[0].Time != wantD {
		t.Errorf("time = %v, want %v (unclamped full-range value)", got[0].Lines[0].Time, wantD)
	}
}

// TestSYLTTimestampClampsAtFullMax checks that a timestamp past 0xFFFFFFFF ms
// clamps to the full uint32 max and reports overflow.
func TestSYLTTimestampClampsAtFullMax(t *testing.T) {
	over := core.SyncedLyrics{Lines: []core.SyncedLine{{Time: time.Duration(0x100000000) * time.Millisecond, Text: "past"}}}
	frames, overflow, _ := syltFrames([]core.SyncedLyrics{over}, 4, "", "")
	if !overflow {
		t.Error("a line past 0xFFFFFFFF ms should report overflow")
	}
	got, _ := ProjectSyncedLyrics(tagWith(4, frames))
	if wantD := time.Duration(0xFFFFFFFF) * time.Millisecond; got[0].Lines[0].Time != wantD {
		t.Errorf("clamped time = %v, want %v (0xFFFFFFFF)", got[0].Lines[0].Time, wantD)
	}
}

// TestSyltLangBytesCanonicalizesCase checks that SYLT language encoding folds
// uppercase ISO-639-2 codes to lowercase before fixed-width pad/truncate, while
// keeping the "XXX" undefined marker and short NUL-padded codes in shape.
func TestSyltLangBytesCanonicalizesCase(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ENG", "eng"},
		{"eng", "eng"},
		{"Fr", "fr\x00"}, // 2-byte code: folded, then NUL-padded
		{"", "XXX"},      // undefined marker unchanged
	}
	for _, c := range cases {
		if got := string(syltLangBytes(c.in)); got != c.want {
			t.Errorf("syltLangBytes(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSYLTUppercaseLanguageWrittenLowercase checks that the encoder writes
// lowercase for both an authored uppercase language and one inherited through
// fallbackLang on a line-only edit. The decoder preserves case, so lowercase
// bytes in the frame prove the encoder performed the fold.
func TestSYLTUppercaseLanguageWrittenLowercase(t *testing.T) {
	explicit := core.SyncedLyrics{Language: "ENG", Lines: []core.SyncedLine{{Time: time.Second, Text: "x"}}}
	frames, _, _ := syltFrames([]core.SyncedLyrics{explicit}, 4, "", "")
	if lang := string(frames[0].Body[1:4]); lang != "eng" {
		t.Errorf("explicit language stored = %q, want eng", lang)
	}
	// Line-only edit: the modeled language is empty, so the file's uppercase code arrives via
	// fallbackLang and is canonicalized on re-encode.
	inherited := core.SyncedLyrics{Lines: []core.SyncedLine{{Time: time.Second, Text: "new"}}}
	frames, _, _ = syltFrames([]core.SyncedLyrics{inherited}, 4, "ENG", "")
	if lang := string(frames[0].Body[1:4]); lang != "eng" {
		t.Errorf("inherited language stored = %q, want eng (canonicalized on re-encode)", lang)
	}
}

// buildSYLT assembles a SYLT frame body with explicit header fields, for the skip/normalize
// tests that need a content type or timestamp format encodeSYLT never writes.
func buildSYLT(enc byte, lang string, tsFmt, content byte, desc string, lines []core.SyncedLine) []byte {
	out := []byte{enc}
	out = append(out, langBytes(lang)...)
	out = append(out, tsFmt, content)
	out = append(out, encodeString(enc, desc)...)
	out = append(out, term(enc)...)
	for _, ln := range lines {
		out = append(out, encodeString(enc, ln.Text)...)
		out = append(out, term(enc)...)
		var ts [4]byte
		binary.BigEndian.PutUint32(ts[:], uint32(ln.Time/time.Millisecond))
		out = append(out, ts[:]...)
	}
	return out
}

// FuzzDecodeSYLT asserts the SYLT body parser never panics and never yields invalid UTF-8
// in the descriptor or any line text.
func FuzzDecodeSYLT(f *testing.F) {
	f.Add([]byte(nil))
	f.Add([]byte{encLatin1, 'e', 'n', 'g', 2, 1})
	f.Add(buildSYLT(encLatin1, "eng", syltFmtMillis, syltContentLyrics, "d", []core.SyncedLine{{Time: time.Second, Text: "x"}}))
	f.Add(buildSYLT(encUTF16, "eng", syltFmtMillis, syltContentLyrics, "dé", []core.SyncedLine{{Time: time.Second, Text: "café"}}))
	// buildSYLT writes a BOM on every UTF-16 string. Add hand-built bytes where only the
	// descriptor carries one.
	f.Add(syltLEDescBOMLessLines())
	roundtrip, _, _ := syltFrames([]core.SyncedLyrics{syltSet()}, 4, "", "")
	f.Add(roundtrip[0].Body)
	f.Fuzz(func(t *testing.T, body []byte) {
		sl, _, ok := decodeSYLT(body)
		if !ok {
			return
		}
		if !utf8.ValidString(sl.Description) {
			t.Errorf("descriptor not valid UTF-8: %q", sl.Description)
		}
		for _, ln := range sl.Lines {
			if !utf8.ValidString(ln.Text) {
				t.Errorf("line text not valid UTF-8: %q", ln.Text)
			}
		}
	})
}
