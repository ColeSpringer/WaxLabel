package tag

import (
	"iter"
	"slices"
)

// TagSet is the authoritative, presence-aware view of canonical tags: an
// ordered multimap from [Key] to its values. Presence is explicit, so the
// three states a typed struct cannot distinguish are all representable:
//
//   - absent: the key is not present ([TagSet.Has] is false);
//   - present and empty: the key is present with no values;
//   - present with values: the usual case.
//
// Key order is preserved (insertion order) so edits stay minimal-change.
// TagSet carries reference state; copy it with [TagSet.Clone] rather than by
// assignment when independent ownership is needed. The zero value is an empty,
// ready-to-use set.
type TagSet struct {
	order  []Key
	values map[Key][]string
}

// NewTagSet returns an empty TagSet.
func NewTagSet() TagSet { return TagSet{} }

func (s *TagSet) ensure() {
	if s.values == nil {
		s.values = make(map[Key][]string)
	}
}

// Has reports whether key is present (regardless of how many values it has).
func (s TagSet) Has(key Key) bool {
	_, ok := s.values[key]
	return ok
}

// Get returns the values for key and whether the key is present. The returned
// slice is a copy; mutating it does not affect the set.
func (s TagSet) Get(key Key) ([]string, bool) {
	v, ok := s.values[key]
	if !ok {
		return nil, false
	}
	return slices.Clone(v), true
}

// First returns the first value for key, or ("", false) if the key is absent
// or present-but-empty.
func (s TagSet) First(key Key) (string, bool) {
	v, ok := s.values[key]
	if !ok || len(v) == 0 {
		return "", false
	}
	return v[0], true
}

// Len reports the number of present keys.
func (s TagSet) Len() int { return len(s.order) }

// Keys returns the present keys in their preserved order.
func (s TagSet) Keys() []Key { return slices.Clone(s.order) }

// All iterates present keys in order, yielding a copy of each value slice.
func (s TagSet) All() iter.Seq2[Key, []string] {
	return func(yield func(Key, []string) bool) {
		for _, k := range s.order {
			if !yield(k, slices.Clone(s.values[k])) {
				return
			}
		}
	}
}

// Set makes key present with exactly vals (replacing any existing values).
// Passing no values marks the key present-but-empty. Values are copied.
func (s *TagSet) Set(key Key, vals ...string) {
	s.ensure()
	if _, ok := s.values[key]; !ok {
		s.order = append(s.order, key)
	}
	s.values[key] = slices.Clone(vals)
}

// Add appends vals to key, making it present if it was absent. Values are
// copied.
func (s *TagSet) Add(key Key, vals ...string) {
	s.ensure()
	if _, ok := s.values[key]; !ok {
		s.order = append(s.order, key)
		s.values[key] = nil
	}
	s.values[key] = append(s.values[key], vals...)
}

// Delete removes key, making it absent. It is a no-op if the key was absent.
func (s *TagSet) Delete(key Key) {
	if _, ok := s.values[key]; !ok {
		return
	}
	delete(s.values, key)
	if i := slices.Index(s.order, key); i >= 0 {
		s.order = slices.Delete(s.order, i, i+1)
	}
}

// Clone returns a deep copy that shares no state with s.
func (s TagSet) Clone() TagSet {
	if s.values == nil {
		return TagSet{}
	}
	out := TagSet{
		order:  slices.Clone(s.order),
		values: make(map[Key][]string, len(s.values)),
	}
	for k, v := range s.values {
		out.values[k] = slices.Clone(v)
	}
	return out
}

// Equal reports whether s and other have the same keys with the same values.
// Order is not significant for equality.
func (s TagSet) Equal(other TagSet) bool {
	if len(s.values) != len(other.values) {
		return false
	}
	for k, v := range s.values {
		ov, ok := other.values[k]
		if !ok || !slices.Equal(v, ov) {
			return false
		}
	}
	return true
}

// patchKind distinguishes the three patch operations.
type patchKind uint8

const (
	opSet patchKind = iota
	opClear
	opAdd
)

func (k patchKind) String() string {
	switch k {
	case opSet:
		return "set"
	case opClear:
		return "clear"
	case opAdd:
		return "add"
	default:
		return "unknown"
	}
}

type patchOp struct {
	kind   patchKind
	key    Key
	values []string
}

// TagPatch is an ordered list of explicit edits — set, clear, add — applied
// against a base [TagSet]. Because each op is explicit there is no zero-value
// ambiguity: clearing a key is distinct from setting it to empty. Later ops
// override earlier ones for the same key.
type TagPatch struct {
	ops []patchOp
}

// NewPatch returns an empty patch.
func NewPatch() TagPatch { return TagPatch{} }

// Set records that key should be replaced with exactly vals. With no values
// the key becomes present-but-empty (use Clear to remove it). Returns the
// patch for chaining.
func (p *TagPatch) Set(key Key, vals ...string) *TagPatch {
	p.ops = append(p.ops, patchOp{kind: opSet, key: key, values: slices.Clone(vals)})
	return p
}

// Clear records that key should be removed (made absent). Returns the patch
// for chaining.
func (p *TagPatch) Clear(key Key) *TagPatch {
	p.ops = append(p.ops, patchOp{kind: opClear, key: key})
	return p
}

// Add records that vals should be appended to key. Returns the patch for
// chaining.
func (p *TagPatch) Add(key Key, vals ...string) *TagPatch {
	p.ops = append(p.ops, patchOp{kind: opAdd, key: key, values: slices.Clone(vals)})
	return p
}

// Len reports the number of recorded operations.
func (p TagPatch) Len() int { return len(p.ops) }

// Keys returns the distinct keys this patch touches, in first-seen order.
func (p TagPatch) Keys() []Key {
	var out []Key
	seen := make(map[Key]bool, len(p.ops))
	for _, op := range p.ops {
		if !seen[op.key] {
			seen[op.key] = true
			out = append(out, op.key)
		}
	}
	return out
}

// Append adds all of other's operations after this patch's, so other's edits
// take effect later (and thus win on conflicts).
func (p *TagPatch) Append(other TagPatch) {
	p.ops = append(p.ops, other.ops...)
}

// Apply returns a new TagSet that is base with the patch applied. base is not
// modified.
func (p TagPatch) Apply(base TagSet) TagSet {
	out := base.Clone()
	for _, op := range p.ops {
		switch op.kind {
		case opSet:
			out.Set(op.key, op.values...)
		case opClear:
			out.Delete(op.key)
		case opAdd:
			out.Add(op.key, op.values...)
		}
	}
	return out
}
