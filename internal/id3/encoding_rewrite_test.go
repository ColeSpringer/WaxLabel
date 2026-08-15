package id3

import (
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// tconTag builds a tag at version carrying one TCON frame per body, so a test can seed the
// stored representation directly.
func tconTag(version byte, bodies ...string) *Tag {
	frames := make([]Frame, 0, len(bodies))
	for _, b := range bodies {
		frames = append(frames, Frame{ID: "TCON", Body: encodeTextFrame(encLatin1, []string{b})})
	}
	return NewEmpty(version).WithFrames(frames)
}

func genreSet(vals ...string) tag.TagSet {
	ts := tag.NewTagSet()
	ts.Set(tag.Genre, vals...)
	return ts
}

// TestEncodingRewriteNeeded pins the predicate that lets --numeric-genre through a codec's
// no-op fast path: true only when the stored genre and the one the write would render
// differ, and false whenever there is nothing to re-encode. The false rows are the ones that
// matter most - each is a file the flag would otherwise churn on every run.
func TestEncodingRewriteNeeded(t *testing.T) {
	cases := []struct {
		name    string
		src     *Tag
		edited  tag.TagSet
		opts    WriteOpts
		want    bool
		because string
	}{
		{
			name: "nil tag", src: nil, edited: genreSet("Rock"),
			opts: WriteOpts{NumericGenre: true}, want: false,
			because: "a codec whose file carries no ID3 container has nothing stored to differ from",
		},
		{
			name: "no TCON", src: NewEmpty(3), edited: genreSet("Rock"),
			opts: WriteOpts{NumericGenre: true}, want: false,
			because: "a file with no genre gets no rewrite forced on it",
		},
		{
			name: "name stored, numeric requested", src: tconTag(3, "Rock"), edited: genreSet("Rock"),
			opts: WriteOpts{NumericGenre: true}, want: true,
			because: "the repro: the canonical value is unchanged but (17) is not Rock",
		},
		{
			name: "already numeric, v2.3", src: tconTag(3, "(17)"), edited: genreSet("Rock"),
			opts: WriteOpts{NumericGenre: true}, want: false,
			because: "idempotency: a second run must leave the file alone",
		},
		{
			name: "already numeric, v2.4", src: tconTag(4, "17"), edited: genreSet("Rock"),
			opts: WriteOpts{NumericGenre: true}, want: false,
			because: "v2.4 writes the reference bare, so the stored form already matches",
		},
		{
			name: "flag absent", src: tconTag(3, "(17)"), edited: genreSet("Rock"),
			opts: WriteOpts{}, want: false,
			because: "not passing --numeric-genre is not a request to re-encode back to the name",
		},
		{
			name: "genre cleared", src: tconTag(3, "Rock"), edited: tag.NewTagSet(),
			opts: WriteOpts{NumericGenre: true}, want: false,
			because: "a removal is a canonical change, not a re-encoding; calling it one would " +
				"label a write that deletes the genre a genre encoding rewrite",
		},
		{
			name: "genre outside the table", src: tconTag(3, "Chiptune Surf"), edited: genreSet("Chiptune Surf"),
			opts: WriteOpts{NumericGenre: true}, want: false,
			because: "nothing resolves, so the render is byte-identical to what is stored",
		},
		{
			name: "bare reference supplied by the edit", src: tconTag(3, "Rock"), edited: genreSet("17"),
			opts: WriteOpts{NumericGenre: true}, want: true,
			because: "the second repro: the value changed but re-projects to Rock, so only this " +
				"keeps the write from collapsing back to a no-op",
		},
		// The rows below are the forms the predicate must NOT touch. Each renders identically
		// with and without numeric conversion, so a rewrite would swap a reference the file
		// already holds for its plain name, or apply an unrelated escaping.
		{
			name: "special reference", src: tconTag(3, "(RX)"), edited: genreSet("Remix"),
			opts: WriteOpts{NumericGenre: true}, want: false,
			because: "RX and CR have no ID3v1 index, so the rewrite would store the literal " +
				"name Remix and destroy the reference the flag exists to prefer",
		},
		{
			name: "bare special reference", src: tconTag(3, "RX"), edited: genreSet("Remix"),
			opts: WriteOpts{NumericGenre: true}, want: false,
			because: "same as the parenthesized form",
		},
		{
			name: "out-of-range reference", src: tconTag(3, "(255)"), edited: genreSet("(255)"),
			opts: WriteOpts{NumericGenre: true}, want: false,
			because: "255 is out of range so it stays a literal; escaping it to ((255) is not " +
				"the numeric normalisation the flag asked for",
		},
		{
			name: "reference with a refinement", src: tconTag(3, "(17)Hard"), edited: genreSet("Rock", "Hard"),
			opts: WriteOpts{NumericGenre: true}, want: false,
			because: "the spec-legal v2.3 refinement form packs two canonical values into one " +
				"frame value; re-rendering would downgrade it to the nonstandard NUL-separated " +
				"extension, trading one representation problem for another",
		},
		{
			name: "two references in one value", src: tconTag(3, "(17)(8)"), edited: genreSet("Rock", "Jazz"),
			opts: WriteOpts{NumericGenre: true}, want: false,
			because: "the same compaction as the refinement form, and already numeric",
		},
		{
			name: "slash-joined pair already in form", src: tconTag(3, "Rock / Jazz"),
			edited: genreSet("Rock", "Jazz"),
			opts:   WriteOpts{NumericGenre: true, Multi: core.ID3MultiSlash}, want: false,
			because: "the join collapses two values into one frame body; comparing the value lists " +
				"unrendered would read 2 against 1 and churn a correct file",
		},
		{
			name: "repeat-frame pair already in form", src: tconTag(3, "(17)", "(8)"),
			edited: genreSet("Rock", "Jazz"),
			opts:   WriteOpts{NumericGenre: true, Multi: core.ID3MultiRepeatFrame}, want: false,
			because: "the stored side must be gathered across every TCON frame, not just the first",
		},
		{
			name: "repeat-frame pair still by name", src: tconTag(3, "Rock", "Jazz"),
			edited: genreSet("Rock", "Jazz"),
			opts:   WriteOpts{NumericGenre: true, Multi: core.ID3MultiRepeatFrame}, want: true,
			because: "the counterpart to the row above: two name frames do need re-encoding",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EncodingRewriteNeeded(c.src, c.edited, c.opts); got != c.want {
				t.Errorf("EncodingRewriteNeeded = %v, want %v: %s", got, c.want, c.because)
			}
		})
	}
}

