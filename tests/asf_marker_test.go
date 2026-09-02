package waxlabel_test

import (
	"context"
	"encoding/binary"
	"slices"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

// guidMarkerHex is the Marker Object GUID as stored on disk.
const guidMarkerHex = "01cd87f451a9cf118ee600c00c205365"

// asfMarker is one marker: its presentation time on the file's own timeline (preroll
// included, as ASF stores it) and its description. The optional fields override the
// entry's own bookkeeping to build the malformed shapes a reader must survive: raw
// description bytes in place of the encoded desc, a wrong entry length, and a wrong
// description length in WCHARs (zero means the correct value).
type asfMarker struct {
	at        time.Duration
	desc      string
	descBytes []byte
	entryLen  uint16
	descLen   uint32
}

// asfMarkers builds a Marker Object: the reserved GUID, the count, the object's name,
// then one entry per marker with an 8-byte offset, the presentation time in 100 ns
// units, the entry length, a send time, flags, and the NUL-terminated UTF-16LE
// description behind its length in WCHARs.
func asfMarkers(name string, marks ...asfMarker) []byte {
	g, _ := hexBytes(guidNoErrCorrHex)
	b := slices.Clone(g)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(marks)))
	b = binary.LittleEndian.AppendUint16(b, 0)
	n := asfUTF16(name)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(n)))
	b = append(b, n...)
	for _, m := range marks {
		desc := m.descBytes
		if desc == nil {
			desc = asfUTF16(m.desc)
		}
		entryLen, descLen := uint16(12+len(desc)), uint32(len(desc)/2)
		if m.entryLen != 0 {
			entryLen = m.entryLen
		}
		if m.descLen != 0 {
			descLen = m.descLen
		}
		b = binary.LittleEndian.AppendUint64(b, 0)
		b = binary.LittleEndian.AppendUint64(b, uint64(m.at/(100*time.Nanosecond)))
		b = binary.LittleEndian.AppendUint16(b, entryLen)
		b = binary.LittleEndian.AppendUint32(b, 0)
		b = binary.LittleEndian.AppendUint32(b, 0)
		b = binary.LittleEndian.AppendUint32(b, descLen)
		b = append(b, desc...)
	}
	return asfObject(guidMarkerHex, b)
}

// truncatedObject cuts an ASF object to n bytes and restates its size, so the object
// walk still hands the short body to its reader.
func truncatedObject(obj []byte, n int) []byte {
	obj = obj[:n]
	binary.LittleEndian.PutUint64(obj[16:24], uint64(n))
	return obj
}

// asfMarkedWith wraps markers in a minimal file with a 3 s preroll.
func asfMarkedWith(marks ...asfMarker) []byte {
	return asfFile(asfFileProperties(time.Minute+3*time.Second, 3000), asfStreamProperties(0x0161, 2, 44100, 16), asfMarkers("", marks...))
}

// asfMarked is the shared fixture: a 3 s preroll, and markers whose presentation
// times carry that preroll, stored out of time order.
func asfMarked() []byte {
	const preroll = 3 * time.Second
	return asfFile(
		asfFileProperties(5*time.Minute+preroll, 3000),
		asfStreamProperties(0x0161, 2, 44100, 16),
		asfMarkers("Chapters",
			asfMarker{at: preroll + 90*time.Second, desc: "Three"},
			asfMarker{at: preroll, desc: "One"},
			asfMarker{at: preroll + 30*time.Second, desc: "Two"}),
	)
}

func TestWMAMarkersReadAsChapters(t *testing.T) {
	doc := mustParseBytes(t, asfMarked())
	chs := doc.Chapters()
	want := []wl.Chapter{
		{Start: 0, Title: "One"},
		{Start: 30 * time.Second, Title: "Two"},
		{Start: 90 * time.Second, Title: "Three"},
	}
	if len(chs) != len(want) {
		t.Fatalf("chapters = %+v, want %d", chs, len(want))
	}
	for i := range want {
		if chs[i].Start != want[i].Start || chs[i].Title != want[i].Title || chs[i].End != 0 {
			t.Errorf("chapter %d = %+v, want start %v title %q and no end", i, chs[i], want[i].Start, want[i].Title)
		}
	}
	caps := doc.Capabilities().Chapters
	if caps.Read != wl.AccessFull || caps.Write != wl.AccessNone {
		t.Errorf("chapters capability = read %s, write %s; want read full, write none", caps.Read, caps.Write)
	}
	if c := wl.CapabilitiesFor(wl.FormatWMA).Chapters; c.Read != wl.AccessFull || c.Write != wl.AccessNone {
		t.Errorf("format-level chapters capability = read %s, write %s; want read full, write none", c.Read, c.Write)
	}
}

