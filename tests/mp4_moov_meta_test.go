package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// mp4MoovMetaFile is a file whose moov holds a sound trak, then kids: a meta of its own, as Apple's
// recorders write, a udta, or both.
func mp4MoovMetaFile(kids ...[]byte) []byte {
	return testfile.MP4TracksFile(nil, func(audio, _, _ uint32) []byte {
		return slices.Concat(testfile.MP4Mvhd(1000, 10000), testfile.MP4SoundTrak(1, audio), slices.Concat(kids...))
	})
}

func mp4MdtaMeta(names, values []string, extra ...[]byte) []byte {
	items := make([][]byte, 0, len(values))
	for i, v := range values {
		items = append(items, testfile.MP4KeyItem(i+1, v))
	}
	return testfile.MP4Meta(slices.Concat([][]byte{testfile.MP4HdlrMdta(), testfile.MP4Keys(names...), testfile.MP4Ilst(items...)}, extra)...)
}

// mp4MoovKids names the children of the first moov.
func mp4MoovKids(t *testing.T, data []byte) []string {
	t.Helper()
	moov, moovLen := mp4FindBox(t, data, 0, len(data), "moov")
	_, names := mp4Boxes(data, moov+8, moov+moovLen)
	return names
}

// mp4AudioAt returns the 120 audio bytes the first stco entry points at.
func mp4AudioAt(t *testing.T, data []byte) []byte {
	t.Helper()
	stco := mp4BoxPayload(t, data, "stco")
	off := int(binary.BigEndian.Uint32(stco[8:]))
	if off+120 > len(data) {
		t.Fatalf("the chunk offset %d lies past the file", off)
	}
	return data[off : off+120]
}

// A tag store in a meta directly under moov is read, and an edit rewrites it where it is.
func TestMP4MoovMetaStore(t *testing.T) {
	for _, c := range []struct {
		name string
		meta []byte
	}{
		{"mdta", mp4MdtaMeta([]string{"title", "artist"}, []string{"Meta Title", "Meta Artist"})},
		{"mdta with padding", mp4MdtaMeta([]string{"title", "artist"}, []string{"Meta Title", "Meta Artist"}, testfile.MP4Atom("free", make([]byte, 64)))},
		{"mdir", testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "Meta Title"), testfile.MP4Text("\xa9ART", "Meta Artist")))},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := mp4MoovMetaFile(c.meta)
			doc := mustParseBytes(t, data)
			if got := doc.Fields().Title; got != "Meta Title" {
				t.Fatalf("TITLE = %q, want the moov.meta store's", got)
			}
			if v, _ := doc.Get(tag.Artist); !slices.Equal(v, []string{"Meta Artist"}) {
				t.Errorf("ARTIST = %v", v)
			}
			digest, err := doc.HashAudioEssence(context.Background(), wl.WithHashSource(wl.BytesSource(data)))
			if err != nil {
				t.Fatal(err)
			}
			for name, edit := range map[string]func(*wl.Editor) *wl.Editor{
				"value":   func(e *wl.Editor) *wl.Editor { return e.Set(tag.Title, "Edited") },
				"new key": func(e *wl.Editor) *wl.Editor { return e.Set(tag.Title, "Edited").Set(tag.Album, "New Album") },
				"long":    func(e *wl.Editor) *wl.Editor { return e.Set(tag.Title, string(bytes.Repeat([]byte("L"), 3000))) },
				"cleared": func(e *wl.Editor) *wl.Editor { return e.Clear(tag.Title).Clear(tag.Artist) },
				"new cover": func(e *wl.Editor) *wl.Editor {
					return e.AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: testfile.PNG()})
				},
			} {
				t.Run(name, func(t *testing.T) {
					plan, err := edit(doc.Edit()).Prepare()
					if err != nil {
						t.Fatal(err)
					}
					out := applyToBytes(t, data, plan)
					if kids := mp4MoovKids(t, out); !slices.Equal(kids, []string{"mvhd", "trak", "meta"}) {
						t.Errorf("moov holds %v, want the store left where it was", kids)
					}
					if !bytes.Equal(mp4AudioAt(t, out), bytes.Repeat([]byte{0xA7}, 120)) {
						t.Error("the chunk offset no longer points at the audio")
					}
					re := mustParseBytes(t, out)
					wantTitle := map[string]string{"value": "Edited", "new key": "Edited", "long": string(bytes.Repeat([]byte("L"), 3000)), "new cover": "Meta Title"}[name]
					if got := re.Fields().Title; got != wantTitle {
						t.Errorf("TITLE = %q, want %q", got, wantTitle)
					}
					if has := re.Tags().Has(tag.Artist); has == (name == "cleared") {
						t.Errorf("ARTIST present = %v", has)
					}
					if d, err := re.HashAudioEssence(context.Background(), wl.WithHashSource(wl.BytesSource(out))); err != nil || !d.Equal(digest) {
						t.Errorf("essence digest %v (%v), was %v", d, err, digest)
					}
					if name == "new key" {
						if v, _ := re.Get(tag.Album); !slices.Equal(v, []string{"New Album"}) {
							t.Errorf("ALBUM = %v", v)
						}
					}
					if name == "new cover" && len(re.Pictures()) != 1 {
						t.Errorf("%d pictures, want 1", len(re.Pictures()))
					}
				})
			}
		})
	}
}

