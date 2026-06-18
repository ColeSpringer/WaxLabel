package waxlabel_test

import (
	"bytes"
	"context"
	"math/rand"
	"os"
	"strconv"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestPreservationProperty is the property-based proof of the preservation-first
// contract: for an arbitrary sequence of canonical edits, writing then
// re-parsing yields exactly the edited tag set - edited keys take their new
// values and every untouched key is preserved. It also checks the audio
// essence is unchanged by any tag-only edit.
func TestPreservationProperty(t *testing.T) {
	ctx := context.Background()
	src, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}

	baseDoc, err := wl.Parse(ctx, wl.BytesSource(src))
	if err != nil {
		t.Fatal(err)
	}
	baseEssence, err := baseDoc.HashAudioEssence(ctx, wl.WithHashSource(wl.BytesSource(src)))
	if err != nil {
		t.Fatal(err)
	}

	keys := []tag.Key{
		tag.Title, tag.Artist, tag.Album, tag.Genre, tag.Comment,
		tag.RecordingDate, tag.TrackNumber, tag.Composer, tag.Key("CUSTOM_FIELD"),
	}
	rng := rand.New(rand.NewSource(42))

	randVals := func() []string {
		n := 1 + rng.Intn(3) // always >=1: Vorbis cannot store "present-empty"
		vals := make([]string, n)
		for i := range vals {
			vals[i] = "v" + strconv.Itoa(rng.Intn(1000))
		}
		return vals
	}

	for iter := 0; iter < 300; iter++ {
		var p tag.TagPatch
		for ops := rng.Intn(5); ops >= 0; ops-- {
			k := keys[rng.Intn(len(keys))]
			switch rng.Intn(3) {
			case 0:
				p.Set(k, randVals()...)
			case 1:
				p.Clear(k)
			case 2:
				p.Add(k, randVals()...)
			}
		}

		doc, err := wl.Parse(ctx, wl.BytesSource(src))
		if err != nil {
			t.Fatalf("iter %d parse: %v", iter, err)
		}
		plan, err := doc.Edit().Apply(p).Prepare()
		if err != nil {
			t.Fatalf("iter %d prepare: %v", iter, err)
		}
		var out bytes.Buffer
		if _, _, err := plan.Execute(ctx, wl.WriteTo(&out, wl.BytesSource(src))); err != nil {
			t.Fatalf("iter %d write: %v", iter, err)
		}

		re, err := wl.Parse(ctx, wl.BytesSource(out.Bytes()))
		if err != nil {
			t.Fatalf("iter %d reparse: %v", iter, err)
		}
		want := p.Apply(baseDoc.Tags())
		if !re.Tags().Equal(want) {
			t.Fatalf("iter %d: round-trip tag set mismatch\n got:  %v\n want: %v",
				iter, dumpTags(re.Tags()), dumpTags(want))
		}

		essence, err := re.HashAudioEssence(ctx, wl.WithHashSource(wl.BytesSource(out.Bytes())))
		if err != nil {
			t.Fatalf("iter %d essence: %v", iter, err)
		}
		if !essence.Equal(baseEssence) {
			t.Fatalf("iter %d: tag edit changed audio essence", iter)
		}
	}
}

func dumpTags(ts tag.TagSet) map[string][]string {
	m := map[string][]string{}
	for k, v := range ts.All() {
		m[string(k)] = v
	}
	return m
}

// TestTypedRoundTrip checks the typed->native->typed identity for the common
// fields: a Tags struct written and re-read projects back to the same values.
func TestTypedRoundTrip(t *testing.T) {
	ctx := context.Background()
	src, _ := os.ReadFile("testdata/notags.flac")

	in := tag.Tags{
		Title:         "Round Trip",
		Artists:       []string{"Alpha", "Beta"},
		Album:         "Album X",
		Genre:         []string{"Electronic"},
		TrackNumber:   4,
		TrackTotal:    9,
		DiscNumber:    1,
		RecordingDate: "2021-03",
		Comment:       "hello",
		MusicBrainz:   tag.MusicBrainzIDs{RecordingID: "rec-1", ArtistID: []string{"a-1", "a-2"}},
	}

	doc, err := wl.Parse(ctx, wl.BytesSource(src))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := doc.Edit().SetTags(in).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, _, err := plan.Execute(ctx, wl.WriteTo(&out, wl.BytesSource(src))); err != nil {
		t.Fatal(err)
	}

	got := mustParseBytes(t, out.Bytes()).Fields()
	if got.Title != in.Title || got.Album != in.Album || got.Comment != in.Comment {
		t.Errorf("scalars: got %+v", got)
	}
	if got.TrackNumber != 4 || got.TrackTotal != 9 || got.DiscNumber != 1 {
		t.Errorf("numbering: %d/%d disc %d", got.TrackNumber, got.TrackTotal, got.DiscNumber)
	}
	if got.RecordingDate != "2021-03" {
		t.Errorf("RecordingDate = %q", got.RecordingDate)
	}
	if got.MusicBrainz.RecordingID != "rec-1" {
		t.Errorf("MB recording = %q", got.MusicBrainz.RecordingID)
	}
	if len(got.MusicBrainz.ArtistID) != 2 {
		t.Errorf("MB artist ids = %v", got.MusicBrainz.ArtistID)
	}
}

func mustParseBytes(t *testing.T, b []byte) *wl.Document {
	t.Helper()
	doc, err := wl.Parse(context.Background(), wl.BytesSource(b))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}
