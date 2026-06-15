package tag

import (
	"slices"
	"strings"
)

// Strategy selects how [Merge] resolves a key present in both inputs.
type Strategy uint8

const (
	// PreferIncoming takes the incoming values wherever incoming is present.
	PreferIncoming Strategy = iota
	// PreferBase keeps the base values wherever base is present.
	PreferBase
	// FillEmpty keeps base values only where base is present and non-empty,
	// otherwise takes incoming. It never overwrites real data.
	FillEmpty
	// Union concatenates base then incoming values, dropping duplicates.
	Union
)

func (s Strategy) String() string {
	switch s {
	case PreferIncoming:
		return "prefer-incoming"
	case PreferBase:
		return "prefer-base"
	case FillEmpty:
		return "fill-empty"
	case Union:
		return "union"
	default:
		return "unknown"
	}
}

// FieldProvenance records, per key, what [Merge] decided and why: the values
// selected, the values rejected, the source label of the winner, and a
// human-readable reason.
type FieldProvenance struct {
	Key      Key
	Selected []string
	Rejected []string
	Source   string // "base", "incoming", "union", or "none"
	Reason   string
}

// Merge combines base and incoming under strategy, returning the merged set
// and per-key provenance. Result key order is base keys first (in base order)
// then incoming-only keys (in incoming order); within a key, Union order is
// base values then new incoming values. Neither input is modified.
func Merge(base, incoming TagSet, strategy Strategy) (TagSet, []FieldProvenance) {
	out := NewTagSet()
	var prov []FieldProvenance

	// Stable key ordering across the union.
	keys := base.Keys()
	for _, k := range incoming.Keys() {
		if !base.Has(k) {
			keys = append(keys, k)
		}
	}

	for _, k := range keys {
		bv, hasB := base.Get(k)
		iv, hasI := incoming.Get(k)

		selected, source, reason := resolve(strategy, hasB, bv, hasI, iv)
		out.Set(k, selected...)
		prov = append(prov, FieldProvenance{
			Key:      k,
			Selected: selected,
			Rejected: rejected(selected, bv, iv),
			Source:   source,
			Reason:   reason,
		})
	}
	return out, prov
}

func resolve(s Strategy, hasB bool, bv []string, hasI bool, iv []string) (sel []string, source, reason string) {
	switch s {
	case PreferIncoming:
		if hasI {
			return iv, "incoming", "incoming present; preferred over base"
		}
		return bv, "base", "incoming absent; kept base"
	case PreferBase:
		if hasB {
			return bv, "base", "base present; kept over incoming"
		}
		return iv, "incoming", "base absent; took incoming"
	case FillEmpty:
		if hasB && !allEmpty(bv) {
			return bv, "base", "base non-empty; not overwritten"
		}
		if hasI {
			return iv, "incoming", "base empty/absent; filled from incoming"
		}
		return bv, "base", "both empty/absent"
	case Union:
		u := unionValues(bv, iv)
		switch {
		case hasB && hasI:
			return u, "union", "combined base and incoming, duplicates dropped"
		case hasB:
			return u, "base", "only base present"
		default:
			return u, "incoming", "only incoming present"
		}
	default:
		return bv, "base", "unknown strategy; kept base"
	}
}

// unionValues concatenates base then incoming, dropping values that are
// duplicates after normalization (whitespace-trimmed, case-insensitive). The
// first occurrence's original spelling is preserved.
func unionValues(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	seen := make(map[string]bool, len(a)+len(b))
	appendUnique := func(vals []string) {
		for _, v := range vals {
			n := normalizeValue(v)
			if seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, v)
		}
	}
	appendUnique(a)
	appendUnique(b)
	return out
}

func normalizeValue(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func allEmpty(vals []string) bool {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}

// rejected returns the values from base or incoming that did not survive into
// selected (compared after normalization).
func rejected(selected, base, incoming []string) []string {
	keep := make(map[string]bool, len(selected))
	for _, v := range selected {
		keep[normalizeValue(v)] = true
	}
	var out []string
	collect := func(vals []string) {
		for _, v := range vals {
			if !keep[normalizeValue(v)] {
				out = append(out, v)
			}
		}
	}
	collect(base)
	collect(incoming)
	return slices.Clip(out)
}
