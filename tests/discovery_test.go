package waxlabel_test

import (
	"testing"

	"github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestCapabilitiesFor checks the file-less, format-level capability query: a
// registered format reports its real, writable support, and the result matches
// what the file-aware Document.Capabilities reports for the same format (both
// route through the one codec call).
func TestCapabilitiesFor(t *testing.T) {
	caps := waxlabel.CapabilitiesFor(waxlabel.FormatFLAC)
	if caps.Format != waxlabel.FormatFLAC {
		t.Errorf("Format = %v, want FLAC", caps.Format)
	}
	if caps.ReadOnly {
		t.Error("FLAC reported ReadOnly")
	}
	if got := caps.GenericField.Write; got != waxlabel.AccessFull {
		t.Errorf("FLAC field write = %v, want full", got)
	}
	// No current format restricts a key's natural cardinality, so MaxValues is the
	// zero default ("defer to the key's Multivalued").
	if mv := caps.GenericField.MaxValues; mv != 0 {
		t.Errorf("FLAC field MaxValues = %d, want 0", mv)
	}
}

// TestCapabilitiesForUnknownIsReadOnly locks the documented fallback: an unknown
// or unimplemented format reports read-only rather than panicking, mirroring
// Document.Capabilities's no-codec path.
func TestCapabilitiesForUnknownIsReadOnly(t *testing.T) {
	caps := waxlabel.CapabilitiesFor(waxlabel.FormatUnknown)
	if !caps.ReadOnly {
		t.Error("unknown format should report ReadOnly")
	}
	if caps.Format != waxlabel.FormatUnknown {
		t.Errorf("Format = %v, want Unknown", caps.Format)
	}
}

// TestKnownKeysCardinalityIsDiscoverable proves a consumer can enumerate the
// editable vocabulary and its cardinality with no hard-coded key list - the data
// a UI needs to render an edit form - using only the public API surface this
// build adds.
func TestKnownKeysCardinalityIsDiscoverable(t *testing.T) {
	keys := tag.KnownKeys()
	if len(keys) == 0 {
		t.Fatal("KnownKeys() returned no keys")
	}
	multi := 0
	for _, k := range keys {
		if k.Description() == "" {
			t.Errorf("known key %q has no description", k)
		}
		if k.Multivalued() {
			multi++
		}
	}
	if multi == 0 {
		t.Error("no key reported Multivalued()==true; expected the artist/genre family")
	}
}
