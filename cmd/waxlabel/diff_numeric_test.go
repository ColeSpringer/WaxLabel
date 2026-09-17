package main

import (
	"encoding/json"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// hasTagChange reports whether a --json diff carries a change for key.
func hasTagChange(jd jsonDiff, key string) bool {
	for _, t := range jd.Tags {
		if t.Key == key {
			return true
		}
	}
	return false
}

// TestDiffNumericFoldScopedToMP4: numeric fold only when MP4 present; text-to-text keeps 01 vs 1.
func TestDiffNumericFoldScopedToMP4(t *testing.T) {
	t.Parallel()

	// Text formats store 01 vs 1 verbatim; diff must report it.
	t.Run("text to text reports leading-zero difference", func(t *testing.T) {
		t.Parallel()
		flac := buildTransferSource(t, notagsFLAC, func(e *wl.Editor) *wl.Editor {
			return e.Set(tag.TrackNumber, "01")
		})
		mp3 := buildTransferSource(t, notagsMP3, func(e *wl.Editor) *wl.Editor {
			return e.Set(tag.TrackNumber, "1")
		})

		if _, _, code := runCLI(t, "diff", flac, mp3); code != 1 {
			t.Errorf("diff exit = %d, want 1 (files differ)", code)
		}
		out, _, code := runCLI(t, "--json", "diff", flac, mp3)
		if code > 1 {
			t.Fatalf("diff --json exit = %d (>1 is an error)\n%s", code, out)
		}
		var jd jsonDiff
		if err := json.Unmarshal([]byte(out), &jd); err != nil {
			t.Fatalf("invalid diff JSON: %v\n%s", err, out)
		}
		if jd.Identical {
			t.Error("diff reported identical; 01 vs 1 stored verbatim in two text formats is a real difference")
		}
		if !hasTagChange(jd, "TRACKNUMBER") {
			t.Errorf("diff tags = %+v, want a TRACKNUMBER change", jd.Tags)
		}
	})

	// MP4 stik canonicalizes 01 to 1; copy carried and diff must agree.
	t.Run("mp4 mediatype fold agrees with copy carried", func(t *testing.T) {
		t.Parallel()
		src := buildTransferSource(t, notagsFLAC, func(e *wl.Editor) *wl.Editor {
			return e.Set(tag.MediaType, "01")
		})
		dst := copyFixture(t, notagsM4A)
		out, _, code := runCLI(t, "--json", "copy", src, dst)
		if code != 0 {
			t.Fatalf("copy exit = %d, want 0\n%s", code, out)
		}
		var jc jsonCopy
		if err := json.Unmarshal([]byte(out), &jc); err != nil {
			t.Fatalf("copy JSON: %v\n%s", err, out)
		}
		if it := fieldItem(t, jc, "MEDIATYPE"); it.Disposition != "carried" {
			t.Errorf("MEDIATYPE copy disposition = %q, want carried; reason=%q", it.Disposition, it.Reason)
		}

		dout, _, dcode := runCLI(t, "--json", "diff", src, dst)
		if dcode > 1 {
			t.Fatalf("diff --json exit = %d (>1 is an error)\n%s", dcode, dout)
		}
		var jd jsonDiff
		if err := json.Unmarshal([]byte(dout), &jd); err != nil {
			t.Fatalf("invalid diff JSON: %v\n%s", err, dout)
		}
		if hasTagChange(jd, "MEDIATYPE") {
			t.Errorf("diff reports a MEDIATYPE change; an MP4 stik atom canonicalizes 01 to 1, so copy (carried) and diff must agree. tags=%+v", jd.Tags)
		}
	})
}
