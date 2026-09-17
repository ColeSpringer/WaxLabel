package core

import "testing"

// TestFoldValueKeyMatchesEqualFold: FoldValueKey agrees with EqualFoldValue (FamilySelector depends on this).
func TestFoldValueKeyMatchesEqualFold(t *testing.T) {
	vals := []string{
		"", " ", "Rock", "rock", " rock ", "ROCK",
		"K", "k", "K",
		"s", "ſ",
		"ı", "i", "I",
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
