package mapping

import (
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TBPM and MVNM text frames; MVIN is a pair frame in internal/id3.
func TestID3ITunesFrames(t *testing.T) {
	cases := []struct {
		id  string
		key tag.Key
	}{
		{"TBPM", tag.BPM},
		{"MVNM", tag.MovementName},
	}
	for _, c := range cases {
		if k, ok := ID3FrameKey(c.id); !ok || k != c.key {
			t.Errorf("ID3FrameKey(%q) = %q, %v; want %s, true", c.id, k, ok, c.key)
		}
		if id, ok := ID3KeyFrame(c.key); !ok || id != c.id {
			t.Errorf("ID3KeyFrame(%s) = %q, %v; want %q, true", c.key, id, ok, c.id)
		}
	}
	if _, ok := ID3FrameKey("MVIN"); ok {
		t.Error("MVIN is a pair frame handled by the id3 codec, not the text-frame table")
	}
	// WORK has no text frame; Picard uses TXXX:WORK. TIT1 stays Grouping.
	if id, ok := ID3KeyFrame(tag.Work); ok {
		t.Errorf("ID3KeyFrame(WORK) = %q, true; want no dedicated frame (TXXX:WORK)", id)
	}
}

// ©wrk, ©mvn, ©enc text atoms round-trip.
func TestMP4ITunesTextAtoms(t *testing.T) {
	cases := []struct {
		name string
		key  tag.Key
	}{
		{"\xa9wrk", tag.Work},
		{"\xa9mvn", tag.MovementName},
		{"\xa9enc", tag.EncodedBy},
	}
	for _, c := range cases {
		if k, ok := MP4TextKey(c.name); !ok || k != c.key {
			t.Errorf("MP4TextKey(%q) = %q, %v; want %s, true", c.name, k, ok, c.key)
		}
		if name, ok := MP4KeyText(c.key); !ok || name != c.name {
			t.Errorf("MP4KeyText(%s) = %q, %v; want %q, true", c.key, name, ok, c.name)
		}
	}
}

// iTunes structured keys fold on freeform read only; write uses structured or ©-text atoms.
func TestMP4ITunesFreeformFold(t *testing.T) {
	cases := []struct {
		key       tag.Key
		spellings []string
	}{
		{tag.ITunesAdvisory, []string{"ITUNESADVISORY", "iTunesAdvisory", "itunesadvisory"}},
		{tag.ITunesGapless, []string{"ITUNESGAPLESS", "iTunesGapless"}},
		{tag.ShowMovement, []string{"SHOWMOVEMENT", "ShowMovement"}},
		{tag.BPM, []string{"BPM", "bpm"}},
		{tag.Work, []string{"WORK", "Work"}},
		{tag.MovementName, []string{"MOVEMENTNAME", "MovementName"}},
		{tag.Movement, []string{"MOVEMENT", "Movement"}},
		{tag.MovementTotal, []string{"MOVEMENTTOTAL", "MovementTotal"}},
		{tag.EncodedBy, []string{"ENCODEDBY", "EncodedBy"}},
	}
	for _, c := range cases {
		for _, spelling := range c.spellings {
			if k, ok := MP4FreeformKey(spelling); !ok || k != c.key {
				t.Errorf("MP4FreeformKey(%q) = %q, %v; want %s, true (case must fold)", spelling, k, ok, c.key)
			}
		}
	}
}
