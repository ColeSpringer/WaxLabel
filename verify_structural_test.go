package waxlabel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestParseLimitsThreadedToVerify: parse limits are recorded, floor the read-back every write runs
// (a verified one here), and carry to the returned document.
func TestParseLimitsThreadedToVerify(t *testing.T) {
	elevated := Limits{MaxAllocBytes: 512 << 20, MaxDepth: 128, MaxElements: 500_000}
	doc, err := ParseFile(context.Background(), "testdata/sample.flac", WithLimits(elevated))
	if err != nil {
		t.Fatal(err)
	}
	if doc.limits != elevated {
		t.Errorf("parsed document limits = %+v, want %+v (the parse-time limits must be recorded)", doc.limits, elevated)
	}
	plan, err := doc.Edit().Set(tag.Title, "Verified").Prepare(WithVerifyEssence())
	if err != nil {
		t.Fatal(err)
	}
	resDoc, _, err := plan.Execute(context.Background(), SaveAsFile(filepath.Join(t.TempDir(), "out.flac")))
	if err != nil {
		t.Fatalf("verified write under elevated limits: %v", err)
	}
	if resDoc.limits != elevated {
		t.Errorf("returned document limits = %+v, want %+v (inherited from the base for a re-edit)", resDoc.limits, elevated)
	}
}

// TestReturnedDocumentCarriesReadBackLimits: a write from a Document parsed under limits below the
// defaults returns one labeled with the floored limits its output was read back under, equal to a
// fresh parse of the output under them; the parse limits alone would refuse that output.
func TestReturnedDocumentCarriesReadBackLimits(t *testing.T) {
	ctx := context.Background()
	tight := Limits{MaxAllocBytes: 1 << 16, MaxDepth: 8, MaxElements: 8}
	doc, err := ParseFile(ctx, "testdata/notags.mp3", WithLimits(tight))
	if err != nil {
		t.Fatalf("parse under tight limits: %v", err)
	}
	ed := doc.Edit()
	for i := range 12 {
		ed.Set(tag.Key("CUSTOMFIELD"+string(rune('A'+i))), "value") // one TXXX frame each
	}
	plan, err := ed.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.mp3")
	resDoc, _, err := plan.Execute(ctx, SaveAsFile(out))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseFile(ctx, out, WithLimits(tight)); err == nil {
		t.Fatal("setup: the output should be over the tight limits")
	}
	if resDoc.limits != bits.DefaultLimits {
		t.Errorf("returned document limits = %+v, want the read-back's %+v", resDoc.limits, bits.DefaultLimits)
	}
	fresh, err := ParseFile(ctx, out, WithLimits(resDoc.limits))
	if err != nil {
		t.Fatalf("parse of the output under the returned document's limits: %v", err)
	}
	for _, d := range documentDrift(resDoc, fresh) {
		t.Errorf("%s:\n  returned %s\n  fresh    %s", d.surface, d.result, d.written)
	}
}

// TestVerifyToleratesElementGrowthPastTightLimit: edit past a tight MaxElements
// still verifies (re-parse floors at library default).
func TestVerifyToleratesElementGrowthPastTightLimit(t *testing.T) {
	// Cap that tagless MP3 clears but a dozen-frame edit exceeds.
	doc, err := ParseFile(context.Background(), "testdata/notags.mp3", WithLimits(Limits{MaxElements: 8}))
	if err != nil {
		t.Fatalf("parse under a tight element cap: %v", err)
	}
	ed := doc.Edit()
	for i := range 12 {
		ed.Set(tag.Key("CUSTOMFIELD"+string(rune('A'+i))), "value") // each custom key renders one TXXX frame
	}
	plan, err := ed.Prepare(WithVerifyEssence())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, _, err := plan.Execute(context.Background(), SaveAsFile(filepath.Join(t.TempDir(), "out.mp3"))); err != nil {
		t.Fatalf("verified write after element growth past the tight parse cap: %v", err)
	}
}

