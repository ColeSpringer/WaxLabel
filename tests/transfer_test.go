package waxlabel_test

import (
	"errors"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestPlanTransferReportsLosses simulates copying an M4B (tags + chapters) into a
// FLAC: the tags carry but the chapters drop, and no destination bytes are needed.
func TestPlanTransferReportsLosses(t *testing.T) {
	src := mustParseFile(t, sampleM4B)
	report, err := src.PlanTransfer(wl.FormatFLAC)
	if err != nil {
		t.Fatalf("PlanTransfer: %v", err)
	}
	if report.Source != wl.FormatMP4 || report.Dest != wl.FormatFLAC {
		t.Errorf("report formats = %s -> %s, want MP4 -> FLAC", report.Source, report.Dest)
	}

	var sawChapterDrop bool
	for _, it := range report.Items {
		switch it.Kind {
		case wl.TransferField:
			if it.Disposition != wl.Carried {
				t.Errorf("field %s = %s, want carried (FLAC writes all fields)", it.Key, it.Disposition)
			}
		case wl.TransferChapter:
			sawChapterDrop = true
			if it.Disposition != wl.Dropped {
				t.Errorf("chapters = %s, want dropped (FLAC has no chapter write)", it.Disposition)
			}
			if it.Reason == "" {
				t.Error("a dropped item must carry a reason")
			}
		}
	}
	if !sawChapterDrop {
		t.Error("expected a dropped chapter item (the M4B has chapters)")
	}
	if report.Lossless() {
		t.Error("dropping chapters is not lossless")
	}
}

// TestPlanTransferUnsupportedDest: simulating a transfer to a format with no codec
// is an error, while a writable destination (Matroska) carries the canonical set.
func TestPlanTransferUnsupportedDest(t *testing.T) {
	src := mustParseFile(t, sampleFLAC)

	if _, err := src.PlanTransfer(wl.FormatUnknown); !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Errorf("PlanTransfer(Unknown) err = %v, want ErrUnsupportedFormat", err)
	}

	report, err := src.PlanTransfer(wl.FormatMatroska)
	if err != nil {
		t.Fatalf("PlanTransfer(Matroska): %v", err)
	}
	carried, _, dropped := report.Counts()
	if carried == 0 || dropped != 0 {
		t.Errorf("writable Matroska should carry the canonical fields, got %+v", report)
	}
}

// TestPrepareTransferReportMatchesResult is the load-bearing invariant: the loss
// report PrepareTransfer returns is exactly what executing its plan produces -
// every carried field lands with the source's values, and the dropped chapters do
// not appear. M4B -> FLAC exercises both a carried set and a dropped set at once.
func TestPrepareTransferReportMatchesResult(t *testing.T) {
	src := mustParseFile(t, sampleM4B)
	dstBytes := readFixture(t, "../testdata/notags.flac") // a blank canvas
	dst := mustParseBytes(t, dstBytes)

	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))

	carriedKeys := map[tag.Key]bool{}
	for _, it := range report.Items {
		switch it.Kind {
		case wl.TransferField:
			srcVals, _ := src.Get(it.Key)
			gotVals, present := result.Get(it.Key)
			switch it.Disposition {
			case wl.Carried:
				carriedKeys[it.Key] = true
				if !present || !slices.Equal(gotVals, srcVals) {
					t.Errorf("carried %s = %v (present=%v), want source values %v", it.Key, gotVals, present, srcVals)
				}
			case wl.Dropped:
				if present {
					t.Errorf("dropped %s should not have been written, got %v", it.Key, gotVals)
				}
			}
		case wl.TransferChapter:
			if it.Disposition == wl.Dropped && len(result.Chapters()) != 0 {
				t.Errorf("chapters reported dropped but result has %d", len(result.Chapters()))
			}
		}
	}
	// The blank destination had no tags, so the result's keys are exactly the
	// carried set - the report and the write cannot disagree on membership.
	for _, k := range result.Tags().Keys() {
		if !carriedKeys[k] {
			t.Errorf("result has key %s the report did not mark carried", k)
		}
	}
}

