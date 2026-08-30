package core

import "testing"

// TestFoldValueKeyMatchesEqualFold pins the equivalence [FamilySelector] depends on: two
// values share a fold key exactly when EqualFoldValue calls them equal. An index that
// disagreed with the scalar comparison would mark a family entry as a conflict on one code
// path and not the other.
func TestFoldValueKeyMatchesEqualFold(t *testing.T) {
	// The awkward cases: a fold orbit whose members are not each other's ToLower
	// (Kelvin sign, long s, dotless i), plus space and multi-rune values.
	vals := []string{
		"", " ", "Rock", "rock", " rock ", "ROCK",
		"K", "k", "K", // Kelvin sign folds with K/k
		"s", "ſ", // long s folds with S/s
		"ı", "i", "I", // dotless i does not fold with i
		"Straße", "STRASSE",
		"café", "CAFÉ",
	}
	for _, a := range vals {
		for _, b := range vals {
			if got, want := FoldValueKey(a) == FoldValueKey(b), EqualFoldValue(a, b); got != want {
				t.Errorf("FoldValueKey(%q)==FoldValueKey(%q) = %v, EqualFoldValue = %v", a, b, got, want)
			}
		}
	}
}
