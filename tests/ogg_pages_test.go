package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// oggPage is one page of a stream whose pages sit back to back.
type oggPage struct {
	off, len int
	flags    byte
	granule  uint64
	seq      uint32
	ends     bool // a packet ends on the page
}

// oggPages walks data page by page up to the first byte that opens no whole page.
func oggPages(data []byte) []oggPage {
	var pages []oggPage
	for off := 0; off+27 <= len(data) && string(data[off:off+4]) == "OggS"; {
		n := 27 + int(data[off+26])
		if off+n > len(data) {
			break
		}
		ends := false
		for _, lacing := range data[off+27 : off+n] {
			n += int(lacing)
			ends = ends || lacing < 255
		}
		if off+n > len(data) {
			break
		}
		pages = append(pages, oggPage{
			off: off, len: n, flags: data[off+5], ends: ends,
			granule: binary.LittleEndian.Uint64(data[off+6:]),
			seq:     binary.LittleEndian.Uint32(data[off+18:]),
		})
		off += n
	}
	return pages
}

// editPages returns a copy of data with edit applied to each page's bytes and the page resealed.
func editPages(data []byte, edit func(i int, page []byte)) []byte {
	out := slices.Clone(data)
	for i, p := range oggPages(out) {
		page := out[p.off : p.off+p.len]
		edit(i, page)
		testfile.OggSealPage(page)
	}
	return out
}

// renumberPages adds delta to every page's sequence number.
func renumberPages(data []byte, delta uint32) []byte {
	return editPages(data, func(_ int, page []byte) {
		binary.LittleEndian.PutUint32(page[18:], binary.LittleEndian.Uint32(page[18:])+delta)
	})
}

// shiftGranules adds delta to the granule position of every page that ends audio: one past the
// header pages, which hold position 0, that completes a packet.
func shiftGranules(data []byte, delta uint64) []byte {
	return editPages(data, func(_ int, page []byte) {
		if g := binary.LittleEndian.Uint64(page[6:]); g != 0 && g != math.MaxUint64 {
			binary.LittleEndian.PutUint64(page[6:], g+delta)
		}
	})
}

// insertAfterPage returns data with junk inserted after its page n, counted from 0.
func insertAfterPage(t *testing.T, data []byte, n int, junk []byte) []byte {
	t.Helper()
	pages := oggPages(data)
	if n >= len(pages) {
		t.Fatalf("no page %d among %d", n, len(pages))
	}
	return slices.Insert(slices.Clone(data), pages[n].off+pages[n].len, junk...)
}

// assertContiguousPageSequence fails unless the pages of data, back to back to its end, count up
// from first without a hole.
func assertContiguousPageSequence(t *testing.T, data []byte, first uint32) {
	t.Helper()
	pages := oggPages(data)
	if pagesEnd(pages) != len(data) {
		t.Fatalf("pages cover %d of %d bytes", pagesEnd(pages), len(data))
	}
	for i, p := range pages {
		if want := first + uint32(i); p.seq != want {
			t.Fatalf("page %d has sequence number %d, want %d", i, p.seq, want)
		}
	}
}

// pagesEnd is the offset where the last of pages ends.
func pagesEnd(pages []oggPage) int {
	if len(pages) == 0 {
		return 0
	}
	last := pages[len(pages)-1]
	return last.off + last.len
}