// TestPrepareTransferOverlayKeepsDestKeys: a key present only in the destination
// survives the overlay (copy adds/overwrites the source's keys, it does not wipe
// the destination).
func TestPrepareTransferOverlayKeepsDestKeys(t *testing.T) {
	src := mustParseBytes(t, readFixture(t, sampleFLAC)) // TITLE/ARTIST/ALBUM/ENCODER
	// A destination FLAC carrying a key the source lacks.
	dstBytes := writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set("CATALOGNUMBER", "KEPT-001")
	})
	dst := mustParseBytes(t, dstBytes)

	plan, _, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))

	if got, _ := result.Get("CATALOGNUMBER"); len(got) != 1 || got[0] != "KEPT-001" {
		t.Errorf("CATALOGNUMBER = %v, want it preserved from the destination", got)
	}
	if got, _ := result.Get("TITLE"); len(got) != 1 || got[0] != "Original Title" {
		t.Errorf("TITLE = %v, want overlaid from the source", got)
	}
}

// TestPrepareTransferCarriesPictures: a source front cover carries into a
// picture-capable destination and matches byte-for-byte. The destination here is MP4,
// whose covr stores image data only - but a plain front cover (no role, no
// description) round-trips losslessly, so the transfer reports it Carried, not Lossy
// (the per-picture metadata loss is the plan warning's job, and there is none here).
func TestPrepareTransferCarriesPictures(t *testing.T) {
	png := tinyPNG()
	srcBytes := writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set("TITLE", "Cover Test")
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: png})
	})
	src := mustParseBytes(t, srcBytes)

	dstBytes := readFixture(t, "../testdata/notags.m4a")
	dst := mustParseBytes(t, dstBytes)

	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	var pic wl.TransferItem
	for _, it := range report.Items {
		if it.Kind == wl.TransferPicture {
			pic = it
		}
	}
	if pic.Disposition != wl.Carried || pic.Count != 1 {
		t.Fatalf("picture item = %+v, want 1 carried (a plain front cover round-trips on MP4)", pic)
	}

	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
	pics := result.Pictures()
	if len(pics) != 1 || !slices.Equal(pics[0].Data, png) {
		t.Errorf("destination picture not carried byte-for-byte (got %d pictures)", len(pics))
	}
}

// TestPlanTransferMatchesPrepareTransfer: the format-only simulation and the
// project-onto-a-document path agree, because both consult the same decision
// function with the same destination capabilities.
func TestPlanTransferMatchesPrepareTransfer(t *testing.T) {
	src := mustParseFile(t, sampleM4B)
	dst := mustParseBytes(t, readFixture(t, "../testdata/notags.flac"))

	sim, err := src.PlanTransfer(dst.Format())
	if err != nil {
		t.Fatalf("PlanTransfer: %v", err)
	}
	_, applied, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}

	if len(sim.Items) != len(applied.Items) {
		t.Fatalf("item counts differ: sim %d, applied %d", len(sim.Items), len(applied.Items))
	}
	for i := range sim.Items {
		if sim.Items[i] != applied.Items[i] {
			t.Errorf("item %d: sim %+v != applied %+v", i, sim.Items[i], applied.Items[i])
		}
	}
}

// TestMP4RejectsUnstorableCover: an MP4 covr atom can only label JPEG/PNG/BMP, so
// a cover in another format must fail loudly at Prepare rather than be silently
// stored mislabeled as JPEG (a corrupt cover a cross-format copy would otherwise
// claim "carried losslessly"). A supported format still writes.
func TestMP4RejectsUnstorableCover(t *testing.T) {
	doc := mustParseBytes(t, readFixture(t, "../testdata/notags.m4a"))
	_, err := doc.Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/webp", Data: []byte("RIFF....WEBP")}).
		Prepare()
	if !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Errorf("err = %v, want ErrUnsupportedTag for a WebP cover on MP4", err)
	}
	if _, err := doc.Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).Prepare(); err != nil {
		t.Errorf("a PNG cover should be accepted: %v", err)
	}
}

