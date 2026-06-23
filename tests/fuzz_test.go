package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// FuzzParse asserts that the parser never panics on arbitrary input and that
// accepted input stays internally consistent: a no-op write reproduces the input
// bytes, and re-parsing succeeds. Run with:
//
//	go test -run x -fuzz FuzzParse
func FuzzParse(f *testing.F) {
	// Seed with the real fixtures and hand-built malformations, including Ogg page
	// edge cases such as multi-page packets and truncated pages.
	for _, p := range []string{
		sampleFLAC, "../testdata/notags.flac", sampleOgg, sampleOpus, notagsOgg, "../testdata/notags.opus",
		sampleMP3, sampleMP324, notagsMP3, sampleWAV, notagsWAV, sampleMP4, notagsMP4,
		sampleMKA, sampleWebM, notagsMKA, chaptersMKA, sampleAIFF, notagsAIFF, sampleAIFC, sampleM4B,
		sampleAAC, notagsAAC,
	} {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte("ID3\x03\x00\x00\x00\x00\x00\x7f"))                                                                                      // ID3v2.3 header claiming 127 body bytes it lacks
	f.Add(append([]byte("ID3\x04\x00\x00\x00\x00\x00\x10"), []byte("TIT2")...))                                                           // truncated v2.4 frame
	f.Add([]byte("\xff\xfb\x90\x00"))                                                                                                     // bare MPEG-1 Layer 3 frame header, no body
	f.Add([]byte("fLaC"))                                                                                                                 // marker only, no blocks
	f.Add([]byte("fLaC\x00\x00\x00\x22"))                                                                                                 // STREAMINFO header, no body
	f.Add([]byte("fLaC\x80\xff\xff\xff"))                                                                                                 // last block, absurd length
	f.Add(append([]byte("ID3\x04\x00\x00\x00\x00\x00\x0a"), []byte("fLaC")...))                                                           // stray ID3 then truncated
	f.Add([]byte("OggS\x00\x02"))                                                                                                         // Ogg capture pattern, truncated header
	f.Add([]byte("OggS\x00\x02\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\xff"))                     // page header claiming a 255-byte body it lacks
	f.Add([]byte("RIFF\x04\x00\x00\x00WAVE"))                                                                                             // RIFF/WAVE, no chunks
	f.Add([]byte("RIFF\xff\xff\xff\xffWAVEdata\xff\xff\xff\xff"))                                                                         // absurd RIFF + data sizes
	f.Add([]byte("RIFF\x10\x00\x00\x00WAVELIST\x04\x00\x00\x00INFO"))                                                                     // empty INFO list
	f.Add([]byte("RF64\x04\x00\x00\x00WAVE"))                                                                                             // RF64, must be rejected, not panic
	f.Add([]byte("\x00\x00\x00\x10ftypM4A \x00\x00\x00\x00"))                                                                             // ftyp only, no moov
	f.Add([]byte("\x00\x00\x00\x08ftyp\x00\x00\x00\x08moov"))                                                                             // empty moov, no tracks
	f.Add([]byte("\x00\x00\x00\x08ftyp\x00\x00\x00\x01moov\xff\xff\xff\xff\xff\xff\xff\xff"))                                             // 64-bit atom, absurd size
	f.Add([]byte("\x00\x00\x00\x10ftypM4A \x00\x00\x00\x08moof"))                                                                         // fragmented: must reject, not panic
	f.Add([]byte("\x00\x00\x00\x10ftypM4A \x00\x00\x00\x00\x00\x00\x00\x14moov\x00\x00\x00\x0cchpl\x01\x00\x00\x00"))                     // chpl v1 header, count truncated
	f.Add([]byte("\x00\x00\x00\x10ftypM4A \x00\x00\x00\x00\x00\x00\x00\x11moov\x00\x00\x00\x09chpl\x00\x00\x00\x00\x05"))                 // chpl v0 declaring 5 chapters, none present
	f.Add([]byte("\x00\x00\x00\x1cftyp00000000000000000000\x00\x00\x00\x11moov\x00\x00\x00\x00udta\x00"))                                 // ftyp(28)+moov(17){udta zero-body}: a created tag must not append past the stray zero
	f.Add([]byte("\x1a\x45\xdf\xa3\x84\x42\x82\x81m"))                                                                                    // EBML magic + truncated DocType
	f.Add([]byte("\x1a\x45\xdf\xa3\xff"))                                                                                                 // EBML magic, unknown-size header
	f.Add([]byte("\x1a\x45\xdf\xa3\x80\x18\x53\x80\x67\xff"))                                                                             // empty EBML header + unknown-size Segment
	f.Add([]byte("\x1a\x45\xdf\xa3\x80\x18\x53\x80\x67\x88\x10\x43\xa7\x70\x84\x45\xb9\x81\xb6"))                                         // EBML + Segment{Chapters{EditionEntry{empty ChapterAtom}}}
	f.Add([]byte("FORM\x00\x00\x00\x04AIFF"))                                                                                             // AIFF, no chunks
	f.Add([]byte("FORM\xff\xff\xff\xffAIFCFVER\xff\xff\xff\xff"))                                                                         // absurd FORM + chunk sizes
	f.Add([]byte("FORM\x00\x00\x00\x12AIFFCOMM\x00\x00\x00\x06\x00\x02\x00\x00\x00\x01"))                                                 // truncated COMM (no 80-bit rate)
	f.Add([]byte("FORM\x00\x00\x00\x14AIFFSSND\x00\x00\x00\x08\x00\x00\x00\x00\x00\x00\x00\x00"))                                         // SSND-only, header but no frames
	f.Add([]byte("FORM\x00\x00\x00\x1eAIFCCOMM\x00\x00\x00\x12\x00\x02\x00\x00\x00\x01\x00\x10\x7f\xff\x80\x00\x00\x00\x00\x00\x00\x00")) // AIFF-C 18-byte COMM, 0x7FFF-exponent Inf/NaN rate decoded
	f.Add([]byte("FORM\x00\x00\x00\x0eAIFFANNO\x00\x00\x00\x06hello\x00"))                                                                // lone ANNO comment chunk
	f.Add([]byte{0xFF, 0xF1, 0x50, 0x40, 0x01, 0x00, 0xFC})                                                                               // valid ADTS header, frame_length 8 but only 7 bytes present (short payload)
	f.Add([]byte{0xFF, 0xF1, 0x50, 0x00, 0x00, 0x00, 0x00})                                                                               // ADTS sync but frame_length 0 (below header)
	f.Add(append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), 0xFF, 0xF1, 0x50, 0x40, 0x01, 0x5F, 0xFC))                                    // empty front ID3 then a bare ADTS frame
	// Regression seeds recovered from prior fuzz runs.
	f.Add([]byte("\x00\x00\x00\bftyp0000moov0")) // MP4: 8-byte ftyp box then a short moov tail
	f.Add([]byte("RIFF0000WAVE000000000"))       // WAV: RIFF/WAVE with ASCII-digit chunk sizes
	f.Add([]byte{})

	ctx := context.Background()
	f.Fuzz(func(t *testing.T, data []byte) {
		doc, err := wl.Parse(ctx, wl.BytesSource(data))
		if err != nil {
			return // rejecting malformed input is fine; panicking is not
		}

		// Accessors on a valid document must not panic.
		_ = doc.Fields()
		_ = doc.Properties()
		_ = doc.Pictures()
		_ = doc.Warnings()
		_ = doc.Inspect()

		// A no-op write must reproduce the exact input bytes. A read-only format may
		// refuse any plan, including a no-op, so accept that and skip the write
		// round-trip. The guard is scoped to non-writable formats so a writable format
		// that wrongly reports ErrUnsupportedFormat still fails here.
		plan, err := doc.Edit().Prepare()
		if err != nil {
			if errors.Is(err, waxerr.ErrUnsupportedFormat) && !doc.Format().Writable() {
				return
			}
			// A file the parser flagged as having no audio essence (WarnNoAudioFrames) is
			// refused by Editor.Prepare (H1, ErrInvalidData): it is a contradictory file the
			// library declines to rewrite, not a regression. Accept it and skip the write
			// round-trip (a no-audio seed has nothing to round-trip anyway).
			if errors.Is(err, waxerr.ErrInvalidData) && hasWarning(doc, wl.WarnNoAudioFrames) {
				return
			}
			t.Fatalf("prepare on a parsed doc failed: %v", err)
		}
		var out bytes.Buffer
		if _, _, err := plan.Execute(ctx, wl.WriteTo(&out, wl.BytesSource(data))); err != nil {
			t.Fatalf("no-op write failed: %v", err)
		}
		if plan.IsNoOp() && !bytes.Equal(out.Bytes(), data) {
			t.Fatalf("no-op write changed bytes: in=%d out=%d", len(data), out.Len())
		}

		// An edit on accepted input must round-trip and re-parse. A codec may
		// legitimately refuse to rewrite some shapes - a chained Ogg stream
		// (ErrChainedStream) or a non-page-aligned / oversized layout
		// (ErrInvalidData) - but any other error from a parsed document is a
		// regression, so fail rather than silently accepting it.
		plan2, err := doc.Edit().Set(tag.Title, "fuzz").Prepare()
		if err != nil {
			// A codec may refuse some shapes: a chained Ogg (ErrChainedStream), a
			// non-page-aligned/oversized layout (ErrInvalidData), an MP4 whose crafted
			// offsets would overflow a 32-bit table on a grow (ErrSizeTooLarge), or a
			// Matroska layout the writer does not handle - no reserved Void, a
			// position that would overflow its width, a Title with no Info element
			// (ErrUnsupportedTag).
			if errors.Is(err, waxerr.ErrChainedStream) || errors.Is(err, waxerr.ErrInvalidData) ||
				errors.Is(err, waxerr.ErrSizeTooLarge) || errors.Is(err, waxerr.ErrUnsupportedTag) {
				return
			}
			t.Fatalf("edit prepare failed: %v", err)
		}
		var out2 bytes.Buffer
		if _, _, err := plan2.Execute(ctx, wl.WriteTo(&out2, wl.BytesSource(data))); err != nil {
			t.Fatalf("edit write failed: %v", err)
		}
		if _, err := wl.Parse(ctx, wl.BytesSource(out2.Bytes())); err != nil {
			t.Fatalf("re-parse of edited output failed: %v", err)
		}

		// Chapter write: a chapter edit on an accepted MP4 rebuilds the QuickTime
		// chapter track; on an accepted Matroska it re-renders the Chapters element. A
		// crafted shape that parses must not panic that rewrite, and its output must
		// re-parse. Other formats do not write chapters.
		if doc.Format() == wl.FormatMP4 || doc.Format() == wl.FormatMatroska {
			cp, err := doc.Edit().SetChapters(
				wl.Chapter{Start: 0, End: time.Second, Title: "a"},
				wl.Chapter{Start: time.Second, Title: "b"},
			).Prepare()
			if err != nil {
				if errors.Is(err, waxerr.ErrInvalidData) || errors.Is(err, waxerr.ErrSizeTooLarge) ||
					errors.Is(err, waxerr.ErrUnsupportedTag) {
					return
				}
				t.Fatalf("chapter edit prepare failed: %v", err)
			}
			var cout bytes.Buffer
			if _, _, err := cp.Execute(ctx, wl.WriteTo(&cout, wl.BytesSource(data))); err != nil {
				t.Fatalf("chapter edit write failed: %v", err)
			}
			if _, err := wl.Parse(ctx, wl.BytesSource(cout.Bytes())); err != nil {
				t.Fatalf("re-parse of chapter-edited output failed: %v", err)
			}
		}
	})
}
