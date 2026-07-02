package waxlabel_test

import (
	"bytes"
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestNumberPairReadPathAgreesAcrossFormats is the cross-format agreement check for the
// track/disc normalization (M3): a slashed TRACKNUMBER stored natively must read back as the
// same (TrackNumber, TrackTotal) canonical pair on every text codec, so dump, copy, and diff
// agree on one file. Per-format tests miss this; the point is that the formats agree. FLAC
// exercises the vorbis/wav post-pass (tag.NormalizeNumberPairs), MP3 the ID3 emitNumTotal path,
// and Matroska the projectTag path, the three mechanisms M3 keeps in lockstep through the shared
// tag.NumberTotalSplit. A malformed pair ("abc/1", "1/2/3") stays verbatim on the number key
// everywhere.
//
// Two residuals are out of this table by design: AIFF maps no numeric key (its post-pass is a
// no-op), and MP4 stores a structural uint16 pair, so "04/09" reads back 4/9 there and a
// literal 0 or an out-of-uint16 value does not survive - MP4 is covered by its own codec tests.
func TestNumberPairReadPathAgreesAcrossFormats(t *testing.T) {
	formats := []struct {
		name  string
		build func(string) []byte
	}{
		{"flac", func(v string) []byte { return flacWithComments("TRACKNUMBER=" + v) }},
		{"mp3", func(v string) []byte { return append(id3v2(4, textFrame(4, "TRCK", v)), mp3Audio(t)...) }},
		{"matroska", func(v string) []byte {
			tags := mkEl(idTags, mkEl(idTag, concat(mkEl(idTargets, nil), mkSimple("PART_NUMBER", v))))
			return buildMatroska("matroska", "", tags)
		}},
	}
	for _, tc := range []struct {
		in       string
		num, tot []string
	}{
		{"4/9", []string{"4"}, []string{"9"}},
		{"04/09", []string{"04"}, []string{"09"}}, // leading zeros preserved on the text codecs
		{"0/12", []string{"0"}, []string{"12"}},   // literal 0 preserved
		{"/2", nil, []string{"2"}},                // empty number side: only the total survives
		{"3/", []string{"3"}, nil},                // empty total side: only the number
		{"abc/1", []string{"abc/1"}, nil},         // malformed: kept verbatim, no total composed
		{"1/2/3", []string{"1/2/3"}, nil},         // malformed: kept verbatim
	} {
		for _, f := range formats {
			doc := mustParseBytes(t, f.build(tc.in))
			gotNum, _ := doc.Get(tag.TrackNumber)
			gotTot, _ := doc.Get(tag.TrackTotal)
			if !slices.Equal(gotNum, tc.num) || !slices.Equal(gotTot, tc.tot) {
				t.Errorf("%s TRACKNUMBER=%q -> num=%v tot=%v, want num=%v tot=%v",
					f.name, tc.in, gotNum, gotTot, tc.num, tc.tot)
			}
		}
	}
}

// TestNumberPairReadPathExplicitTotalWins pins the both-present precedence rule: an explicit
// companion total wins over the total a slashed number would derive, matching tag.ParseNumPair
// and the editor. The FLAC read path splits the number but leaves the explicit TRACKTOTAL.
func TestNumberPairReadPathExplicitTotalWins(t *testing.T) {
	doc := mustParseBytes(t, flacWithComments("TRACKNUMBER=4/9", "TRACKTOTAL=20"))
	if n, _ := doc.Get(tag.TrackNumber); !slices.Equal(n, []string{"4"}) {
		t.Errorf("TrackNumber = %v, want [4]", n)
	}
	if tt, _ := doc.Get(tag.TrackTotal); !slices.Equal(tt, []string{"20"}) {
		t.Errorf("TrackTotal = %v, want [20] (explicit total wins over the slash's 9)", tt)
	}
}

// TestNumberPairReadPathByteIdenticalNoOp is the M3 "why this is safe" guarantee: normalizing
// "4/9" on read must not perturb the file. The native Vorbis comment stays verbatim, so a
// no-op edit stays a no-op and the bytes are byte-identical (the read-time split lives only in
// the canonical projection, not on disk).
func TestNumberPairReadPathByteIdenticalNoOp(t *testing.T) {
	src := flacWithComments("TRACKNUMBER=4/9")
	plan, err := mustParseBytes(t, src).Edit().Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() {
		t.Error("a no-op edit of a file with a slashed track number should stay a no-op")
	}
	if out := applyToBytes(t, src, plan); !bytes.Equal(out, src) {
		t.Errorf("no-op output differs from source (%d vs %d bytes); the native 4/9 must stay verbatim", len(out), len(src))
	}
}

// TestVorbisSlashPairEditRewritesComment guards against silent edit loss when a FLAC/Ogg
// stores the pair only as a slashed TRACKNUMBER: the read path splits "4/9" into
// TRACKNUMBER=4 + TRACKTOTAL=9, but the native comment is still "4/9". Editing either half
// must rewrite the slash comment from the split values (vorbis.Rebuild), or the preserved
// "4/9" would re-project and resurrect the edited-away value.
func TestVorbisSlashPairEditRewritesComment(t *testing.T) {
	// Clearing the derived total must stick, not reappear from the slash number.
	t.Run("clear total", func(t *testing.T) {
		src := flacWithComments("TRACKNUMBER=4/9")
		plan, err := mustParseBytes(t, src).Edit().Clear(tag.TrackTotal).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		re := mustParseBytes(t, applyToBytes(t, src, plan))
		if v, ok := re.Get(tag.TrackTotal); ok {
			t.Errorf("TrackTotal = %v after clear, want absent (must not resurface from 4/9)", v)
		}
		if n, _ := re.Get(tag.TrackNumber); !slices.Equal(n, []string{"4"}) {
			t.Errorf("TrackNumber = %v, want [4]", n)
		}
	})
	// Changing only the number must keep the derived total (it has no comment of its own).
	t.Run("set number keeps total", func(t *testing.T) {
		src := flacWithComments("TRACKNUMBER=4/9")
		plan, err := mustParseBytes(t, src).Edit().Set(tag.TrackNumber, "7").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		re := mustParseBytes(t, applyToBytes(t, src, plan))
		if n, _ := re.Get(tag.TrackNumber); !slices.Equal(n, []string{"7"}) {
			t.Errorf("TrackNumber = %v, want [7]", n)
		}
		if tot, _ := re.Get(tag.TrackTotal); !slices.Equal(tot, []string{"9"}) {
			t.Errorf("TrackTotal = %v, want [9] preserved when only the number changed", tot)
		}
	})
	// Changing the total to a new value writes that value, not the embedded 9.
	t.Run("set total", func(t *testing.T) {
		src := flacWithComments("TRACKNUMBER=4/9")
		plan, err := mustParseBytes(t, src).Edit().Set(tag.TrackTotal, "20").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		re := mustParseBytes(t, applyToBytes(t, src, plan))
		if tot, _ := re.Get(tag.TrackTotal); !slices.Equal(tot, []string{"20"}) {
			t.Errorf("TrackTotal = %v, want [20]", tot)
		}
		if n, _ := re.Get(tag.TrackNumber); !slices.Equal(n, []string{"4"}) {
			t.Errorf("TrackNumber = %v, want [4]", n)
		}
	})
	// An unrelated edit must leave the slash comment byte-for-byte untouched (minimal change).
	t.Run("unrelated edit preserves slash", func(t *testing.T) {
		src := flacWithComments("TRACKNUMBER=4/9")
		plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "New").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if out := applyToBytes(t, src, plan); !bytes.Contains(out, []byte("TRACKNUMBER=4/9")) {
			t.Error("an unrelated edit must preserve the native TRACKNUMBER=4/9 comment verbatim")
		}
	})
	// An explicit total comment placed before the slash number must not be duplicated when only
	// the number is edited (the slash block must not re-derive a total that has its own comment).
	t.Run("explicit total first, no duplicate", func(t *testing.T) {
		src := flacWithComments("TRACKTOTAL=9", "TRACKNUMBER=4/9")
		plan, err := mustParseBytes(t, src).Edit().Set(tag.TrackNumber, "7").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		re := mustParseBytes(t, applyToBytes(t, src, plan))
		if v, _ := re.Get(tag.TrackTotal); !slices.Equal(v, []string{"9"}) {
			t.Errorf("TrackTotal = %v, want [9] (no phantom duplicate)", v)
		}
	})
	// An untouched explicit total comment (using a non-canonical spelling, after an unrelated
	// comment) must be preserved verbatim in place - not relabeled to TRACKTOTAL or relocated.
	t.Run("untouched total preserved verbatim and in place", func(t *testing.T) {
		src := flacWithComments("TRACKNUMBER=4/9", "COMMENT=hi", "TOTALTRACKS=20")
		plan, err := mustParseBytes(t, src).Edit().Set(tag.TrackNumber, "5").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, src, plan)
		if !bytes.Contains(out, []byte("TOTALTRACKS=20")) {
			t.Errorf("untouched TOTALTRACKS=20 must be preserved verbatim; got %q", out)
		}
		if bytes.Contains(out, []byte("TRACKTOTAL")) {
			t.Errorf("untouched TOTALTRACKS must not be relabeled to TRACKTOTAL; got %q", out)
		}
	})
	// The DISCNUMBER branch behaves identically: an explicit DISCTOTAL first must not be
	// duplicated when the disc number is edited.
	t.Run("disc: explicit total first, no duplicate", func(t *testing.T) {
		src := flacWithComments("DISCTOTAL=2", "DISCNUMBER=1/2")
		plan, err := mustParseBytes(t, src).Edit().Set(tag.DiscNumber, "3").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		re := mustParseBytes(t, applyToBytes(t, src, plan))
		if n, _ := re.Get(tag.DiscNumber); !slices.Equal(n, []string{"3"}) {
			t.Errorf("DiscNumber = %v, want [3]", n)
		}
		if v, _ := re.Get(tag.DiscTotal); !slices.Equal(v, []string{"2"}) {
			t.Errorf("DiscTotal = %v, want [2] (no phantom duplicate)", v)
		}
	})
	// DISCNUMBER slash-only clear-total also sticks (the derived total has no comment).
	t.Run("disc: clear derived total", func(t *testing.T) {
		src := flacWithComments("DISCNUMBER=1/2")
		plan, err := mustParseBytes(t, src).Edit().Clear(tag.DiscTotal).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		re := mustParseBytes(t, applyToBytes(t, src, plan))
		if v, ok := re.Get(tag.DiscTotal); ok {
			t.Errorf("DiscTotal = %v after clear, want absent", v)
		}
		if n, _ := re.Get(tag.DiscNumber); !slices.Equal(n, []string{"1"}) {
			t.Errorf("DiscNumber = %v, want [1]", n)
		}
	})
}

// TestWAVSlashTrackNumberNoFalseFamilyConflict checks that a single IPRT="4/9", which normalizes
// to TrackNumber=4 + TrackTotal=9, does not read back as a family conflict. The RIFF family view
// must reflect the same split rather than compare the raw "4/9" against the normalized
// TrackNumber, which would mark the only INFO row a conflict and raise a spurious
// conflicting-families finding.
func TestWAVSlashTrackNumberNoFalseFamilyConflict(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"IPRT", "4/9"}), wavData(400))
	doc := mustParseBytes(t, data)
	if got := doc.Fields().TrackNumber; got != 4 {
		t.Fatalf("TrackNumber = %d, want 4", got)
	}
	for _, f := range doc.Lint() {
		if f.Code == "conflicting-families" {
			t.Errorf("spurious lint finding for a single normalized IPRT: %s", f)
		}
	}
	for _, f := range doc.Families() {
		if !f.Selected {
			t.Errorf("RIFF family %s=%v marked unselected (false conflict) for a single IPRT=4/9", f.Key, f.Values)
		}
	}
}
