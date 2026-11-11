package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
)

// unreadOwnedFLAC holds a chapter and synced lyrics that read, beside comments of both kinds that
// do not: a time that does not parse with its title, and an untimed SYNCEDLYRICS.
func unreadOwnedFLAC(t *testing.T) string {
	t.Helper()
	return writeTempFile(t, "unread.flac", testfile.FLAC(testfile.CommentBlock("test",
		"TITLE=x",
		"CHAPTER001=bogus", "CHAPTER001NAME=Intro",
		"CHAPTER002=00:00:05.000", "CHAPTER002NAME=Verse",
		"SYNCEDLYRICS=[00:01.00]sung", "SYNCEDLYRICS=just words",
		"METADATA_BLOCK_PICTURE=not~base64",
	), testfile.FLACBlock(1, make([]byte, 1024))))
}

// TestClearFlagsRemoveUnreadOwnedComments: dump reports the comments nothing reads, and the clear
// of their kind or of their name writes the file without them.
func TestClearFlagsRemoveUnreadOwnedComments(t *testing.T) {
	t.Parallel()
	if out, _, _ := runCLI(t, "dump", unreadOwnedFLAC(t)); strings.Count(out, "[malformed-tag-entry]") != 2 {
		t.Errorf("dump should report the unread chapter and lyrics comments:\n%s", out)
	}
	for _, tc := range []struct {
		args []string
		gone string
		note string // the flag a --clear of the name is pointed to
	}{
		{[]string{"--clear-chapters"}, "CHAPTER001=bogus", ""},
		{[]string{"--clear", "CHAPTER001"}, "CHAPTER001=bogus", "--clear-chapters"},
		{[]string{"--clear", "chapter001name"}, "CHAPTER001NAME=Intro", "--clear-chapters"},
		{[]string{"--clear-synced-lyrics"}, "SYNCEDLYRICS=just words", ""},
		{[]string{"--clear", "SYNCEDLYRICS"}, "SYNCEDLYRICS=just words", "--clear-synced-lyrics"},
		{[]string{"--remove-pictures"}, "METADATA_BLOCK_PICTURE=not~base64", ""},
		{[]string{"--clear", "METADATA_BLOCK_PICTURE"}, "METADATA_BLOCK_PICTURE=not~base64", "--remove-pictures"},
		{[]string{"--clear-chapters", "--add-chapter", "0:02=New"}, "CHAPTER001=bogus", ""},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			f := unreadOwnedFLAC(t)
			out, errb, code := runCLI(t, append([]string{"set", f}, tc.args...)...)
			if code != 0 {
				t.Fatalf("exit %d\n%s\n%s", code, out, errb)
			}
			if !strings.Contains(out, "unread ") || strings.Contains(out+errb, "malformed-tag-entry-dropped") {
				t.Errorf("the plan should list the removal as an operation, with no warning:\n%s\n%s", out, errb)
			}
			// The note says what the name holds in FLAC and Ogg and which flag clears that, and
			// that elsewhere the name is a custom field.
			if strings.Contains(errb, "note:") != (tc.note != "") || !strings.Contains(errb, tc.note) ||
				tc.note != "" && !strings.Contains(errb, "FLAC and Ogg") {
				t.Errorf("want a note naming %q:\n%s", tc.note, errb)
			}
			if data, err := os.ReadFile(f); err != nil || bytes.Contains(data, []byte(tc.gone)) {
				t.Errorf("%q is still in the file (read error %v)", tc.gone, err)
			}
		})
	}
}

// TestChapterReplacementWarnsAboutUnreadComments: chapters added over comments nothing read drop
// them with a warning, which --strict refuses.
func TestChapterReplacementWarnsAboutUnreadComments(t *testing.T) {
	t.Parallel()
	out, errb, code := runCLI(t, "set", unreadOwnedFLAC(t), "--add-chapter", "0:02=New")
	if code != 0 || !strings.Contains(out+errb, "[malformed-tag-entry-dropped]") {
		t.Errorf("exit %d, want 0 with a malformed-tag-entry-dropped warning:\n%s\n%s", code, out, errb)
	}
	f := unreadOwnedFLAC(t)
	before, _ := os.ReadFile(f)
	if out, errb, code := runCLI(t, "set", "--strict", f, "--add-chapter", "0:02=New"); code != 2 {
		t.Errorf("--strict: exit %d, want 2\n%s\n%s", code, out, errb)
	}
	if after, _ := os.ReadFile(f); !bytes.Equal(before, after) {
		t.Error("--strict refused but the file changed")
	}
}

// TestClearNoteForAnOwnedNameElsewhere: outside FLAC and Ogg such a name is a custom field, which
// the clear removes and the note says.
func TestClearNoteForAnOwnedNameElsewhere(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, sampleMP3)
	if _, errb, code := runCLI(t, "set", f, "--set", "CHAPTER001=mine"); code != 0 {
		t.Fatalf("seeding: exit %d, %s", code, errb)
	}
	_, errb, code := runCLI(t, "set", f, "--clear", "CHAPTER001")
	if code != 0 || !strings.Contains(errb, "custom field") {
		t.Errorf("exit %d, want a note that says the name is a custom field elsewhere:\n%s", code, errb)
	}
	if out, _, _ := runCLI(t, "dump", f); strings.Contains(out, "CHAPTER001") {
		t.Errorf("the custom field is still there:\n%s", out)
	}
}
