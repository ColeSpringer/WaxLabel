package core

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/colespringer/waxlabel/internal/bits"
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
	// PictureLoss on pictures capability only; per-picture loss grading in [ProjectTransferOnto].
	PictureLoss PictureLoss
	// ChapterLoss on chapters capability only.
	ChapterLoss ChapterLoss
	// ChapterTitleByteMax title byte cap; 0 = none (MP4 chpl 255).
	ChapterTitleByteMax int
	// ChapterTimeUnit is the step the format counts chapter times in, so a finer time is not
	// stored exactly. 0 = not stated. Chapters capability only.
	ChapterTimeUnit time.Duration
	// ChapterTimeMax is the largest chapter time the format's field holds; its writer clamps a
	// later one. 0 = none. Chapters capability only.
	ChapterTimeMax time.Duration
	// SyncedLyricsLoss on synced-lyrics capability only.
	SyncedLyricsLoss SyncedLyricsLoss
	// SyncedLyricsTimeMax line timestamp cap; 0 = none (SYLT/LRC).
	SyncedLyricsTimeMax time.Duration
	// TextLoss on the COMMENT and LYRICS field capabilities only.
	TextLoss TextItemLoss
	// PictureMIMEs allowed cover MIMEs; nil = unrestricted. Pictures capability only.
	PictureMIMEs []string
	// PictureGeometry: the format has fields for a picture's size, so one declared for bytes
	// that give none (a link, an image type nothing here reads) is stored (FLAC PICTURE block).
	PictureGeometry bool

	// reducesValue: per-value Lossy vs Carried in [dispose].
	reducesValue func(string) bool
	// dropsValue: per-value Dropped; precedes reducesValue.
	dropsValue func(string) bool
	// slotPictures: which pictures a store of uniquely named items holds (APE). nil added = transfer path.
	slotPictures func(pics []Picture, added []bool) []int
	slotReason   string
	// pictureTooLarge: why a picture is past what the store holds, "" when it fits. nil = any size.
	pictureTooLarge func(Picture) string
	// storedLanguage: see [WithStoredLanguage]. nil = as given.
	storedLanguage func(string) string
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

// WithPictureSlots attaches the selection of the pictures a store of uniquely named items holds
// (APE): slots returns the indexes kept, and may depend on the items the file holds.
func WithPictureSlots(c Capability, slots func(pics []Picture, added []bool) []int, reason string) Capability {
	c.slotPictures = slots
	c.slotReason = reason
	return c
}

// WithPictureSizeLimit attaches the size check of a store that holds a picture only up to a
// size: tooLarge returns why p does not fit ([SizeLimitReason]), "" when it does.
func WithPictureSizeLimit(c Capability, tooLarge func(Picture) string) Capability {
	c.pictureTooLarge = tooLarge
	return c
}

// WithStoredLanguage attaches the form a synced lyrics set's language takes in a store that
// keeps it in a field of its own shape (the three bytes of an ID3 frame): stored returns what a
// read gives for a language written as lang.
func WithStoredLanguage(c Capability, stored func(lang string) string) Capability {
	c.storedLanguage = stored
	return c
}

// PartitionPictureSize returns the indexes of the pics within c's size limit, and the reason the
// first of the others is dropped. No limit keeps all.
func PartitionPictureSize(c Capability, pics []Picture) (keptIdx []int, reason string) {
	keptIdx = make([]int, 0, len(pics))
	for i, p := range pics {
		if c.pictureTooLarge != nil {
			if r := c.pictureTooLarge(p); r != "" {
				if reason == "" {
					reason = r
				}
				continue
			}
		}
		keptIdx = append(keptIdx, i)
	}
	return keptIdx, reason
}

