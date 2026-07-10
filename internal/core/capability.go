package core

import (
	"slices"
	"strings"
	"time"

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
	// ChapterLoss records chapter metadata this format cannot preserve on chapter
	// writes. It is set only on the chapters capability. ProjectTransfer uses it to
	// mark a chapter set Lossy only when those chapters actually carry affected
	// metadata, matching the editor's write warning.
	ChapterLoss ChapterLoss
	// ChapterTitleByteMax caps the byte length of a chapter title this format stores, or 0 for no
	// limit. MP4's Nero chpl (an 8-bit length prefix) truncates a title past 255 bytes; FLAC/Ogg
	// have no such cap. ProjectTransfer uses it to mark a chapter set Lossy when a title exceeds it,
	// so a copy that will truncate a title reports the loss instead of a clean carry, matching the
	// write-time chapter-title-truncated warning. Set only on the chapters capability.
	ChapterTitleByteMax int
	// SyncedLyricsLoss records synced-lyrics metadata this format cannot preserve. It is
	// set only on the synced-lyrics capability. ProjectTransfer uses it to mark a
	// synced-lyrics set Lossy only when those sets carry affected metadata (a per-set
	// language or descriptor an LRC store drops), matching the editor's write warning.
	SyncedLyricsLoss SyncedLyricsLoss
	// SyncedLyricsTimeMax caps a synced-lyric line's timestamp this format stores, or 0 for no
	// limit. ID3v2 SYLT's 32-bit millisecond field (~49.7 days) and the VorbisComment LRC
	// store's re-parse ceiling ([MaxLRCTime]) both bound it. ProjectTransfer uses it to mark a
	// synced-lyrics set Lossy when a line exceeds it, so a copy that will clamp a timestamp
	// reports the loss instead of a clean carry, matching the write-time
	// synced-lyrics-timestamp-clamped warning - the analogue of ChapterTitleByteMax. Set only on
	// the synced-lyrics capability.
	SyncedLyricsTimeMax time.Duration
	// PictureMIMEs lists the cover MIME types this format can store; nil means no
	// per-MIME restriction. The format may still store no pictures at all, which is
	// decided by Write == AccessNone. A non-nil list (MP4's covr
	// atom: JPEG/PNG/BMP) lets the transfer layer drop a single unrepresentable cover
	// per-image instead of failing the whole copy. The pictures capability only.
	PictureMIMEs []string

	// reducesValue, when set, decides per value whether this capability stores reduced
	// fidelity. It refines [dispose] for fields whose loss depends on the value, such as
	// an ID3v2.3 year-only date that stores "2021" losslessly but truncates "2021-05-03".
	// The field stays unexported because Capability is publicly aliased and JSON-marshaled.
	reducesValue func(string) bool
	// dropsValue, when set, decides per value whether this capability cannot store it at
	// all. It takes precedence over reducesValue in [dispose], so a value omitted by the
	// writer is reported as dropped rather than merely lossy.
	dropsValue func(string) bool
}

// WithValueReduction returns a copy of c with a per-value reduction predicate. Internal
// codecs use it when a field's transfer grade depends on the specific value being stored.
func WithValueReduction(c Capability, reduces func(string) bool) Capability {
	c.reducesValue = reduces
	return c
}

// WithValueDrop returns a copy of c with a per-value drop predicate. Internal codecs use
// it to keep transfer reports aligned with values their writer omits entirely.
func WithValueDrop(c Capability, drops func(string) bool) Capability {
	c.dropsValue = drops
	return c
}

// Representable reports whether the pictures capability c can store picture p's image
// format. A nil PictureMIMEs list imposes no MIME restriction; Write still decides
// whether pictures are supported at all. The check uses [Picture.EffectiveMIME], so a
// JPEG labeled image/jpg or IMAGE/JPEG is accepted, while a GIF mislabeled as JPEG is
// rejected. ProjectTransfer and PrepareTransfer use this same predicate, keeping the
// report and write filter aligned.
func Representable(c Capability, p Picture) bool {
	return MIMERepresentable(c, p.EffectiveMIME())
}

