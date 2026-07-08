package waxlabel_test

import (
	"bytes"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// v22WithUnknownFrame builds a v2.2-tagged MP3 carrying a real title ("TT2") plus an
// unknown three-character frame "TXY" (absent from the upgrade table). On modernization
// the unknown frame is preserved under a space-padded, non-conformant "TXY " ID.
func v22WithUnknownFrame(t *testing.T) []byte {
	t.Helper()
	return append(id3v2(2,
		textFrame22("TT2", "V22 Title"),
		textFrame22("TXY", "unknown-v22-payload"),
	), mp3Audio(t)...)
}

// TestMP3V22UnknownFrameNoPhantom is a regression guard: an unknown ID3v2.2 frame is
// preserved under a space-padded "TXY " ID when the tag is modernized to v2.3, but that
// non-conformant ID must never surface as a phantom canonical tag - not on the first
// read, not on re-read of the written file, and not in the plan preview (which must
// equal a fresh re-parse). The frame's bytes are still kept verbatim.
func TestMP3V22UnknownFrameNoPhantom(t *testing.T) {
	data := v22WithUnknownFrame(t)

	// First read: the unknown frame is opaque, so it is not projected as a tag.
	doc := mustParseBytes(t, data)
	if v, ok := doc.Get(tag.Key("TXY")); ok {
		t.Fatalf("unknown v2.2 frame surfaced as canonical TXY=%v on first read", v)
	}

	// An unrelated edit modernizes the tag to v2.3 and rewrites the frame region.
	plan, err := doc.Edit().Set(tag.Album, "V22 Album").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	// The plan preview must not invent a TXY change: the preview is not a separate guess,
	// it equals a fresh re-parse of the output.
	for _, c := range plan.Changes() {
		if c.Key == tag.Key("TXY") {
			t.Errorf("plan preview invented a phantom TXY change: %v", c)
		}
	}
	out := applyToBytes(t, data, plan)

	// The preserved frame's bytes survive verbatim.
	if !bytes.Contains(out, []byte("unknown-v22-payload")) {
		t.Error("the unknown v2.2 frame body was not preserved on modernization")
	}

	// Re-read: "TXY " now decodes as a normal (non-opaque) frame, yet it must still not
	// surface as a phantom canonical tag - this is the projection gate.
	re := mustParseBytes(t, out)
	if v, ok := re.Get(tag.Key("TXY")); ok {
		t.Errorf("phantom canonical TXY=%v appeared on re-read of the modernized tag", v)
	}
	if re.Fields().Title != "V22 Title" || re.Fields().Album != "V22 Album" {
		t.Errorf("modernized tag lost real tags: title=%q album=%q", re.Fields().Title, re.Fields().Album)
	}
}

// TestMP3V22UnknownFrameIdempotent confirms the preserved frame is stable once it is a
// normal non-opaque frame in the modernized file: a no-op write is byte-identical, and a
// second real edit keeps the frame verbatim without minting a phantom.
func TestMP3V22UnknownFrameIdempotent(t *testing.T) {
	data := v22WithUnknownFrame(t)
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Album, "V22 Album").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	doc := mustParseBytes(t, out)

	// A no-op edit on the modernized file writes byte-identical output.
	noop, err := doc.Edit().Set(tag.Album, "V22 Album").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !noop.IsNoOp() {
		t.Error("re-setting an unchanged tag on the modernized file should be a no-op")
	}
	if again := applyToBytes(t, out, noop); !bytes.Equal(again, out) {
		t.Error("a no-op write on the modernized file must be byte-identical")
	}

	// A second, real edit keeps the preserved frame and adds no phantom.
	plan2, err := doc.Edit().Set(tag.Artist, "New Artist").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out2 := applyToBytes(t, out, plan2)
	if !bytes.Contains(out2, []byte("unknown-v22-payload")) {
		t.Error("a chained edit dropped the preserved unknown frame")
	}
	re := mustParseBytes(t, out2)
	if _, ok := re.Get(tag.Key("TXY")); ok {
		t.Error("a chained edit surfaced a phantom TXY")
	}
	if a := re.Fields().Artists; len(a) != 1 || a[0] != "New Artist" {
		t.Errorf("chained edit lost the new artist: %v", a)
	}
}

// TestMP3V22UnknownFrameKeyCollision exercises the write.go gates: setting the canonical
// key TXY (which renders as a TXXX:TXY frame) on the modernized file must keep the
// preserved "TXY " frame AND write the new value - neither clobbers the other. Without
// the frameKeys conformance gate the rebuilder treats the preserved "TXY " frame as a
// stale representation of canonical TXY and drops it.
func TestMP3V22UnknownFrameKeyCollision(t *testing.T) {
	data := v22WithUnknownFrame(t)
	modernize, err := mustParseBytes(t, data).Edit().Set(tag.Album, "V22 Album").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, modernize)

	plan, err := mustParseBytes(t, out).Edit().Set(tag.Key("TXY"), "collide").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out2 := applyToBytes(t, out, plan)

	// The preserved unknown-frame bytes must survive the key collision.
	if !bytes.Contains(out2, []byte("unknown-v22-payload")) {
		t.Error("--set TXY dropped the preserved unknown v2.2 frame")
	}
	// The canonical TXY value is written (via TXXX) and reads back.
	re := mustParseBytes(t, out2)
	if v, ok := re.Get(tag.Key("TXY")); !ok || len(v) != 1 || v[0] != "collide" {
		t.Errorf("canonical TXY = %v (present=%v), want [collide]", v, ok)
	}
}
