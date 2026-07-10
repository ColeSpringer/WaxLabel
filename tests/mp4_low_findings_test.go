package waxlabel_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestMP4MultiValueInteropNote checks that a multi-valued text field on MP4 surfaces the
// informational mp4-multi-value note (the iTunes ilst stores it as several data atoms, which many
// readers show only the first of), round-trips all values, and does not escalate --strict (it is
// informational, nothing is lost). A single-valued field and a structured slot do not warn.
func TestMP4MultiValueInteropNote(t *testing.T) {
	src := readFixture(t, "../testdata/notags.m4a")

	// A multi-valued ARTIST surfaces the note, keyed on ARTIST, and round-trips both values.
	plan, err := mustParseBytes(t, src).Edit().Set(tag.Artist, "A", "B").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	noted := false
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnMP4MultiValue && slices.Contains(w.Keys, tag.Artist) {
			noted = true
		}
	}
	if !noted {
		t.Errorf("a multi-valued MP4 ARTIST must surface mp4-multi-value; got %v", plan.Report().Warnings)
	}
	re := mustParseBytes(t, applyToBytes(t, src, plan))
	if got, _ := re.Tags().Get(tag.Artist); len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Errorf("round-trip ARTIST = %v, want [A B] (all values written)", got)
	}

	// A single-valued field does not warn.
	pSingle, err := mustParseBytes(t, src).Edit().Set(tag.Artist, "Solo").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range pSingle.Report().Warnings {
		if w.Code == wl.WarnMP4MultiValue {
			t.Errorf("a single-valued ARTIST must not warn mp4-multi-value; got %v", pSingle.Report().Warnings)
		}
	}

	notesKey := func(plan *wl.Plan, key tag.Key) bool {
		for _, w := range plan.Report().Warnings {
			if w.Code == wl.WarnMP4MultiValue && slices.Contains(w.Keys, key) {
				return true
			}
		}
		return false
	}

	// A multi-valued custom (freeform ----) key also writes one data atom per value, so it warns too.
	custom := tag.MustKey("CUSTOMTAG")
	pFree, err := mustParseBytes(t, src).Edit().Set(custom, "A", "B").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !notesKey(pFree, custom) {
		t.Errorf("a multi-valued freeform key must surface mp4-multi-value; got %v", pFree.Report().Warnings)
	}

	// A multi-valued numeric genre writes one gnre atom per genre, so it warns as well.
	pGnre, err := mustParseBytes(t, src).Edit().Set(tag.Genre, "Rock", "Jazz").Prepare(wl.WithNumericGenre())
	if err != nil {
		t.Fatal(err)
	}
	if !notesKey(pGnre, tag.Genre) {
		t.Errorf("a multi-valued numeric genre must surface mp4-multi-value; got %v", pGnre.Report().Warnings)
	}

	// The note is authored-scoped: an unrelated edit on a file that ALREADY holds a multi-valued
	// field must not warn about that untouched field, so a plain edit does not emit interop noise.
	withMulti := applyToBytes(t, src, plan) // the file now stores ARTIST=[A,B]
	pUnrelated, err := mustParseBytes(t, withMulti).Edit().Set(tag.Title, "New Title").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range pUnrelated.Report().Warnings {
		if w.Code == wl.WarnMP4MultiValue {
			t.Errorf("an unrelated edit must not warn about a pre-existing multi-valued field; got %v", pUnrelated.Report().Warnings)
		}
	}
	// But re-authoring the multi-valued field (adding a third value) does warn again.
	pReauthor, err := mustParseBytes(t, withMulti).Edit().Set(tag.Artist, "A", "B", "C").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !notesKey(pReauthor, tag.Artist) {
		t.Errorf("re-authoring a multi-valued ARTIST must warn; got %v", pReauthor.Report().Warnings)
	}
}