// MIMERepresentable is [Representable] after the sniff: it decides from an
// already-computed effective MIME, so a caller can sniff once and reuse the result. A
// PictureMIMEs entry ending in "/*" is a wildcard matching any MIME of that top-level
// type (e.g. image/* accepts image/tiff); exact entries still match by equality, so a
// fixed list like MP4's is unaffected.
func MIMERepresentable(c Capability, mime string) bool {
	if len(c.PictureMIMEs) == 0 || slices.Contains(c.PictureMIMEs, mime) {
		return true
	}
	// Match a trailing "/*" wildcard against the lowercased MIME, mirroring a reader that
	// gates attachments on a lowercase HasPrefix (Matroska accepts any image/ subtype), so
	// a valid-but-unsniffable subtype is not over-conservatively graded Dropped.
	lower := strings.ToLower(mime)
	for _, pat := range c.PictureMIMEs {
		if prefix, ok := strings.CutSuffix(pat, "/*"); ok && strings.HasPrefix(lower, prefix+"/") {
			return true
		}
	}
	return false
}

// PartitionRepresentable splits pics into the covers a pictures capability c can store and
// the effective MIME type of each cover it cannot, one entry per dropped cover in first-seen
// order (so UnrepresentableReason can dedup them and a dropped item's count stays right). It
// also returns keptIdx: the original index in pics of each kept cover, so a caller carrying a
// per-picture slice parallel to pics (the editor's added-mask) can filter it in the same pass
// rather than re-running the representability test. This is the one per-image split shared by
// ProjectTransfer's report, PrepareTransfer's write filter, and the editor's drop-unsupported
// path, so the three cannot drift on which covers a destination keeps. Each cover's effective
// MIME is computed once.
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

// RecordingDateV23Capability describes RECORDINGDATE in an ID3v2.3 tag. Its write level
// stays AccessFull because the ID3 writer already reports the precise precision loss through
// ReducedDates; the value-reduction predicate attached by id3 gives transfers the same
// per-value grading without adding another editor warning.
//
// The Fidelity string is deliberately component-agnostic: v2.3 splits a date across TYER (year),
// TDAT (day+month), and TIME (hour+minute), so which component a given value loses varies: a
// "2021-06" drops the month, a "2021-06-15T10" drops the hour, a full "...T10:30:45" drops the
// seconds. A component-specific reason ("seconds dropped") is wrong for the month case, so the shared
// transfer reason names none; the per-value [value-reduced] write warning carries the specific one.
func RecordingDateV23Capability() Capability {
	return Capability{
		Read: AccessFull, Write: AccessFull,
		Representation: "ID3v2.3 TYER+TDAT+TIME",
		Fidelity:       "ID3v2.3 date frames store reduced precision, so a finer component was dropped",
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
	// fieldClassifier is the per-field transfer hook WithFieldClassifier attaches, nil unless a
	// codec sets one. It is unexported so it never JSON-marshals (Capabilities is a public
	// alias); WithFieldClassifier documents what it grades and when ProjectTransfer runs it.
	fieldClassifier FieldClassifier
}

// FieldClassifier is the per-field transfer-grading hook [Capabilities.WithFieldClassifier]
// attaches. It receives a field's canonical key, the values the destination would store, and
// the whole source tag set, and returns an overriding disposition and reason plus whether to
// apply them - a false third result leaves the format-level grade untouched. Naming the shape
// once keeps the struct field, the setter, and the codec classifiers that satisfy it from
// drifting.
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

// WithSyncedLyrics returns a copy of c with its synced-lyrics capability set. This keeps
// [NewCapabilities] stable for existing codec construction while allowing codecs to opt
// in to the additional capability dimension. Codecs that do not call it retain the zero
// Capability, whose AccessNone read/write reports "no synced lyrics".
func (c Capabilities) WithSyncedLyrics(sl Capability) Capabilities {
	c.SyncedLyrics = sl
	return c
}

// WithFieldClassifier returns a copy of c with a per-field transfer classifier. It is the
// third and most granular of the transfer grading hooks: [WithValueReduction] and
// [WithValueDrop] decide per value (func(string) bool, consumed in [dispose]'s value loop
// with drop-before-reduce precedence), while this one alone sees a field's cardinality,
// its key, and its sibling fields. Codecs use it to grade the writer-side drops those
// per-value predicates cannot express: Matroska keeping only the first of a multi-value
// TITLE (cardinality), a Vorbis reserved-namespace custom key (the key), an ID3 total
// whose sibling number is non-numeric (a cross-field decision). [ProjectTransfer] consults
// it only for a field graded Carried, which it may override to Dropped or Lossy to match a
// writer-side drop; it never overrides a field the format-level capability already graded
// Dropped or Lossy. The three hooks stay separate because they differ in granularity;
// folding them into one is a larger refactor not warranted pre-v1.0.
func (c Capabilities) WithFieldClassifier(fn FieldClassifier) Capabilities {
	c.fieldClassifier = fn
	return c
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
