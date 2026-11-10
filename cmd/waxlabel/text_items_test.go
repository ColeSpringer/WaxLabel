package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// describedFile is a copy of fixture holding one comment and one lyric with the given qualifiers.
func describedFile(t *testing.T, fixture string, comment wl.Comment, lyrics wl.Lyrics) string {
	t.Helper()
	return buildTransferSource(t, fixture, func(e *wl.Editor) *wl.Editor {
		return e.SetComments(comment).SetLyrics(lyrics)
	})
}

var (
	describedComment = wl.Comment{Text: "hello", Description: "My Desc", Language: "eng"}
	describedLyrics  = wl.Lyrics{Text: "la la", Description: "Lyr Desc", Language: "deu"}
)

// dump prints a comment's and a lyric's language and description after the value, and lists
// both in JSON.
func TestDumpShowsCommentAndLyricsQualifiers(t *testing.T) {
	t.Parallel()
	path := describedFile(t, notagsMP3, describedComment, describedLyrics)
	out, _, code := runCLI(t, "dump", path)
	if code != 0 {
		t.Fatalf("dump exit = %d\n%s", code, out)
	}
	for _, want := range []string{`COMMENT  hello  (eng, "My Desc")`, `LYRICS   la la  (deu, "Lyr Desc")`} {
		if !strings.Contains(out, want) {
			t.Errorf("dump lacks %q:\n%s", want, out)
		}
	}
	jd := dumpJSON(t, path)
	if want := []jsonTextItem{{Text: "hello", Description: "My Desc", Language: "eng"}}; !slices.Equal(jd.Comments, want) {
		t.Errorf("comments = %+v, want %+v", jd.Comments, want)
	}
	if want := []jsonTextItem{{Text: "la la", Description: "Lyr Desc", Language: "deu"}}; !slices.Equal(jd.Lyrics, want) {
		t.Errorf("lyrics = %+v, want %+v", jd.Lyrics, want)
	}
}

// A comment with no qualifier prints as before, and a file with none lists empty arrays.
func TestDumpPlainCommentHasNoQualifierSuffix(t *testing.T) {
	t.Parallel()
	path := buildTransferSource(t, notagsFLAC, func(e *wl.Editor) *wl.Editor { return e.Set(tag.Comment, "plain") })
	out, _, _ := runCLI(t, "dump", path)
	if !strings.Contains(out, "COMMENT  plain\n") {
		t.Errorf("dump of a plain comment:\n%s", out)
	}
	if out, _, _ := runCLI(t, "--json", "dump", notagsFLAC); !strings.Contains(out, `"comments": []`) || !strings.Contains(out, `"lyrics": []`) {
		t.Errorf("dump --json of a file with no comment lacks the empty arrays:\n%s", out)
	}
}

// Two files that differ only in a comment's description are not identical.
func TestDiffComparesCommentQualifiers(t *testing.T) {
	t.Parallel()
	a := describedFile(t, notagsMP3, describedComment, describedLyrics)
	plain := describedComment
	plain.Description = ""
	b := describedFile(t, notagsMP3, plain, describedLyrics)

	out, _, code := runCLI(t, "diff", a, b)
	if code != 1 {
		t.Fatalf("diff exit = %d, want 1\n%s", code, out)
	}
	if want := `  ~ COMMENT description: "My Desc" -> ""` + "\n"; !strings.Contains(out, want) {
		t.Errorf("diff lacks %q:\n%s", want, out)
	}

	out, _, code = runCLI(t, "--json", "diff", a, b)
	if code != 1 {
		t.Fatalf("diff --json exit = %d, want 1\n%s", code, out)
	}
	var jd jsonDiff
	if err := json.Unmarshal([]byte(out), &jd); err != nil {
		t.Fatalf("diff --json: %v\n%s", err, out)
	}
	if jd.Identical || !jd.Comments.Changed || jd.Lyrics.Changed {
		t.Errorf("identical=%v comments.changed=%v lyrics.changed=%v, want false true false", jd.Identical, jd.Comments.Changed, jd.Lyrics.Changed)
	}
	if want := []jsonTextItem{{Text: "hello", Description: "My Desc", Language: "eng"}}; !slices.Equal(jd.Comments.A, want) {
		t.Errorf("comments.a = %+v, want %+v", jd.Comments.A, want)
	}
	if want := []jsonTextItem{{Text: "hello", Language: "eng"}}; !slices.Equal(jd.Comments.B, want) {
		t.Errorf("comments.b = %+v, want %+v", jd.Comments.B, want)
	}
}