// TestPrepareTransferToMatroska projects a FLAC's tags onto a Matroska canvas and
// confirms the report matches the written result - a cross-format transfer into a
// now-writable container (Title lands in Info.Title, the rest in SimpleTags).
func TestPrepareTransferToMatroska(t *testing.T) {
	src := mustParseFile(t, sampleFLAC)
	dstBytes := readFixture(t, "../testdata/notags.mka")
	dst := mustParseBytes(t, dstBytes)

	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	if carried, _, dropped := report.Counts(); carried == 0 || dropped != 0 {
		t.Fatalf("expected canonical fields carried, got %+v", report)
	}
	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
	// Check the cleanly-mapping core fields land with the source values (TITLE goes
	// to Info.Title, the rest to SimpleTags). ENCODER is asserted too: it now maps
	// to the canonical Encoder key on both the Vorbis and Matroska sides, so it
	// round-trips exactly (previously Matroska read it back as EncodedBy, a latent
	// cross-format asymmetry the dedicated ENCODER key resolves).
	for _, k := range []tag.Key{tag.Title, tag.Artist, tag.Album, tag.Encoder} {
		srcVals, _ := src.Get(k)
		gotVals, present := result.Get(k)
		if len(srcVals) > 0 && (!present || !slices.Equal(gotVals, srcVals)) {
			t.Errorf("carried %s = %v (present=%v), want %v", k, gotVals, present, srcVals)
		}
	}
}

// coverBearingFLAC returns an in-memory FLAC carrying a title and a front cover,
// the cross-format source for the WebM cover-gating tests.
func coverBearingFLAC(t *testing.T, title string) []byte {
	t.Helper()
	return writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set("TITLE", title)
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()})
	})
}

// TestPlanTransferMatroskaCoverWritable: a format-only PlanTransfer has no
// destination file, so Matroska answers file-agnostically and still reports a
// cover writable. The WebM cover refusal is a per-file constraint only a real
// destination (PrepareTransfer/copy) can see, so PlanTransfer(Format) stays right
// by construction.
func TestPlanTransferMatroskaCoverWritable(t *testing.T) {
	src := mustParseBytes(t, coverBearingFLAC(t, "Cover Test"))
	report, err := src.PlanTransfer(wl.FormatMatroska)
	if err != nil {
		t.Fatalf("PlanTransfer(Matroska): %v", err)
	}
	var pic wl.TransferItem
	for _, it := range report.Items {
		if it.Kind == wl.TransferPicture {
			pic = it
		}
	}
	if pic.Disposition != wl.Carried {
		t.Errorf("cover = %s, want carried (no file means the WebM constraint is invisible)", pic.Disposition)
	}
}

// TestPrepareTransferCoverToWebMDropsCover covers a file-dependent capability:
// projecting a cover-bearing source onto WebM reports the cover dropped
// (Attachments is outside the WebM subset), and the executed plan carries no
// picture while the tags still land. Before file-aware capabilities this reported
// "carried" and then errored at Prepare.
func TestPrepareTransferCoverToWebMDropsCover(t *testing.T) {
	src := mustParseBytes(t, coverBearingFLAC(t, "WebM Transfer"))

	dstBytes := readFixture(t, sampleWebM)
	dst := mustParseBytes(t, dstBytes)

	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer onto WebM: %v", err)
	}
	var pic wl.TransferItem
	for _, it := range report.Items {
		if it.Kind == wl.TransferPicture {
			pic = it
		}
	}
	if pic.Disposition != wl.Dropped {
		t.Fatalf("cover = %s, want dropped for a WebM destination", pic.Disposition)
	}
	if pic.Reason == "" {
		t.Error("a dropped cover must carry a reason")
	}

	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
	if got := result.Pictures(); len(got) != 0 {
		t.Errorf("WebM result has %d pictures, want 0 (the cover was dropped)", len(got))
	}
	// The tags still transfer - only the cover is gated by the WebM subset.
	if got, ok := result.Get(tag.Title); !ok || !slices.Equal(got, []string{"WebM Transfer"}) {
		t.Errorf("carried TITLE = %v (present=%v), want [WebM Transfer]", got, ok)
	}
}

