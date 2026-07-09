package wav

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
)

// nonLyricSYLTFrame builds a SYLT frame body with a non-lyric content type (2 = trivia), which the
// projection skips as not-lyrics but the rebuild preserves verbatim. Layout:
// encoding(1) language(3) timestamp-format(1) content-type(1) descriptor(NUL) [text(NUL) ts(4)].
func nonLyricSYLTFrame() id3.Frame {
	body := []byte{0x00}                        // Latin-1
	body = append(body, "eng"...)               // language
	body = append(body, 0x02)                   // timestamp format 2 = milliseconds
	body = append(body, 0x02)                   // content type 2 = trivia (non-lyric)
	body = append(body, 0x00)                   // empty descriptor terminator
	body = append(body, "note"...)              // one line's text
	body = append(body, 0x00)                   // text terminator
	body = append(body, 0x00, 0x00, 0x00, 0x64) // timestamp 100ms
	return id3.Frame{ID: "SYLT", Body: body}
}

// TestWAVSyncedLyricsOpCountsModelSetsNotSYLTFrames checks that the "synced lyrics: N" op counts
// modeled lyric sets (len(edited.SyncedLyrics)), not raw SYLT frames. A non-lyric SYLT the file
// already carries is skipped on read but preserved verbatim into the written id3 chunk, so counting
// raw SYLT frames would report 2 for a single-set lyrics edit; the model count reports 1.
func TestWAVSyncedLyricsOpCountsModelSetsNotSYLTFrames(t *testing.T) {
	ctx := context.Background()

	// A WAV carrying an id3 chunk whose only SYLT is non-lyric.
	id3Body := id3.Render(4, []id3.Frame{nonLyricSYLTFrame()}, 0)
	chunks := bytes.Join([][]byte{
		wavFmtChunk(),
		wavChunk("data", bytes.Repeat([]byte{0x11, 0x22}, 16)),
		wavChunk("id3 ", id3Body),
	}, nil)
	src := riffWrap(chunks, nil, nil)

	base, err := parse(ctx, core.BytesSource(src), core.DefaultParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(base.SyncedLyrics) != 0 {
		t.Fatalf("non-lyric SYLT must not project as lyrics; got %d sets", len(base.SyncedLyrics))
	}

	// Add one lyric set: base has 0 modeled sets, edited has 1, so the op reads "synced lyrics: 1".
	edited := base.Clone()
	edited.SyncedLyrics = []core.SyncedLyrics{{
		Language: "eng",
		Lines:    []core.SyncedLine{{Time: time.Second, Text: "Hi"}},
	}}

	plan, err := Codec{}.Plan(ctx, base, edited, core.DefaultWriteOptions())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !slices.Contains(plan.Report.Operations, "synced lyrics: 1") {
		t.Errorf("operations = %v, want a 'synced lyrics: 1' op (model set count)", plan.Report.Operations)
	}
	if slices.Contains(plan.Report.Operations, "synced lyrics: 2") {
		t.Error("operations counted the preserved non-lyric SYLT frame (2); it must count only modeled sets")
	}
}
