package mapping

import (
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestID3TXXXKeyTCMP checks that ffmpeg's TXXX:TCMP user frame folds onto canonical
// COMPILATION (case- and whitespace-insensitive), matching the dedicated TCMP text frame.
func TestID3TXXXKeyTCMP(t *testing.T) {
	for _, desc := range []string{"TCMP", "tcmp", " Tcmp "} {
		if k, ok := ID3TXXXKey(desc); !ok || k != tag.Compilation {
			t.Errorf("ID3TXXXKey(%q) = %q, %v; want COMPILATION, true", desc, k, ok)
		}
	}
	// An unlisted description stays a custom key, not COMPILATION.
	if k, ok := ID3TXXXKey("SOMETHINGELSE"); !ok || k == tag.Compilation {
		t.Errorf("ID3TXXXKey(unlisted) = %q, %v; want a custom key (not COMPILATION)", k, ok)
	}
}

// TestID3LyricistFrame pins the LYRICIST mapping coupling: the conformant TEXT frame
// reads onto canonical LYRICIST and writes back to TEXT, and a legacy TXXX:LYRICIST
// user frame still folds onto LYRICIST on read.
func TestID3LyricistFrame(t *testing.T) {
	if k, ok := ID3FrameKey("TEXT"); !ok || k != tag.Lyricist {
		t.Errorf("ID3FrameKey(\"TEXT\") = %q, %v; want LYRICIST, true", k, ok)
	}
	if id, ok := ID3KeyFrame(tag.Lyricist); !ok || id != "TEXT" {
		t.Errorf("ID3KeyFrame(LYRICIST) = %q, %v; want TEXT, true", id, ok)
	}
	if k, ok := ID3TXXXKey("LYRICIST"); !ok || k != tag.Lyricist {
		t.Errorf("ID3TXXXKey(\"LYRICIST\") = %q, %v; want LYRICIST, true", k, ok)
	}
}

// TestID3InvolvedRoles pins the involved-people mapping both directions: each role's
// canonical Picard function round-trips, the read lookup folds case, and the read-only
// aliases fold onto the canonical key while the write spelling stays canonical.
func TestID3InvolvedRoles(t *testing.T) {
	cases := []struct {
		key tag.Key
		fn  string
	}{
		{tag.Producer, "producer"},
		{tag.Engineer, "engineer"},
		{tag.Mixer, "mix"},
		{tag.Arranger, "arranger"},
		{tag.DJMixer, "DJ-mix"},
	}
	for _, c := range cases {
		if got, ok := ID3InvolvedFunction(c.key); !ok || got != c.fn {
			t.Errorf("ID3InvolvedFunction(%s) = %q, %v; want %q, true", c.key, got, ok, c.fn)
		}
		if got, ok := ID3InvolvedRoleKey(c.fn); !ok || got != c.key {
			t.Errorf("ID3InvolvedRoleKey(%q) = %q, %v; want %s, true", c.fn, got, ok, c.key)
		}
	}

	// WRITER is not an involved-people role: it is a TXXX:Writer user frame.
	if fn, ok := ID3InvolvedFunction(tag.Writer); ok {
		t.Errorf("ID3InvolvedFunction(WRITER) = %q, true; want false (WRITER is a TXXX frame)", fn)
	}

	// The read lookup folds case, and the two diverging roles read back from their canonical
	// key spelling too (Picard writes "mix"/"DJ-mix", but a file may carry "Mix"/"DJ-MIX").
	for _, c := range []struct {
		fn   string
		want tag.Key
	}{
		{"Mix", tag.Mixer},
		{"DJ-MIX", tag.DJMixer},
		{"PRODUCER", tag.Producer},
	} {
		if got, ok := ID3InvolvedRoleKey(c.fn); !ok || got != c.want {
			t.Errorf("ID3InvolvedRoleKey(%q) = %q, %v; want %s, true (case must fold)", c.fn, got, ok, c.want)
		}
	}

	// Read-only aliases fold onto the canonical key, yet the write spelling stays canonical.
	for _, c := range []struct {
		fn   string
		want tag.Key
	}{
		{"mixer", tag.Mixer},
		{"dj-mixer", tag.DJMixer},
		{"djmixer", tag.DJMixer},
		{"dj mix", tag.DJMixer},
		{"dj mixer", tag.DJMixer},
		{"dj_mixer", tag.DJMixer},
		{"DJ_MIXER", tag.DJMixer}, // case folds too
	} {
		if got, ok := ID3InvolvedRoleKey(c.fn); !ok || got != c.want {
			t.Errorf("ID3InvolvedRoleKey(%q) = %q, %v; want %s, true (read alias must fold)", c.fn, got, ok, c.want)
		}
	}
	if got, _ := ID3InvolvedFunction(tag.Mixer); got != "mix" {
		t.Errorf("ID3InvolvedFunction(MIXER) = %q, want mix (write stays canonical, not the read alias)", got)
	}
	if got, _ := ID3InvolvedFunction(tag.DJMixer); got != "DJ-mix" {
		t.Errorf("ID3InvolvedFunction(DJMIXER) = %q, want DJ-mix", got)
	}

	// An unmodeled involvement does not resolve (it is preserved on write, not projected).
	if k, ok := ID3InvolvedRoleKey("mastering"); ok {
		t.Errorf("ID3InvolvedRoleKey(mastering) = %q, true; want no match", k)
	}

	// ID3InvolvedKeys is the deterministic emit order.
	want := []tag.Key{tag.Producer, tag.Engineer, tag.Mixer, tag.Arranger, tag.DJMixer}
	if got := ID3InvolvedKeys(); !slices.Equal(got, want) {
		t.Errorf("ID3InvolvedKeys() = %v, want %v", got, want)
	}
}

// TestID3TXXXKeyDJMixer folds a foreign TXXX:DJ MIXER / DJ-MIXER user frame onto canonical
// DJMIXER on read. Writes always target TIPL/IPLS, so this only widens read acceptance.
func TestID3TXXXKeyDJMixer(t *testing.T) {
	for _, desc := range []string{"DJ MIXER", "DJ-MIXER", "DJ_MIXER", "dj mixer"} {
		if k, ok := ID3TXXXKey(desc); !ok || k != tag.DJMixer {
			t.Errorf("ID3TXXXKey(%q) = %q, %v; want DJMIXER, true", desc, k, ok)
		}
	}
}