// A qualifier is compared only where both formats store it, and a language one side leaves
// unset is no difference: a lossless copy reads as identical.
func TestDiffQualifiersAcrossFormats(t *testing.T) {
	t.Parallel()
	mp3 := describedFile(t, notagsMP3, describedComment, describedLyrics)
	cases := []struct {
		name    string
		other   string
		comment wl.Comment
		changed bool
	}{
		{"flac stores neither", notagsFLAC, wl.Comment{Text: "hello"}, false},
		{"matroska stores the language", notagsMKA, wl.Comment{Text: "hello", Language: "eng"}, false},
		{"matroska with another language", notagsMKA, wl.Comment{Text: "hello", Language: "fra"}, true},
		{"matroska with no language", notagsMKA, wl.Comment{Text: "hello"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			other := describedFile(t, c.other, c.comment, wl.Lyrics{Text: "la la", Language: "deu"})
			out, _, _ := runCLI(t, "--json", "diff", mp3, other)
			var jd jsonDiff
			if err := json.Unmarshal([]byte(out), &jd); err != nil {
				t.Fatalf("diff --json: %v\n%s", err, out)
			}
			if jd.Comments.Changed != c.changed || jd.Lyrics.Changed {
				t.Errorf("comments.changed=%v lyrics.changed=%v, want %v false\n%s", jd.Comments.Changed, jd.Lyrics.Changed, c.changed, out)
			}
		})
	}
}

// --comment-desc/--comment-lang and --lyrics-desc/--lyrics-lang qualify the values the same
// invocation sets or adds.
func TestSetCommentAndLyricsQualifierFlags(t *testing.T) {
	t.Parallel()
	path := copyFixture(t, notagsMP3)
	out, errOut, code := runCLI(t, "set", path, "--set", "COMMENT=hello", "--comment-desc", "My Desc", "--comment-lang", "fra",
		"--set", "LYRICS=la la", "--lyrics-desc", "Lyr Desc", "--lyrics-lang", "deu")
	if code != 0 {
		t.Fatalf("set exit = %d\n%s%s", code, out, errOut)
	}
	jd := dumpJSON(t, path)
	if want := []jsonTextItem{{Text: "hello", Description: "My Desc", Language: "fra"}}; !slices.Equal(jd.Comments, want) {
		t.Errorf("comments = %+v, want %+v", jd.Comments, want)
	}
	if want := []jsonTextItem{{Text: "la la", Description: "Lyr Desc", Language: "deu"}}; !slices.Equal(jd.Lyrics, want) {
		t.Errorf("lyrics = %+v, want %+v", jd.Lyrics, want)
	}

	// --add qualifies the added value and leaves the file's own comment as it was.
	if out, errOut, code := runCLI(t, "set", path, "--add", "COMMENT=second", "--comment-lang", "spa"); code != 0 {
		t.Fatalf("set --add exit = %d\n%s%s", code, out, errOut)
	}
	want := []jsonTextItem{{Text: "hello", Description: "My Desc", Language: "fra"}, {Text: "second", Language: "spa"}}
	if jd := dumpJSON(t, path); !slices.Equal(jd.Comments, want) {
		t.Errorf("comments after --add = %+v, want %+v", jd.Comments, want)
	}
}

// A description alone changes an existing comment, and the plan names the change.
func TestPlanShowsQualifierChange(t *testing.T) {
	t.Parallel()
	path := describedFile(t, notagsMP3, describedComment, describedLyrics)
	out, _, code := runCLI(t, "plan", path, "--set", "COMMENT=hello", "--comment-desc", "Other")
	if code != 0 {
		t.Fatalf("plan exit = %d\n%s", code, out)
	}
	if want := `~ COMMENT description: "My Desc" -> "Other"`; !strings.Contains(out, want) {
		t.Errorf("plan lacks %q:\n%s", want, out)
	}
}

func TestQualifierFlagUsageErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"desc without a comment value", []string{"--set", "TITLE=x", "--comment-desc", "d"}, "--comment-desc needs --set COMMENT= or --add COMMENT="},
		{"lang without a lyrics value", []string{"--set", "COMMENT=x", "--lyrics-lang", "eng"}, "--lyrics-lang needs --set LYRICS= or --add LYRICS="},
		{"lang with a cleared comment", []string{"--clear", "COMMENT", "--comment-lang", "eng"}, "--comment-lang needs --set COMMENT= or --add COMMENT="},
		{"malformed language", []string{"--set", "COMMENT=x", "--comment-lang", "english"}, "--comment-lang"},
		{"empty language", []string{"--set", "COMMENT=x", "--comment-lang", ""}, "--comment-lang cannot be empty"},
		{"NUL in a description", []string{"--set", "LYRICS=x", "--lyrics-desc", "a\x00b"}, "--lyrics-desc"},
		{"machine description", []string{"--set", "COMMENT=x", "--comment-desc", "iTunNORM"}, `--comment-desc "iTunNORM" names machine data`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := copyFixture(t, notagsMP3)
			for _, cmd := range []string{"set", "plan"} {
				_, errOut, code := runCLI(t, append([]string{cmd, path}, c.args...)...)
				if code != 2 || !strings.Contains(errOut, c.want) {
					t.Errorf("%s exit = %d, stderr %q; want 2 naming %q", cmd, code, errOut, c.want)
				}
			}
		})
	}
}

// A qualifier the format cannot store is a warning, and --strict refuses the write.
func TestQualifierFlagOnAFormatThatDropsIt(t *testing.T) {
	t.Parallel()
	path := copyFixture(t, notagsFLAC)
	args := []string{"set", path, "--set", "COMMENT=x", "--comment-desc", "d", "--comment-lang", "eng"}
	out, errOut, code := runCLI(t, args...)
	if code != 0 || !strings.Contains(out+errOut, "[value-reduced] COMMENT: description and language not carried") {
		t.Errorf("set exit = %d, want 0 with a value-reduced warning\n%s%s", code, out, errOut)
	}
	path = copyFixture(t, notagsFLAC)
	args[1] = path
	if _, errOut, code := runCLI(t, append(args, "--strict")...); code != 2 || !strings.Contains(errOut, "description and language not carried") {
		t.Errorf("set --strict exit = %d, stderr %q; want 2 naming the loss", code, errOut)
	}
	if v := tagValues(dumpJSON(t, path), "COMMENT"); len(v) != 0 {
		t.Errorf("a refused write stored COMMENT = %v", v)
	}
}

// copy carries a comment's qualifiers, and --strict refuses where the destination drops them.
func TestCopyGradesCommentQualifiers(t *testing.T) {
	t.Parallel()
	src := describedFile(t, notagsMP3, describedComment, describedLyrics)

	dst := copyFixture(t, notagsMP3)
	out, errOut, code := runCLI(t, "copy", "--strict", src, dst)
	if code != 0 || !strings.Contains(out, "2 carried, 0 lossy, 0 dropped") {
		t.Fatalf("copy onto MP3 exit = %d\n%s%s", code, out, errOut)
	}
	if jd := dumpJSON(t, dst); !slices.Equal(jd.Comments, []jsonTextItem{{Text: "hello", Description: "My Desc", Language: "eng"}}) {
		t.Errorf("copied comments = %+v", jd.Comments)
	}
	if _, _, code := runCLI(t, "diff", src, dst); code != 0 {
		t.Errorf("diff after a lossless copy exit = %d, want 0", code)
	}

	dst = copyFixture(t, notagsFLAC)
	out, errOut, code = runCLI(t, "copy", "--strict", src, dst)
	if code != 2 || !strings.Contains(out, "lossy   COMMENT: description not carried") ||
		!strings.Contains(out, "lossy   LYRICS: description and language not carried") {
		t.Errorf("copy --strict onto FLAC exit = %d, want 2 with the lossy lines\n%s%s", code, out, errOut)
	}
	// The grades stay field items, as every other key's are.
	out, _, code = runCLI(t, "--json", "copy", src, dst)
	var jc jsonCopy
	if err := json.Unmarshal([]byte(out), &jc); err != nil || code != 0 {
		t.Fatalf("copy --json exit = %d: %v\n%s", code, err, out)
	}
	for _, it := range jc.Transfer {
		if (it.Key == "COMMENT" || it.Key == "LYRICS") && (it.Kind != "field" || it.Disposition != "lossy") {
			t.Errorf("transfer item %+v, want a lossy field", it)
		}
	}
}