// TestTransferPictureDisposition verifies that a picture set can be lossy even
// when the image bytes carry losslessly. The report is Lossy only when the
// destination drops role or description metadata the pictures actually carry,
// matching the destination's write-time picture-metadata warning. MP4 drops role
// and description, Matroska drops only non-front roles, and FLAC drops neither.
func TestTransferPictureDisposition(t *testing.T) {
	png := tinyPNG()
	flacWith := func(p wl.Picture) []byte {
		return writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
			e.Set("TITLE", "X")
			e.AddPicture(p)
		})
	}
	front := wl.Picture{Type: wl.PicFrontCover, Data: png}
	describedFront := wl.Picture{Type: wl.PicFrontCover, Description: "liner", Data: png}
	back := wl.Picture{Type: wl.PicBackCover, Data: png}
	other := wl.Picture{Type: wl.PicOther, Data: png}

	disp := func(srcBytes []byte, dst wl.Format) wl.Disposition {
		rep, err := mustParseBytes(t, srcBytes).PlanTransfer(dst)
		if err != nil {
			t.Fatalf("PlanTransfer to %s: %v", dst, err)
		}
		for _, it := range rep.Items {
			if it.Kind == wl.TransferPicture {
				return it.Disposition
			}
		}
		t.Fatalf("no picture item in transfer to %s", dst)
		return wl.Dropped
	}

	cases := []struct {
		name string
		src  []byte
		dst  wl.Format
		want wl.Disposition
	}{
		{"plain front -> MP4", flacWith(front), wl.FormatMP4, wl.Carried},
		{"back cover -> MP4", flacWith(back), wl.FormatMP4, wl.Lossy},
		{"described front -> MP4", flacWith(describedFront), wl.FormatMP4, wl.Lossy},
		{"described front -> Matroska", flacWith(describedFront), wl.FormatMatroska, wl.Carried},
		{"back cover -> Matroska", flacWith(back), wl.FormatMatroska, wl.Lossy},
		{"other -> Matroska", flacWith(other), wl.FormatMatroska, wl.Carried}, // PicOther round-trips as Other
		{"other -> MP4", flacWith(other), wl.FormatMP4, wl.Lossy},             // MP4 reads every cover back as front
		{"plain front -> FLAC", flacWith(front), wl.FormatFLAC, wl.Carried},
	}
	for _, c := range cases {
		if got := disp(c.src, c.dst); got != c.want {
			t.Errorf("%s: disposition = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestPlanTransferNumericGenreLossy verifies that under --numeric-genre a recognized GENRE is
// stored as a numeric reference (ID3 TCON / MP4 gnre) and re-read as its canonical name, so
// the destination mutates the value. The transfer report must grade it Lossy, not Carried;
// without the option the genre carries verbatim. Every numeric-genre destination - AAC, MP3,
// and MP4 (gnre) - must report consistently.
func TestPlanTransferNumericGenreLossy(t *testing.T) {
	src := mustParseBytes(t, append(id3v2(3, textFrame(3, "TCON", "Rock"), textFrame(3, "TIT2", "T")), mp3Audio(t)...))

	genreDisp := func(dst wl.Format, opts ...wl.WriteOption) wl.Disposition {
		report, err := src.PlanTransfer(dst, opts...)
		if err != nil {
			t.Fatalf("PlanTransfer(%v): %v", dst, err)
		}
		for _, it := range report.Items {
			if it.Kind == wl.TransferField && it.Key == tag.Genre {
				return it.Disposition
			}
		}
		t.Fatalf("transfer report to %v has no GENRE field", dst)
		return wl.Carried
	}

	// AIFF and WAV join AAC/MP3/MP4 under --numeric-genre. AIFF has no native genre slot, so
	// genre always routes through the ID3 chunk; WAV may force an ID3 chunk for the rest of a
	// transfer (a multi-value field, an unmapped key, a picture), and its numeric TCON then
	// wins read precedence - so the value-blind capability reports GENRE partial conservatively
	// for both (matching the other ID3 codecs). A plain (non-numeric) transfer still carries.
	for _, dst := range []wl.Format{wl.FormatAAC, wl.FormatMP3, wl.FormatMP4, wl.FormatAIFF, wl.FormatWAV} {
		if d := genreDisp(dst); d != wl.Carried {
			t.Errorf("GENRE transfer to %v without numeric-genre = %s, want carried", dst, d)
		}
		if d := genreDisp(dst, wl.WithNumericGenre()); d != wl.Lossy {
			t.Errorf("GENRE transfer to %v under numeric-genre = %s, want lossy (numeric resolution mutates the value)", dst, d)
		}
	}
}

// TestAiffNumericGenreReducedWarns verifies that AIFF has no native genre slot, so a
// --numeric-genre write stores the genre as a numeric ID3 TCON, re-read as its canonical
// name - mutating "rock" -> "Rock". The edit must warn value-reduced naming GENRE (it
// previously graded the field carried and stayed silent); a plain write keeps the text
// genre verbatim with no warning.
func TestAiffNumericGenreReducedWarns(t *testing.T) {
	data := aiffFile("AIFF", stdCOMM(), aiffSSND(400), aiffID3(id3v2(4, textFrame(4, "TIT2", "T"))))

	num, err := mustParseBytes(t, data).Edit().Set(tag.Genre, "rock").Prepare(wl.WithNumericGenre())
	if err != nil {
		t.Fatalf("prepare numeric: %v", err)
	}
	if !hasKeyedValueReduced(num.Report().Warnings, tag.Genre) {
		t.Errorf("AIFF --numeric-genre GENRE must warn value-reduced; got %v", num.Report().Warnings)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, data, num)).Tags().First(tag.Genre); v != "Rock" {
		t.Errorf("AIFF --numeric-genre GENRE round-trip = %q, want the mutated canonical Rock", v)
	}

	plain := prepareWith(t, data, func(e *wl.Editor) { e.Set(tag.Genre, "rock") })
	if hasKeyedValueReduced(plain.Report().Warnings, tag.Genre) {
		t.Errorf("a plain (text) GENRE write must not warn value-reduced; got %v", plain.Report().Warnings)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, data, plain)).Tags().First(tag.Genre); v != "rock" {
		t.Errorf("plain GENRE round-trip = %q, want rock verbatim", v)
	}
}

// TestWavNumericGenreReducedWithID3 verifies that WAV genre uses native LIST/INFO IGNR
// text unless an id3 chunk is present, in which case the id3 value wins read precedence.
// With a preserved id3 chunk a --numeric-genre write's numeric TCON becomes authoritative
// and mutates "rock" -> "Rock", so it must warn; a bare WAV keeps IGNR text losslessly and
// must not warn.
func TestWavNumericGenreReducedWithID3(t *testing.T) {
	withID3 := wavFile(wavFmtPCM(), wavID3(id3v2(4, textFrame(4, "TIT2", "T"))), wavData(400))
	wplan, err := mustParseBytes(t, withID3).Edit().Set(tag.Genre, "rock").Prepare(wl.WithNumericGenre())
	if err != nil {
		t.Fatalf("prepare withID3: %v", err)
	}
	if !hasKeyedValueReduced(wplan.Report().Warnings, tag.Genre) {
		t.Errorf("WAV with an id3 chunk: --numeric-genre GENRE must warn value-reduced; got %v", wplan.Report().Warnings)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, withID3, wplan)).Tags().First(tag.Genre); v != "Rock" {
		t.Errorf("WAV+id3 --numeric-genre GENRE round-trip = %q, want the mutated Rock", v)
	}

	bare := wavFile(wavFmtPCM(), wavData(400))
	bplan, err := mustParseBytes(t, bare).Edit().Set(tag.Genre, "rock").Prepare(wl.WithNumericGenre())
	if err != nil {
		t.Fatalf("prepare bare: %v", err)
	}
	if hasKeyedValueReduced(bplan.Report().Warnings, tag.Genre) {
		t.Errorf("a bare WAV keeps native IGNR text, so --numeric-genre GENRE must not warn; got %v", bplan.Report().Warnings)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, bare, bplan)).Tags().First(tag.Genre); v != "rock" {
		t.Errorf("bare WAV GENRE round-trip = %q, want rock verbatim (IGNR text)", v)
	}
}

