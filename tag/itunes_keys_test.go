package tag

import (
	"slices"
	"testing"
)

// TestValidMP4IntValue checks the unsigned MP4-integer contract: a non-negative decimal
// integer within the key's atom width, whitespace-tolerant, with ParseUint's leading-'+'
// rejection intended (the atom stores an unsigned magnitude with no sign to round-trip).
func TestValidMP4IntValue(t *testing.T) {
	for _, v := range []string{"0", "1", "2", "4", "255", " 2 "} {
		if !ValidMP4IntValue(ITunesAdvisory, v) {
			t.Errorf("ValidMP4IntValue(ITUNESADVISORY, %q) = false, want true", v)
		}
	}
	for _, v := range []string{"256", "-1", "+3", "abc", "1.5", ""} {
		if ValidMP4IntValue(ITunesAdvisory, v) {
			t.Errorf("ValidMP4IntValue(ITUNESADVISORY, %q) = true, want false", v)
		}
	}
	// The movement atoms are two bytes wide.
	for _, k := range []Key{Movement, MovementTotal} {
		if !ValidMP4IntValue(k, "65535") {
			t.Errorf("ValidMP4IntValue(%s, 65535) = false, want true", k)
		}
		if ValidMP4IntValue(k, "65536") {
			t.Errorf("ValidMP4IntValue(%s, 65536) = true, want false", k)
		}
	}
	// A non-member key is never flagged.
	if !ValidMP4IntValue(Title, "abc") {
		t.Error("a non-MP4-integer key should never be flagged")
	}
	// The public MediaType wrapper keeps its contract.
	if !ValidMediaTypeValue(MediaType, "2") || ValidMediaTypeValue(MediaType, "256") {
		t.Error("ValidMediaTypeValue changed behavior for MEDIATYPE")
	}
	if !ValidMediaTypeValue(Title, "not a number") {
		t.Error("ValidMediaTypeValue should never flag a non-MediaType key")
	}
}

