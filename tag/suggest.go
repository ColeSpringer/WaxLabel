package tag

import "strings"

// closestKeyMaxDist caps the edit distance at which [ClosestKey] still offers a
// suggestion. Two covers the common near-misses - a single transposed pair
// (TITEL vs TITLE) or one inserted/removed separator ("TRACK NUMBER" vs
// TRACKNUMBER) - while staying tight enough that an unrelated custom field draws
// no spurious "did you mean?".
const closestKeyMaxDist = 2

// ClosestKey returns the published canonical key closest to s by edit distance,
// and whether a close-enough match was found. It powers the CLI's "did you
// mean?" hint on an unknown-key note, catching a near-miss like TITEL -> TITLE or
// "TRACK NUMBER" -> TRACKNUMBER while staying quiet for a genuinely unrelated
// custom field. s is compared case-insensitively (and whitespace-trimmed)
// against the uppercase vocabulary; ties resolve to the first key in sorted
// order (sortedKnownKeys), so the suggestion is deterministic.
func ClosestKey(s string) (Key, bool) {
	up := strings.ToUpper(strings.TrimSpace(s))
	if up == "" {
		return "", false
	}
	// A recognized alias resolves before the distance fallback. DISC and TRACK are too far
	// from their canonical names for Levenshtein alone to help.
	if k, ok := AliasKey(up); ok {
		return k, true
	}
	best := Key("")
	bestDist := -1
	for _, k := range sortedKnownKeys {
		d := levenshtein(up, string(k))
		if bestDist < 0 || d < bestDist {
			best, bestDist = k, d
		}
	}
	// Only offer a suggestion when it is genuinely close: cap the distance at
	// closestKeyMaxDist and require it to be strictly less than the typed key's own
	// length, so a tiny unknown key is not judged "near" a longer one on a couple of
	// coincidental letters (the shortest known key is 4 bytes).
	if bestDist >= 0 && bestDist <= closestKeyMaxDist && bestDist < len(up) {
		return best, true
	}
	return "", false
}

// levenshtein returns the edit distance between a and b (insertion, deletion, or
// substitution). It runs only over short canonical key strings, so the simple
// two-row dynamic program is more than fast enough and allocates a pair of
// len(b)+1 rows rather than the full matrix.
func levenshtein(a, b string) int {
	switch {
	case a == b:
		return 0
	case len(a) == 0:
		return len(b)
	case len(b) == 0:
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}