// TestMP4TrackNumberZeroWarns checks the MP4-specific TRACKNUMBER=0 case. decodePair drops
// a 0 slot on read (its num>0/total>0 guards treat 0 as unset), so a user's 0 never round-trips
// and the write must warn - even when the pair does not collapse: 0 paired with a real total
// still loses the 0 on read (0/12 reads back as total-only), while the representable total is
// not flagged.
func TestMP4TrackNumberZeroWarns(t *testing.T) {
	base := mp4Tagged(mp4Text("\xa9nam", "T"))
	msgFor := func(p *wl.Plan, key tag.Key) (string, bool) {
		for _, w := range p.Report().Warnings {
			if w.Code == wl.WarnValueDropped && slices.Contains(w.Keys, key) {
				return w.Message, true
			}
		}
		return "", false
	}
	dropped := func(p *wl.Plan, key tag.Key) bool {
		_, ok := msgFor(p, key)
		return ok
	}

	p, err := mustParseBytes(t, base).Edit().Set(tag.TrackNumber, "0").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	// A 0 slot is written (0/N) but read back as unset, so the warning must say the value
	// reads back as absent, not the hard-rejection "cannot be represented".
	if msg, ok := msgFor(p, tag.TrackNumber); !ok {
		t.Errorf("TRACKNUMBER=0 must warn value-dropped (a 0 slot is dropped on read); got %v", p.Report().Warnings)
	} else if !strings.Contains(msg, "reads back as absent") || strings.Contains(msg, "cannot be represented") {
		t.Errorf("TRACKNUMBER=0 warning should say the 0 reads back as absent, not that it cannot be represented; got %q", msg)
	}

	p2, err := mustParseBytes(t, base).Edit().Set(tag.TrackNumber, "0").Set(tag.TrackTotal, "12").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !dropped(p2, tag.TrackNumber) {
		t.Errorf("TRACKNUMBER=0 with TRACKTOTAL=12 still loses the 0 on read; must warn; got %v", p2.Report().Warnings)
	}
	if dropped(p2, tag.TrackTotal) {
		t.Errorf("TRACKTOTAL=12 is representable and must not warn; got %v", p2.Report().Warnings)
	}

	// An overflow (not a 0) is a genuinely unrepresentable value: it keeps the "cannot be
	// represented ... was dropped" wording, so the two drop reasons stay distinguishable.
	p3, err := mustParseBytes(t, base).Edit().Set(tag.TrackNumber, "70000").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if msg, ok := msgFor(p3, tag.TrackNumber); !ok {
		t.Errorf("TRACKNUMBER=70000 must warn value-dropped; got %v", p3.Report().Warnings)
	} else if !strings.Contains(msg, "cannot be represented") {
		t.Errorf("TRACKNUMBER=70000 (uint16 overflow) should keep the 'cannot be represented' wording; got %q", msg)
	}
}

// TestMP4CompilationCoercionWarns verifies that COMPILATION is a single boolean byte (cpil), so a
// non-boolean value is coerced to false and written (0) rather than dropped. The write must
// surface a value-coerced warning naming the key - the honest disposition, since the key does land
// on disk - rather than the old value-dropped, which contradicted the change set showing ["0"]. A
// recognized boolean spelling stores faithfully and must not warn.
func TestMP4CompilationCoercionWarns(t *testing.T) {
	base := mp4Tagged(mp4Text("\xa9nam", "T"))

	hasCoerced := func(p *wl.Plan) bool {
		for _, w := range p.Report().Warnings {
			if w.Code == wl.WarnValueCoerced && slices.Contains(w.Keys, tag.Compilation) {
				return true
			}
		}
		return false
	}

	for _, v := range []string{"2", "maybe"} {
		p, err := mustParseBytes(t, base).Edit().Set(tag.Compilation, v).Prepare()
		if err != nil {
			t.Fatalf("COMPILATION=%q: %v", v, err)
		}
		if !hasCoerced(p) {
			t.Errorf("non-boolean COMPILATION=%q must warn value-coerced; got %v", v, p.Report().Warnings)
		}
	}
	for _, v := range []string{"1", "true", "0", "no"} {
		p, err := mustParseBytes(t, base).Edit().Set(tag.Compilation, v).Prepare()
		if err != nil {
			t.Fatalf("COMPILATION=%q: %v", v, err)
		}
		if hasCoerced(p) {
			t.Errorf("recognized boolean COMPILATION=%q must not warn value-coerced; got %v", v, p.Report().Warnings)
		}
	}

	// No-op preservation: on a file already cpil=0, COMPILATION=maybe coerces to the same 0,
	// so the write is a byte-identical no-op - yet the coercion warning must still surface (the
	// DowngradeNoOp preserve-list), or the silent normalization would vanish at exit 0.
	cpil0 := mp4Tagged(mp4Text("\xa9nam", "T"), mp4Atom("cpil", mp4Data(21, []byte{0})))
	p, err := mustParseBytes(t, cpil0).Edit().Set(tag.Compilation, "maybe").Prepare()
	if err != nil {
		t.Fatalf("COMPILATION=maybe on cpil=0: %v", err)
	}
	if !p.IsNoOp() {
		t.Errorf("COMPILATION=maybe on a cpil=0 file should be a no-op (maybe -> 0 == existing 0)")
	}
	if !hasCoerced(p) {
		t.Errorf("value-coerced warning must survive the no-op downgrade; got %v", p.Report().Warnings)
	}
}