// SizeLimitReason words an item of n bytes past limit, which holds says of the format's store
// ("the picture is 17.0 MiB, over the 16.0 MiB a FLAC metadata block holds"), "" when n fits.
func SizeLimitReason(item string, n, limit int64, holds string) string {
	if n <= limit {
		return ""
	}
	got, max := bits.SizeOverLimit(n, limit)
	return fmt.Sprintf("%s is %s, over the %s %s", item, got, max, holds)
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

// PartitionPictureSlots returns the indexes of the pics a store of uniquely named items holds,
// and the reason it holds no more. No such store keeps all.
func PartitionPictureSlots(c Capability, pics []Picture) (keptIdx []int, reason string) {
	if c.slotPictures == nil || len(pics) == 0 {
		keptIdx = make([]int, len(pics))
		for i := range keptIdx {
			keptIdx[i] = i
		}
		return keptIdx, ""
	}
	return c.slotPictures(pics, nil), c.slotReason
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
	OutputGain      AccessLevel
	GenericField    Capability             // default for canonical keys
	perField        map[tag.Key]Capability // overrides
	fieldClassifier FieldClassifier        // per-field transfer override; unexported for JSON
	// rendered: see [Capabilities.WithRenderedFields].
	rendered RenderedFields
	// textDefaultLanguage: see [Capabilities.TextDefaultLanguage].
	textDefaultLanguage string
	// maxValueBytes: see [Capabilities.WithValueSizeLimit]. 0 = no limit.
	maxValueBytes  int64
	valueSizeStore string
	readOnlyReason error // codec write refusal; unexported for JSON
}

// FieldClassifier overrides transfer grading for one field (cardinality, reserved keys,
// siblings). values are the key's as a transfer writes them ([GradedTransferValues]), and
// written is the source with the pictures, chapters and synced lyrics the transfer brings.
type FieldClassifier func(key tag.Key, values []string, written *Media) (Disposition, string, bool)

// RenderedFields grades the fields of a transfer by what a format's own writer stores of them,
// so every loss the writer makes is graded by the writer itself.
type RenderedFields struct {
	// Render writes fields as a write onto a file holding no metadata does, beside the comment
	// and lyric items, pictures, chapters and synced lyrics of written, and returns what a read
	// of that file gives: the tags, and the comment and lyric items.
	Render func(written *Media, fields tag.TagSet) *Media
	// Reason may say in the format's terms why k, one of fields, is graded d by the rendering:
	// "" keeps the reason the grade came with.
	Reason func(k tag.Key, d Disposition, fields tag.TagSet) string
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

// WithTextQualifiers sets what a COMMENT or LYRICS item loses in this format, on both keys' field
// capabilities, and the language its writer gives an item that has none ("" when it leaves the
// field out). A format that does not call it stores the text alone.
func (c Capabilities) WithTextQualifiers(loss TextItemLoss, defaultLanguage string) Capabilities {
	c.textDefaultLanguage = defaultLanguage
	perField := maps.Clone(c.perField)
	if perField == nil {
		perField = map[tag.Key]Capability{}
	}
	for _, k := range []tag.Key{tag.Comment, tag.Lyrics} {
		fc := c.Field(k)
		fc.TextLoss = loss
		perField[k] = fc
	}
	c.perField = perField
	return c
}

// WithValueSizeLimit states the most bytes one field value can take in this format's store,
// which holds names in the reason a longer one is dropped from a transfer ("a FLAC metadata
// block holds").
func (c Capabilities) WithValueSizeLimit(max int64, holds string) Capabilities {
	c.maxValueBytes, c.valueSizeStore = max, holds
	return c
}

// TextDefaultLanguage is the language this format's writer fills in for a comment or lyric given
// none, so one read from it says little about the text; "" when the format has no such default.
func (c Capabilities) TextDefaultLanguage() string { return c.textDefaultLanguage }

// WithFieldClassifier attaches per-field transfer grading. It grades what the capability table
// calls carried, and may drop what the table calls lossy.
func (c Capabilities) WithFieldClassifier(fn FieldClassifier) Capabilities {
	c.fieldClassifier = fn
	return c
}

// WithRenderedFields attaches grading by the format's writer, under the rule of
// [Capabilities.WithFieldClassifier]. Only the fields the table stores are rendered.
func (c Capabilities) WithRenderedFields(r RenderedFields) Capabilities {
	c.rendered = r
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