// A rewrite goes on from the first page's own sequence number: whatever the edit does to the
// header pages, the written pages count up from it without a hole.
func TestOggRewriteKeepsPageNumbering(t *testing.T) {
	const first = 100
	type edit struct {
		name  string
		apply func(*wl.Editor)
	}
	edits := []edit{
		{"title", func(e *wl.Editor) { e.Set(tag.Title, "Renumbered") }},
		{"cover spilling onto more header pages", func(e *wl.Editor) {
			e.AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: pattern(70000)})
		}},
	}
	gain := []edit{
		{"output gain", func(e *wl.Editor) { e.SetOutputGain(-512) }},
		{"output gain and title", func(e *wl.Editor) { e.SetOutputGain(-512).Set(tag.Title, "Renumbered") }},
	}
	for _, f := range oggFixtures {
		src := renumberPages(readFixture(t, f), first)
		assertContiguousPageSequence(t, src, first)
		cases := edits
		if f == sampleOpus {
			cases = slices.Concat(edits, gain)
		}
		for _, c := range cases {
			t.Run(f+"/"+c.name, func(t *testing.T) {
				e := mustParseBytes(t, src).Edit()
				c.apply(e)
				plan, err := e.Prepare(wl.WithUnrecognizedPictures())
				if err != nil {
					t.Fatal(err)
				}
				out := applyToBytes(t, src, plan)
				assertContiguousPageSequence(t, out, first)
				if got, want := essenceOf(t, out), essenceOf(t, src); !got.Equal(want) {
					t.Errorf("essence = %s, want %s", got, want)
				}
			})
		}
	}
}

// A header packet spilling over pages leaves pages no packet ends on, which hold granule position
// -1; the file still reads whole and takes the next edit.
func TestOggContinuationHeaderPagesHoldNoGranule(t *testing.T) {
	for _, f := range oggFixtures {
		src := readFixture(t, f)
		plan, err := mustParseBytes(t, src).Edit().
			AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: pattern(200000)}).
			Prepare(wl.WithUnrecognizedPictures())
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, src, plan)
		open := 0
		for i, p := range oggPages(out) {
			if p.ends {
				continue
			}
			open++
			if p.granule != math.MaxUint64 {
				t.Errorf("%s: page %d, which no packet ends on, holds granule %#x", f, i, p.granule)
			}
		}
		if open < 2 {
			t.Fatalf("%s: setup: the cover leaves %d pages no packet ends on", f, open)
		}
		doc := mustParseBytes(t, out)
		if got, want := doc.Properties().First().Duration, mustParseBytes(t, src).Properties().First().Duration; got != want {
			t.Errorf("%s: duration = %v, want %v", f, got, want)
		}
		next, err := doc.Edit().Set(tag.Title, "Again").Prepare()
		if err != nil {
			t.Fatalf("%s: edit after the spill: %v", f, err)
		}
		if got := mustParseBytes(t, applyToBytes(t, out, next)).Fields().Title; got != "Again" {
			t.Errorf("%s: title = %q", f, got)
		}
	}
}

// A file cut inside its last page reads up to the last whole page: the tags, the duration that
// page's granule position gives and the essence of the file cut at the page boundary, with a
// truncated-audio warning. A rewrite keeps the cut page's bytes where they were.
func TestOggTruncatedFinalPage(t *testing.T) {
	for _, f := range oggFixtures {
		t.Run(f, func(t *testing.T) {
			whole := readFixture(t, f)
			pages := oggPages(whole)
			last := pages[len(pages)-1]
			if last.len <= 100 {
				t.Fatalf("setup: the last page is %d bytes", last.len)
			}
			cut, boundary := whole[:len(whole)-100], whole[:last.off]
			partial := cut[last.off:]

			doc, atBoundary := mustParseBytes(t, cut), mustParseBytes(t, boundary)
			if !hasWarning(doc, wl.WarnTruncatedAudio) {
				t.Errorf("warnings = %v, want truncated-audio", doc.Warnings())
			}
			if hasWarning(doc, wl.WarnTrailingBytes) {
				t.Errorf("the cut page is reported as trailing bytes too: %v", doc.Warnings())
			}
			if got := doc.Fields().Title; got != "Sample Title" {
				t.Errorf("title = %q", got)
			}
			rate := doc.Properties().First().SampleRate
			wantSamples := pages[len(pages)-2].granule
			if doc.Format() == wl.FormatOggOpus {
				wantSamples -= 312 // the fixture's pre-skip
			}
			if got, want := doc.Properties().First().Duration, samplesDuration(wantSamples, rate); got != want {
				t.Errorf("duration = %v, want %v, up to the last whole page", got, want)
			}
			if got, want := essenceOf(t, cut), essenceOf(t, boundary); !got.Equal(want) {
				t.Errorf("essence = %s, want that of the file cut at the page boundary, %s", got, want)
			}
			if hasWarning(atBoundary, wl.WarnTruncatedAudio) && doc.Format() != wl.FormatOggFLAC {
				t.Errorf("a file cut at a page boundary warns truncated-audio: %v", atBoundary.Warnings())
			}

			for name, edit := range map[string]func(*wl.Editor){
				"title": func(e *wl.Editor) { e.Set(tag.Title, "Cut") },
				"cover spilling onto more header pages": func(e *wl.Editor) {
					e.Set(tag.Title, "Cut").AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: pattern(70000)})
				},
			} {
				e := doc.Edit()
				edit(e)
				plan, err := e.Prepare(wl.WithUnrecognizedPictures())
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				out := applyToBytes(t, cut, plan)
				if !bytes.HasSuffix(out, partial) {
					t.Errorf("%s: the cut page's bytes do not end the rewritten file", name)
				}
				got := mustParseBytes(t, out)
				if got.Fields().Title != "Cut" || !hasWarning(got, wl.WarnTruncatedAudio) {
					t.Errorf("%s: reread title %q, warnings %v", name, got.Fields().Title, got.Warnings())
				}
				if after := essenceOf(t, out); !after.Equal(essenceOf(t, cut)) {
					t.Errorf("%s: the essence changed", name)
				}
			}
		})
	}
}