// documentDrift compares every surface a Document exposes, in a fixed order.
func documentDrift(a, b *Document) []surfaceDiff {
	var out []surfaceDiff
	note := func(surface string, x, y any, equal bool) {
		if !equal {
			out = append(out, surfaceDiff{surface, fmt.Sprintf("%+v", x), fmt.Sprintf("%+v", y)})
		}
	}
	note("format", a.Format(), b.Format(), a.Format() == b.Format())
	at, bt := a.Tags(), b.Tags()
	note("tags", tagLines(at), tagLines(bt), at.Equal(bt))
	ap, bp := a.Properties(), b.Properties()
	note("properties", ap, bp, reflect.DeepEqual(ap, bp))
	aw, bw := warningLines(a.Warnings()), warningLines(b.Warnings())
	note("warnings", aw, bw, slices.Equal(aw, bw))
	apics, bpics := a.Pictures(), b.Pictures()
	note("pictures", pictureLines(apics), pictureLines(bpics), core.EqualPictures(apics, bpics))
	ac, bc := a.Chapters(), b.Chapters()
	note("chapters", ac, bc, core.EqualChapters(ac, bc))
	as, bs := a.SyncedLyrics(), b.SyncedLyrics()
	note("synced lyrics", as, bs, core.EqualSyncedLyrics(as, bs))
	af, bf := familyLines(a.Families()), familyLines(b.Families())
	note("families", af, bf, slices.Equal(af, bf))
	note("padding", a.Padding(), b.Padding(), a.Padding() == b.Padding())
	al, bl := lintLines(a.Lint()), lintLines(b.Lint())
	note("lint", al, bl, slices.Equal(al, bl))
	ao, bo := a.HasOpaqueLegacyContent(), b.HasOpaqueLegacyContent()
	note("legacy opaque", ao, bo, ao == bo)
	ae, be := a.media.EssenceRanges(), b.media.EssenceRanges()
	note("essence", ae, be, slices.Equal(ae, be))
	an, bn := nativeLines(a), nativeLines(b)
	note("native", an, bn, slices.Equal(an, bn))
	return out
}

func warningLines(ws []Warning) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, fmt.Sprintf("%s %v", w.String(), w.Keys))
	}
	slices.Sort(out)
	return out
}

func familyLines(fs []FamilyValue) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, fmt.Sprintf("%s %s %s %q selected=%v legacy=%v", f.Key, f.Family, f.Scope, f.Values, f.Selected, f.Legacy))
	}
	slices.Sort(out)
	return out
}

func lintLines(fs []Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, fmt.Sprintf("%s fixable=%v", f.String(), f.Fixable))
	}
	slices.Sort(out)
	return out
}

func nativeLines(d *Document) []string {
	n := d.Native()
	if n == nil {
		return nil
	}
	var out []string
	for _, e := range n.Describe() {
		out = append(out, fmt.Sprintf("%s %d%s %s", e.Kind, e.Size, e.Unit, e.Note))
	}
	return out
}

// literalPlan is a plan for doc that writes out verbatim, as a codec that rendered those bytes would
// hand back; result is the media the codec claims the bytes hold.
func literalPlan(doc *Document, out []byte, result *core.Media, opts ...WriteOption) *Plan {
	wp := &core.WritePlan{Segments: []bits.Segment{bits.Lit(out)}, Result: result}
	return &Plan{doc: doc, plan: wp, opts: resolveWriteOptions(opts)}
}

func testdataBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// assertRefusedEverywhere runs the plan mk builds for a fresh copy of sample.flac against every
// destination, with and without WithVerifyEssence, and fails unless the guard whose message holds
// want refuses each with ErrInvalidData, having committed, streamed and left nothing.
func assertRefusedEverywhere(t *testing.T, want string, mk func(doc *Document, opts ...WriteOption) *Plan) {
	t.Helper()
	for _, verify := range []bool{false, true} {
		var opts []WriteOption
		if verify {
			opts = append(opts, WithVerifyEssence())
		}
		for _, dest := range []string{"SaveBack", "SaveAsFile", "WriteTo"} {
			name := dest
			if verify {
				name += " verified"
			}
			t.Run(name, func(t *testing.T) {
				path, data := tempFixture(t, "sample.flac")
				doc, err := ParseFile(context.Background(), path)
				if err != nil {
					t.Fatal(err)
				}
				out := filepath.Join(t.TempDir(), "out.flac")
				var streamed bytes.Buffer
				dst := map[string]Destination{"SaveBack": SaveBack(), "SaveAsFile": SaveAsFile(out), "WriteTo": WriteTo(&streamed, nil)}[dest]
				got, res, err := mk(doc, opts...).Execute(context.Background(), dst)
				if !errors.Is(err, waxerr.ErrInvalidData) || !strings.Contains(err.Error(), want) || res.Committed || got != nil {
					t.Fatalf("Execute = (doc %v, committed %v, err %v), want (nil, false, ErrInvalidData saying %q)", got != nil, res.Committed, err, want)
				}
				assertBytes(t, path, data)
				if _, err := os.Stat(out); err == nil {
					t.Error("a refused SaveAsFile left an output file")
				}
				if streamed.Len() != 0 {
					t.Errorf("a refused WriteTo streamed %d bytes", streamed.Len())
				}
				assertNoTemps(t, filepath.Dir(path))
				assertNoTemps(t, filepath.Dir(out))
			})
		}
	}
}

// TestStructuralVerifyRejectsUnidentifiableOutput: a write whose output would not read back as the
// source's format refuses before anything is committed or streamed, with or without
// WithVerifyEssence: one that no codec recognizes, one that reads as another format, and one that
// is recognized but does not parse.
func TestStructuralVerifyRejectsUnidentifiableOutput(t *testing.T) {
	flac := testdataBytes(t, "sample.flac")
	cases := []struct {
		name, want string
		out        []byte
	}{
		{"junk before the file", "no longer be recognizable as FLAC", slices.Concat([]byte("JUNK"), flac)},
		{"another format", "would read as MP3, not FLAC", testdataBytes(t, "notags.mp3")},
		{"unparseable", "would not parse back cleanly", slices.Concat([]byte("fLaC"), bytes.Repeat([]byte{0xFF}, 64))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertRefusedEverywhere(t, c.want, func(doc *Document, opts ...WriteOption) *Plan {
				return literalPlan(doc, c.out, doc.media, opts...)
			})
		})
	}
}

// TestVerifyRejectsNoAudioOutput: an output that reads back as the source's format but holds no
// audio refuses the write.
func TestVerifyRejectsNoAudioOutput(t *testing.T) {
	flac := testdataBytes(t, "sample.flac")
	src, err := Parse(context.Background(), BytesSource(flac))
	if err != nil {
		t.Fatal(err)
	}
	headerOnly := flac[:src.media.AudioStart]
	if doc, err := Parse(context.Background(), BytesSource(headerOnly)); err != nil || !hasNoAudioWarning(doc.media) {
		t.Fatalf("setup: the metadata alone should parse as a FLAC with no audio (err %v)", err)
	}
	assertRefusedEverywhere(t, "would have no audio", func(doc *Document, opts ...WriteOption) *Plan {
		return literalPlan(doc, headerOnly, doc.media, opts...)
	})
}

// TestVerifyRejectsMissingItems: an output that holds fewer pictures, chapters or synced lyric sets
// than the plan's result is refused, with no discard-warning exemption: the result already reflects
// the drops an edit meant.
func TestVerifyRejectsMissingItems(t *testing.T) {
	flac := testdataBytes(t, "sample.flac") // holds none of the three
	for items, claim := range map[string]func(*core.Media){
		"pictures": func(m *core.Media) {
			m.Pictures = []core.Picture{{Type: PicFrontCover, MIME: "image/png", Data: testfile.PNG()}}
		},
		"chapters":      func(m *core.Media) { m.Chapters = []core.Chapter{{Title: "Lost"}} },
		"synced lyrics": func(m *core.Media) { m.SyncedLyrics = []core.SyncedLyrics{{Lines: []core.SyncedLine{{Text: "Lost"}}}} },
	} {
		t.Run(items, func(t *testing.T) {
			assertRefusedEverywhere(t, "the planned "+items, func(doc *Document, opts ...WriteOption) *Plan {
				result := doc.media.Clone()
				claim(result)
				p := literalPlan(doc, flac, result, opts...)
				p.plan.Report.Warnings = core.Warn(nil, core.WarnPictureUnsupported, "an item was dropped")
				return p
			})
		})
	}
}

