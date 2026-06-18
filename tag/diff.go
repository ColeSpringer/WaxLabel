package tag

import "slices"

// ChangeKind names how one key differs between two tag sets.
type ChangeKind uint8

const (
	// ChangeUnknown is the zero value, so a never-set ChangeKind is detectably
	// invalid rather than silently reading as a real kind.
	ChangeUnknown ChangeKind = iota
	// ChangeAdded marks a key present only in the edited set.
	ChangeAdded
	// ChangeRemoved marks a key present only in the base set.
	ChangeRemoved
	// ChangeChanged marks a key present in both but with different values.
	ChangeChanged
)

// String renders the kind as the diff(1)-style word used in both textual and
// machine-readable output ("added", "removed", "changed").
func (k ChangeKind) String() string {
	switch k {
	case ChangeAdded:
		return "added"
	case ChangeRemoved:
		return "removed"
	case ChangeChanged:
		return "changed"
	default:
		return "unknown"
	}
}

// Change is one key's difference between a base and an edited [TagSet]: the key,
// how it changed, and the relevant values. Old holds the base values (set for a
// removed or changed key); New holds the edited values (set for an added or
// changed key).
type Change struct {
	Key  Key
	Kind ChangeKind
	Old  []string
	New  []string
}

// Diff reports the per-key delta from base to edited: keys dropped (removed),
// keys whose values changed, then keys introduced (added). Removed and changed
// keys come first in base's order, added keys last in edited's order, so the
// result is stable and minimal-change. Values are compared order-significantly
// (the same equality a codec uses to detect an edit), so a key present in both
// with identical values yields no Change.
//
// It is the single tag-diff primitive shared by the CLI's diff command and the
// write-plan change preview, so the two cannot drift.
func Diff(base, edited TagSet) []Change {
	var out []Change
	// Read the unexported fields directly rather than through Keys/Get: those
	// clone defensively for external callers, but here (inside the package) we
	// need clones only for the values we actually keep in a Change, which must be
	// detached copies the caller can hold. Unchanged keys allocate nothing.
	for _, k := range base.order {
		ov := base.values[k]
		if nv, ok := edited.values[k]; ok {
			if !slices.Equal(ov, nv) {
				out = append(out, Change{Key: k, Kind: ChangeChanged, Old: slices.Clone(ov), New: slices.Clone(nv)})
			}
		} else {
			out = append(out, Change{Key: k, Kind: ChangeRemoved, Old: slices.Clone(ov)})
		}
	}
	for _, k := range edited.order {
		if _, ok := base.values[k]; !ok {
			out = append(out, Change{Key: k, Kind: ChangeAdded, New: slices.Clone(edited.values[k])})
		}
	}
	return out
}