// TestValidBPMValue checks the BPM contract: a non-negative decimal no greater than 65535,
// fractions included, rejecting the scientific/hex/underscored forms ParseFloat alone would
// accept and any sign (the tmpo atom is unsigned).
func TestValidBPMValue(t *testing.T) {
	for _, v := range []string{"128", "0", "65535", "174.99", "174.0", " 128 "} {
		if !ValidBPMValue(BPM, v) {
			t.Errorf("ValidBPMValue(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"65536", "65535.4", "-1", "+3", "abc", "1.2.3", "1e3", "0x1p7", "1_0", "", "."} {
		if ValidBPMValue(BPM, v) {
			t.Errorf("ValidBPMValue(%q) = true, want false", v)
		}
	}
	if !ValidBPMValue(Title, "1e3") {
		t.Error("a non-BPM key should never be flagged")
	}
}

// TestITunesKeyContracts pins the predicate memberships and validator categories for the
// iTunes structured keys, so the linter, set-time note, trim gate, and diff fold all agree
// on how each key is classified.
func TestITunesKeyContracts(t *testing.T) {
	wantLintCode := map[Key]string{
		ITunesAdvisory: "malformed-number",
		Movement:       "malformed-number",
		MovementTotal:  "malformed-number",
		BPM:            "malformed-number",
		ITunesGapless:  "malformed-boolean",
		ShowMovement:   "malformed-boolean",
	}
	for k, code := range wantLintCode {
		val, ok := ValidatorFor(k)
		if !ok {
			t.Errorf("ValidatorFor(%s) reports no contract, want %s", k, code)
			continue
		}
		if val.LintCode != code {
			t.Errorf("ValidatorFor(%s).LintCode = %q, want %q", k, val.LintCode, code)
		}
	}
	// The free-text keys carry no contract.
	for _, k := range []Key{Work, MovementName} {
		if _, ok := ValidatorFor(k); ok {
			t.Errorf("ValidatorFor(%s) reports a contract; work and movement name are free text", k)
		}
	}
	// Boolean membership: the two flags beside Compilation, and nothing else new.
	for _, k := range []Key{ITunesGapless, ShowMovement} {
		if !IsBooleanKey(k) {
			t.Errorf("IsBooleanKey(%s) = false, want true", k)
		}
	}
	if IsBooleanKey(ITunesAdvisory) || IsBooleanKey(BPM) {
		t.Error("ITUNESADVISORY and BPM are not boolean keys")
	}
	// Trimmable membership: the single-token integer and BPM keys trim, the text and
	// boolean keys do not (booleans stay untrimmed like Compilation).
	for _, k := range []Key{ITunesAdvisory, BPM, Movement, MovementTotal} {
		if !IsTrimmableKey(k) {
			t.Errorf("IsTrimmableKey(%s) = false, want true", k)
		}
	}
	for _, k := range []Key{Work, MovementName, ITunesGapless, ShowMovement} {
		if IsTrimmableKey(k) {
			t.Errorf("IsTrimmableKey(%s) = true, want false", k)
		}
	}
	// MP4-canonical membership: every key an MP4 integer atom normalizes, and no text key.
	for _, k := range []Key{MediaType, ITunesAdvisory, Movement, MovementTotal, BPM} {
		if !IsMP4CanonicalKey(k) {
			t.Errorf("IsMP4CanonicalKey(%s) = false, want true", k)
		}
	}
	for _, k := range []Key{Work, MovementName, ITunesGapless, ShowMovement} {
		if IsMP4CanonicalKey(k) {
			t.Errorf("IsMP4CanonicalKey(%s) = true, want false", k)
		}
	}
	// The movement pair folds leading zeros like the other MP4-integer slots.
	if !NumericValuesEqual(Movement, []string{"090"}, []string{"90"}) {
		t.Error(`NumericValuesEqual(MOVEMENT, "090", "90") = false, want true`)
	}
	// A sign does NOT fold on the unsigned keys: their atoms drop "+1" (ParseUint) rather
	// than storing 1, so "+1" and "1" are a dropped value against a stored one. The signed
	// trkn slots keep folding it.
	if NumericValuesEqual(ITunesAdvisory, []string{"+1"}, []string{"1"}) {
		t.Error(`NumericValuesEqual(ITUNESADVISORY, "+1", "1") = true, want false (MP4 drops the signed value)`)
	}
	if !NumericValuesEqual(ITunesAdvisory, []string{"01"}, []string{"1"}) {
		t.Error(`NumericValuesEqual(ITUNESADVISORY, "01", "1") = false, want true`)
	}
	if !NumericValuesEqual(TrackNumber, []string{"+3"}, []string{"3"}) {
		t.Error(`NumericValuesEqual(TRACKNUMBER, "+3", "3") = false, want true (Atoi stores the sign's value)`)
	}
	// The public MediaType wrapper stays a no-op for the other MP4-integer keys.
	if !ValidMediaTypeValue(Movement, "70000") {
		t.Error(`ValidMediaTypeValue(MOVEMENT, "70000") = false, want true (non-MediaType keys are not judged)`)
	}
	// BPM folds an all-zero fraction onto the whole number tmpo stores (an unwarned,
	// Carried-graded canonicalization), while a genuine fraction stays a reported change
	// (tmpo's rounding there is warned). The decimal fold is BPM-only: a trkn slot drops
	// a decimal rather than storing it.
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"174.0", "174", true},
		{"0174.00", "174", true},
		{"174.", "174", true},
		{"174.99", "175", false},
		{"174.5", "174", false},
	} {
		if got := NumericValuesEqual(BPM, []string{c.a}, []string{c.b}); got != c.want {
			t.Errorf("NumericValuesEqual(BPM, %q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	if NumericValuesEqual(TrackNumber, []string{"3.0"}, []string{"3"}) {
		t.Error(`NumericValuesEqual(TRACKNUMBER, "3.0", "3") = true, want false (trkn drops a decimal)`)
	}
}

// TestProjectITunesFields: the iTunes structured accessors project from their canonical keys
// and round-trip through Patch (both sides of the mirror), the booleans as flags and the
// numeric atoms as strings.
func TestProjectITunesFields(t *testing.T) {
	ts := NewTagSet()
	ts.Set(ITunesAdvisory, "1")
	ts.Set(ITunesGapless, "1")
	ts.Set(ShowMovement, "yes")
	ts.Set(BPM, "174.99")
	ts.Set(Work, "Symphony No. 5")
	ts.Set(MovementName, "Allegro con brio")
	ts.Set(Movement, "3")
	ts.Set(MovementTotal, "12")

	check := func(label string, tg Tags) {
		got := []string{tg.ITunesAdvisory, tg.BPM, tg.Work, tg.MovementName, tg.Movement, tg.MovementTotal}
		want := []string{"1", "174.99", "Symphony No. 5", "Allegro con brio", "3", "12"}
		if !slices.Equal(got, want) {
			t.Errorf("%s: string fields = %v, want %v", label, got, want)
		}
		if !tg.ITunesGapless || !tg.ShowMovement {
			t.Errorf("%s: boolean fields = %v/%v, want true/true", label, tg.ITunesGapless, tg.ShowMovement)
		}
	}
	check("Project", Project(ts))
	// Both sides of the mirror: a field populated on only one side drops here. The
	// ShowMovement "yes" re-emits as the canonical "1" flag, which still projects true.
	check("Project -> Patch", Project(Project(ts).Patch().Apply(NewTagSet())))
}
