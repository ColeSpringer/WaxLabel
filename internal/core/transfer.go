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
)

func (k TransferKind) String() string {
	switch k {
	case TransferPicture:
		return "picture"
	case TransferChapter:
		return "chapter"
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
)

func (d Disposition) String() string {
	switch d {
	case Carried:
		return "carried"
	case Lossy:
		return "lossy"
	default:
		return "dropped"
	}
}

// TransferItem is one piece of metadata's fate in a transfer. Key is set for
// TransferField items; for the picture and chapter sets it is empty and Count
// is the number of items in the set.
type TransferItem struct {
	Kind        TransferKind
	Key         tag.Key
	Count       int
	Disposition Disposition
	Reason      string
}

// TransferReport is the result of projecting a source document's canonical
// metadata onto a destination format: one item per field/picture-set/chapter-set,
// in source order. It is purely descriptive (no I/O) and is the exact projection
// a transfer applies, so a report and the write it predicts cannot disagree.
type TransferReport struct {
	Source Format
	Dest   Format
	Items  []TransferItem
}

// Counts tallies the items by disposition.
func (r TransferReport) Counts() (carried, lossy, dropped int) {
	for _, it := range r.Items {
		switch it.Disposition {
		case Carried:
			carried++
		case Lossy:
			lossy++
		case Dropped:
			dropped++
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
// This is the single decision point both PlanTransfer (simulation) and the
// transfer apply path consult, so the reported fate and the bytes a transfer
// actually writes derive from the same computation.
func ProjectTransfer(src *Media, dst Capabilities) []TransferItem {
	var items []TransferItem
	for _, k := range src.Tags.Keys() {
		vals, _ := src.Tags.Get(k)
		disp, reason := dispose(dst.Field(k), dst.ReadOnly, len(vals), "this field")
		items = append(items, TransferItem{
			Kind: TransferField, Key: k, Count: len(vals),
			Disposition: disp, Reason: reason,
		})
	}
	if n := len(src.Pictures); n > 0 {
		disp, reason := dispose(dst.Pictures, dst.ReadOnly, n, "pictures")
		items = append(items, TransferItem{
			Kind: TransferPicture, Count: n, Disposition: disp, Reason: reason,
		})
	}
	if n := len(src.Chapters); n > 0 {
		disp, reason := dispose(dst.Chapters, dst.ReadOnly, n, "chapters")
		items = append(items, TransferItem{
			Kind: TransferChapter, Count: n, Disposition: disp, Reason: reason,
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
func dispose(c Capability, readOnly bool, count int, noun string) (Disposition, string) {
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
	if c.Write == AccessPartial {
		if c.Fidelity != "" {
			return Lossy, c.Fidelity
		}
		if len(c.Constraints) > 0 {
			return Lossy, strings.Join(c.Constraints, "; ")
		}
		return Lossy, "stored with reduced fidelity"
	}
	return Carried, ""
}
