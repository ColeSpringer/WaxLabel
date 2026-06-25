package core

import (
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

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
	// PictureLoss grades which picture metadata this format drops on write (the pictures
	// capability only; [PictureLossNone] for fields, chapters, and lossless formats).
	// ProjectTransfer uses it to mark a picture set Lossy only when the specific pictures
	// carry metadata the destination will drop, matching the codec's write-time warning.
	PictureLoss PictureLoss
}

// NumericGenreCapability returns the GENRE override for codecs that can store a
// recognized genre as a numeric reference under --numeric-genre. Reading the value back
// yields the canonical genre name, so spelling or case may change. The capability is
// conservative: some values still write losslessly, but capability data is value-blind.
// Edit warnings compare the written result to the requested value before warning, while
// transfer reports grade AccessPartial as Lossy without a per-value check.
func NumericGenreCapability(repr string) Capability {
	return Capability{
		Read: AccessFull, Write: AccessPartial,
		Representation: repr,
		Fidelity:       "a recognized genre is stored as a numeric reference and re-read as its canonical name",
	}
}

// OriginalDateV23Capability returns the ORIGINALDATE override for an ID3-backed codec
// writing ID3v2.3. Its TORY frame holds only the year, so YYYY-MM or YYYY-MM-DD values
// lose sub-year precision. Keeping this in one helper keeps the ID3-backed codecs in
// agreement; v2.4 writes the full TDOR string and needs no override.
func OriginalDateV23Capability() Capability {
	return Capability{
		Read: AccessFull, Write: AccessPartial,
		Representation: "ID3v2.3 TORY (year only)",
		Fidelity:       "ID3v2.3 TORY stores the year only",
	}
}

// Capabilities describes what a format (under a given set of options) can do.
// Field returns per-key detail; the format-level fields cover the common case.
type Capabilities struct {
	Format   Format
	ReadOnly bool
	Pictures Capability
	Chapters Capability
	// Padding grades how completely the format honors the post-metadata padding
	// controls (--padding / --no-padding), as one AccessLevel rather than a full
	// Capability (it has no representation or per-key detail):
	//   - AccessFull: written and shrunk on every edit (FLAC, which always rewrites
	//     its metadata block - PaddingPolicy.ClampTarget).
	//   - AccessPartial: a forced rewrite can grow the region, but a fit-in-place edit
	//     keeps the existing padding and cannot shrink it (the front-tag codecs MP4,
	//     MP3, and AAC - PaddingPolicy.ReuseOrTarget).
	//   - AccessNone: the format has no padding concept (Ogg/Opus, WAV, AIFF,
	//     Matroska).
	// The CLI reads it to tell the user when a padding flag does not (fully) apply,
	// and caps renders it ("none"/"partial"/"full" via AccessLevel.String).
	Padding      AccessLevel
	GenericField Capability             // default for canonical keys
	perField     map[tag.Key]Capability // overrides
}

// NewCapabilities builds a Capabilities with the given padding level and per-field
// overrides.
func NewCapabilities(f Format, readOnly bool, generic, pictures, chapters Capability, padding AccessLevel, perField map[tag.Key]Capability) Capabilities {
	return Capabilities{
		Format:       f,
		ReadOnly:     readOnly,
		GenericField: generic,
		Pictures:     pictures,
		Chapters:     chapters,
		Padding:      padding,
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

// Reason returns the text used when a partial-write capability reduces fidelity.
// Fidelity wins over Constraints; if neither is present, it returns a generic fallback.
// Transfer and edit warnings share this helper so they describe the same loss the same
// way.
func (c Capability) Reason() string {
	if c.Fidelity != "" {
		return c.Fidelity
	}
	if len(c.Constraints) > 0 {
		return strings.Join(c.Constraints, "; ")
	}
	return "stored with reduced fidelity"
}
