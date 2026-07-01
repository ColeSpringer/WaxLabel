package mp4

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
)

// mkStsdPayload builds a minimal stsd payload (the bytes after the stsd box header)
// with one audio sample entry at the given sound-sample-description version. It plants
// sentinel bytes at the v0/v1 geometry offsets so a v2-unaware reader would surface
// them as channels / sample-size / sample-rate.
func mkStsdPayload(soundVersion uint16) []byte {
	b := make([]byte, 44)
	binary.BigEndian.PutUint32(b[4:8], 1) // entry_count
	copy(b[12:16], "mp4a")                // format four-cc
	binary.BigEndian.PutUint16(b[24:26], soundVersion)
	binary.BigEndian.PutUint16(b[32:34], 0xFFFF) // channels offset (sentinel)
	binary.BigEndian.PutUint16(b[34:36], 0xFFFF) // sample-size offset
	binary.BigEndian.PutUint16(b[40:42], 0xFFFF) // sample-rate integer offset
	return b
}

// parseStsdPayload wraps a payload in an 8-byte stsd box header and runs parseStsd.
func parseStsdPayload(t *testing.T, payload []byte) *doc {
	t.Helper()
	raw := append([]byte{0, 0, 0, 0, 's', 't', 's', 'd'}, payload...)
	binary.BigEndian.PutUint32(raw[0:4], uint32(len(raw)))
	d := &doc{}
	n := node{name: [4]byte{'s', 't', 's', 'd'}, offset: 0, headerLen: 8, size: int64(len(raw))}
	parseStsd(core.BytesSource(raw), n, d, 1<<20)
	return d
}

// TestParseStsdV2SkipsGeometry checks that a v2+ AudioSampleEntry does not use the fixed
// v0/v1 geometry offsets. Reading those offsets would feed bogus channels and sample rate
// into the essence-digest salt. The codec 4CC is still read, and a v0 entry still reads
// the fixed-offset geometry.
func TestParseStsdV2SkipsGeometry(t *testing.T) {
	d := parseStsdPayload(t, mkStsdPayload(2))
	if got := string(d.cfg.codec[:]); got != "mp4a" {
		t.Errorf("codec = %q, want mp4a", got)
	}
	if d.cfg.channels != 0 || d.cfg.sampleSize != 0 || d.cfg.sampleRate != 0 {
		t.Errorf("v2 entry surfaced bogus geometry: channels=%d sampleSize=%d sampleRate=%d (want all 0)",
			d.cfg.channels, d.cfg.sampleSize, d.cfg.sampleRate)
	}
	// The salt is deterministic: codec four-cc followed by zero geometry.
	_, salt := Codec{}.EssenceExtent(&core.Media{Native: d})
	if want := append([]byte("mp4a"), make([]byte, 8)...); !bytes.Equal(salt, want) {
		t.Errorf("essence salt = % x, want % x (deterministic, no bogus geometry)", salt, want)
	}

	// A v0 entry still reads the geometry from the fixed offsets.
	d0 := parseStsdPayload(t, mkStsdPayload(0))
	if d0.cfg.channels != 0xFFFF || d0.cfg.sampleSize != 0xFFFF || d0.cfg.sampleRate != 0xFFFF {
		t.Errorf("v0 entry should read fixed-offset geometry: channels=%d sampleSize=%d sampleRate=%d",
			d0.cfg.channels, d0.cfg.sampleSize, d0.cfg.sampleRate)
	}
}

// TestCapabilitiesPictureMIMEsCloned guards against the public Capabilities aliasing the
// package coverMIMEs backing array: a caller mutating the returned slice must not corrupt
// the write-time cover guard, which reads the package var via coverMIMESupported.
func TestCapabilitiesPictureMIMEsCloned(t *testing.T) {
	mimes := Codec{}.Capabilities(nil, core.WriteOptions{}).Pictures.PictureMIMEs
	if len(mimes) == 0 {
		t.Fatal("expected MP4 PictureMIMEs to be populated")
	}
	mimes[0] = "image/evil" // a caller mutating the slice it was handed
	if !coverMIMESupported("image/jpeg") {
		t.Error("mutating the returned PictureMIMEs corrupted coverMIMESupported (slice aliased the package var)")
	}
	if coverMIMESupported("image/evil") {
		t.Error("coverMIMESupported accepted a value injected through the returned slice")
	}
}