// TestGenreReference pins what --numeric-genre converts. A genre name and a bare reference
// both reach the write version's canonical reference form, so one pass cannot leave a
// library mixing "17" and "(17)". A parenthesized value, a special reference, and a
// non-canonical integer are left for the escape and passthrough branches.
func TestGenreReference(t *testing.T) {
	cases := []struct {
		in      string
		version byte
		want    string // "" means no reference exists
	}{
		{"Rock", 3, "(17)"},
		{"Rock", 4, "17"},
		{"rock", 3, "(17)"}, // genreIndex folds case
		{"17", 3, "(17)"},   // a bare reference the edit supplied, normalized in one pass
		{"17", 4, "17"},
		{"(17)", 3, ""},          // stored as the escaped literal "((17)" by long-standing behavior
		{"RX", 3, ""},            // resolves to Remix, which has no ID3v1 index
		{"(RX)", 3, ""},          // same
		{"007", 3, ""},           // not a canonical integer, so it is a literal name
		{"-5", 3, ""},            // out of range
		{"255", 3, ""},           // out of range
		{"Chiptune Surf", 3, ""}, // not a standard genre
	}
	for _, c := range cases {
		got, ok := genreReference(c.in, c.version)
		if !ok {
			got = ""
		}
		if got != c.want {
			t.Errorf("genreReference(%q, v2.%d) = %q, want %q", c.in, c.version, got, c.want)
		}
	}
}

// A UTF-16 TCON holding the same text as a Latin-1 one must not read as a difference: both
// sides are decoded before comparison, so the text encoding cannot masquerade as an
// encoding rewrite and churn the file.
func TestEncodingRewriteNeededIgnoresTextEncoding(t *testing.T) {
	src := NewEmpty(3).WithFrames([]Frame{{ID: "TCON", Body: encodeTextFrame(encUTF16, []string{"(17)"})}})
	if EncodingRewriteNeeded(src, genreSet("Rock"), WriteOpts{NumericGenre: true}) {
		t.Error("a UTF-16 (17) already stores the requested representation; no rewrite is needed")
	}
}
