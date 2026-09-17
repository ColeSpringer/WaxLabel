package core

import (
	"slices"
	"strings"
	"time"

	"github.com/colespringer/waxlabel/tag"
)

// AccessLevel grades support (none, partial, full). Read and write can diverge.
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

// Capability reports read/write support and loss metadata. Option-dependent per format.
type Capability struct {
	Read           AccessLevel
	Write          AccessLevel
	Representation string   // how the value is stored natively
	Fidelity       string   // e.g. "lossless", "year-only"
	Constraints    []string // e.g. "ASCII only", "fixed vocabulary"
	// MaxItems caps set size (pictures, chapters); 0 = no limit. Used by transfer dispose.
	MaxItems int
	// MaxValues is a discovery cardinality hint; transfer uses Fidelity/Constraints instead.
	MaxValues int
	// PictureLoss on pictures capability only; per-picture loss grading in ProjectTransfer.
	PictureLoss PictureLoss
	// ChapterLoss on chapters capability only.
	ChapterLoss ChapterLoss
	// ChapterTitleByteMax title byte cap; 0 = none (MP4 chpl 255).
	ChapterTitleByteMax int
	// SyncedLyricsLoss on synced-lyrics capability only.
	SyncedLyricsLoss SyncedLyricsLoss
	// SyncedLyricsTimeMax line timestamp cap; 0 = none (SYLT/LRC).
	SyncedLyricsTimeMax time.Duration
	// PictureMIMEs allowed cover MIMEs; nil = unrestricted. Pictures capability only.
	PictureMIMEs []string

	// reducesValue: per-value Lossy vs Carried in [dispose].
	reducesValue func(string) bool
	// dropsValue: per-value Dropped; precedes reducesValue.
	dropsValue func(string) bool
	// slotPictures: fixed slot selection (APE). nil added = transfer path.
	slotPictures func(pics []Picture, added []bool) []int
	slotReason   string
}

// WithValueReduction attaches a per-value reduction predicate.
func WithValueReduction(c Capability, reduces func(string) bool) Capability {
	c.reducesValue = reduces
	return c
}

// WithValueDrop attaches a per-value drop predicate.
func WithValueDrop(c Capability, drops func(string) bool) Capability {
	c.dropsValue = drops
	return c
}

// WithPictureSlots attaches fixed slot selection for pictures (APE).
func WithPictureSlots(c Capability, slots func(pics []Picture, added []bool) []int, reason string) Capability {
	c.slotPictures = slots
	c.slotReason = reason
	return c
}

// Representable reports whether c can store p's effective MIME.
func Representable(c Capability, p Picture) bool {
	return MIMERepresentable(c, p.EffectiveMIME())
}

// MIMERepresentable checks mime against PictureMIMEs; "image/*" wildcards supported.
func MIMERepresentable(c Capability, mime string) bool {
	if len(c.PictureMIMEs) == 0 || slices.Contains(c.PictureMIMEs, mime) {
		return true
	}
	// Wildcard match (Matroska image/*).
	lower := strings.ToLower(mime)
	for _, pat := range c.PictureMIMEs {
		if prefix, ok := strings.CutSuffix(pat, "/*"); ok && strings.HasPrefix(lower, prefix+"/") {
			return true
		}
	}
	return false
}

// PartitionRepresentable splits pics by representable MIME. Shared by transfer and editor.
func PartitionRepresentable(c Capability, pics []Picture) (kept []Picture, keptIdx []int, droppedMIMEs []string) {
	for i, p := range pics {
		if mime := p.EffectiveMIME(); MIMERepresentable(c, mime) {
			kept = append(kept, p)
			keptIdx = append(keptIdx, i)
		} else {
			droppedMIMEs = append(droppedMIMEs, mime)
		}
	}
	return kept, keptIdx, droppedMIMEs
}

// PartitionPictureSlots applies slot partition; no partition keeps all.
func PartitionPictureSlots(c Capability, pics []Picture) (kept []Picture, dropped int, reason string) {
	if c.slotPictures == nil || len(pics) == 0 {
		return pics, 0, ""
	}
	keptIdx := c.slotPictures(pics, nil)
	if len(keptIdx) == len(pics) {
		return pics, 0, ""
	}
	kept = make([]Picture, 0, len(keptIdx))
	for _, i := range keptIdx {
		kept = append(kept, pics[i])
	}
	return kept, len(pics) - len(kept), c.slotReason
}