// TestVerifyComparesReparsedEssence: WithVerifyEssence hashes the output once, over the essence a
// fresh parse finds, and compares it with the source's digest, so audio one byte longer than the
// source's is refused. A plan whose own essence ranges cover other bytes than that parse finds is
// refused; the same bytes split differently pass. WriteTo verifies too, hashing the source's essence
// first.
func TestVerifyComparesReparsedEssence(t *testing.T) {
	ctx := context.Background()
	var passes int
	hashHook = func() { passes++ }
	t.Cleanup(func() { hashHook = nil })

	verifiedTitlePlan := func(t *testing.T) *Plan {
		t.Helper()
		path, _ := tempFixture(t, "sample.flac")
		doc, err := ParseFile(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := doc.Edit().Set(tag.Title, "Verified").Prepare(WithVerifyEssence())
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	destinations := func(t *testing.T) map[string]Destination {
		return map[string]Destination{
			"SaveAsFile": SaveAsFile(filepath.Join(t.TempDir(), "out.flac")),
			"WriteTo":    WriteTo(&bytes.Buffer{}, nil),
		}
	}

	for name, wantPasses := range map[string]int{"SaveAsFile": 1, "WriteTo": 2} {
		t.Run("hashes once "+name, func(t *testing.T) {
			plan := verifiedTitlePlan(t)
			passes = 0
			if _, res, err := plan.Execute(ctx, destinations(t)[name]); err != nil || !res.Committed {
				t.Fatalf("verified write: err=%v committed=%v", err, res.Committed)
			}
			if passes != wantPasses {
				t.Errorf("essence hash passes = %d, want %d", passes, wantPasses)
			}
		})
	}

	split := func(m *core.Media, empty bool) {
		r := m.EssenceRanges()[0]
		mid := (r[0] + r[1]) / 2
		m.AudioRanges = [][2]int64{{r[0], mid}, {mid, r[1]}}
		if empty {
			m.AudioRanges = [][2]int64{{r[0], r[0]}, {r[0], mid}, {mid, mid}, {mid, r[1]}}
		}
	}
	for name, c := range map[string]struct {
		move    func(m *core.Media)
		refused bool
	}{
		"ranges split":                   {func(m *core.Media) { split(m, false) }, false},
		"ranges split around empty ones": {func(m *core.Media) { split(m, true) }, false},
		"start moved":                    {func(m *core.Media) { m.AudioStart++ }, true},
	} {
		for dest := range destinations(t) {
			t.Run(name+" "+dest, func(t *testing.T) {
				plan := verifiedTitlePlan(t)
				result := plan.plan.Result.Clone()
				c.move(result)
				plan.plan.Result = result
				_, res, err := plan.Execute(ctx, destinations(t)[dest])
				if !c.refused {
					if err != nil || !res.Committed {
						t.Fatalf("Execute = (committed %v, err %v), want it written: the ranges cover the read-back's bytes", res.Committed, err)
					}
					return
				}
				if !errors.Is(err, waxerr.ErrInvalidData) || !strings.Contains(err.Error(), "not where the plan placed it") || res.Committed {
					t.Fatalf("Execute = (committed %v, err %v), want the placement refusal before any commit", res.Committed, err)
				}
			})
		}
	}

	// copyPlan copies the whole source, with one byte inserted mid-audio when insert is set and the
	// plan's audio end moved to match. Inserted, not replaced, so the copy tap still sees every
	// source byte and only the digest can tell the audio changed.
	copyPlan := func(doc *Document, insert bool, opts ...WriteOption) *Plan {
		m := doc.media
		mid := (m.AudioStart + m.AudioEnd) / 2
		segs := []bits.Segment{bits.Copy(0, mid), bits.Copy(mid, m.Identity.Size-mid)}
		result := m.Clone()
		if insert {
			segs = slices.Insert(segs, 1, bits.Lit([]byte{0}))
			result.AudioEnd++
		}
		return &Plan{doc: doc, plan: &core.WritePlan{Segments: segs, Result: result}, opts: resolveWriteOptions(opts)}
	}
	for dest := range destinations(t) {
		t.Run("audio changed "+dest, func(t *testing.T) {
			path, _ := tempFixture(t, "sample.flac")
			doc, err := ParseFile(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, res, err := copyPlan(doc, false, WithVerifyEssence()).Execute(ctx, destinations(t)[dest]); err != nil || !res.Committed {
				t.Fatalf("verified unchanged copy: err=%v committed=%v, want it written", err, res.Committed)
			}
			if _, res, err := copyPlan(doc, true).Execute(ctx, destinations(t)[dest]); err != nil || !res.Committed {
				t.Fatalf("unverified write with a byte inserted: err=%v committed=%v, want it written", err, res.Committed)
			}
			_, res, err := copyPlan(doc, true, WithVerifyEssence()).Execute(ctx, destinations(t)[dest])
			if !errors.Is(err, waxerr.ErrInvalidData) || !strings.Contains(err.Error(), "does not match the source") || res.Committed {
				t.Fatalf("verified write with a byte inserted: (committed %v, err %v), want the digest refusal before any commit", res.Committed, err)
			}
		})
	}
}

// TestReadBackKeepsReadErrors: a read that fails during a write's read-back keeps its cause, as a
// failed copy does, instead of reading as corrupt output. A WriteTo source that fails inside the
// audio streams nothing, with or without verify, and an output that cannot be read at all is not
// called unrecognizable, while one cut short is invalid data by its short read.
func TestReadBackKeepsReadErrors(t *testing.T) {
	ctx := context.Background()
	data := testdataBytes(t, "sample.flac")
	doc, err := Parse(ctx, BytesSource(data))
	if err != nil {
		t.Fatal(err)
	}
	src := &testfile.Faulty{Src: BytesSource(data), Fails: testfile.From(doc.media.AudioStart)}
	for _, verify := range []bool{false, true} {
		var opts []WriteOption
		if verify {
			opts = append(opts, WithVerifyEssence())
		}
		plan, err := doc.Edit().Set(tag.Title, "Read Error").Prepare(opts...)
		if err != nil {
			t.Fatal(err)
		}
		var streamed bytes.Buffer
		_, res, err := plan.Execute(ctx, WriteTo(&streamed, src))
		if _, ok := errors.AsType[*fs.PathError](err); !ok || errors.Is(err, waxerr.ErrInvalidData) || res.Committed || streamed.Len() != 0 {
			t.Errorf("verify=%v: (committed %v, streamed %d, err %v), want the read error and nothing streamed", verify, res.Committed, streamed.Len(), err)
		}
	}

	plan, err := doc.Edit().Set(tag.Title, "Read Error").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.verifyOutput(ctx, failingReaderAt{size: 1 << 10}, nil); !errors.Is(err, errDisk) || errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("unreadable output: err = %v, want the read error, not invalid data", err)
	}
	if _, err := plan.verifyOutput(ctx, cutShort{size: 1 << 10}, nil); !errors.Is(err, waxerr.ErrInvalidData) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("output cut short: err = %v, want invalid data from the short read", err)
	}
}

// cutShort declares size bytes but holds none, as an output cut short under the read-back does: every
// read ends early with io.EOF.
type cutShort struct{ size int64 }

func (c cutShort) Size() int64                       { return c.size }
func (c cutShort) ReadAt([]byte, int64) (int, error) { return 0, io.EOF }

// TestHashKeepsReadErrors: a read that fails inside the hashed bytes keeps its cause, while a source
// that ends inside the essence is invalid data.
func TestHashKeepsReadErrors(t *testing.T) {
	ctx := context.Background()
	data := testdataBytes(t, "sample.flac")
	doc, err := Parse(ctx, BytesSource(data))
	if err != nil {
		t.Fatal(err)
	}
	mid := (doc.media.AudioStart + doc.media.AudioEnd) / 2
	for name, hash := range map[string]func(context.Context, ...HashOption) (AudioDigest, error){
		"HashAudioEssence": doc.HashAudioEssence,
		"HashFile":         doc.HashFile,
	} {
		if _, err := hash(ctx, WithHashSource(&testfile.Faulty{Src: BytesSource(data), Fails: testfile.From(mid)})); !errors.Is(err, syscall.EIO) || errors.Is(err, waxerr.ErrInvalidData) {
			t.Errorf("%s with a read error: err = %v, want the read error, not invalid data", name, err)
		}
	}
	if _, err := doc.HashAudioEssence(ctx, WithHashSource(BytesSource(data[:mid]))); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("HashAudioEssence of a source cut short: err = %v, want ErrInvalidData", err)
	}
}