// A chapter edit beside a tag edit rewrites the store under moov.meta and the udta it adds or holds.
func TestMP4MoovMetaStoreWithChapterEdit(t *testing.T) {
	meta := mp4MdtaMeta([]string{"title"}, []string{"Meta Title"})
	chapters := []wl.Chapter{{Start: 0, Title: "One"}, {Start: 500 * time.Millisecond, Title: "Two"}}
	for name, data := range map[string][]byte{
		"no udta":         mp4MoovMetaFile(meta),
		"udta after":      mp4MoovMetaFile(meta, testfile.MP4Atom("udta", testfile.MP4Atom("XTRA", []byte("kept")))),
		"udta before":     mp4MoovMetaFile(testfile.MP4Atom("udta", testfile.MP4Atom("XTRA", []byte("kept"))), meta),
		"udta text atoms": mp4MoovMetaFile(testfile.MP4Atom("udta", mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "Meta Title"))), meta),
	} {
		t.Run(name, func(t *testing.T) {
			doc := mustParseBytes(t, data)
			plan, err := doc.Edit().SetChapters(chapters...).Set(tag.Title, "With Chapters").Set(tag.Genre, "Spoken").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out := applyToBytes(t, data, plan)
			re := mustParseBytes(t, out)
			if got := re.Fields().Title; got != "With Chapters" {
				t.Errorf("TITLE = %q", got)
			}
			if v, _ := re.Get(tag.Genre); !slices.Equal(v, []string{"Spoken"}) {
				t.Errorf("GENRE = %v", v)
			}
			if got := re.Chapters(); len(got) != 2 || got[1].Title != "Two" || got[1].Start != 500*time.Millisecond {
				t.Errorf("chapters = %+v", got)
			}
			if n := bytes.Count(out, []byte("ilst")); n != 1 {
				t.Errorf("%d ilst atoms, want the one under moov.meta", n)
			}
			if !bytes.Equal(mp4AudioAt(t, out), bytes.Repeat([]byte{0xA7}, 120)) {
				t.Error("the chunk offset no longer points at the audio")
			}
			if len(re.Lint()) != 0 {
				t.Errorf("lint after the edit: %v", re.Lint())
			}

			plan, err = re.Edit().ClearChapters().Set(tag.Title, "Without").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			re = mustParseBytes(t, applyToBytes(t, out, plan))
			if got := re.Fields().Title; got != "Without" || len(re.Chapters()) != 0 {
				t.Errorf("after the clear: TITLE %q, %d chapters", got, len(re.Chapters()))
			}
		})
	}
}