// TestWMAMarkerForms covers the marker shapes a reader meets: no description, a time
// inside the preroll (clamped to the start), and a count the object cannot back.
func TestWMAMarkerForms(t *testing.T) {
	t.Run("untitled and clamped", func(t *testing.T) {
		data := asfFile(
			asfFileProperties(time.Minute, 3000),
			asfStreamProperties(0x0161, 2, 44100, 16),
			asfMarkers("", asfMarker{at: time.Second}),
		)
		chs := mustParseBytes(t, data).Chapters()
		if len(chs) != 1 || chs[0].Title != "" || chs[0].Start != 0 {
			t.Errorf("chapters = %+v, want one untitled chapter at the start", chs)
		}
	})
	t.Run("count past the entries", func(t *testing.T) {
		obj := asfMarkers("", asfMarker{at: 5 * time.Second, desc: "only"})
		// The count field follows the 24-byte object header and the reserved GUID.
		binary.LittleEndian.PutUint32(obj[24+16:24+20], 4)
		data := asfFile(asfFileProperties(time.Minute, 0), asfStreamProperties(0x0161, 2, 44100, 16), obj)
		doc := mustParseBytes(t, data)
		if chs := doc.Chapters(); len(chs) != 1 || chs[0].Title != "only" {
			t.Errorf("chapters = %+v, want the one entry present", chs)
		}
		if !hasWarning(doc, wl.WarnMalformedTagEntry) {
			t.Error("expected a malformed-tag-entry warning for the short marker list")
		}
	})
}

// TestWMAMarkerEntryShapes pins the reader to the description-length stepping ffprobe
// uses and to the string forms a description can take: an entry length field that lies
// does not derail the entries after it, a description length past its entry is bounded
// by the entry, a NUL inside the description ends the title (a title carrying one could
// not be copied anywhere), and a description length too wide for a 32-bit int is safe.
func TestWMAMarkerEntryShapes(t *testing.T) {
	const preroll = 3 * time.Second
	for _, c := range []struct {
		name  string
		marks []asfMarker
		want  []string
	}{
		{"entry length field lies", []asfMarker{{at: preroll, desc: "One", entryLen: 0xFFFF}, {at: preroll + time.Second, desc: "Two"}}, []string{"One", "Two"}},
		{"embedded NUL ends the title", []asfMarker{{at: preroll, descBytes: append(asfUTF16("One"), asfUTF16("junk")...)}}, []string{"One"}},
		{"description length past the object is bounded", []asfMarker{{at: preroll, desc: "One", descLen: 1 << 31}}, []string{"One"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, ch := range mustParseBytes(t, asfMarkedWith(c.marks...)).Chapters() {
				got = append(got, ch.Title)
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("chapters = %v, want %v", got, c.want)
			}
		})
	}
	// A title the reader let through with a NUL would make the editor refuse the copy.
	src := mustParseBytes(t, asfMarkedWith(asfMarker{at: preroll, descBytes: append(asfUTF16("One"), asfUTF16("junk")...)}))
	if _, _, err := src.PrepareTransfer(mustParseBytes(t, readFixture(t, notagsFLAC))); err != nil {
		t.Errorf("copy out of the file: %v", err)
	}
}

func TestWMAMarkerElementCap(t *testing.T) {
	doc, err := wl.Parse(context.Background(), wl.BytesSource(asfMarked()), wl.WithLimits(wl.Limits{MaxElements: 2}))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(doc.Chapters()); n != 2 {
		t.Errorf("got %d chapters under a cap of 2", n)
	}
	if !hasWarning(doc, wl.WarnElementCap) {
		t.Error("expected an element-cap warning")
	}
}

// TestWMAMarkersTransfer is the transcode case: a WMA source's markers carry into a
// destination that stores chapters.
func TestWMAMarkersTransfer(t *testing.T) {
	src := mustParseBytes(t, asfMarked())
	dstBytes := readFixture(t, notagsFLAC)
	plan, report, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatal(err)
	}
	var carried int
	for _, it := range report.Items {
		if it.Kind == wl.TransferChapter && it.Disposition == wl.Carried {
			carried += it.Count
		}
	}
	if carried != 3 {
		t.Errorf("report carried %d chapters, want 3: %+v", carried, report.Items)
	}
	got := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).Chapters()
	if len(got) != 3 || got[1].Title != "Two" || got[1].Start != 30*time.Second {
		t.Errorf("destination chapters = %+v", got)
	}
}

func TestWMANativeNamesMarkerObject(t *testing.T) {
	var kinds []string
	for _, e := range mustParseBytes(t, asfMarked()).Native().Describe() {
		kinds = append(kinds, e.Kind)
	}
	if !slices.Contains(kinds, "Marker") {
		t.Errorf("native view = %v, want a Marker entry", strings.Join(kinds, ", "))
	}
}
