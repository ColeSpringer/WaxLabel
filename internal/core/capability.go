package core

import "github.com/colespringer/waxlabel/tag"

// AccessLevel grades how completely a dimension is supported. It is
// deliberately not a single yes/no: a field can be readable but not writable,
// or representable only with reduced fidelity.
type AccessLevel uint8

const (
	AccessNone AccessLevel = iota
	AccessPartial
	AccessFull
)

func (a AccessLevel) String() string {
	switch a {
	case AccessFull:
		return "full"
	case AccessPartial:
		return "partial"
	default:
		return "none"
	}
}

// Capability reports support along several independent dimensions rather than
// collapsing them into one enum, because they genuinely diverge (a field may
// be fully readable yet only lossily writable). It is option-dependent: the
// same field differs between, say, WAV with an "id3 " chunk and WAV INFO-only.
type Capability struct {
	Read           AccessLevel
	Write          AccessLevel
	Representation string   // how the value is stored natively
	Fidelity       string   // e.g. "lossless", "year-only"
	Constraints    []string // e.g. "ASCII only", "fixed vocabulary"
	// MaxItems caps a set-valued dimension (pictures, chapters): the most the
	// format can store, or 0 for no limit. It makes a hard structural limit (e.g.
	// MP4's 255-chapter Nero chpl cap) machine-checkable, so a transfer can report
	// an over-limit set as dropped instead of advertising it carried and then
	// failing at write time.
	MaxItems int
	// MaxValues caps how many values one field may hold under this format: 0 for
	// no format-level limit (the field's cardinality is then the key's own, via
	// [tag.Key.Multivalued]), or 1 for a format that forces a single value even on
	// an inherently multi-valued key. It is a cardinality hint for discovery
	// (enumerating editable fields; the caps command), set only by the rare format
	// that restricts a key's natural cardinality; the common case leaves it 0.
	// Unlike MaxItems, it is deliberately not consulted by the transfer report: a
	// multi-value downgrade a destination actually performs is expressed through
	// Fidelity/Constraints, which the destination's writer honors (see dispose).
	MaxValues int
}

// Capabilities describes what a format (under a given set of options) can do.
// Field returns per-key detail; the format-level fields cover the common case.
type Capabilities struct {
	Format       Format
	ReadOnly     bool
	Pictures     Capability
	Chapters     Capability
	GenericField Capability             // default for canonical keys
	perField     map[tag.Key]Capability // overrides
}

// NewCapabilities builds a Capabilities with the given per-field overrides.
func NewCapabilities(f Format, readOnly bool, generic, pictures, chapters Capability, perField map[tag.Key]Capability) Capabilities {
	return Capabilities{
		Format:       f,
		ReadOnly:     readOnly,
		GenericField: generic,
		Pictures:     pictures,
		Chapters:     chapters,
		perField:     perField,
	}
}

// Field returns the capability for key, falling back to GenericField when
// there is no specific override.
func (c Capabilities) Field(key tag.Key) Capability {
	if cap, ok := c.perField[key]; ok {
		return cap
	}
	return c.GenericField
}
