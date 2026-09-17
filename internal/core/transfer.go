package core

import (
	"fmt"
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// TransferKind names transferred metadata category (field, picture, chapter, synced lyrics).
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

// Disposition grades cross-format transfer outcome (carried, lossy, dropped, excluded).
type Disposition uint8

const (
	// Carried: stored losslessly.
	Carried Disposition = iota
	// Lossy: stored with reduced fidelity.
	Lossy
	// Dropped: destination cannot store it.
	Dropped
	// Excluded: policy skip; not a capability loss.
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

// TransferItem is one metadata item's transfer fate. Count is set size for picture/chapter/lyrics.
type TransferItem struct {
	Kind        TransferKind
	Key         tag.Key
	Count       int
	Disposition Disposition
	Reason      string
}

// TransferReport projects source metadata onto destination capabilities. Set items may split
// carried/lossy/dropped. Descriptive only; matches transfer write filter.
type TransferReport struct {
	Source Format
	Dest   Format
	Items  []TransferItem
}

// Counts tallies by disposition. Set items sum Count; field items count as one each.
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

// HasDropped reports any Dropped item (distinct from merely lossy).
func (r TransferReport) HasDropped() bool {
	_, _, dropped := r.Counts()
	return dropped > 0
}

// ProjectTransfer grades each metadata item against dst capabilities.
// Shared by PlanTransfer and apply path. Pictures may also drop unrepresentable MIMEs.
func ProjectTransfer(src *Media, dst Capabilities) []TransferItem {
	var items []TransferItem
	for _, k := range src.Tags.Keys() {
		vals, _ := src.Tags.Get(k)
		if k.DescribesOwnAudio() {
			// Own-audio keys are excluded; copy stays lossless.
			items = append(items, TransferItem{
				Kind: TransferField, Key: k, Count: len(vals), Disposition: Excluded,
				Reason: "not transferred; describes this file's own audio, not the work, so the destination keeps whatever it already had for this key",
			})
			continue
		}
		// Grade stored form: trim trimmable keys before predicates.
		graded := vals
		if tag.IsTrimmableKey(k) {
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
		// fieldClassifier may override a Carried grade to match writer-side drops.
		if disp == Carried && dst.fieldClassifier != nil {
			if d, r, ok := dst.fieldClassifier(k, graded, src.Tags); ok {
				disp, reason = d, r
			}
		}
		items = append(items, TransferItem{
			Kind: TransferField, Key: k, Count: len(vals),
			Disposition: disp, Reason: reason,
		})
	}
	if len(src.Pictures) > 0 {
		// Split by representable MIME (same as write filter).
		rep, _, unrepMIMEs := PartitionRepresentable(dst.Pictures, src.Pictures)
		// Then partition fixed slots (APE). Skipped when pictures cannot be written.
		var slotDropped int
		var slotReason string
		if !dst.ReadOnly && dst.Pictures.Write != AccessNone {
			rep, slotDropped, slotReason = PartitionPictureSlots(dst.Pictures, rep)
		}
		if len(rep) > 0 {
			disp, reason := dispose(dst.Pictures, dst.ReadOnly, len(rep), "pictures", nil)
			if disp == Carried {
				// Per-picture metadata loss splits carried vs lossy.
				var carried, lossy int
				for _, p := range rep {
					if pictureLosesMetadata(p, dst.Pictures.PictureLoss) {
						lossy++
					} else {
						carried++
					}
				}
				items = appendCarriedLossyItems(items, TransferPicture, carried, lossy, dst.Pictures.Reason())
			} else {
				items = append(items, TransferItem{
					Kind: TransferPicture, Count: len(rep), Disposition: disp, Reason: reason,
				})
			}
		}
		if slotDropped > 0 {
			items = append(items, TransferItem{
				Kind: TransferPicture, Count: slotDropped, Disposition: Dropped, Reason: slotReason,
			})
		}
		if len(unrepMIMEs) > 0 {
			items = append(items, TransferItem{
				Kind: TransferPicture, Count: len(unrepMIMEs), Disposition: Dropped,
				Reason: UnrepresentableReason(dst.Format, unrepMIMEs),
			})
		}
	}
	if n := len(src.Chapters); n > 0 {
		chapters := src.Chapters
		disp, reason := dispose(dst.Chapters, dst.ReadOnly, n, "chapters", nil)
		if disp == Carried {
			// Per-chapter metadata or title-cap loss splits carried vs lossy.
			var carried, lossy int
			lostMetadata := false
			for i, c := range chapters {
				metaLoss := ChapterLosesMetadata(chapters, i, dst.Chapters.ChapterLoss)
				titleLoss := dst.Chapters.ChapterTitleByteMax > 0 && len(c.Title) > dst.Chapters.ChapterTitleByteMax
				if metaLoss || titleLoss {
					lossy++
					lostMetadata = lostMetadata || metaLoss
				} else {
					carried++
				}
			}
			// One lossy reason: metadata wins over title-cap-only.
			lossyReason := dst.Chapters.Reason()
			if !lostMetadata {
				lossyReason = "chapter title is too long and was truncated"
			}
			items = appendCarriedLossyItems(items, TransferChapter, carried, lossy, lossyReason)
		} else {
			items = append(items, TransferItem{
				Kind: TransferChapter, Count: n, Disposition: disp, Reason: reason,
			})
		}
	}
	if n := len(src.SyncedLyrics); n > 0 {
		disp, reason := dispose(dst.SyncedLyrics, dst.ReadOnly, n, "synced lyrics", nil)
		if disp == Carried {
			// Per-set metadata or timestamp clamp splits carried vs lossy.
			var carried, lossy int
			lostMetadata := false
			for _, sl := range src.SyncedLyrics {
				metaLoss := SyncedLyricsSetLosesMetadata(sl, dst.SyncedLyrics.SyncedLyricsLoss)
				clampLoss := SyncedLyricsSetClampOverflows(sl, dst.SyncedLyrics.SyncedLyricsTimeMax)
				if metaLoss || clampLoss {
					lossy++
					lostMetadata = lostMetadata || metaLoss
				} else {
					carried++
				}
			}
			// One lossy reason: metadata wins over clamp-only.
			lossyReason := dst.SyncedLyrics.Reason()
			if !lostMetadata {
				lossyReason = "a synced-lyric timestamp is too large and was clamped"
			}
			items = appendCarriedLossyItems(items, TransferSyncedLyric, carried, lossy, lossyReason)
		} else {
			items = append(items, TransferItem{
				Kind: TransferSyncedLyric, Count: n, Disposition: disp, Reason: reason,
			})
		}
	}
	return items
}

// appendCarriedLossyItems appends carried then merged lossy items (empty reason on carried).
func appendCarriedLossyItems(items []TransferItem, kind TransferKind, carried, lossy int, lossyReason string) []TransferItem {
	if carried > 0 {
		items = append(items, TransferItem{Kind: kind, Count: carried, Disposition: Carried})
	}
	if lossy > 0 {
		items = append(items, TransferItem{Kind: kind, Count: lossy, Disposition: Lossy, Reason: lossyReason})
	}
	return items
}

// dispose grades metadata against capability c. Read-only and MaxItems overrun -> Dropped.
// Does not use MaxValues (discovery hint only; loss is via Fidelity/Constraints).
func dispose(c Capability, readOnly bool, count int, noun string, values []string) (Disposition, string) {
	if readOnly {
		return Dropped, "destination is read-only"
	}
	if c.Write == AccessNone {
		// User-facing drop wording names destination limits, not internal Representation strings.
		if c.Read != AccessNone {
			return Dropped, "destination cannot write " + noun
		}
		return Dropped, "destination format does not store " + noun
	}
	if c.MaxItems > 0 && count > c.MaxItems {
		return Dropped, fmt.Sprintf("exceeds the destination limit of %d", c.MaxItems)
	}
	// dropsValue before reducesValue: omit -> Dropped, not Lossy.
	if c.dropsValue != nil {
		for _, v := range values {
			if c.dropsValue(v) {
				return Dropped, "the destination cannot store this value"
			}
		}
	}
	// reducesValue: value-dependent Lossy vs Carried.
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

// UnrepresentableReason names dst and distinct unrepresentable MIMEs (effective MIME).
func UnrepresentableReason(dst Format, mimes []string) string {
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
