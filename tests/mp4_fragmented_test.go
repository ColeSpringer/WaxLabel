package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// A movie fragment's sample offsets live in its trun, which the stco/co64 fixups cannot
// reach, so a fragmented file is readable but unwritable. The refusal predicate is a
// top-level moof: an mvex on its own only declares that fragments MAY follow, and files
// carrying one are ordinary and fully writable.

// mp4Fragmented builds a tagged progressive file that also declares mvex and carries a
// moof. Carrying both boxes matches a real fragmented file, and pairs with
// TestMP4MvexWithoutFragmentsIsWritable to pin the predicate on the moof.
func mp4Fragmented(title string) []byte {
	data := mp4AssembleExtra(nil, mp4Mvex(),
		mp4Meta(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", title))))
	return append(data, mp4Atom("moof", make([]byte, 16))...)
}

func TestMP4MvexWithoutFragmentsIsWritable(t *testing.T) {
	// ffmpeg -movflags frag_keyframe on a short stream emits a moov carrying mvex but no
	// moof at all: an ordinary progressive file with a forward declaration. It must read,
	// write, and keep its media addressable.
	data := mp4AssembleExtra(nil, mp4Mvex(),
		mp4Meta(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "Declared"))))
	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "Declared" {
		t.Fatalf("Title = %q, want Declared", got)
	}
	if hasWarning(doc, wl.WarnFragmented) {
		t.Errorf("mvex without a moof is not fragmented; got %v", doc.Warnings())
	}

	plan, err := doc.Edit().Set(tag.Title, "A substantially longer title that grows the ilst").Prepare()
	if err != nil {
		t.Fatalf("mvex-only file must be writable: %v", err)
	}
	out := applyToBytes(t, data, plan)
	if got := mustParseBytes(t, out).Fields().Title; got != "A substantially longer title that grows the ilst" {
		t.Errorf("re-parsed Title = %q", got)
	}
	// The real regression guard: a re-parse would succeed even with a broken offset, so
	// assert the chunk offset still resolves to the mdat payload after the grow.
	if mdat, stco := mp4Index(t, out); mdat != stco {
		t.Errorf("stco entry = %d, want the mdat payload offset %d", stco, mdat)
	}
}

func TestMP4FragmentedReadable(t *testing.T) {
	doc := mustParseBytes(t, mp4Fragmented("Fragged"))
	if got := doc.Fields().Title; got != "Fragged" {
		t.Errorf("Title = %q, want Fragged (the init moov's ilst reads exactly)", got)
	}
	if !hasWarning(doc, wl.WarnFragmented) {
		t.Errorf("expected a fragmented warning; got %v", doc.Warnings())
	}
}

func TestMP4FragmentedWriteRefused(t *testing.T) {
	data := mp4Fragmented("Fragged")
	// The chapter arm guards against anyone later dropping Chapters.Write to AccessNone
	// for a read-only file: the editor would then refuse with ErrUnsupportedTag and
	// "chapters cannot be written to an MP4 file" before Plan ever runs, replacing the
	// precise refusal with a wrong sentinel and a false claim about the format.
	cases := map[string]func(*wl.Editor) *wl.Editor{
		"tag edit": func(e *wl.Editor) *wl.Editor { return e.Set(tag.Title, "Edited") },
		"chapter edit": func(e *wl.Editor) *wl.Editor {
			return e.SetChapters(wl.Chapter{Start: 0, Title: "A"}, wl.Chapter{Start: time.Second, Title: "B"})
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := edit(mustParseBytes(t, data).Edit()).Prepare()
			if !errors.Is(err, waxerr.ErrFragmented) {
				t.Fatalf("write error = %v, want ErrFragmented", err)
			}
		})
	}
}

func TestMP4FragmentedNoOpCopiesVerbatim(t *testing.T) {
	// The refusals sit after the no-op fast path, so an edit that changes nothing still
	// copies the file verbatim rather than failing.
	data := mp4Fragmented("Fragged")
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Fragged").Prepare()
	if err != nil {
		t.Fatalf("an unchanged fragmented file must still plan a verbatim copy: %v", err)
	}
	if !plan.IsNoOp() {
		t.Error("plan should be a no-op (nothing changed)")
	}
	if out := applyToBytes(t, data, plan); !bytes.Equal(out, data) {
		t.Errorf("no-op output differs from the input (%d bytes vs %d)", len(out), len(data))
	}
}