// TestMP4NumberNormalizationNotCoerced covers the direct-set path: a non-canonical trkn/disk number
// (a leading zero or a sign) is stored as its 16-bit integer, but the leading zero or sign is a
// numerically-lossless canonicalization, so the write does NOT surface a value-coerced warning for
// it (a copy grades it Carried and diff treats it as no change). The boolean COMPILATION coercion,
// which genuinely stores a fabricated value, still warns.
func TestMP4NumberNormalizationNotCoerced(t *testing.T) {
	base := mp4Tagged(mp4Text("\xa9nam", "T"))

	coercionMsg := func(p *wl.Plan, key tag.Key) (string, bool) {
		for _, w := range p.Report().Warnings {
			if w.Code == wl.WarnValueCoerced && slices.Contains(w.Keys, key) {
				return w.Message, true
			}
		}
		return "", false
	}

	// A leading-zero TRACKNUMBER and a signed DISCNUMBER store as their integers with no warning.
	for _, c := range []struct {
		key tag.Key
		val string
	}{
		{tag.TrackNumber, "03"},
		{tag.DiscNumber, "+2"},
		{tag.TrackNumber, "3"},
	} {
		p, err := mustParseBytes(t, base).Edit().Set(c.key, c.val).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := coercionMsg(p, c.key); ok {
			t.Errorf("%s=%q must not warn value-coerced (numeric canonicalization is not a loss); got %v", c.key, c.val, p.Report().Warnings)
		}
	}

	// The boolean coercion path is unchanged: COMPILATION=maybe still warns, worded for a boolean.
	pBool, err := mustParseBytes(t, base).Edit().Set(tag.Compilation, "maybe").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if msg, ok := coercionMsg(pBool, tag.Compilation); !ok {
		t.Errorf("COMPILATION=maybe must warn value-coerced; got %v", pBool.Report().Warnings)
	} else if !strings.Contains(msg, "boolean") {
		t.Errorf("COMPILATION coercion must keep the boolean wording; got %q", msg)
	}
}

// TestMP4TruncatedMdatOverrunsTrailingMoov verifies that a final mdat whose declared size runs
// past EOF is clamped, swallowing whatever follows it. When a moov sits after such an
// mdat the parser never sees it, so the failure must be reported as truncation (the real
// cause) rather than the misleading "no moov box".
func TestMP4TruncatedMdatOverrunsTrailingMoov(t *testing.T) {
	ftyp := mp4Ftyp()
	moov := mp4Moov(nil, 0) // valid, but it will be swallowed by the over-declared mdat
	content := []byte{0xA7, 0xA7}
	// Declare the mdat larger than the bytes that actually remain (content + moov),
	// so it overruns EOF and the trailing moov falls inside its clamped extent.
	declared := 8 + len(content) + len(moov) + 100
	mdat := append(mp4be32(declared), []byte("mdat")...)
	mdat = append(mdat, content...)
	data := slices.Concat(ftyp, mdat, moov)

	_, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if err == nil {
		t.Fatal("expected a parse error for a truncated mdat overrunning the moov")
	}
	if !errors.Is(err, waxerr.ErrInvalidData) || !strings.Contains(err.Error(), "truncat") {
		t.Errorf("error = %v, want a truncation diagnostic (not a bare 'no moov box')", err)
	}
}
