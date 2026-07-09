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

// TestMP4NumberNormalizationCoercionWarns covers the direct-set path: a non-canonical trkn/disk
// number (a leading zero or a sign) is stored as its 16-bit integer, so the write surfaces a
// value-coerced warning worded for a number and showing the stored value, rather than the boolean
// wording COMPILATION uses, which would leave "what did it store?" ambiguous. The copy path already
// grades 03 as lossy (WithValueReduction); this brings the write path in line with it.
func TestMP4NumberNormalizationCoercionWarns(t *testing.T) {
	base := mp4Tagged(mp4Text("\xa9nam", "T"))

	coercionMsg := func(p *wl.Plan, key tag.Key) (string, bool) {
		for _, w := range p.Report().Warnings {
			if w.Code == wl.WarnValueCoerced && slices.Contains(w.Keys, key) {
				return w.Message, true
			}
		}
		return "", false
	}

	// A leading-zero TRACKNUMBER coerces to the integer 3: the warning must be number-worded and
	// name the stored value, so the report says what is on disk.
	p, err := mustParseBytes(t, base).Edit().Set(tag.TrackNumber, "03").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if msg, ok := coercionMsg(p, tag.TrackNumber); !ok {
		t.Errorf("TRACKNUMBER=03 must warn value-coerced; got %v", p.Report().Warnings)
	} else if strings.Contains(msg, "boolean") {
		t.Errorf("TRACKNUMBER coercion must not use the boolean wording; got %q", msg)
	} else if !strings.Contains(msg, `"03"`) || !strings.Contains(msg, "stored as 3") {
		t.Errorf("TRACKNUMBER=03 coercion must name the literal and the stored integer 3; got %q", msg)
	}

	// A signed DISCNUMBER likewise coerces to its integer.
	pSigned, err := mustParseBytes(t, base).Edit().Set(tag.DiscNumber, "+2").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if msg, ok := coercionMsg(pSigned, tag.DiscNumber); !ok {
		t.Errorf("DISCNUMBER=+2 must warn value-coerced; got %v", pSigned.Report().Warnings)
	} else if strings.Contains(msg, "boolean") || !strings.Contains(msg, "stored as 2") {
		t.Errorf("DISCNUMBER=+2 coercion must be number-worded and show 2; got %q", msg)
	}

	// A canonical number stores faithfully and must not warn.
	pOK, err := mustParseBytes(t, base).Edit().Set(tag.TrackNumber, "3").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := coercionMsg(pOK, tag.TrackNumber); ok {
		t.Errorf("canonical TRACKNUMBER=3 must not warn value-coerced; got %v", pOK.Report().Warnings)
	}

	// The boolean coercion path is unchanged: COMPILATION=maybe keeps the boolean wording, so the
	// two coercion messages stay distinguishable.
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
