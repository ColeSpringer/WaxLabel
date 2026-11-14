package main

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
)

// longestLine is the length of the longest line of text.
func longestLine(text string) int {
	n := 0
	for line := range strings.SplitSeq(text, "\n") {
		n = max(n, len(line))
	}
	return n
}

// TestSanitizingWriterCapsLines: no line passes the writer longer than maxLineBytes. What a
// line had past that is counted in its place; shorter lines are untouched, however they are
// split across writes.
func TestSanitizingWriterCapsLines(t *testing.T) {
	long := strings.Repeat("x", maxLineBytes+5000)
	exact := strings.Repeat("y", maxLineBytes)
	for _, chunk := range []int{1 << 20, 4096, 7} {
		var under bytes.Buffer
		sw := newSanitizingWriter(&under)
		text := "short\n" + long + "\n" + exact + "\nlast " + long
		for at := 0; at < len(text); at += chunk {
			if _, err := sw.Write([]byte(text[at:min(at+chunk, len(text))])); err != nil {
				t.Fatal(err)
			}
		}
		if err := sw.Close(); err != nil {
			t.Fatal(err)
		}
		cut := long[:maxLineBytes] + "…[+" + wl.HumanBytes(5000) + "]"
		tail := "last " + long
		want := "short\n" + cut + "\n" + exact + "\n" + tail[:maxLineBytes] + "…[+" + wl.HumanBytes(5005) + "]"
		if got := under.String(); got != want {
			t.Errorf("chunks of %d: %d bytes out, longest line %d; want %d bytes with two lines cut", chunk, len(got), longestLine(got), len(want))
		}
	}

	// The cut falls on a rune boundary.
	var under bytes.Buffer
	sw := newSanitizingWriter(&under)
	if _, err := sw.Write([]byte(strings.Repeat("x", maxLineBytes-1) + "€€\n")); err != nil {
		t.Fatal(err)
	}
	if got, want := under.String(), strings.Repeat("x", maxLineBytes-1)+"…[+6 B]\n"; got != want {
		t.Errorf("a rune across the cap: tail %q, want %q", got[len(got)-20:], want[len(want)-20:])
	}
	// A line cut short of the cap by that takes nothing more from the next write.
	under.Reset()
	for _, part := range []string{strings.Repeat("x", maxLineBytes-1) + "€", "yy\n"} {
		if _, err := sw.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := under.String(), strings.Repeat("x", maxLineBytes-1)+"…[+5 B]\n"; got != want {
		t.Errorf("a line continued behind a cut rune: tail %q, want %q", got[len(got)-20:], want[len(want)-20:])
	}
	// The README gives the cap as 16 KiB.
	if maxLineBytes != 16<<10 {
		t.Errorf("maxLineBytes = %d; the README says 16 KiB", maxLineBytes)
	}

	// A write that fails commits nothing: the retry is cut where the first try would have been.
	fw := &failOnceWriter{failNext: true}
	sw = newSanitizingWriter(fw)
	if _, err := sw.Write([]byte(long)); err == nil {
		t.Fatal("expected the write error")
	}
	fw.failNext = false
	if _, err := sw.Write([]byte(long + "\n")); err != nil {
		t.Fatal(err)
	}
	if got := fw.got.String(); got != long[:maxLineBytes]+"…[+"+wl.HumanBytes(5000)+"]\n" {
		t.Errorf("after a failed write the line is %d bytes", len(got))
	}
}

// TestNoUnboundedLine: whatever a file or an argument holds, no line of text output runs on.
// The fields with a bound of their own keep to it; the writer's cap takes the rest.
func TestNoUnboundedLine(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("v", 50000)
	bounded := func(label, text string, max int) {
		t.Helper()
		if n := longestLine(text); n > max {
			t.Errorf("%s: a line of %d bytes (limit %d)", label, n, max)
		}
	}

	// Rendered from a parsed file: each field at its own bound.
	var b strings.Builder
	renderPictures(&b, []wl.Picture{{Type: wl.PicFrontCover, MIME: "image/png", Description: long}})
	renderChapters(&b, []wl.Chapter{{Title: "t", Language: long, LanguageIETF: long}})
	renderSyncedLyrics(&b, []wl.SyncedLyrics{{Language: long, Description: long, Lines: []wl.SyncedLine{{Time: time.Second, Text: "x"}}}})
	fmt.Fprintln(&b, audioLine(trackProps(long, wl.AudioTrack{Codec: long, SampleRate: 44100})))
	fmt.Fprintln(&b, audioLine(trackProps(long, wl.AudioTrack{SampleRate: 44100})))
	bounded("renderers", b.String(), 4400)

	// Through the CLI.
	f := copyFixture(t, sampleFLAC)
	cover := writeTempImage(t, "c.png", testfile.PNG())
	for _, c := range []struct {
		args []string
		max  int
	}{
		{[]string{"plan", f, "--add-cover", cover, "--picture-description", long}, 4400},
		{[]string{"set", f, "--add-cover", cover, "--picture-description", long, "--set", "DATE=" + long, "--set", "TRACKNUMBER=-" + strings.Repeat("1", 5000)}, maxLineBytes + 20},
		{[]string{"dump", f}, 4400},
		{[]string{"lint", f}, 400},
		{[]string{"lint", "--fix", f}, 400},
		{[]string{"plan", f, "--set", "TRACKNUMBER=" + long}, maxLineBytes + 20},
		// A warning names its key at the length a key is shown at, and quotes a value like one.
		{[]string{"plan", copyFixture(t, td("notags.m4a")), "--add", long + "=a", "--add", long + "=b"}, 4400},
		{[]string{"plan", "--strict", copyFixture(t, td("notags.m4a")), "--add", long + "=a", "--add", long + "=b"}, 700},
		{[]string{"plan", copyFixture(t, td("notags.m4a")), "--set", "TRACKNUMBER=" + long, "--set", "COMPILATION=" + long, "--set", "BPM=" + long}, 4400},
		{[]string{"plan", "--strict", copyFixture(t, td("notags.m4a")), "--set", "TRACKNUMBER=" + long, "--set", "COMPILATION=" + long}, 700},
		{[]string{"plan", copyFixture(t, td("notags.m4a")), "--set", "BPM=120." + strings.Repeat("5", 5000)}, 4400},
		{[]string{"plan", copyFixture(t, td("notags.m4a")), "--add", "TRACKNUMBER=1", "--add", "TRACKNUMBER=2/" + long}, 4400},
		{[]string{"plan", copyFixture(t, td("notags.flac")), "--set", "COMMENT=c", "--comment-desc", long}, 4400},
		{[]string{"plan", f, "--add-chapter", long}, maxLineBytes + 20},
		{[]string{"plan", f, "--set", "COMMENT=c", "--comment-lang", long}, maxLineBytes + 20},
		{[]string{"plan", f, "--remove-picture", long}, maxLineBytes + 20},
		{[]string{"plan", f, "--set", "Ä" + long + "=1"}, maxLineBytes + 20},
		{[]string{"dump", long}, maxLineBytes + 20},
	} {
		out, errb, _ := runCLI(t, c.args...)
		bounded(strings.Join(c.args[:1], " ")+" stdout", out, c.max)
		bounded(strings.Join(c.args[:1], " ")+" stderr", errb, c.max)
	}

	// A key with a thousand values: the change line still shows what they become, and the
	// per-family row counts what it leaves out.
	crowded := writeTempImage(t, "crowded.flac",
		testfile.TaggedFLAC("v", slices.Repeat([]string{"ALBUM=FLAC subset decoder testbench"}, 1000)...))
	plan, _, _ := runCLI(t, "plan", crowded, "--set", "ALBUM=x")
	if !strings.Contains(plan, " more] -> x\n") {
		t.Errorf("plan of a crowded key should end its change line with the new value:\n%.200s", plan)
	}
	bounded("plan of a crowded key", plan, 4400)
	native, _, _ := runCLI(t, "dump", "--native", crowded)
	if !strings.Contains(native, " more]\n") {
		t.Error("the per-family row of a crowded key should count the values it leaves out")
	}
	bounded("dump --native of a crowded key", native, 4400)

	// A warning about a key the file holds names it at the length a key is shown at; the JSON
	// of dump keeps the message whole.
	odd := writeTempImage(t, "odd.flac", testfile.TaggedFLAC("v", strings.Repeat("K", 5000)+"=bad\xffvalue", "TITLE=t"))
	for _, cmd := range []string{"dump", "lint"} {
		text, _, _ := runCLI(t, cmd, odd)
		if !strings.Contains(text, "invalid-text") {
			t.Fatalf("%s should report invalid-text:\n%.300s", cmd, text)
		}
		bounded(cmd+" of a long key with a bad value", text, 400)
	}
	asJSON, _, _ := runCLI(t, "--json", "dump", odd)
	if doc := decodeJSONOne[jsonDocument](t, asJSON); len(doc.Warnings) == 0 || len(doc.Warnings[0].Message) < 5000 {
		t.Errorf("dump --json should carry the warning's message whole: %d warnings", len(doc.Warnings))
	}

	// A negative number behind padding a file holds, quoted by lint at the length of a snippet.
	padded := writeTempImage(t, "padded.flac", testfile.TaggedFLAC("v", "TRACKNUMBER=-5"+strings.Repeat(" ", 5000)))
	out, errb, _ := runCLI(t, "lint", padded)
	if !strings.Contains(out, "negative-numeric") {
		t.Fatalf("the padded number should lint negative-numeric:\n%.300s%s", out, errb)
	}
	bounded("lint of a padded number", out, 400)

	// A comment's language is whatever a Matroska file says it is.
	const idTagLanguage = 0x447A
	comment := testfile.El(testfile.IDSimpleTag, testfile.El(testfile.IDTagName, []byte("COMMENT")),
		testfile.El(idTagLanguage, []byte(long)), testfile.El(testfile.IDTagString, []byte("c")))
	mka := writeTempImage(t, "lang.mka", testfile.Matroska("matroska", "T", testfile.El(testfile.IDTags, testfile.TagGroup(0, comment))))
	out, errb, _ = runCLI(t, "dump", mka)
	if !strings.Contains(out, "COMMENT") || !strings.Contains(out, "(vvvv") {
		t.Fatalf("the comment should show its language:\n%.600s%s", out, errb)
	}
	bounded("dump of a long comment language", out, 400)

	// JSON is not text output: it keeps every byte.
	out, _, _ = runCLI(t, "--json", "dump", f)
	if doc := decodeJSONOne[jsonDocument](t, out); len(doc.Pictures) != 1 || doc.Pictures[0].Description != long ||
		!slices.Equal(tagValues(doc, "RECORDINGDATE"), []string{long}) {
		t.Error("--json should carry the description and the value whole")
	}
	// A lint message quotes a value as a warning does, in JSON too: the value itself is in dump.
	out, _, _ = runCLI(t, "--json", "lint", f)
	for _, fd := range decodeJSONOne[jsonLint](t, out).Findings {
		if len(fd.Message) > 300 {
			t.Errorf("lint --json: %s message of %d bytes", fd.Code, len(fd.Message))
		}
	}
}