// A plain ID3 comment copies onto a format with no language field under --strict, as it did
// before comments carried one.
func TestCopyStrictOfAPlainCommentOntoFLAC(t *testing.T) {
	t.Parallel()
	src := buildTransferSource(t, notagsMP3, func(e *wl.Editor) *wl.Editor { return e.Set(tag.Comment, "hello") })
	dst := copyFixture(t, notagsFLAC)
	if out, errOut, code := runCLI(t, "copy", "--strict", src, dst); code != 0 {
		t.Errorf("copy --strict exit = %d\n%s%s", code, out, errOut)
	}
	if _, _, code := runCLI(t, "diff", src, dst); code != 0 {
		t.Errorf("diff after the copy exit = %d, want 0", code)
	}
}

// caps names the comment and lyrics qualifiers a format stores.
func TestCapsListsCommentQualifiers(t *testing.T) {
	t.Parallel()
	for format, want := range map[string][]string{
		"mp3":  {"description", "language"},
		"wav":  {"description", "language"},
		"mka":  {"language"},
		"flac": {},
		"wma":  {},
	} {
		line := "  qualifiers:    none\n"
		if len(want) > 0 {
			line = "  qualifiers:    " + strings.Join(want, ", ") + " (COMMENT and LYRICS)\n"
		}
		if out, _, code := runCLI(t, "caps", "--format", format); code != 0 || !strings.Contains(out, line) {
			t.Errorf("caps --format %s exit = %d, want the line %q:\n%s", format, code, line, out)
		}
		out, _, _ := runCLI(t, "--json", "caps", "--format", format)
		var jc jsonCaps
		if err := json.Unmarshal([]byte(out), &jc); err != nil {
			t.Fatalf("caps --json: %v\n%s", err, out)
		}
		if jc.Qualifiers == nil || !slices.Equal(jc.Qualifiers, want) {
			t.Errorf("caps --json --format %s qualifiers = %#v, want %#v", format, jc.Qualifiers, want)
		}
	}
}

// A lyric added in another language is not a conflict: --strict writes it, and dump marks
// neither value.
func TestAddedTranslationIsNotAConflict(t *testing.T) {
	t.Parallel()
	path := describedFile(t, notagsMP3, describedComment, wl.Lyrics{Text: "words", Language: "eng"})
	out, errOut, code := runCLI(t, "set", "--strict", path, "--add", "LYRICS=Worte", "--lyrics-lang", "deu", "--lyrics-desc", "Translation")
	if code != 0 {
		t.Fatalf("set --strict exit = %d\n%s%s", code, out, errOut)
	}
	if out, _, _ := runCLI(t, "dump", path); strings.Contains(out, "(conflict)") || !strings.Contains(out, `Worte  (deu, "Translation")`) {
		t.Errorf("dump of a lyric and its translation:\n%s", out)
	}
	for _, tg := range dumpJSON(t, path).Tags {
		if tg.Key == "LYRICS" && tg.Cardinality != "" {
			t.Errorf("LYRICS cardinality = %q, want none", tg.Cardinality)
		}
	}
	if out, _, code := runCLI(t, "lint", path); code != 0 {
		t.Errorf("lint exit = %d\n%s", code, out)
	}
}

// The qualifier flags label the values the invocation adds, not a value the file already holds
// that happens to read the same.
func TestQualifierFlagsLeaveAnExistingEqualValueAlone(t *testing.T) {
	t.Parallel()
	path := describedFile(t, notagsMP3, wl.Comment{Text: "x", Description: "Old", Language: "eng"}, describedLyrics)
	if out, errOut, code := runCLI(t, "set", path, "--add", "COMMENT=x", "--comment-desc", "New"); code != 0 {
		t.Fatalf("set exit = %d\n%s%s", code, out, errOut)
	}
	want := []jsonTextItem{{Text: "x", Description: "Old", Language: "eng"}, {Text: "x", Description: "New", Language: "eng"}}
	if jd := dumpJSON(t, path); !slices.Equal(jd.Comments, want) {
		t.Errorf("comments = %+v, want %+v", jd.Comments, want)
	}
}

// A long description is elided in dump like a long value; JSON keeps it whole.
func TestDumpElidesALongDescription(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("d", 6000)
	path := describedFile(t, notagsMP3, wl.Comment{Text: "c", Description: long}, describedLyrics)
	if out, _, _ := runCLI(t, "dump", path); strings.Contains(out, long) || !strings.Contains(out, "…[+") {
		t.Errorf("dump did not elide a %d-byte description", len(long))
	}
	if jd := dumpJSON(t, path); len(jd.Comments) != 1 || jd.Comments[0].Description != long {
		t.Error("dump --json does not hold the whole description")
	}
}