// samplesDuration is the duration of n samples at rate, as the parsers round it.
func samplesDuration(n uint64, rate int) time.Duration {
	return time.Duration(float64(n) / float64(rate) * float64(time.Second))
}

// A file cut inside its header pages holds no stream to read: it is refused as invalid data.
func TestOggTruncatedHeaderPagesRefused(t *testing.T) {
	for _, f := range oggFixtures {
		whole := readFixture(t, f)
		pages := oggPages(whole)
		for name, n := range map[string]int{
			"inside the first page":  pages[0].len - 5,
			"inside the second page": pages[1].off + pages[1].len - 5,
		} {
			_, err := wl.Parse(context.Background(), wl.BytesSource(whole[:n]))
			if !errors.Is(err, waxerr.ErrInvalidData) {
				t.Errorf("%s cut %s: err = %v, want invalid data", f, name, err)
			}
		}
	}
}

// Bytes between two pages belong to no page. The pages behind them still read, so the duration
// and the essence are the clean file's, and a rewrite keeps the bytes between pages: behind the
// first page when they sat among the header pages, where they were otherwise.
func TestOggJunkBetweenPages(t *testing.T) {
	junk := []byte("0123456789")
	// A page-shaped run in the junk whose checksum does not hold is junk too.
	forged := slices.Concat([]byte("--"), testfile.OggPage(0, 7, 1, 9, []byte("not a page")), []byte("--"))
	forged[2+22] ^= 0xFF
	for _, f := range oggFixtures {
		clean := readFixture(t, f)
		cleanDoc := mustParseBytes(t, clean)
		for _, c := range []struct {
			name  string
			after int
			junk  []byte
		}{
			{"among the header pages", 0, junk},
			{"before the first audio page", 1, junk},
			{"between audio pages", 2, junk},
			{"holding a forged page", 2, forged},
		} {
			t.Run(f+"/"+c.name, func(t *testing.T) {
				data := insertAfterPage(t, clean, c.after, c.junk)
				doc := mustParseBytes(t, data)
				if !hasWarning(doc, wl.WarnPageGap) || !lintHasCode(doc, "page-gap") {
					t.Errorf("warnings = %v, want page-gap, in lint too", doc.Warnings())
				}
				if hasWarning(doc, wl.WarnChainedStream) || hasWarning(doc, wl.WarnTrailingBytes) {
					t.Errorf("warnings = %v, want the gap alone", doc.Warnings())
				}
				if got, want := doc.Properties().First(), cleanDoc.Properties().First(); got.Duration != want.Duration || got.Bitrate != want.Bitrate {
					t.Errorf("duration %v at %d bps, want the clean file's %v at %d", got.Duration, got.Bitrate, want.Duration, want.Bitrate)
				}
				if got, want := essenceOf(t, data), essenceOf(t, clean); !got.Equal(want) {
					t.Errorf("essence = %s, want the clean file's %s", got, want)
				}
				if got := doc.Fields().Title; got != "Sample Title" {
					t.Errorf("title = %q", got)
				}

				for name, edit := range map[string]func(*wl.Editor){
					"title": func(e *wl.Editor) { e.Set(tag.Title, "Gapped") },
					"cover spilling onto more header pages": func(e *wl.Editor) {
						e.AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: pattern(70000)})
					},
				} {
					e, ce := doc.Edit(), cleanDoc.Edit()
					edit(e)
					edit(ce)
					plan, err := e.Prepare(wl.WithUnrecognizedPictures())
					if err != nil {
						t.Fatalf("%s: %v", name, err)
					}
					cleanPlan, err := ce.Prepare(wl.WithUnrecognizedPictures())
					if err != nil {
						t.Fatalf("%s: %v", name, err)
					}
					out, want := applyToBytes(t, data, plan), applyToBytes(t, clean, cleanPlan)
					at := bytes.Index(out, c.junk)
					if at < 0 {
						t.Fatalf("%s: the bytes between the pages are gone", name)
					}
					// Without the junk the file is the clean file's rewrite, pages numbered in a row.
					if got := slices.Delete(slices.Clone(out), at, at+len(c.junk)); !bytes.Equal(got, want) {
						t.Errorf("%s: the rewrite differs from the clean file's beyond the junk", name)
					}
					if !slices.ContainsFunc(oggPages(want), func(p oggPage) bool { return p.off == at }) {
						t.Errorf("%s: the junk at %d sits inside a page", name, at)
					}
					if !hasWarning(mustParseBytes(t, out), wl.WarnPageGap) {
						t.Errorf("%s: the rewritten file no longer reports the gap", name)
					}
				}
			})
		}
	}
}