func TestMP4FragmentedSegmentUnsupported(t *testing.T) {
	// A fragmented media segment has no movie box by design. The ftyp is mandatory here:
	// Sniff requires it, so without one the file never reaches the MP4 codec and the test
	// would pass for the wrong reason.
	data := slices.Concat(
		mp4Ftyp(),
		mp4Atom("styp", []byte("msdh\x00\x00\x00\x00msdhmsix")),
		mp4Atom("moof", make([]byte, 16)),
		mp4Atom("mdat", bytes.Repeat([]byte{0xA7}, 64)),
	)
	_, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Fatalf("segment parse error = %v, want ErrUnsupportedFormat", err)
	}
	// Not invalid-data: the segment is well-formed, it just carries no moov, so it must
	// keep the exit-3 classification rather than reading as a corrupt file.
	if errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("a well-formed segment must not classify as invalid data: %v", err)
	}
}

func TestMP4UnwritableFilesReportReadOnly(t *testing.T) {
	// Every shape Plan refuses outright must also report ReadOnly, so the capability
	// matches what a write would do. A file that claimed to be writable and then failed
	// in Plan would break the report==result invariant the Codec contract states.
	cases := map[string][]byte{
		"fragmented":   mp4Fragmented("Fragged"),
		"iloc":         mp4IlocFile(),
		"unknown saio": mp4UnknownSaioFile(),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			doc := mustParseBytes(t, data)
			if !doc.Capabilities().ReadOnly {
				t.Error("an unwritable file should report ReadOnly")
			}
			// Confirm the pairing rather than assume it: the write really is refused.
			if _, err := doc.Edit().Set(tag.Title, "Edited longer title to force a grow").Prepare(); err == nil {
				t.Error("expected the write to be refused")
			}
		})
	}
	// The format at large is writable; only these files are not. Pins the nil-guard
	// split, since a file-agnostic query has no document to consult.
	if wl.CapabilitiesFor(wl.FormatMP4).ReadOnly {
		t.Error("the MP4 format itself must not report ReadOnly")
	}
}

func TestMP4UnwritableDestinationDropsTransfer(t *testing.T) {
	// With ReadOnly reported, a transfer onto such a file reports clean per-item drops
	// AND returns the codec's own refusal: the report says what could not be carried, the
	// error says the write will not happen. Returning only the drops let the transfer
	// collapse into a silent no-op that exited 0, while the same edit through the editor
	// exited 3.
	src := mustParseBytes(t, mp4Tagged(mp4Text("\xa9nam", "Source Title")))
	for name, data := range map[string][]byte{"iloc": mp4IlocFile(), "unknown saio": mp4UnknownSaioFile()} {
		t.Run(name, func(t *testing.T) {
			_, report, err := src.PrepareTransfer(mustParseBytes(t, data))
			if !errors.Is(err, waxerr.ErrUnsupportedFormat) {
				t.Fatalf("PrepareTransfer error = %v, want ErrUnsupportedFormat (the codec's own refusal)", err)
			}
			for _, it := range report.Items {
				if it.Kind == wl.TransferField && it.Key == tag.Title {
					if it.Disposition != wl.Dropped || it.Reason != "destination is read-only" {
						t.Errorf("TITLE = %s (%q), want dropped as read-only", it.Disposition, it.Reason)
					}
				}
			}
		})
	}
}

func TestMP4FragmentedTransferDropsEverything(t *testing.T) {
	// The first real exercise of ReadOnly: true. A read-only destination drops every item
	// and the transfer is refused with the codec's own error - ErrFragmented here, not the
	// generic unsupported-format the iloc/saio cases give, since the two are distinct
	// exit-code rows and flattening them would lose the reason.
	src := mustParseBytes(t, mp4Tagged(
		mp4Text("\xa9nam", "Source Title"),
		mp4Text("\xa9ART", "Source Artist"),
	))
	dstBytes := mp4Fragmented("Fragged")
	plan, report, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
	if !errors.Is(err, waxerr.ErrFragmented) {
		t.Fatalf("PrepareTransfer error = %v, want ErrFragmented", err)
	}
	if plan != nil {
		t.Error("a refused transfer must return no plan")
	}
	if len(report.Items) == 0 {
		t.Fatal("expected transfer items to report on")
	}
	for _, it := range report.Items {
		if it.Disposition != wl.Dropped {
			t.Errorf("%s %s = %s, want dropped", it.Kind, it.Key, it.Disposition)
		}
		if it.Reason != "destination is read-only" {
			t.Errorf("%s %s reason = %q, want %q", it.Kind, it.Key, it.Reason, "destination is read-only")
		}
	}
}

