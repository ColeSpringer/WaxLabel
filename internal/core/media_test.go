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