// eofAtEnd serves its bytes with io.EOF on every read that reaches their end, full reads included,
// as io.ReaderAt allows.
type eofAtEnd []byte

func (b eofAtEnd) Size() int64 { return int64(len(b)) }

func (b eofAtEnd) ReadAt(p []byte, off int64) (int, error) {
	n, err := BytesSource(b).ReadAt(p, off)
	if err == nil && off+int64(n) == int64(len(b)) {
		err = io.EOF
	}
	return n, err
}

// TestFullReadsEndingWithEOF: a source that reports io.EOF with the full read that reaches its end
// parses as the same bytes from BytesSource do, tags at the end included, hashes to the same
// essence digest, and takes a verified WriteTo.
func TestFullReadsEndingWithEOF(t *testing.T) {
	ctx := context.Background()
	flac := testdataBytes(t, "sample.flac")
	for name, data := range map[string][]byte{
		"sample.mp3":          testdataBytes(t, "sample.mp3"), // a trailing ID3v1
		"sample.wv":           testdataBytes(t, "sample.wv"),  // APEv2 tags at the end
		"sample.ape":          testdataBytes(t, "sample.ape"),
		"sample.flac":         flac, // audio to the end
		"trailing-id3v1.flac": slices.Concat(flac, testfile.ID3v1("Trailing Title", "", "", "", "", 255)),
	} {
		t.Run(name, func(t *testing.T) {
			want, err := Parse(ctx, BytesSource(data))
			if err != nil {
				t.Fatal(err)
			}
			got, err := Parse(ctx, eofAtEnd(data))
			if err != nil {
				t.Fatalf("parse with EOF at the end: %v", err)
			}
			for _, d := range documentDrift(want, got) {
				t.Errorf("%s:\n  BytesSource %s\n  EOF at end  %s", d.surface, d.result, d.written)
			}
			wantSum, err := want.HashAudioEssence(ctx, WithHashSource(BytesSource(data)))
			if err != nil {
				t.Fatal(err)
			}
			if sum, err := got.HashAudioEssence(ctx, WithHashSource(eofAtEnd(data))); err != nil || !sum.Equal(wantSum) {
				t.Errorf("HashAudioEssence = (%v, %v), want %v", sum, err, wantSum)
			}
			plan, err := got.Edit().Set(tag.Title, "Verified").Prepare(WithVerifyEssence())
			if err != nil {
				t.Fatal(err)
			}
			if _, res, err := plan.Execute(ctx, WriteTo(&bytes.Buffer{}, eofAtEnd(data))); err != nil || !res.Committed {
				t.Errorf("verified WriteTo: (committed %v, err %v), want it written", res.Committed, err)
			}
		})
	}
}
