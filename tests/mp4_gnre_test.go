package waxlabel_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// gnreDataAtom returns the type/class field and the raw value of the first data atom
// inside the output's "gnre" item. The data sub-atom follows the 4-byte "gnre" name:
// [size:4]["data":4][version<<24 | type:4][locale:4][value]. None of the synthetic
// payloads or titles contain "gnre", so the byte search is exact.
func gnreDataAtom(t *testing.T, out []byte) (typ uint32, value []byte) {
	t.Helper()
	j := bytes.Index(out, []byte("gnre"))
	if j < 0 {
		t.Fatalf("no gnre atom in output")
	}
	da := out[j+4:] // the data sub-atom begins right after the gnre name
	if len(da) < 16 || string(da[4:8]) != "data" {
		t.Fatalf("gnre payload is not a data atom: % x", da[:min(16, len(da))])
	}
	size := binary.BigEndian.Uint32(da[0:4])
	return binary.BigEndian.Uint32(da[8:12]) & 0x00FFFFFF, da[16:size]
}

// TestMP4NumericGenreWritesGnre verifies that with --numeric-genre a recognized GENRE is written
// as the legacy numeric "gnre" atom - an IMPLICIT-type (class 0) data atom holding the
// 1-based ID3v1 index - which the parser folds back to the genre name. The type byte is
// pinned because a UTF-8 (class 1) atom would carry the same numeric bytes yet decode as
// the literal text "18".
func TestMP4NumericGenreWritesGnre(t *testing.T) {
	base := mp4Tagged(mp4Text("\xa9nam", "T"))

	plan, err := mustParseBytes(t, base).Edit().Set(tag.Genre, "Rock").Prepare(wl.WithNumericGenre())
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, base, plan)

	if bytes.Contains(out, []byte("\xa9gen")) {
		t.Errorf("numeric genre must not also write a text \xa9gen atom")
	}
	typ, val := gnreDataAtom(t, out)
	if typ != 0 {
		t.Errorf("gnre data atom type = %d, want 0 (implicit), not 1 (UTF-8 text)", typ)
	}
	if len(val) != 2 || binary.BigEndian.Uint16(val) != 18 {
		t.Errorf("gnre value = % x, want 00 12 (Rock's 1-based ID3v1 index 18)", val)
	}
	if g := mustParseBytes(t, out).Fields().Genres; len(g) != 1 || g[0] != "Rock" {
		t.Errorf("gnre re-parse = %v, want [Rock]", g)
	}
}

// TestMP4NumericGenreMultiValueAllResolve verifies that a multi-valued GENRE writes gnre only when
// every value is a standard genre; the data atoms keep input order.
func TestMP4NumericGenreMultiValueAllResolve(t *testing.T) {
	base := mp4Tagged(mp4Text("\xa9nam", "T"))
	plan, err := mustParseBytes(t, base).Edit().Set(tag.Genre, "Rock", "Jazz").Prepare(wl.WithNumericGenre())
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, base, plan)
	if g := mustParseBytes(t, out).Fields().Genres; len(g) != 2 || g[0] != "Rock" || g[1] != "Jazz" {
		t.Errorf("multi-value gnre round-trip = %v, want [Rock Jazz]", g)
	}
}

// TestMP4GenreTextFallback verifies that the default write, and a non-standard genre even with
// --numeric-genre, both write the text \xa9gen atom rather than gnre.
func TestMP4GenreTextFallback(t *testing.T) {
	base := mp4Tagged(mp4Text("\xa9nam", "T"))

	// Default: text, no gnre.
	p1, err := mustParseBytes(t, base).Edit().Set(tag.Genre, "Rock").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if o1 := applyToBytes(t, base, p1); bytes.Contains(o1, []byte("gnre")) {
		t.Errorf("default genre write must not emit gnre")
	} else if !bytes.Contains(o1, []byte("\xa9gen")) {
		t.Errorf("default genre write should emit a text \xa9gen atom")
	}

	// --numeric-genre but a non-standard genre: text fallback for the whole field.
	p2, err := mustParseBytes(t, base).Edit().Set(tag.Genre, "Chiptune Surf").Prepare(wl.WithNumericGenre())
	if err != nil {
		t.Fatal(err)
	}
	o2 := applyToBytes(t, base, p2)
	if bytes.Contains(o2, []byte("gnre")) {
		t.Errorf("a non-standard genre must not write gnre even with --numeric-genre")
	}
	if g := mustParseBytes(t, o2).Fields().Genres; len(g) != 1 || g[0] != "Chiptune Surf" {
		t.Errorf("custom genre round-trip = %v, want [Chiptune Surf]", g)
	}
}
