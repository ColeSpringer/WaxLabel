package core

import (
	"fmt"
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// TransferKind names the category of a transferred piece of metadata: a
// canonical field, the picture set, or the chapter list.
type TransferKind uint8

const (
	TransferField TransferKind = iota
	TransferPicture
	TransferChapter
	TransferSyncedLyric
)

func (k TransferKind) String() string {
	switch k {
	case TransferPicture:
		return "picture"
	case TransferChapter:
		return "chapter"
	case TransferSyncedLyric:
		return "synced lyrics"
	default:
		return "field"
	}
}

// Disposition grades how a piece of metadata survives a cross-format transfer.
// The three states mirror the capability model: a destination either stores a
// value faithfully, stores it with reduced fidelity, or cannot store it at all.
type Disposition uint8

const (
	// Carried means the destination stores the value losslessly.
	Carried Disposition = iota
	// Lossy means the destination stores it with reduced fidelity (the Reason
	// records how).
	Lossy
	// Dropped means the destination cannot store it (the Reason records why).
	Dropped
	// Excluded means policy deliberately leaves the value out of the transfer. It is
	// reported with a reason, but it is not a capability loss and does not make an
	// otherwise clean copy lossy.
	Excluded
)

func (d Disposition) String() string {
	switch d {
	case Carried:
		return "carried"
	case Lossy:
		return "lossy"
	case Excluded:
		return "excluded"
	default:
		return "dropped"
	}
}

// TransferItem is one piece of metadata's fate in a transfer. Key is set for
// TransferField items; for the picture, chapter, and synced-lyrics sets it is empty and
// Count is the number of items in the set.
type TransferItem struct {
	Kind        TransferKind
	Key         tag.Key
	Count       int
	Disposition Disposition
	Reason      string
}

// TransferReport is the result of projecting a source document's canonical
// metadata onto a destination format: one item per field and chapter-set, in source
// order, and one or two picture items (a destination that stores only certain cover
// MIME types splits the set into a carried and a dropped item). It is purely
// descriptive (no I/O) and is built from the same projection a transfer write applies.
type TransferReport struct {
	Source Format
	Dest   Format
	Items  []TransferItem
}

// Counts tallies the items by disposition. A picture- or chapter-set item stands for
// it.Count pictures/chapters, so its Count is summed - the headline then matches the
// per-item detail and the JSON counts. A field item is a single unit (its Count is
// the value count, e.g. two ARTIST values, not a number of fields), so it counts as one.
func (r TransferReport) Counts() (carried, lossy, dropped int) {
	for _, it := range r.Items {
		n := 1
		if it.Kind == TransferPicture || it.Kind == TransferChapter || it.Kind == TransferSyncedLyric {
			n = it.Count
		}
		switch it.Disposition {
		case Carried:
			carried += n
		case Lossy:
			lossy += n
		case Dropped:
			dropped += n
		}
	}
	return carried, lossy, dropped
}

// Lossless reports whether every item carries without loss (nothing lossy or
// dropped).
func (r TransferReport) Lossless() bool {
	_, lossy, dropped := r.Counts()
	return lossy == 0 && dropped == 0
}

// ProjectTransfer computes how each piece of src's canonical metadata fares
// against the destination capabilities dst: a field/picture/chapter that the
// destination writes fully is Carried, one it writes with reduced fidelity is
// Lossy, and one it cannot write is Dropped. The capabilities already fold in the
// destination's write options, so option-dependent support (a format that gains a
// container only under certain options) is reflected here without this function
// needing the options itself.
//
// The picture set may split into two items when the destination stores only certain
// cover MIME types (see [Representable]): the representable subset is graded as usual,
// and the rest become one Dropped item naming the unsupported MIME types.
//
// PlanTransfer (simulation) and the transfer apply path both use this computation, so
// the reported fates match the write filter.
func ProjectTransfer(src *Media, dst Capabilities) []TransferItem {
	var items []TransferItem
	for _, k := range src.Tags.Keys() {
		vals, _ := src.Tags.Get(k)
		if k.DescribesOwnAudio() {
			// Metadata copies do not carry audio. Values that describe this file's audio,
			// such as encoder stamps, ReplayGain, or an AcoustID fingerprint, would describe
			// the source samples after being copied to a different destination. Report the
			// skip explicitly, but keep the copy lossless.
			items = append(items, TransferItem{
				Kind: TransferField, Key: k, Count: len(vals), Disposition: Excluded,
				Reason: "not transferred; describes this file's own audio, not the work; the destination keeps its own value",
			})
			continue
		}
		// Grade the value the writer would store, not the raw parsed bytes. Trimmable fields
		// ([tag.IsTrimmableKey]: numeric, date, media-type, ReplayGain) are trimmed before
		// rendering, so value-level predicates should see the stored form (matching
		// TrimTokenValue's own gate). ReplayGain keys are filtered upstream as own-audio, so in
		// practice only the first three reach here; keying off the shared predicate keeps this gate
		// from drifting from TrimTokenValue when a trimmable key is added.
		graded := vals
		if tag.IsTrimmableKey(k) {
			// Copy on write: most stored values are already clean, so they reuse vals.
			cloned := false
			for i, v := range vals {
				trimmed := tag.TrimTokenValue(k, v)
				if trimmed == v {
					continue
				}
				if !cloned {
					graded = append([]string(nil), vals...)
					cloned = true
				}
				graded[i] = trimmed
			}
		}
		disp, reason := dispose(dst.Field(k), dst.ReadOnly, len(graded), "this field", graded)
		items = append(items, TransferItem{
			Kind: TransferField, Key: k, Count: len(vals),
			Disposition: disp, Reason: reason,
		})
		// A slashed track/disc number (TRACKNUMBER=3/12) is split into its number and total
		// keys by the codec read path, so TRACKTOTAL is an ordinary key graded by this loop -
		// no transfer-time derivation is needed.
	}
	if len(src.Pictures) > 0 {
		// Split the set by per-image representability. A destination such as MP4 can store
		// covers only in certain MIME types, so it drops just the unsupported covers instead
		// of failing the whole transfer. Each cover's effective MIME is computed once and
		// reused for the split decision and the dropped reason.
		var rep []Picture
		var unrepMIMEs []string
		for _, p := range src.Pictures {
			if mime := p.EffectiveMIME(); MIMERepresentable(dst.Pictures, mime) {
				rep = append(rep, p)
			} else {
				unrepMIMEs = append(unrepMIMEs, mime)
			}
		}
		if len(rep) > 0 {
			disp, reason := dispose(dst.Pictures, dst.ReadOnly, len(rep), "pictures", nil)
			if disp == Carried {
				// dispose reports picture sets as Carried when the image bytes themselves carry
				// byte-for-byte. MP4 and Matroska can still drop a picture's role or description,
				// but that loss is per-picture, so partition the representable subset: covers that
				// keep their metadata stay Carried, and those that lose it become Lossy. Copying a
				// front cover plus a back cover then reports 1 carried, 1 lossy, rather than the
				// whole set flipped to lossy by the one affected cover. Only the Carried branch
				// splits. A Dropped target (read-only, or no picture support) or a Lossy one keeps a
				// single full-count item with its target-level reason. The Lossy case is defensive:
				// no picture capability uses AccessPartial today.
				var carried, lossy []Picture
				for _, p := range rep {
					if pictureLosesMetadata(p, dst.Pictures.PictureLoss) {
						lossy = append(lossy, p)
					} else {
						carried = append(carried, p)
					}
				}
				// Deterministic item order for stable human/JSON output: carried first (empty
				// Reason), then lossy, then the dropped-MIME item emitted below. Counts() sums
				// multiple picture items, so the headline stays exact.
				if len(carried) > 0 {
					items = append(items, TransferItem{
						Kind: TransferPicture, Count: len(carried), Disposition: Carried,
					})
				}
				if len(lossy) > 0 {
					// Use dst.Pictures.Reason() rather than an inline Fidelity/Constraints check, so
					// the lossy-picture reason is worded like the chapter and synced-lyrics paths
					// below and is never empty.
					items = append(items, TransferItem{
						Kind: TransferPicture, Count: len(lossy), Disposition: Lossy, Reason: dst.Pictures.Reason(),
					})
				}
			} else {
				items = append(items, TransferItem{
					Kind: TransferPicture, Count: len(rep), Disposition: disp, Reason: reason,
				})
			}
		}
		if len(unrepMIMEs) > 0 {
			items = append(items, TransferItem{
				Kind: TransferPicture, Count: len(unrepMIMEs), Disposition: Dropped,
				Reason: unrepresentableReason(dst.Format, unrepMIMEs),
			})
		}
	}
	if n := len(src.Chapters); n > 0 {
		disp, reason := dispose(dst.Chapters, dst.ReadOnly, n, "chapters", nil)
		// Start+title-only destinations carry starts and titles but drop other chapter
		// metadata. Upgrade only sets carrying metadata the destination cannot represent;
		// plain chapter lists remain Carried.
		if disp == Carried && ChaptersLoseMetadata(src.Chapters, dst.Chapters.ChapterLoss) {
			disp = Lossy
			reason = dst.Chapters.Reason()
		}
		// A title longer than the format's byte cap (MP4's 255-byte chpl length prefix) is
		// truncated on write, a silent content loss the metadata check above does not cover, so
		// grade it Lossy too. Checked only when still Carried, so a set already Lossy keeps its
		// (broader) reason. len is the byte length, matching the chpl prefix.
		if disp == Carried && dst.Chapters.ChapterTitleByteMax > 0 {
			for _, c := range src.Chapters {
				if len(c.Title) > dst.Chapters.ChapterTitleByteMax {
					disp = Lossy
					reason = "chapter title is too long and was truncated"
					break
				}
			}
		}
		items = append(items, TransferItem{
			Kind: TransferChapter, Count: n, Disposition: disp, Reason: reason,
		})
	}
	if n := len(src.SyncedLyrics); n > 0 {
		disp, reason := dispose(dst.SyncedLyrics, dst.ReadOnly, n, "synced lyrics", nil)
		// LRC destinations carry the timed text but drop the per-set language and
		// descriptor. Upgrade only sets carrying metadata the destination cannot
		// represent; plain timed-text sets remain Carried.
		if disp == Carried && SyncedLyricsLoseMetadata(src.SyncedLyrics, dst.SyncedLyrics.SyncedLyricsLoss) {
			disp = Lossy
			reason = dst.SyncedLyrics.Reason()
		}
		// A line timestamp past the destination's 32-bit millisecond field (SYLT) or LRC
		// re-parse ceiling is clamped on write, a silent content loss the metadata check above
		// does not cover, so grade it Lossy too - the synced-lyrics analogue of the chapter
		// title-byte-cap upgrade. Checked only when still Carried, so a set already Lossy keeps
		// its (broader) reason.
		if disp == Carried && SyncedLyricsClampOverflows(src.SyncedLyrics, dst.SyncedLyrics.SyncedLyricsTimeMax) {
			disp = Lossy
			reason = "a synced-lyric timestamp is too large and was clamped"
		}
		items = append(items, TransferItem{
			Kind: TransferSyncedLyric, Count: n, Disposition: disp, Reason: reason,
		})
	}
	return items
}

// dispose grades how a piece of metadata (count items of it) survives against the
// destination capability c, returning the disposition and a human-readable reason
// drawn from the capability's own description. noun names the metadata kind
// ("pictures" / "chapters" / "this field") for a destination-focused drop reason.
// A read-only destination drops everything; a set that exceeds the capability's
// hard MaxItems is dropped (the destination would reject the whole set at write
// time, so reporting it carried would be a lie); otherwise the write level decides.
//
// Note: this does not consult Capability.MaxValues. dispose is the predictive half
// of the report==write invariant, and the apply path (PrepareTransfer) only skips
// Dropped items - a Lossy field is still written with all its values, the loss
// realized by the destination codec's writer. No codec truncates values to
// MaxValues at write time, so reporting a MaxValues truncation here would promise a
// write that never happens. A format that genuinely reduces a multi-value field
// expresses that through its Fidelity/Constraints (which its writer honors, e.g.
// WAV's single-valued INFO); MaxValues is a cardinality hint for discovery (caps),
// not a transfer-fidelity signal.
func dispose(c Capability, readOnly bool, count int, noun string, values []string) (Disposition, string) {
	if readOnly {
		return Dropped, "destination is read-only"
	}
	if c.Write == AccessNone {
		// Destination-focused wording: the reason a user sees is "what the target
		// format can't hold", not the source-side Representation string ("no covers",
		// "not modeled"), which read as internal jargon in the loss report.
		return Dropped, "destination format does not store " + noun
	}
	if c.MaxItems > 0 && count > c.MaxItems {
		return Dropped, fmt.Sprintf("exceeds the destination limit of %d", c.MaxItems)
	}
	// A value-drop predicate decides, per value, whether the destination cannot store it
	// at all. Check it before reduction so an omitted value is reported as dropped, not
	// lossy.
	if c.dropsValue != nil {
		for _, v := range values {
			if c.dropsValue(v) {
				return Dropped, "the destination cannot store this value"
			}
		}
	}
	// A value-reduction predicate decides Lossy vs Carried from the actual values. This
	// covers fields whose fidelity is value-dependent, such as a year-only date field.
	if c.reducesValue != nil {
		for _, v := range values {
			if c.reducesValue(v) {
				return Lossy, c.Reason()
			}
		}
		return Carried, ""
	}
	if c.Write == AccessPartial {
		return Lossy, c.Reason()
	}
	return Carried, ""
}

// unrepresentableReason names the destination format and the distinct MIME types it
// cannot store, in first-seen order, for a dropped-cover item's reason. mimes are the
// effective MIMEs rejected by MIMERepresentable, so a GIF mislabeled as JPEG reports
// "cannot store image/gif" rather than the stored label.
func unrepresentableReason(dst Format, mimes []string) string {
	seen := map[string]bool{}
	var distinct []string
	for _, m := range mimes {
		if !seen[m] {
			seen[m] = true
			distinct = append(distinct, m)
		}
	}
	return fmt.Sprintf("%s cannot store %s", dst, strings.Join(distinct, ", "))
}