// A 20 second Vorbis stream with ten bytes of junk after its fourth page reads to its full length,
// and ffmpeg decodes the rewrite that renumbers its pages around the junk.
func TestOggDifferentialJunkBetweenPages(t *testing.T) {
	requireTool(t, "ffmpeg")
	dir := t.TempDir()
	clean := readFixture(t, ffmpegEncode(t, filepath.Join(dir, "tone.ogg"),
		"-f", "lavfi", "-i", "sine=frequency=440:duration=20", "-c:a", "libvorbis"))
	want := mustParseBytes(t, clean).Properties().First().Duration
	if want < 19*time.Second {
		t.Fatalf("setup: the clean stream is %v long", want)
	}
	data := insertAfterPage(t, clean, 3, []byte("0123456789"))
	doc := mustParseBytes(t, data)
	if got := doc.Properties().First().Duration; got != want || !hasWarning(doc, wl.WarnPageGap) {
		t.Errorf("duration = %v, want %v; warnings %v", got, want, doc.Warnings())
	}

	plan, err := doc.Edit().Set(tag.Comment, strings.Repeat("c", 70000)).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(plan.Report().Operations, func(op string) bool { return strings.Contains(op, "renumber") }) {
		t.Fatalf("operations = %v, want a renumber", plan.Report().Operations)
	}
	path := writeTempFile(t, "gapped.ogg", applyToBytes(t, data, plan))
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-xerror",
		"-i", path, "-f", "null", "-").CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg rejected the rewrite: %v\n%s", err, out)
	}
	if got := ffprobeStream(t, path).Duration; (got - want).Abs() > 50*time.Millisecond {
		t.Errorf("ffprobe reads %v from the rewrite, want %v", got, want)
	}
}

