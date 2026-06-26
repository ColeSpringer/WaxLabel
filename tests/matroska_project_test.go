package waxlabel_test

import (
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// albumTag wraps SimpleTags in an album-scope (TargetTypeValue 50) Tag group.
func albumTag(simples ...[]byte) []byte {
	return mkEl(idTag, concat(mkEl(idTargets, mkUint(idTgtTypeVal, 50)), concat(simples...)))
}

// trackTag wraps SimpleTags in a track-scope (TargetTypeValue 50 + TrackUID) Tag group.
func trackTag(simples ...[]byte) []byte {
	return mkEl(idTag, concat(
		mkEl(idTargets, concat(mkUint(idTgtTypeVal, 50), mkUint(idTagTrackUID, 7))),
		concat(simples...)))
}

// TestMatroskaProjectFaithful checks that the canonical Tags view projects each scope
// without fold-deduping across all scopes. Intra-scope duplicates and case or
// whitespace variants survive, cross-scope echoes collapse to one canonical value, and
// distinct cross-scope values still surface and are reported by the families view.
func TestMatroskaProjectFaithful(t *testing.T) {
	t.Run("intra-scope duplicates preserved", func(t *testing.T) {
		data := buildMatroska("matroska", "T", mkEl(idTags, albumTag(
			mkSimple("ARTIST", "Solo"), mkSimple("ARTIST", "Solo"), mkSimple("ARTIST", "Band"))))
		got, _ := mustParseBytes(t, data).Get(tag.Artist)
		if want := []string{"Solo", "Solo", "Band"}; !slices.Equal(got, want) {
			t.Errorf("ARTIST = %v, want %v (intra-scope dup must survive, matching FLAC)", got, want)
		}
	})

	t.Run("case and whitespace variants preserved", func(t *testing.T) {
		data := buildMatroska("matroska", "T", mkEl(idTags, albumTag(
			mkSimple("ARTIST", "Solo"), mkSimple("ARTIST", "solo"), mkSimple("ARTIST", "Solo "))))
		got, _ := mustParseBytes(t, data).Get(tag.Artist)
		if want := []string{"Solo", "solo", "Solo "}; !slices.Equal(got, want) {
			t.Errorf("ARTIST = %v, want %v (case/whitespace variants must survive verbatim)", got, want)
		}
	})

	t.Run("cross-scope echo suppressed", func(t *testing.T) {
		data := buildMatroska("matroska", "T", mkEl(idTags, concat(
			albumTag(mkSimple("ARTIST", "AA")),
			trackTag(mkSimple("ARTIST", "AA")))))
		re := mustParseBytes(t, data)
		got, _ := re.Get(tag.Artist)
		if want := []string{"AA"}; !slices.Equal(got, want) {
			t.Errorf("ARTIST = %v, want %v (the album/track echo is one canonical value)", got, want)
		}
		if hasWarning(re, wl.WarnConflictingFamilies) {
			t.Error("a same-value cross-scope echo must not be a conflict")
		}
	})

	t.Run("cross-scope distinct both kept and flagged", func(t *testing.T) {
		data := buildMatroska("matroska", "T", mkEl(idTags, concat(
			albumTag(mkSimple("ARTIST", "AA")),
			trackTag(mkSimple("ARTIST", "BB")))))
		re := mustParseBytes(t, data)
		got, _ := re.Get(tag.Artist)
		if want := []string{"AA", "BB"}; !slices.Equal(got, want) {
			t.Errorf("ARTIST = %v, want %v (album primary, then the distinct track value)", got, want)
		}
		if !hasWarning(re, wl.WarnConflictingFamilies) {
			t.Error("distinct values across scopes should flag a conflicting-families warning")
		}
	})
}

// TestMatroskaProjectTitleAuthoritative covers the TITLE special case. Info.Title is
// authoritative, so an album-scope TITLE SimpleTag that echoes it does not duplicate
// the canonical title. A genuinely different cross-scope TITLE still surfaces as a
// conflict.
func TestMatroskaProjectTitleAuthoritative(t *testing.T) {
	t.Run("Info.Title plus echoing album TITLE = one canonical title", func(t *testing.T) {
		data := buildMatroska("matroska", "MyTitle", mkEl(idTags, albumTag(mkSimple("TITLE", "MyTitle"))))
		re := mustParseBytes(t, data)
		got, _ := re.Get(tag.Title)
		if want := []string{"MyTitle"}; !slices.Equal(got, want) {
			t.Errorf("TITLE = %v, want %v (Info.Title authoritative; the echoing SimpleTag must not duplicate it)", got, want)
		}
		if hasWarning(re, wl.WarnConflictingFamilies) {
			t.Error("an echoing album TITLE is not a conflict")
		}
	})

	t.Run("Info.Title plus different track TITLE conflicts without a flat duplicate", func(t *testing.T) {
		data := buildMatroska("matroska", "MyTitle", mkEl(idTags, trackTag(mkSimple("TITLE", "Other"))))
		re := mustParseBytes(t, data)
		got, _ := re.Get(tag.Title)
		if want := []string{"MyTitle", "Other"}; !slices.Equal(got, want) {
			t.Errorf("TITLE = %v, want %v (authoritative Info.Title first, then the distinct value, no repeat)", got, want)
		}
		if !hasWarning(re, wl.WarnConflictingFamilies) {
			t.Error("a different cross-scope TITLE should flag a conflicting-families warning")
		}
	})
}

// TestMatroskaWriteDupEchoPreservesMultiplicity covers the read/write symmetry around
// duplicated album values and narrower-scope echoes. The reader surfaces the album
// duplicates and suppresses the echo, so an unrelated edit must not subtract the album
// copies as covered by that single echo. Distinct cross-scope values must still survive
// without duplication.
func TestMatroskaWriteDupEchoPreservesMultiplicity(t *testing.T) {
	t.Run("album dup plus narrower echo survives an unrelated edit", func(t *testing.T) {
		data := buildMatroska("matroska", "T", mkEl(idTags, concat(
			albumTag(mkSimple("ARTIST", "Solo"), mkSimple("ARTIST", "Solo")),
			trackTag(mkSimple("ARTIST", "Solo")))))
		if got, _ := mustParseBytes(t, data).Get(tag.Artist); !slices.Equal(got, []string{"Solo", "Solo"}) {
			t.Fatalf("read ARTIST = %v, want [Solo Solo]", got)
		}
		out, outDoc := saveMatroska(t, data, mustParseBytes(t, data).Edit().Set(tag.Album, "X"))
		if got, _ := mustParseBytes(t, out).Get(tag.Artist); !slices.Equal(got, []string{"Solo", "Solo"}) {
			t.Errorf("after an unrelated edit, reparsed ARTIST = %v, want [Solo Solo] (the album dup must not be subtracted as a covered echo)", got)
		}
		// result == fresh parse: the returned document must agree with the bytes.
		res, _ := outDoc.Get(tag.Artist)
		re, _ := mustParseBytes(t, out).Get(tag.Artist)
		if !slices.Equal(res, re) {
			t.Errorf("result doc ARTIST %v != reparse %v", res, re)
		}
	})

	t.Run("distinct cross-scope value still both kept after an edit", func(t *testing.T) {
		data := buildMatroska("matroska", "T", mkEl(idTags, concat(
			albumTag(mkSimple("ARTIST", "AA")),
			trackTag(mkSimple("ARTIST", "BB")))))
		out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().Set(tag.Album, "X"))
		if got, _ := mustParseBytes(t, out).Get(tag.Artist); !slices.Equal(got, []string{"AA", "BB"}) {
			t.Errorf("after an edit, reparsed ARTIST = %v, want [AA BB] (album primary then the distinct track value)", got)
		}
	})

	t.Run("two album-scope groups do not grow or shrink on an unrelated edit", func(t *testing.T) {
		// Both groups are at the album scope, so the reader does not suppress either - the
		// canonical owns both. The sync must carry exactly two across the two groups: not
		// drop both (set-based subtract) nor leave both groups full while re-emitting the
		// canonical (the echo carve-out misfiring on a same-scope group), which would grow
		// by one on every edit.
		data := buildMatroska("matroska", "T", mkEl(idTags, concat(
			albumTag(mkSimple("ARTIST", "Solo")),
			albumTag(mkSimple("ARTIST", "Solo")))))
		if got, _ := mustParseBytes(t, data).Get(tag.Artist); !slices.Equal(got, []string{"Solo", "Solo"}) {
			t.Fatalf("read ARTIST = %v, want [Solo Solo]", got)
		}
		out := data
		// Repeat the edit a few times: a compounding bug grows the count each pass.
		for i := 0; i < 3; i++ {
			out, _ = saveMatroska(t, out, mustParseBytes(t, out).Edit().Set(tag.Album, "X"))
			if got, _ := mustParseBytes(t, out).Get(tag.Artist); !slices.Equal(got, []string{"Solo", "Solo"}) {
				t.Fatalf("pass %d: ARTIST = %v, want a stable [Solo Solo]", i+1, got)
			}
		}
	})
}