// With a store in moov.udta.meta too, that one is read and written; the one under moov.meta shows in
// the families, fills the keys the other lacks, and follows every write.
func TestMP4MoovMetaBesideUdtaStore(t *testing.T) {
	udta := testfile.MP4Atom("udta", testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "Udta Title"))))
	meta := mp4MdtaMeta([]string{"title", "artist", "location"}, []string{"Meta Title", "Meta Artist", "+48.85+002.35/"})
	data := mp4MoovMetaFile(udta, meta)
	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "Udta Title" {
		t.Fatalf("TITLE = %q, want the udta store's", got)
	}
	if v, _ := doc.Get(tag.Artist); !slices.Equal(v, []string{"Meta Artist"}) {
		t.Errorf("ARTIST = %v, want the moov.meta store's, which alone holds one", v)
	}
	if !hasLintCode(doc, "conflicting-families") || !slices.ContainsFunc(doc.Families(), func(f wl.FamilyValue) bool {
		return f.Key == tag.Title && !f.Selected && slices.Equal(f.Values, []string{"Meta Title"})
	}) {
		t.Errorf("the moov.meta title does not show as a conflicting family: %+v", doc.Families())
	}
	metaItems := func(t *testing.T, out []byte) []byte {
		t.Helper()
		moov, moovLen := mp4FindBox(t, out, 0, len(out), "moov")
		off, size := mp4FindBox(t, out, moov+8, moov+moovLen, "meta")
		return out[off : off+size]
	}
	t.Run("unrelated edit", func(t *testing.T) {
		plan, err := doc.Edit().Set(tag.Album, "Album").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		m := metaItems(t, out)
		if !bytes.Contains(m, testfile.MP4KeyItem(1, "Udta Title")) || !bytes.Contains(m, testfile.MP4KeyItem(2, "Meta Artist")) ||
			!bytes.Contains(m, testfile.MP4KeyItem(3, "+48.85+002.35/")) {
			t.Error("the moov.meta store does not hold the title read, its artist and its own location")
		}
		if bytes.Contains(m, []byte("Album")) {
			t.Error("the moov.meta store gained a key it did not hold")
		}
		re := mustParseBytes(t, out)
		if hasLintCode(re, "conflicting-families") {
			t.Errorf("the stores still disagree: %v", re.Lint())
		}
		if got := re.Fields().Title; got != "Udta Title" {
			t.Errorf("TITLE = %q after the edit", got)
		}
		if v, _ := re.Get(tag.Album); !slices.Equal(v, []string{"Album"}) {
			t.Errorf("ALBUM = %v", v)
		}
	})
	t.Run("edit and clear", func(t *testing.T) {
		plan, err := doc.Edit().Set(tag.Title, "New").Clear(tag.Artist).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		m := metaItems(t, out)
		if !bytes.Contains(m, testfile.MP4KeyItem(1, "New")) || bytes.Contains(m, []byte("Meta Artist")) {
			t.Error("the moov.meta store did not follow the edit and the clear")
		}
		re := mustParseBytes(t, out)
		if got := re.Fields().Title; got != "New" || re.Tags().Has(tag.Artist) {
			t.Errorf("TITLE %q, keys %v", got, re.Tags().Keys())
		}
	})
	t.Run("no tag edit", func(t *testing.T) {
		plan, err := doc.Edit().SetChapters(wl.Chapter{Title: "One"}).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if out := applyToBytes(t, data, plan); !bytes.Equal(metaItems(t, out), meta) {
			t.Error("a chapter edit rewrote the moov.meta store")
		}
	})
}

// ffprobe reads a moov.meta keys store after an edit as it did before: the file ffmpeg wrote, its
// udta wrapper taken off so the meta sits under moov.
func TestMP4DifferentialMoovMetaStore(t *testing.T) {
	requireTool(t, "ffmpeg")
	requireTool(t, "ffprobe")
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mov")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-c:a", "aac", "-movflags", "+use_metadata_tags", "-metadata", "title=Original", "-metadata", "artist=Someone", src).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	moov, moovLen := mp4FindBox(t, data, 0, len(data), "moov")
	udta, udtaLen := mp4FindBox(t, data, moov+8, moov+moovLen, "udta")
	if _, names := mp4Boxes(data, udta+8, udta+udtaLen); !slices.Equal(names, []string{"meta"}) || moov+moovLen != len(data) {
		t.Skipf("ffmpeg wrote udta children %v; the test needs a udta of one meta ending the file", names)
	}
	data = slices.Concat(data[:udta], data[udta+8:])
	binary.BigEndian.PutUint32(data[moov:], uint32(moovLen-8))
	path := filepath.Join(dir, "meta.mov")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if tags := ffprobeFormatTags(t, path); tags["title"] != "Original" {
		t.Fatalf("ffprobe does not read the moov.meta store: %v", tags)
	}
	doc := mustParseFile(t, path)
	if got := doc.Fields().Title; got != "Original" {
		t.Fatalf("TITLE = %q, want Original", got)
	}
	plan, err := doc.Edit().Set(tag.Title, "Edited").Set(tag.Album, "New Album").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}
	tags := ffprobeFormatTags(t, path)
	for k, want := range map[string]string{"title": "Edited", "artist": "Someone", "album": "New Album"} {
		if tags[k] != want {
			t.Errorf("ffprobe %s = %q, want %q (%v)", k, tags[k], want, tags)
		}
	}
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-i", path, "-f", "null", "-").CombinedOutput(); err != nil || len(out) != 0 {
		t.Errorf("ffmpeg does not decode the edited file cleanly: %v\n%s", err, out)
	}
}

