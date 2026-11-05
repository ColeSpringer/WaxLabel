package waxlabel_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/waxerr"
)

// A chunk offset addresses media inside an mdat, so it always sits after the metadata region a
// rewrite replaces. An offset that instead points INTO that region can be carried below the start
// of the file when the region shrinks; a chapter clear drops a whole text track out of moov.

// mp4Co64 builds a 64-bit chunk-offset table.
func mp4Co64(entries ...uint64) []byte {
	body := slices.Concat([]byte{0, 0, 0, 0}, testfile.BE32(len(entries)))
	for _, e := range entries {
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, e)
		body = append(body, b...)
	}
	return testfile.MP4Atom("co64", body)
}

// mp4AudioTrakChapCo64 is testfile.MP4AudioTrakChap with a co64 in place of the stco, so a shrinking
// rewrite covers the 64-bit entry path, where a wrap is not caught by the stco table's own
// 4 GiB ceiling.
func mp4AudioTrakChapCo64(chapTrackID int, chunk uint64) []byte {
	tkhd := testfile.MP4Atom("tkhd", slices.Concat([]byte{0, 0, 0, 0}, make([]byte, 8), testfile.BE32(1), make([]byte, 4)))
	tref := testfile.MP4Atom("tref", testfile.MP4Atom("chap", testfile.BE32(chapTrackID)))
	stbl := testfile.MP4Atom("stbl", slices.Concat(testfile.MP4StsdAudio(), mp4Co64(chunk)))
	minf := testfile.MP4Atom("minf", stbl)
	mdia := testfile.MP4Atom("mdia", slices.Concat(testfile.MP4HdlrSoun(), testfile.MP4Mdhd(44100), minf))
	return testfile.MP4Atom("trak", slices.Concat(tkhd, tref, mdia))
}

func TestMP4ShrinkingRewriteRefusesUnderflowingOffset(t *testing.T) {
	// 300 chapters make the text trak several KiB, so clearing them shrinks moov by far
	// more than the crafted chunk offset's distance from the insertion point.
	const n = 300
	starts := make([]int, n)
	titles := make([]string, n)
	for i := range n {
		starts[i] = i * 1000
		titles[i] = fmt.Sprintf("Chapter number %d with padding text", i)
	}
	// Above moov.offset (so the shift applies) but far below the shrink's magnitude.
	const chunk = 200

	kids := slices.Concat(
		mp4AudioTrakChapCo64(2, chunk),
		testfile.MP4TextTrak(2, 1000, starts, titles, 99999),
	)
	data := slices.Concat(testfile.MP4Ftyp(), testfile.MP4Atom("moov", kids),
		testfile.MP4Atom("mdat", slices.Concat(bytes.Repeat([]byte{0xA7}, 120), testfile.MP4SamplesBlob(titles))))

	_, err := mustParseBytes(t, data).Edit().SetChapters().Prepare()
	if !errors.Is(err, waxerr.ErrInvalidData) {
		t.Fatalf("clear-chapters error = %v, want ErrInvalidData (the offset cannot be shifted)", err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("co64")) {
		t.Errorf("error should name the offending box: %v", err)
	}
}