// TestMatroskaWriteTitleOnlyInScopedTag: a title that lives only in an album-scope
// TITLE SimpleTag (the Info element has no Title child) must survive an unrelated edit.
// A Tags re-render drops the managed TITLE SimpleTag, so the writer must migrate the
// title into the authoritative Info.Title rather than silently dropping it.
func TestMatroskaWriteTitleOnlyInScopedTag(t *testing.T) {
	// An Info element present but with no Title child, plus an album-scope TITLE SimpleTag.
	info := mkEl(idInfo, mkEl(idDuration, make([]byte, 8)))
	tags := mkEl(idTags, albumTag(mkSimple("TITLE", "MyTitle"), mkSimple("ARTIST", "AA")))
	seg := concat(info, mkAudioCluster(), tags)
	data := concat(mkEl(idEBML, mkStr(idDocType, "matroska")), mkEl(idSegment, seg))

	if got, _ := mustParseBytes(t, data).Get(tag.Title); !slices.Equal(got, []string{"MyTitle"}) {
		t.Fatalf("read TITLE = %v, want [MyTitle]", got)
	}
	// An unrelated edit (changing ARTIST) re-renders Tags, dropping the managed TITLE tag.
	out, outDoc := saveMatroska(t, data, mustParseBytes(t, data).Edit().Set(tag.Artist, "BB"))
	if got, _ := mustParseBytes(t, out).Get(tag.Title); !slices.Equal(got, []string{"MyTitle"}) {
		t.Errorf("after an unrelated edit, reparsed TITLE = %v, want [MyTitle] (migrated to Info.Title, not lost)", got)
	}
	if got, _ := outDoc.Get(tag.Title); !slices.Equal(got, []string{"MyTitle"}) {
		t.Errorf("result doc TITLE = %v, want [MyTitle] (result must equal a fresh parse)", got)
	}
}