// TestWavForcedID3NumericGenreWarns verifies that a bare WAV whose edit forces an
// id3 chunk into existence - here an unmapped key, which LIST/INFO cannot store - routes
// genre through that chunk too, so under --numeric-genre the numeric TCON mutates "rock" ->
// "Rock" even though the base file had no id3 chunk. The value-blind capability cannot see
// the forcing, so it reports GENRE partial under --numeric-genre and the edited-vs-result
// comparison turns that into a precise value-reduced warning (no false positive on the
// genre-only bare case, which TestWavNumericGenreReducedWithID3 covers).
func TestWavForcedID3NumericGenreWarns(t *testing.T) {
	bare := wavFile(wavFmtPCM(), wavData(400))
	plan, err := mustParseBytes(t, bare).Edit().
		Set(tag.Genre, "rock").
		Set(tag.Key("PRIVATE_X"), "x"). // unmapped key -> not INFO-representable -> forces an id3 chunk
		Prepare(wl.WithNumericGenre())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !hasKeyedValueReduced(plan.Report().Warnings, tag.Genre) {
		t.Errorf("a forced-id3 bare WAV --numeric-genre GENRE must warn value-reduced; got %v", plan.Report().Warnings)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, bare, plan)).Tags().First(tag.Genre); v != "Rock" {
		t.Errorf("forced-id3 GENRE round-trip = %q, want the mutated Rock", v)
	}
}