// A stream lasts from the granule position it starts at, so one whose positions are all moved on
// by a second is as long as before.
func TestOggDurationCountsFromTheStartGranule(t *testing.T) {
	for _, f := range oggFixtures {
		clean := readFixture(t, f)
		want := mustParseBytes(t, clean).Properties().First()
		got := mustParseBytes(t, shiftGranules(clean, uint64(want.SampleRate))).Properties().First()
		if got.Duration != want.Duration || got.TotalSamples != want.TotalSamples || got.Bitrate != want.Bitrate {
			t.Errorf("%s: %v, %d samples, %d bps; want the unmoved stream's %v, %d, %d",
				f, got.Duration, got.TotalSamples, got.Bitrate, want.Duration, want.TotalSamples, want.Bitrate)
		}
	}
}

// A stream ffmpeg cut from a longer one without rewriting its timestamps starts at a granule
// position past 0: its length is the samples ffmpeg decodes from it.
func TestOggDifferentialCutStreamLength(t *testing.T) {
	requireTool(t, "ffmpeg")
	for _, c := range []struct{ ext, codec string }{{"ogg", "libvorbis"}, {"opus", "libopus"}, {"oga", "flac"}} {
		t.Run(c.codec, func(t *testing.T) {
			dir := t.TempDir()
			whole := ffmpegEncode(t, filepath.Join(dir, "tone."+c.ext),
				"-f", "lavfi", "-i", "sine=frequency=440:duration=5", "-c:a", c.codec)
			cut := ffmpegEncode(t, filepath.Join(dir, "cut."+c.ext), "-ss", "2.5", "-i", whole, "-copyts", "-c", "copy")
			tr := mustParseFile(t, cut).Properties().First()
			if pages := oggPages(readFixture(t, cut)); pages[2].granule < uint64(2*tr.SampleRate) {
				t.Fatalf("setup: the cut stream's first audio page ends at granule position %d", pages[2].granule)
			}
			pcm, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-i", cut,
				"-f", "s16le", "-ac", "1", "-ar", strconv.Itoa(tr.SampleRate), "-").Output()
			if err != nil {
				t.Fatalf("ffmpeg decode: %v", err)
			}
			decoded := uint64(len(pcm) / 2)
			if tr.TotalSamples != decoded {
				t.Errorf("total samples = %d (%v), ffmpeg decodes %d", tr.TotalSamples, tr.Duration, decoded)
			}
			if c.codec != "flac" {
				return
			}
			// ffmpeg leaves STREAMINFO's count at 0. One that still declares the whole stream,
			// as a copy of libFLAC's header would, is reported and set aside.
			data := readFixture(t, cut)
			binary.BigEndian.PutUint32(data[27+1+17+14:], 5*uint32(tr.SampleRate))
			testfile.OggSealPage(data[:oggPages(data)[0].len])
			doc := mustParseBytes(t, data)
			if got := doc.Properties().First().TotalSamples; got != decoded || !hasWarning(doc, wl.WarnTruncatedAudio) {
				t.Errorf("with the whole stream declared: total samples = %d, want %d; warnings %v", got, decoded, doc.Warnings())
			}
		})
	}
}

// Junk among the header pages stays ahead of the pages a rewrite builds, so the first audio page
// still follows a header page and reads as it did, even one whose checksum does not hold. Junk
// ahead of the first audio page stays apart from it.
func TestOggHeaderJunkRewriteKeepsTheAudioAsRead(t *testing.T) {
	for _, f := range oggFixtures {
		clean := readFixture(t, f)
		first := oggPages(clean)[2]
		damaged := insertAfterPage(t, clean, 0, []byte("0123456789"))
		damaged[10+first.off+first.len-1] ^= 0x01
		apart := insertAfterPage(t, insertAfterPage(t, clean, 1, bytes.Repeat([]byte{0xA5}, 40000)), 0, bytes.Repeat([]byte{0x5A}, 40000))
		for name, data := range map[string][]byte{"a first audio page with a broken checksum": damaged, "two long runs": apart} {
			doc := mustParseBytes(t, data)
			want := doc.Properties().First().Duration
			if want != mustParseBytes(t, clean).Properties().First().Duration {
				t.Fatalf("%s, %s: setup: the source reads %v", f, name, want)
			}
			plan, err := doc.Edit().Set(tag.Title, "Edited").Prepare()
			if err != nil {
				t.Fatalf("%s, %s: %v", f, name, err)
			}
			out := applyToBytes(t, data, plan)
			got := mustParseBytes(t, out)
			if got.Properties().First().Duration != want || got.Fields().Title != "Edited" {
				t.Errorf("%s, %s: the rewrite reads %v, title %q; want %v", f, name, got.Properties().First().Duration, got.Fields().Title, want)
			}
			if after, before := essenceOf(t, out), essenceOf(t, data); !after.Equal(before) {
				t.Errorf("%s, %s: the edit changed the essence", f, name)
			}
		}
	}
}

