package id3

import (
	"encoding/binary"
	"slices"
	"testing"
	"unicode/utf16"
)

// leBOM is the UTF-16 little-endian byte-order mark.
var leBOM = []byte{0xFF, 0xFE}

// rawUTF16LE encodes s as little-endian UTF-16 code units without a byte-order mark. Some
// foreign frames use this form after a first BOM-bearing string.
func rawUTF16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return b
}

// TestUTF16OrderResolve checks the byte-order rules directly: default big-endian, each BOM
// applies to its own string, and BOM-less strings inherit the most recent BOM.
func TestUTF16OrderResolve(t *testing.T) {
	le := append(slices.Clone(leBOM), 0x41, 0x00) // FF FE 'A'
	be := []byte{0xFE, 0xFF, 0x00, 0x41}          // BE BOM + 'A'
	none := []byte{0x41, 0x00}                    // no BOM
	cases := []struct {
		name string
		seq  [][]byte
		want []bool // littleEndian decision per string, in order
	}{
		{"default is big-endian", [][]byte{none}, []bool{false}},
		{"no-BOM then LE-BOM", [][]byte{none, le}, []bool{false, true}},
		{"LE-BOM carries to a later BOM-less string", [][]byte{le, none}, []bool{true, true}},
		{"each BOM wins for its own string", [][]byte{le, be}, []bool{true, false}},
		{"BE-BOM then BOM-less stays big-endian", [][]byte{be, none}, []bool{false, false}},
	}
	for _, c := range cases {
		o := &utf16Order{}
		for i, s := range c.seq {
			if got := o.resolve(s); got != c.want[i] {
				t.Errorf("%s: resolve #%d = %v, want %v", c.name, i, got, c.want[i])
			}
		}
	}
}

// TestDecodeTextFrameUTF16OrderCarries covers a multi-value UTF-16 text frame whose first
// value has a little-endian BOM and whose later value omits it.
func TestDecodeTextFrameUTF16OrderCarries(t *testing.T) {
	body := []byte{encUTF16}
	body = append(body, leBOM...)
	body = append(body, rawUTF16LE("Köln")...) // first value: LE BOM
	body = append(body, 0x00, 0x00)            // value terminator
	body = append(body, rawUTF16LE("café")...) // second value: no BOM, must inherit LE

	got := decodeStrings(encUTF16, body[1:])
	if want := []string{"Köln", "café"}; !slices.Equal(got, want) {
		t.Errorf("decoded values = %q, want %q (the BOM-less second value must inherit LE)", got, want)
	}
}

// TestDecodeCommentUTF16OrderCarries covers a COMM frame whose descriptor carries the BOM
// and whose comment value omits it.
func TestDecodeCommentUTF16OrderCarries(t *testing.T) {
	body := []byte{encUTF16, 'e', 'n', 'g'}
	body = append(body, leBOM...)
	body = append(body, rawUTF16LE("Liner")...) // descriptor: LE BOM
	body = append(body, 0x00, 0x00)
	body = append(body, rawUTF16LE("café note")...) // value: no BOM

	desc, vals, ok := decodeCommentFrame(body)
	if !ok {
		t.Fatal("decodeCommentFrame failed on a well-formed frame")
	}
	if desc != "Liner" {
		t.Errorf("descriptor = %q, want %q", desc, "Liner")
	}
	if want := []string{"café note"}; !slices.Equal(vals, want) {
		t.Errorf("comment values = %q, want %q (BOM-less value must inherit the descriptor's LE)", vals, want)
	}
}

// TestDecodeLangTextUTF16OrderCarries covers a USLT frame whose descriptor carries the BOM
// and whose lyric text omits it.
func TestDecodeLangTextUTF16OrderCarries(t *testing.T) {
	body := []byte{encUTF16, 'e', 'n', 'g'}
	body = append(body, leBOM...)
	body = append(body, rawUTF16LE("Refrain")...) // descriptor: LE BOM
	body = append(body, 0x00, 0x00)
	body = append(body, rawUTF16LE("première ligne")...) // text: no BOM

	desc, text, ok := decodeLangText(body)
	if !ok {
		t.Fatal("decodeLangText failed on a well-formed frame")
	}
	if desc != "Refrain" {
		t.Errorf("descriptor = %q, want %q", desc, "Refrain")
	}
	if text != "première ligne" {
		t.Errorf("lyric text = %q, want %q (BOM-less text must inherit the descriptor's LE)", text, "première ligne")
	}
}

// TestDecodeSYLTUTF16OrderCarries covers a SYLT frame with one BOM-bearing descriptor and
// BOM-less timed lines.
func TestDecodeSYLTUTF16OrderCarries(t *testing.T) {
	sl, _, ok := decodeSYLT(syltLEDescBOMLessLines())
	if !ok {
		t.Fatal("decodeSYLT failed on a well-formed frame")
	}
	if sl.Description != "Desc" {
		t.Errorf("descriptor = %q, want %q", sl.Description, "Desc")
	}
	got := make([]string, len(sl.Lines))
	for i, ln := range sl.Lines {
		got[i] = ln.Text
	}
	if want := []string{"First café", "Second"}; !slices.Equal(got, want) {
		t.Errorf("line texts = %q, want %q (BOM-less lines must inherit the descriptor's LE)", got, want)
	}
}

// syltLEDescBOMLessLines hand-builds a SYLT body whose descriptor carries a little-endian
// BOM while its lines do not. buildSYLT cannot produce this shape because encodeString
// writes a BOM on every UTF-16 string.
func syltLEDescBOMLessLines() []byte {
	out := []byte{encUTF16, 'e', 'n', 'g', syltFmtMillis, syltContentLyrics}
	out = append(out, leBOM...)
	out = append(out, rawUTF16LE("Desc")...) // descriptor: LE BOM
	out = append(out, 0x00, 0x00)
	lines := []struct {
		text string
		ms   uint32
	}{
		{"\nFirst café", 1000}, // leading "\n" is the conventional line marker, stripped on read
		{"\nSecond", 2000},
	}
	for _, ln := range lines {
		out = append(out, rawUTF16LE(ln.text)...) // no BOM: must inherit LE from the descriptor
		out = append(out, 0x00, 0x00)
		var ts [4]byte
		binary.BigEndian.PutUint32(ts[:], ln.ms)
		out = append(out, ts[:]...)
	}
	return out
}