// TestTransferCarriesV23MultiValueWarning is the F11 regression: a copy that carries a
// multi-value field verbatim onto an ID3v2.3 destination - while another field changes, so
// the multi-value frame is preserved rather than re-rendered - must surface the
// [id3-multi-value] caveat, the same one a direct multi-value set warns. The earlier gap
// was that the warning was raised only for a re-rendered multi-value, so a carried-verbatim
// one slipped through and the copy reported the field "carried" with no caveat.
func TestTransferCarriesV23MultiValueWarning(t *testing.T) {
	base := readFixture(t, sampleMP3) // ID3v2.3
	// A v2.3 MP3 carrying a genuine multi-value ARTIST.
	multi := applyToBytes(t, base, mustPlan(t, mustParseBytes(t, base).Edit().Set(tag.Artist, "A", "B", "C")))

	// Source and destination both hold ARTIST=[A,B,C] but differ in TITLE, so the copy
	// changes only TITLE and carries the multi-value ARTIST verbatim.
	src := applyToBytes(t, multi, mustPlan(t, mustParseBytes(t, multi).Edit().Set(tag.Title, "Source Title")))
	dst := applyToBytes(t, multi, mustPlan(t, mustParseBytes(t, multi).Edit().Set(tag.Title, "Dest Title")))

	plan, _, err := mustParseBytes(t, src).PrepareTransfer(mustParseBytes(t, dst))
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	if plan.IsNoOp() {
		t.Fatal("expected a real write (TITLE changes), got a no-op")
	}
	warned := false
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnID3MultiValue {
			warned = true
		}
	}
	if !warned {
		t.Errorf("a copy carrying a v2.3 multi-value verbatim must warn id3-multi-value; got %v", plan.Report().Warnings)
	}
	// The carried multi-value still round-trips for our own reader.
	if got := mustParseBytes(t, applyToBytes(t, dst, plan)).Fields().Artists; !slices.Equal(got, []string{"A", "B", "C"}) {
		t.Errorf("carried multi-value artists = %v, want [A B C]", got)
	}
}

// mustPlan prepares an edit, failing the test on error.
func mustPlan(t *testing.T, ed *wl.Editor) *wl.Plan {
	t.Helper()
	p, err := ed.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