// A page that would end the read of the stream, by beginning another stream or belonging to one
// that never began, counts only if its checksum holds. Bytes shaped like such a page are junk,
// wherever they sit, and a page of the stream with a flipped flag is a gap, not the stream's end.
func TestOggOnlyAWholePageEndsTheStream(t *testing.T) {
	clean := readFixture(t, sampleOgg)
	want := mustParseBytes(t, clean)
	forged := testfile.OggPage(2, 0, 99, 0, []byte("\x01vorbis forged"))
	forged[22] ^= 0xFF
	pages := oggPages(clean)
	flagged := slices.Clone(clean)
	flagged[pages[2].off+5] |= 0x02 // the first audio page claims to begin a stream

	full := want.Properties().First().Duration
	for name, c := range map[string]struct {
		data  []byte
		code  wl.WarningCode
		whole bool // every page of the stream still reads
	}{
		"a forged page behind the stream":  {slices.Concat(clean, forged), wl.WarnTrailingBytes, true},
		"a bare capture pattern and zeros": {slices.Concat(clean, []byte("OggS"), make([]byte, 60)), wl.WarnTrailingBytes, true},
		"an audio page flagged as a start": {flagged, wl.WarnPageGap, false},
	} {
		doc := mustParseBytes(t, c.data)
		if hasWarning(doc, wl.WarnChainedStream) || !hasWarning(doc, c.code) {
			t.Errorf("%s: warnings = %v, want %v and no chained-stream", name, doc.Warnings(), c.code)
		}
		if _, err := doc.HashAudioEssence(context.Background(), wl.WithHashSource(wl.BytesSource(c.data))); err != nil {
			t.Errorf("%s: no essence digest: %v", name, err)
		}
		if _, err := doc.Edit().Set(tag.Title, "Still Writable").Prepare(); err != nil {
			t.Errorf("%s: an edit is refused: %v", name, err)
		}
		// The flagged page is skipped as a gap; the page behind it still reads.
		if got := doc.Properties().First().Duration; c.whole && got != full || !c.whole && (got <= 0 || got >= full) {
			t.Errorf("%s: duration = %v of %v", name, got, full)
		}
	}

	// A strip that leaves such bytes right behind the last page does not turn the file chained.
	data := slices.Concat(clean, apeTag(map[string]string{"Composer": "C"}), forged)
	plan, err := mustParseBytes(t, data).Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Equal(out, slices.Concat(clean, forged)) {
		t.Fatal("setup: the strip did not leave the forged page behind the stream")
	}
	if got := mustParseBytes(t, out); hasWarning(got, wl.WarnChainedStream) {
		t.Errorf("the stripped file reads as chained: %v", got.Warnings())
	}
}