// PartitionPictureSlotsEdited is editor path with added mask. ok false: no slot limit.
func PartitionPictureSlotsEdited(c Capability, pics []Picture, added []bool) (keptIdx []int, reason string, ok bool) {
	if c.slotPictures == nil {
		return nil, "", false
	}
	return c.slotPictures(pics, added), c.slotReason, true
}

// NumericGenreCapability is GENRE under --numeric-genre (partial write).
func NumericGenreCapability(repr string) Capability {
	return Capability{
		Read: AccessFull, Write: AccessPartial,
		Representation: repr,
		Fidelity:       "a recognized genre is stored as a numeric reference and re-read as its canonical name",
	}
}

// OriginalDateV23Capability is ORIGINALDATE for ID3v2.3 (TORY year only).
func OriginalDateV23Capability() Capability {
	return Capability{
		Read: AccessFull, Write: AccessPartial,
		Representation: "ID3v2.3 TORY (year only)",
		Fidelity:       "ID3v2.3 TORY stores the year only",
	}
}

// RecordingDateV23Capability is RECORDINGDATE for ID3v2.3. Per-value loss via id3 predicate.
func RecordingDateV23Capability() Capability {
	return Capability{
		Read: AccessFull, Write: AccessFull,
		Representation: "ID3v2.3 TYER+TDAT+TIME",
		Fidelity:       "ID3v2.3 date frames store the parts separately, so a finer component is dropped or the value reads back respelled",
	}
}

// Capabilities describes what a format (under a given set of options) can do.
// Field returns per-key detail; the format-level fields cover the common case.
type Capabilities struct {
	Format       Format
	ReadOnly     bool
	Pictures     Capability
	Chapters     Capability
	SyncedLyrics Capability
	// Padding: full (FLAC), partial (MP3/MP4/AAC front tag), none (Ogg/WAV/Matroska).
	Padding AccessLevel
	// OutputGain: Opus AccessFull; muxed Opus in other containers is not decoded here.
	OutputGain   AccessLevel
	GenericField Capability             // default for canonical keys
	perField     map[tag.Key]Capability // overrides
	fieldClassifier FieldClassifier // per-field transfer override; unexported for JSON
	readOnlyReason error            // codec write refusal; unexported for JSON
}

// FieldClassifier overrides transfer grading for one field (cardinality, reserved keys, siblings).
type FieldClassifier func(key tag.Key, values []string, all tag.TagSet) (Disposition, string, bool)

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

// WithSyncedLyrics sets synced-lyrics capability.
func (c Capabilities) WithSyncedLyrics(sl Capability) Capabilities {
	c.SyncedLyrics = sl
	return c
}

// WithOutputGain sets output-gain capability.
func (c Capabilities) WithOutputGain(level AccessLevel) Capabilities {
	c.OutputGain = level
	return c
}

// WithFieldClassifier attaches per-field transfer grading. Overrides Carried only.
func (c Capabilities) WithFieldClassifier(fn FieldClassifier) Capabilities {
	c.fieldClassifier = fn
	return c
}

// WithReadOnlyReason attaches the codec's write refusal error.
func (c Capabilities) WithReadOnlyReason(err error) Capabilities {
	c.readOnlyReason = err
	return c
}

// ReadOnlyReason returns attached write refusal, or nil.
func (c Capabilities) ReadOnlyReason() error { return c.readOnlyReason }

// Field returns the capability for key, falling back to GenericField when
// there is no specific override.
func (c Capabilities) Field(key tag.Key) Capability {
	if cap, ok := c.perField[key]; ok {
		return cap
	}
	return c.GenericField
}

// Reason returns loss text: Fidelity, else Constraints, else generic fallback.
func (c Capability) Reason() string {
	if c.Fidelity != "" {
		return c.Fidelity
	}
	if len(c.Constraints) > 0 {
		return strings.Join(c.Constraints, "; ")
	}
	return "stored with reduced fidelity"
}
