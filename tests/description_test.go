package waxlabel_test

import (
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestVorbisDescriptionDistinctFromComment protects against merging native COMMENT and
// DESCRIPTION into one canonical field. A mapping-level round-trip can miss that case,
// so this uses one document carrying both fields and edits only COMMENT.
func TestVorbisDescriptionDistinctFromComment(t *testing.T) {
	streamInfo := make([]byte, 34)
	streamInfo[0], streamInfo[1] = 0x10, 0x00
	streamInfo[2], streamInfo[3] = 0x10, 0x00
	streamInfo[10] = 0x0A
	streamInfo[11] = 0xC4
	streamInfo[12] = 0x40 | (1 << 1)
	streamInfo[13] = 15 << 4

	block := func(code byte, last bool, body []byte) []byte {
		h := []byte{code, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
		if last {
			h[0] |= 0x80
		}
		return append(h, body...)
	}

	// No fixture carries both native fields, so synthesize one using the suite's Vorbis
	// comment renderer.
	vorbis := renderVC("COMMENT=original comment", "DESCRIPTION=the blurb")
	data := []byte("fLaC")
	data = append(data, block(0, false, streamInfo)...) // STREAMINFO
	data = append(data, block(4, false, vorbis)...)     // VORBIS_COMMENT
	data = append(data, block(1, true, make([]byte, 4))...)
	data = append(data, []byte{0xFF, 0xF8}...) // pretend audio frame

	doc := mustParseBytes(t, data)
	if got, _ := doc.Tags().Get(tag.Comment); len(got) != 1 || got[0] != "original comment" {
		t.Fatalf("COMMENT on parse = %v, want [original comment]", got)
	}
	if got, _ := doc.Tags().Get(tag.Description); len(got) != 1 || got[0] != "the blurb" {
		t.Fatalf("DESCRIPTION on parse = %v, want [the blurb] (must not fold into COMMENT)", got)
	}

	// Edit only COMMENT; DESCRIPTION must be untouched.
	plan, err := doc.Edit().Set(tag.Comment, "edited comment").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	re := mustParseBytes(t, out)
	if got, _ := re.Tags().Get(tag.Comment); len(got) != 1 || got[0] != "edited comment" {
		t.Errorf("COMMENT after edit = %v, want [edited comment]", got)
	}
	if got, _ := re.Tags().Get(tag.Description); len(got) != 1 || got[0] != "the blurb" {
		t.Errorf("DESCRIPTION after editing only COMMENT = %v, want [the blurb] (data loss!)", got)
	}
}