// TestMatroskaDuplicateSurvivesWriter proves the duplicate round-trips through the
// writer's renderTags, not only the reader: a forced (non-title) edit re-renders the
// canonical ARTIST set, and a fresh parse of the output still reads all three values.
// A no-op edit on the same file stays a no-op (the canonical view is byte-stable).
func TestMatroskaDuplicateSurvivesWriter(t *testing.T) {
	data := buildMatroska("matroska", "T", mkEl(idTags, albumTag(
		mkSimple("ARTIST", "Solo"), mkSimple("ARTIST", "Solo"), mkSimple("ARTIST", "Band"))))

	out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().Set(tag.Album, "X"))
	if got, _ := mustParseBytes(t, out).Get(tag.Artist); !slices.Equal(got, []string{"Solo", "Solo", "Band"}) {
		t.Errorf("ARTIST after a forced write = %v, want [Solo Solo Band] (the dup must survive renderTags)", got)
	}

	plan, err := mustParseBytes(t, data).Edit().Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !plan.IsNoOp() {
		t.Error("a no-op edit on a duplicate-bearing file must stay a no-op (no spurious rewrite)")
	}
}

// TestMatroskaDuplicateTransferCount: copying a duplicate-bearing Matroska ARTIST into
// FLAC (which writes every value) reports all three carried - the count derives from
// the now-faithful src.Tags, so the old "0 dropped / fewer carried" misreport is gone.
func TestMatroskaDuplicateTransferCount(t *testing.T) {
	data := buildMatroska("matroska", "T", mkEl(idTags, albumTag(
		mkSimple("ARTIST", "Solo"), mkSimple("ARTIST", "Solo"), mkSimple("ARTIST", "Band"))))
	report, err := mustParseBytes(t, data).PlanTransfer(wl.FormatFLAC)
	if err != nil {
		t.Fatalf("PlanTransfer: %v", err)
	}
	var found bool
	for _, it := range report.Items {
		if it.Kind == wl.TransferField && it.Key == tag.Artist {
			found = true
			if it.Count != 3 {
				t.Errorf("ARTIST transfer count = %d, want 3", it.Count)
			}
			if it.Disposition != wl.Carried {
				t.Errorf("ARTIST disposition = %v, want carried (FLAC writes every value)", it.Disposition)
			}
		}
	}
	if !found {
		t.Error("no ARTIST transfer item found")
	}
}
