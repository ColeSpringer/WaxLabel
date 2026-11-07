package core

import "testing"

// TestCloneDetachesTextItems: a clone's comment and lyrics items are its own.
func TestCloneDetachesTextItems(t *testing.T) {
	m := &Media{
		Comments: []TextItem{{Text: "c", Description: "d", Language: "eng"}},
		Lyrics:   []TextItem{{Text: "l", Language: "deu"}},
	}
	c := m.Clone()
	if len(c.Comments) != 1 || c.Comments[0] != m.Comments[0] || len(c.Lyrics) != 1 || c.Lyrics[0] != m.Lyrics[0] {
		t.Fatalf("clone items = %+v / %+v, want %+v / %+v", c.Comments, c.Lyrics, m.Comments, m.Lyrics)
	}
	c.Comments[0].Text = "changed"
	c.Lyrics[0].Text = "changed"
	if m.Comments[0].Text != "c" || m.Lyrics[0].Text != "l" {
		t.Errorf("mutating the clone changed the original: %+v / %+v", m.Comments, m.Lyrics)
	}
}

// TestFamilyNumbering: nothing projects Lyrics3, so its slot is retired but kept, and the families
// after it keep the numbers the root package re-exports.
func TestFamilyNumbering(t *testing.T) {
	if got := Family(5).String(); got != "unknown" {
		t.Errorf("Family(5) = %q, want unknown", got)
	}
	if FamilyAPEv2 != 4 || FamilyMP4 != 6 || FamilyASF != 10 {
		t.Errorf("APEv2, MP4, ASF = %d, %d, %d; want 4, 6, 10", FamilyAPEv2, FamilyMP4, FamilyASF)
	}
}