// TestEssenceMdatsTrimsFrontChapters checks the front-only mdat trim used by MP4 essence
// digests. Front-loaded chapter samples are excluded, chapter-only mdats are dropped, and
// chapter samples after the first audio chunk stay included.
func TestEssenceMdatsTrimsFrontChapters(t *testing.T) {
	// chapTrak spans [200,300); a chunk-offset table whose atom offset is in that range
	// belongs to the chapter text track.
	chapTrak := &atomRef{offset: 200, size: 100}
	chapTable := func(entries ...uint64) offsetTable { return offsetTable{offset: 210, entries: entries} }
	audioTable := func(entries ...uint64) offsetTable { return offsetTable{offset: 400, entries: entries} }

	for _, c := range []struct {
		name   string
		mdats  [][2]int64
		tables []offsetTable
		want   [][2]int64
	}{
		{
			// Shared mdat [1000,1500): chapter text at the front (1000), audio after (1100).
			name:   "front chapter trimmed off a shared mdat",
			mdats:  [][2]int64{{1000, 500}},
			tables: []offsetTable{chapTable(1000), audioTable(1100)},
			want:   [][2]int64{{1100, 1500}},
		},
		{
			// Audio first: nothing to trim.
			name:   "do no harm when audio leads the mdat",
			mdats:  [][2]int64{{1000, 500}},
			tables: []offsetTable{audioTable(1000), chapTable(2050)}, // chapter chunk is elsewhere
			want:   [][2]int64{{1000, 1500}},
		},
		{
			// Audio at 1000, a chapter chunk interleaved after it: stays in the digest.
			name:   "chapter after audio stays included",
			mdats:  [][2]int64{{1000, 500}},
			tables: []offsetTable{audioTable(1000), chapTable(1400)},
			want:   [][2]int64{{1000, 1500}},
		},
		{
			// A second mdat holding only chapter samples is dropped.
			name:   "chapter-only mdat dropped",
			mdats:  [][2]int64{{1000, 500}, {2000, 200}},
			tables: []offsetTable{audioTable(1000), chapTable(2050)},
			want:   [][2]int64{{1000, 1500}},
		},
		{
			// No non-chapter table at all: nothing to classify, keep the mdat whole.
			name:   "no non-chapter table keeps the mdat whole",
			mdats:  [][2]int64{{1000, 500}},
			tables: []offsetTable{chapTable(1000)},
			want:   [][2]int64{{1000, 1500}},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := &doc{chapTrak: chapTrak, mdats: c.mdats, offTables: c.tables}
			got := essenceMdats(d)
			if len(got) != len(c.want) {
				t.Fatalf("essenceMdats = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("range %d = %v, want %v (full: %v)", i, got[i], c.want[i], got)
				}
			}
		})
	}
}

// TestElstSegmentDurationSum covers the edit-list playable-duration parser used for the
// audio track's own trimmed duration.
func TestElstSegmentDurationSum(t *testing.T) {
	be := binary.BigEndian
	// v0: two entries, segment_durations 1000 + 500.
	v0 := make([]byte, 8+2*12)
	be.PutUint32(v0[4:8], 2)
	be.PutUint32(v0[8:12], 1000)
	be.PutUint32(v0[20:24], 500)
	if got := elstSegmentDurationSum(v0); got != 1500 {
		t.Errorf("v0 sum = %d, want 1500", got)
	}
	// v1: one entry with a 64-bit segment_duration beyond uint32.
	v1 := make([]byte, 8+20)
	v1[0] = 1
	be.PutUint32(v1[4:8], 1)
	be.PutUint64(v1[8:16], 9_000_000_000)
	if got := elstSegmentDurationSum(v1); got != 9_000_000_000 {
		t.Errorf("v1 sum = %d, want 9000000000", got)
	}
	// Truncated payload is bounds-checked (no panic, no over-read).
	if got := elstSegmentDurationSum([]byte{0, 0, 0, 0, 0, 0, 0, 9}); got != 0 {
		t.Errorf("truncated sum = %d, want 0", got)
	}
	// A hostile entry_count (0xFFFFFFFF) over a tiny buffer must be rejected by the up-front
	// bound rather than looped ~4.3 billion times: it returns 0 immediately, for v0 and v1.
	hostileV0 := make([]byte, 8)
	be.PutUint32(hostileV0[4:8], 0xFFFFFFFF)
	if got := elstSegmentDurationSum(hostileV0); got != 0 {
		t.Errorf("hostile v0 count sum = %d, want 0", got)
	}
	hostileV1 := make([]byte, 8)
	hostileV1[0] = 1
	be.PutUint32(hostileV1[4:8], 0xFFFFFFFF)
	if got := elstSegmentDurationSum(hostileV1); got != 0 {
		t.Errorf("hostile v1 count sum = %d, want 0", got)
	}
}

// TestBoundedCount pins the shared count-loop guard every MP4 table decoder routes through:
// entries that exactly fit are accepted, one entry past the buffer is rejected, and a hostile
// uint32-max count stays within int64 (no overflow) and is rejected.
func TestBoundedCount(t *testing.T) {
	// 2 entries of 12 bytes after an 8-byte header need 32 bytes; exactly 32 fits.
	if !boundedCount(2, 8, 12, 32) {
		t.Error("boundedCount(2, 8, 12, 32) = false, want true (exact fit)")
	}
	// One byte short must be rejected.
	if boundedCount(2, 8, 12, 31) {
		t.Error("boundedCount(2, 8, 12, 31) = true, want false (one past)")
	}
	// A hostile uint32-max count over a tiny buffer: header+count*width (~8.6e10) stays well
	// within int64, so the arithmetic does not overflow and the guard rejects it.
	if boundedCount(0xFFFFFFFF, 8, 20, 8) {
		t.Error("boundedCount(0xFFFFFFFF, 8, 20, 8) = true, want false (hostile count)")
	}
}
