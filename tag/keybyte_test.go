package tag

import (
	"strings"
	"testing"
)

// TestKeyByteSweep sweeps every byte value, so the charset floor is pinned by
// enumeration. The floor is the intersection of every format's key syntax: the Vorbis
// comment specification stops at 0x7D, so '~' (0x7E) is out even though APEv2, ID3 TXXX
// descriptions, and MP4 freeform names accept it.
func TestKeyByteSweep(t *testing.T) {
	for b := 0x00; b <= 0xFF; b++ {
		c := byte(b)
		want := c >= 0x20 && c <= 0x7D && c != '=' && !(c >= 'a' && c <= 'z')
		// Key.Valid is the predicate itself; ParseKey wraps it in a fold and a trim, which
		// the cases below cover separately. A fixed valid prefix keeps the empty key out.
		if got := Key("X" + string(c)).Valid(); got != want {
			t.Errorf("Key(%q).Valid() = %v, want %v", "X"+string(c), got, want)
		}
	}
	// ParseKey accepts what Valid accepts, and additionally folds case and trims the ends.
	for _, c := range []struct {
		in   string
		want Key
	}{
		{"title", Title}, {"  X  ", "X"}, {"A}B", "A}B"}, {"A B", "A B"},
	} {
		if k, err := ParseKey(c.in); err != nil || k != c.want {
			t.Errorf("ParseKey(%q) = %q, %v; want %s", c.in, k, err, c.want)
		}
	}
	for _, in := range []string{"", "   ", "A~B", "A=B", "A\u00e9B"} {
		if k, err := ParseKey(in); err == nil {
			t.Errorf("ParseKey(%q) = %q, want an error", in, k)
		}
	}
}

// TestTildeKeyRejected pins '~' (0x7E) as an invalid key byte, so a change that widens the
// range back to 0x7E fails here with the reason attached.
func TestTildeKeyRejected(t *testing.T) {
	_, err := ParseKey("KEY WITH~TILDE")
	if err == nil {
		t.Fatal("'~' is not a legal Vorbis field-name byte, so it must not be a legal canonical key byte")
	}
	// The message shows a printable offender as a character, not as hex.
	if !strings.Contains(err.Error(), "'~'") {
		t.Errorf("error = %v, want it to name '~' as a character", err)
	}
	if Key("A~B").Valid() {
		t.Error("Key.Valid must agree with ParseKey about '~'")
	}
	// The byte just below it stays legal; the bound is exactly 0x7D.
	if !Key("A}B").Valid() {
		t.Error("'}' (0x7D) is a legal Vorbis field-name byte and must stay a legal key byte")
	}
}