// A file with more junk between its pages than the read crosses is read up to where the read
// gives up, and says so; the pages behind that are not read, so it has no essence digest.
func TestOggReadThatGivesUpHasNoDigest(t *testing.T) {
	clean := readFixture(t, sampleOgg)
	pages := oggPages(clean)
	audio := clean[pages[2].off : pages[2].off+pages[2].len]
	data := slices.Clone(clean[:pages[2].off])
	for range 300 { // more gaps than a read crosses
		data = append(append(data, '-'), audio...)
	}
	doc := mustParseBytes(t, data)
	gaps := 0
	for _, w := range doc.Warnings() {
		if w.Code == wl.WarnPageGap {
			gaps++
		}
	}
	if gaps != 2 {
		t.Errorf("warnings = %v, want page-gap for the gaps and for the read giving up", doc.Warnings())
	}
	if _, err := doc.HashAudioEssence(context.Background(), wl.WithHashSource(wl.BytesSource(data))); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("err = %v, want the digest refused as invalid data", err)
	}
	if doc.Fields().Title != "Sample Title" {
		t.Errorf("title = %q", doc.Fields().Title)
	}
	// What the read left holds pages: it is not junk that belongs to nothing, and a rewrite
	// would renumber the pages it read and not those, so the file is read-only.
	if hasWarning(doc, wl.WarnTrailingBytes) {
		t.Errorf("warnings = %v: the unread pages are reported as trailing bytes", doc.Warnings())
	}
	if _, err := doc.Edit().Set(tag.Comment, strings.Repeat("c", 70000)).Prepare(); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("an edit: err = %v, want a refusal as invalid data", err)
	}
	if caps := doc.Capabilities(); !caps.ReadOnly {
		t.Error("capabilities report the file writable")
	}
}

// Pages ahead of the page that begins the stream are bytes that precede it, like any others:
// the tail of a stream whose start is gone, ahead of a whole stream, reads as that stream.
func TestOggBehindPagesOfAnotherStream(t *testing.T) {
	clean := readFixture(t, sampleOgg)
	pages := oggPages(clean)
	stray := clean[pages[3].off:]
	for name, lead := range map[string][]byte{
		"the last page of a stream":            stray,
		"junk and the last page of a stream":   slices.Concat([]byte("junk"), stray),
		"a tag and the last page of a stream":  slices.Concat(testfile.ID3v2(3, testfile.TextFrame(3, "TCOM", "C")), stray),
		"a page that begins an unknown stream": testfile.OggPage(2, 0, 9, 0, []byte("unknown codec")),
	} {
		data := slices.Concat(lead, clean)
		doc, err := wl.Parse(context.Background(), wl.BytesSource(data))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if doc.Format() != wl.FormatOggVorbis || doc.Fields().Title != "Sample Title" || !hasWarning(doc, wl.WarnLeadingBytes) {
			t.Errorf("%s: format %v, title %q, warnings %v", name, doc.Format(), doc.Fields().Title, doc.Warnings())
		}
		if got, want := essenceOf(t, data), essenceOf(t, clean); !got.Equal(want) {
			t.Errorf("%s: essence = %s, want the stream's own %s", name, got, want)
		}
	}
}

// A chained file's later links are not read: junk among their pages and a cut last page are
// theirs, and leave what the first link reads as alone.
func TestOggChainedLaterLinksLeaveTheFirstAlone(t *testing.T) {
	// The first link's STREAMINFO declares a little more than its pages hold, within a page.
	first := readFixture(t, sampleOggFLAC)
	const declared = 66150 + 100
	binary.BigEndian.PutUint32(first[27+1+17+14:], declared)
	testfile.OggSealPage(first[:oggPages(first)[0].len])
	if got := mustParseBytes(t, first).Properties().First().TotalSamples; got != declared {
		t.Fatalf("setup: the first link alone reads %d samples", got)
	}
	later := insertAfterPage(t, readFixture(t, sampleOgg), 1, []byte("0123456789"))
	data := slices.Concat(first, later[:len(later)-100])
	doc := mustParseBytes(t, data)
	if !hasWarning(doc, wl.WarnChainedStream) || hasWarning(doc, wl.WarnPageGap) {
		t.Errorf("warnings = %v, want chained-stream and no page-gap for the later link's junk", doc.Warnings())
	}
	if got := doc.Properties().First().TotalSamples; got != declared {
		t.Errorf("the first link reads %d samples, want STREAMINFO's %d", got, declared)
	}
}
