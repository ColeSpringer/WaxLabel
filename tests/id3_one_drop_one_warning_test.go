package waxlabel_test

import (
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// An edit of a file tagged in ID3 tells of a value it drops once: no key takes two
// value-dropped warnings, whatever the container, the tag version or the multi-value policy.
func TestID3EditWarnsOfADropOnce(t *testing.T) {
	title := func(v byte) []byte { return testfile.ID3v2(v, testfile.TextFrame(v, "TIT2", "T")) }
	files := map[string][]byte{
		"mp3 v2.4":          append(title(4), mp3Audio(t)...),
		"mp3 v2.3":          append(title(3), mp3Audio(t)...),
		"aac v2.4":          append(title(4), adtsStream(2, 8, 100)...),
		"wav INFO":          testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "T"}), testfile.WAVData(400)),
		"wav INFO and id3":  testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "T"}, [2]string{"IPRT", "3/12"}), wavID3(title(4)), testfile.WAVData(400)),
		"wav id3 v2.3":      testfile.WAV(testfile.WAVFmtPCM(), wavID3(title(3)), testfile.WAVData(400)),
		"aiff id3 v2.4":     testfile.AIFF("AIFF", stdCOMM(), stdSSND(), aiffID3(title(4))),
		"aiff with no tags": testfile.AIFF("AIFF", stdCOMM(), stdSSND()),
	}
	type edit = func(*wl.Editor) *wl.Editor
	set := func(k tag.Key, vals ...string) edit {
		return func(e *wl.Editor) *wl.Editor { return e.Set(k, vals...) }
	}
	both := func(a, b edit) edit { return func(e *wl.Editor) *wl.Editor { return b(a(e)) } }
	edits := map[string]edit{
		"number, all empty":             set(tag.TrackNumber, "", ""),
		"number, then an empty":         set(tag.TrackNumber, "3", ""),
		"numbers, then an empty":        set(tag.TrackNumber, "3", "4", ""),
		"number, an empty first":        set(tag.TrackNumber, "", "3"),
		"total, all empty":              set(tag.TrackTotal, "", ""),
		"disc, all empty":               set(tag.DiscNumber, "", "", ""),
		"number and total, each twice":  both(set(tag.TrackNumber, "3", ""), set(tag.TrackTotal, "12", "")),
		"total beside no number":        both(set(tag.TrackNumber, "A1"), set(tag.TrackTotal, "12", "")),
		"totals beside no number":       both(set(tag.TrackNumber, "A1"), set(tag.TrackTotal, "12", "13")),
		"empty totals beside no number": both(set(tag.TrackNumber, "A1"), set(tag.TrackTotal, "", "")),
		"movement, all empty":           set(tag.Movement, "", ""),
		"movement total beside none":    both(set(tag.Movement, "x"), set(tag.MovementTotal, "5", "")),
		"genre, all empty":              set(tag.Genre, "", ""),
		"genre, then empties":           set(tag.Genre, "Rock", "", ""),
		"date, all empty":               set(tag.RecordingDate, "", ""),
		"date with no year":             set(tag.RecordingDate, "garbage", ""),
		"original date with no year":    set(tag.OriginalDate, "garbage", ""),
		"release date, all empty":       set(tag.ReleaseDate, "", ""),
		"credit, one empty":             set(tag.Producer, ""),
		"credit, all empty":             set(tag.Producer, "", ""),
		"credit, then an empty":         set(tag.Producer, "P", ""),
		"recording id, all empty":       set(tag.MBRecordingID, "", ""),
		"title, all empty":              set(tag.Title, "", ""),
		"artist, then empties":          set(tag.Artist, "A", "", ""),
		"comment, all empty":            set(tag.Comment, "", ""),
		"lyrics, all empty":             set(tag.Lyrics, "", ""),
		"custom key, all empty":         set(tag.Key("MYKEY"), "", ""),
	}
	policies := map[string][]wl.WriteOption{
		"":       nil,
		"repeat": {wl.WithID3MultiValue(wl.ID3MultiRepeatFrame)},
		"slash":  {wl.WithID3MultiValue(wl.ID3MultiSlash)},
	}
	for fname, data := range files {
		for ename, ed := range edits {
			for pname, opts := range policies {
				plan, err := ed(mustParseBytes(t, data).Edit()).Prepare(opts...)
				if err != nil {
					continue // an edit this container refuses warns of nothing
				}
				drops := map[tag.Key][]string{}
				for _, w := range plan.Report().Warnings {
					if w.Code == wl.WarnValueDropped {
						for _, k := range w.Keys {
							drops[k] = append(drops[k], w.Message)
						}
					}
				}
				for k, msgs := range drops {
					if len(msgs) > 1 {
						t.Errorf("%s, %s %s: %d value-dropped warnings for %s: %q", fname, ename, pname, len(msgs), k, msgs)
					}
				}
			}
		}
	}
}
