package id3

import (
	"encoding/binary"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
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
		frames, _ := syltFrames([]core.SyncedLyrics{in}, version, "")
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

// TestSYLTEmptyLanguageFallback checks a re-rendered set whose modeled language is empty
// falls back to the supplied original language, so a line-only edit keeps it.
func TestSYLTEmptyLanguageFallback(t *testing.T) {
	frames, _ := syltFrames([]core.SyncedLyrics{{Lines: []core.SyncedLine{{Time: 0, Text: "x"}}}}, 4, "deu")
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
	frames, _ := syltFrames([]core.SyncedLyrics{a, b}, 4, "")
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
	frames, _ := syltFrames(sets, 4, "")
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

// TestSYLTTimestampOverflow checks a line past the SYLT 32-bit millisecond field (~49.7
// days) is reported as clamped, so the codec can surface a warning instead of silently
// moving the lyric.
func TestSYLTTimestampOverflow(t *testing.T) {
	// ~60 days, past the 32-bit millisecond ceiling.
	set := core.SyncedLyrics{Lines: []core.SyncedLine{{Time: 60 * 24 * time.Hour, Text: "way out"}}}
	_, overflow := syltFrames([]core.SyncedLyrics{set}, 4, "")
	if !overflow {
		t.Error("expected overflow for a line past the 32-bit ms field")
	}
	// A normal line does not report overflow.
	if _, ov := syltFrames([]core.SyncedLyrics{syltSet()}, 4, ""); ov {
		t.Error("a normal set should not report overflow")
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
	roundtrip, _ := syltFrames([]core.SyncedLyrics{syltSet()}, 4, "")
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