// The first store's total, held in its track entry's "n/total", stands over the second store's.
func TestMP4SecondStoreDoesNotOverrideAPairTotal(t *testing.T) {
	udta := testfile.MP4Atom("udta", testfile.MP4Meta(testfile.MP4HdlrMdta(), testfile.MP4Keys("track"), testfile.MP4Ilst(testfile.MP4KeyItem(1, "3/12"))))
	meta := testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(mp4Pair("trkn", 3, 15), testfile.MP4Text("\xa9ART", "Meta Artist")))
	data := mp4MoovMetaFile(udta, meta)
	doc := mustParseBytes(t, data)
	total, _ := doc.Get(tag.TrackTotal)
	artist, _ := doc.Get(tag.Artist)
	if !slices.Equal(total, []string{"12"}) || !slices.Equal(artist, []string{"Meta Artist"}) || !hasLintCode(doc, "conflicting-families") {
		t.Errorf("TRACKTOTAL %v, ARTIST %v, lint %v; want 12, the second store's artist and the conflict", total, artist, doc.Lint())
	}
	// So does one a freeform of the first store holds behind a slash.
	slashed := testfile.MP4Atom("udta", testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(mp4Freeform("com.apple.iTunes", "TRACKNUMBER", "3/12"))))
	if total, _ := mustParseBytes(t, mp4MoovMetaFile(slashed, meta)).Get(tag.TrackTotal); !slices.Equal(total, []string{"12"}) {
		t.Errorf("TRACKTOTAL = %v beside a freeform 3/12, want 12", total)
	}
	plan, err := doc.Edit().Set(tag.Album, "A").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, testfile.MP4KeyItem(1, "3/12")) {
		t.Error("an edit of the album rewrote the first store's track entry")
	}
	if total, _ := mustParseBytes(t, out).Get(tag.TrackTotal); !slices.Equal(total, []string{"12"}) {
		t.Errorf("TRACKTOTAL = %v after the edit", total)
	}
}

// QuickTime's own form of the meta under moov has no version and flags word ahead of its children.
func TestMP4MoovMetaInQuickTimeForm(t *testing.T) {
	bare := mp4MetaBare(testfile.MP4HdlrMdta(), testfile.MP4Keys("title", "artist"),
		testfile.MP4Ilst(testfile.MP4KeyItem(1, "Meta Title"), testfile.MP4KeyItem(2, "Meta Artist")))
	data := mp4MoovMetaFile(bare)
	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "Meta Title" {
		t.Fatalf("TITLE = %q", got)
	}
	plan, err := doc.Edit().Set(tag.Title, "Edited").Set(tag.Album, "New Album").SetChapters(wl.Chapter{Title: "One"}).Prepare(wl.WithVerifyEssence())
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	re := mustParseBytes(t, out)
	if v, _ := re.Get(tag.Album); re.Fields().Title != "Edited" || !slices.Equal(v, []string{"New Album"}) || len(re.Chapters()) != 1 || len(re.Lint()) != 0 {
		t.Errorf("after the edit: TITLE %q, ALBUM %v, %d chapters, lint %v", re.Fields().Title, v, len(re.Chapters()), re.Lint())
	}
	moov, moovLen := mp4FindBox(t, out, 0, len(out), "moov")
	meta, _ := mp4FindBox(t, out, moov+8, moov+moovLen, "meta")
	if string(out[meta+12:meta+16]) != "hdlr" {
		t.Error("the meta gained a version and flags word, or lost its first child")
	}
}

// A chapter write that can make no chapter track (no track id is free) writes the chpl alone, and
// the store under moov.meta with it.
func TestMP4MoovMetaStoreWithAChplOnlyChapterWrite(t *testing.T) {
	meta := mp4MdtaMeta([]string{"title"}, []string{"Meta Title"})
	for name, kids := range map[string][]byte{
		"no udta": meta, "udta ahead": slices.Concat(testfile.MP4Atom("udta", testfile.MP4Atom("XTRA", []byte("kept"))), meta),
	} {
		data := testfile.MP4TracksFile(nil, func(audio, _, _ uint32) []byte {
			return slices.Concat(testfile.MP4Mvhd(1000, 1000), testfile.MP4SoundTrak(-1, audio), kids) // track id 0xFFFFFFFF
		})
		plan, err := mustParseBytes(t, data).Edit().SetChapters(wl.Chapter{Title: "One"}, wl.Chapter{Start: 500 * time.Millisecond, Title: "Two"}).
			Set(tag.Title, "With Chapters").Set(tag.Genre, "Spoken").Prepare(wl.WithVerifyEssence())
		if err != nil {
			t.Fatal(err)
		}
		if ops := plan.Report().Operations; len(ops) == 0 || ops[0] != "2-chapter write (chpl)" {
			t.Fatalf("%s: operations %q, want the chpl-only write", name, ops)
		}
		out := applyToBytes(t, data, plan)
		re := mustParseBytes(t, out)
		if v, _ := re.Get(tag.Genre); re.Fields().Title != "With Chapters" || !slices.Equal(v, []string{"Spoken"}) || len(re.Chapters()) != 2 {
			t.Errorf("%s: TITLE %q, GENRE %v, %d chapters", name, re.Fields().Title, v, len(re.Chapters()))
		}
		if n := bytes.Count(out, []byte("ilst")); n != 1 || !bytes.Equal(mp4AudioAt(t, out), bytes.Repeat([]byte{0xA7}, 120)) {
			t.Errorf("%s: %d ilst atoms, or the chunk offset no longer points at the audio", name, n)
		}
	}
}