func TestMP4IlstItemNamedLikeOffsetTable(t *testing.T) {
	// Offset-table collection is scoped to stbl children. An unscoped findAll over moov
	// descends into udta/meta/ilst, so an ilst item whose four-cc collides with a table
	// name was decoded as a real table: the parse failed on its bogus entry count, or a
	// growing edit emitted a patch inside the rewritten ilst region and tripped the
	// overlap guard.
	data := mp4Tagged(
		mp4Text("\xa9nam", "T"),
		mp4Text("stco", "not an offset table"),
		mp4Text("co64", "nor is this"),
		mp4Text("saio", "nor this"),
	)
	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "T" {
		t.Fatalf("Title = %q, want T", got)
	}

	plan, err := doc.Edit().Set(tag.Title, "A substantially longer title that grows the ilst").Prepare()
	if err != nil {
		t.Fatalf("an ilst item named like an offset table must not break the write: %v", err)
	}
	out := applyToBytes(t, data, plan)
	// mp4Index finds the real stco, which precedes the ilst in the file.
	if mdat, stco := mp4Index(t, out); mdat != stco {
		t.Errorf("stco entry = %d, want the mdat payload offset %d", stco, mdat)
	}
}

// mp4Iloc builds a minimal iloc item-location box.
func mp4Iloc() []byte {
	return mp4Atom("iloc", slices.Concat([]byte{0, 0, 0, 0}, make([]byte, 12)))
}

// mp4IlocFile builds a tagged file carrying an iloc beside its ilst, inside
// moov.udta.meta.
func mp4IlocFile() []byte {
	return mp4Assemble(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "T")), mp4Iloc())
}

func TestMP4IlocRefusedAtEveryPlacement(t *testing.T) {
	// An iloc's extents are absolute file offsets that nothing patches, so ANY rewrite
	// that moves bytes strands them - not only one sharing the moov.udta.meta this codec
	// replaces. The spec's usual placement is a top-level meta, so a guard that saw only
	// the moov.udta.meta case would miss the shape real files carry and shift the mdat out
	// from under the extents while reporting success.
	tagged := mp4Tagged(mp4Text("\xa9nam", "T"))
	cases := map[string][]byte{
		"moov.udta.meta": mp4IlocFile(),
		"top-level meta": slices.Concat(tagged, mp4Meta(mp4Iloc())),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			doc := mustParseBytes(t, data)
			if got := doc.Fields().Title; got != "T" {
				t.Fatalf("Title = %q, want T (an iloc file still reads)", got)
			}
			_, err := doc.Edit().Set(tag.Title, "A substantially longer title that grows the ilst").Prepare()
			if !errors.Is(err, waxerr.ErrUnsupportedFormat) {
				t.Fatalf("growing edit err = %v, want ErrUnsupportedFormat", err)
			}
		})
	}
}

func TestMP4IlstItemNamedLikeSampleTableContainer(t *testing.T) {
	// trak, mdia, minf, and stbl are all container atoms, so walkAtoms descends into an
	// ilst item carrying one of those four-ccs. Collecting offset tables by name search
	// would reach a crafted table nested inside the very region a growing write replaces,
	// and emit a byte patch there. Resolving the spec path moov > trak > mdia > minf > stbl
	// closes the nested case, not just the direct-child one.
	data := mp4Tagged(
		mp4Text("\xa9nam", "T"),
		mp4Atom("stbl", mp4Stco(0x4000)),
		mp4Atom("minf", mp4Atom("stbl", mp4Stco(0x4000))),
		mp4Atom("trak", mp4Atom("mdia", mp4Atom("minf", mp4Atom("stbl", mp4Stco(0x4000))))),
	)
	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "T" {
		t.Fatalf("Title = %q, want T", got)
	}
	plan, err := doc.Edit().Set(tag.Title, "A substantially longer title that grows the ilst").Prepare()
	if err != nil {
		t.Fatalf("a nested sample-table container inside ilst must not break the write: %v", err)
	}
	out := applyToBytes(t, data, plan)
	if mdat, stco := mp4Index(t, out); mdat != stco {
		t.Errorf("stco entry = %d, want the mdat payload offset %d", stco, mdat)
	}
}

func TestMP4IlocErrorNamesTheBox(t *testing.T) {
	_, err := mustParseBytes(t, mp4IlocFile()).Edit().
		Set(tag.Title, "A substantially longer title that grows the ilst").Prepare()
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("iloc")) {
		t.Errorf("error should name the iloc box: %v", err)
	}
}
