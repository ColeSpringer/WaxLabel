package tag

import "strings"

const closestKeyMaxDist = 2 // TITEL/TITLE, "TRACK NUMBER"/TRACKNUMBER

// ClosestKey returns the nearest known key within closestKeyMaxDist, if any.
func ClosestKey(s string) (Key, bool) {
	up := strings.ToUpper(strings.TrimSpace(s))
	if up == "" {
		return "", false
	}
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
	if bestDist >= 0 && bestDist <= closestKeyMaxDist && bestDist < len(up) {
		return best, true
	}
	return "", false
}

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
