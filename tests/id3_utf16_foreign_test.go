package waxlabel_test

import (
	"slices"
	"testing"
	"unicode/utf16"

	"github.com/colespringer/waxlabel/tag"
)

// utf16LENoBOM encodes s as little-endian UTF-16 code units without a byte-order mark.
func utf16LENoBOM(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return b
}

// id3FrameRaw builds an ID3v2.4 frame with an already-encoded body (sync-safe size, no
// flags), letting a test plant raw bytes a normal text-frame helper would never emit.
func id3FrameRaw(id string, body []byte) []byte {
	out := append([]byte(id), syncsafe(len(body))...)
	out = append(out, 0, 0) // flags
	return append(out, body...)
}

// TestID3ForeignUTF16SurvivesChainedEdit covers a foreign frame whose first UTF-16 value
// has a little-endian BOM and whose later value omits it. An unrelated edit preserves the
// TPE1 frame bytes, so the re-parse exercises the same foreign input a second time.
func TestID3ForeignUTF16SurvivesChainedEdit(t *testing.T) {
	// A multi-value TPE1: first value LE BOM, second value BOM-less LE (must inherit LE).
	tpe1 := []byte{1} // encUTF16
	tpe1 = append(tpe1, 0xFF, 0xFE)
	tpe1 = append(tpe1, utf16LENoBOM("Björk")...)
	tpe1 = append(tpe1, 0x00, 0x00)
	tpe1 = append(tpe1, utf16LENoBOM("Múm")...)

	data := append(id3v2(4, id3FrameRaw("TPE1", tpe1), textFrame(4, "TIT2", "Orig")), mp3Audio(t)...)

	doc := mustParseBytes(t, data)
	want := []string{"Björk", "Múm"}
	if got := doc.Fields().Artists; !slices.Equal(got, want) {
		t.Fatalf("artists = %q, want %q (the BOM-less second value must inherit LE, not byte-swap)", got, want)
	}

	// Chained in-memory edit of an unrelated field: the TPE1 frame is preserved verbatim,
	// so re-parsing decodes the foreign UTF-16 bytes a second time.
	out := applyToBytes(t, data, mustPlan(t, doc.Edit().Set(tag.Title, "New")))
	re := mustParseBytes(t, out)
	if got := re.Fields().Artists; !slices.Equal(got, want) {
		t.Errorf("after the unrelated edit, artists = %q, want %q preserved", got, want)
	}
	if got, _ := re.Get(tag.Title); !slices.Equal(got, []string{"New"}) {
		t.Errorf("after the edit, TITLE = %q, want [New]", got)
	}
}
