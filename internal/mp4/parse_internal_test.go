package mp4

import (
	"bytes"
	"encoding/binary"
	"math"
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

// mkAlacStsdPayload builds an stsd payload with one v0 'alac' sample entry whose
// fixed sample_size field carries the conventional 16 while the nested magic
// cookie declares cookieDepth bits.
func mkAlacStsdPayload(cookieDepth byte) []byte {
	return mkAlacStsdPayloadRates(cookieDepth, 44100, 44100, 2)
}

// mkAlacStsdPayloadRates builds the same v0 'alac' entry with the sample entry's own
// 16.16 rate field and the magic cookie's rate and channel count chosen independently,
// so a hi-res cookie can disagree with a rate field that cannot hold it.
func mkAlacStsdPayloadRates(cookieDepth byte, entryRate, cookieRate uint32, cookieChannels byte) []byte {
	b := make([]byte, 8+36+36)
	be := binary.BigEndian
	be.PutUint32(b[4:8], 1)      // entry_count
	be.PutUint32(b[8:12], 36+36) // entry size: fixed fields + cookie box
	copy(b[12:16], "alac")
	be.PutUint16(b[22:24], 1)             // data_reference_index
	be.PutUint16(b[32:34], 2)             // channels
	be.PutUint16(b[34:36], 16)            // sample_size, pinned at 16 by convention
	be.PutUint32(b[40:44], entryRate<<16) // sample_rate 16.16
	be.PutUint32(b[44:48], 36)            // cookie box size
	copy(b[48:52], "alac")                // cookie box type
	be.PutUint32(b[56:60], 4096)          // frameLength
	b[61] = cookieDepth                   // bitDepth
	b[62], b[63], b[64] = 40, 10, 14      // pb, mb, kb
	b[65] = cookieChannels                // numChannels
	be.PutUint16(b[66:68], 255)           // maxRun
	be.PutUint32(b[76:80], cookieRate)    // sampleRate
	return b
}

// mkStreamInfo builds a 34-byte FLAC STREAMINFO body with a recognizable MD5 and fixed
// block-size bounds.
func mkStreamInfo(rate, channels, depth int, total uint64) []byte {
	body := make([]byte, 34)
	be := binary.BigEndian
	be.PutUint16(body[0:2], 4096) // min block size
	be.PutUint16(body[2:4], 4096) // max block size
	body[10] = byte(rate >> 12)
	body[11] = byte(rate >> 4)
	body[12] = byte(rate<<4) | byte(channels-1)<<1 | byte((depth-1)>>4)
	body[13] = byte((depth-1)&0x0F)<<4 | byte(total>>32)&0x0F
	be.PutUint32(body[14:18], uint32(total))
	for i := range body[18:34] {
		body[18+i] = byte(i + 1)
	}
	return body
}

// mkDfLa builds a dfLa box (a FullBox wrapping FLAC metadata blocks) whose single block
// carries the given type and body.
func mkDfLa(blockType byte, body []byte) []byte {
	b := make([]byte, 12+4+len(body))
	binary.BigEndian.PutUint32(b[0:4], uint32(len(b)))
	copy(b[4:8], "dfLa")
	b[12] = 0x80 | blockType&0x7F // last-metadata-block flag
	b[13], b[14], b[15] = byte(len(body)>>16), byte(len(body)>>8), byte(len(body))
	copy(b[16:], body)
	return b
}

// mkFlacStsdPayload builds an stsd payload with one v0 'fLaC' sample entry carrying the
// given dfLa box. The entry's own 16.16 rate field holds the rate only when it fits, so a
// muxer writes 0 above 65535.
func mkFlacStsdPayload(entryRate uint32, dfla []byte) []byte {
	b := make([]byte, 8+36+len(dfla))
	be := binary.BigEndian
	be.PutUint32(b[4:8], 1)                     // entry_count
	be.PutUint32(b[8:12], uint32(36+len(dfla))) // entry size
	copy(b[12:16], "fLaC")
	be.PutUint16(b[22:24], 1)             // data_reference_index
	be.PutUint16(b[32:34], 2)             // channels
	be.PutUint16(b[34:36], 16)            // sample_size
	be.PutUint32(b[40:44], entryRate<<16) // sample_rate 16.16
	copy(b[44:], dfla)
	return b
}

// mkV2StsdPayload builds an stsd payload with one version 2 sound sample entry: the
// QuickTime layout whose float64 rate and uint32 channel count replace the fixed v0/v1
// fields, followed by any extension boxes.
func mkV2StsdPayload(fourcc string, rate float64, channels, bits uint32, ext []byte) []byte {
	b := make([]byte, 8+72+len(ext))
	be := binary.BigEndian
	be.PutUint32(b[4:8], 1)                    // entry_count
	be.PutUint32(b[8:12], uint32(72+len(ext))) // entry size
	copy(b[12:16], fourcc)
	be.PutUint16(b[22:24], 1)  // data_reference_index
	be.PutUint16(b[24:26], 2)  // sound sample description version 2
	be.PutUint16(b[32:34], 3)  // always 3
	be.PutUint16(b[34:36], 16) // always 16
	be.PutUint16(b[36:38], 0xFFFE)
	be.PutUint32(b[40:44], 1<<16) // always 65536
	be.PutUint32(b[44:48], 72)    // sizeOfStructOnly
	be.PutUint64(b[48:56], math.Float64bits(rate))
	be.PutUint32(b[56:60], channels)
	be.PutUint32(b[60:64], 0x7F000000)
	be.PutUint32(b[64:68], bits) // constBitsPerChannel
	copy(b[80:], ext)
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

// TestParseStsdAlacCookieBitDepth checks that an ALAC entry's reported bit depth
// comes from the magic cookie rather than the sample entry's sample_size field,
// which convention pins at 16 whatever the payload depth. The essence-digest salt
// keeps the raw entry value so existing ALAC digests are unchanged.
func TestParseStsdAlacCookieBitDepth(t *testing.T) {
	d := parseStsdPayload(t, mkAlacStsdPayload(24))
	if d.track.BitsPerSample != 24 {
		t.Errorf("BitsPerSample = %d, want 24 (from the magic cookie)", d.track.BitsPerSample)
	}
	if d.cfg.sampleSize != 16 {
		t.Errorf("cfg.sampleSize = %d, want the raw entry value 16 (digest salt must not change)", d.cfg.sampleSize)
	}
}

// TestParseStsdAlacCookieFallback checks the degrade paths: when the cookie is
// absent, truncated, or declares an implausible depth, the entry's sample_size
// stands.
func TestParseStsdAlacCookieFallback(t *testing.T) {
	for _, c := range []struct {
		name    string
		payload []byte
	}{
		{"no cookie", mkAlacStsdPayload(24)[:8+36]},
		{"implausible depth 0", mkAlacStsdPayload(0)},
		{"implausible depth 64", mkAlacStsdPayload(64)},
		{"truncated cookie box", mkAlacStsdPayload(24)[:8+36+12]},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := append([]byte(nil), c.payload...)
			binary.BigEndian.PutUint32(p[8:12], uint32(len(p)-8)) // entry size matches the trim
			d := parseStsdPayload(t, p)
			if d.track.BitsPerSample != 16 {
				t.Errorf("BitsPerSample = %d, want the entry value 16", d.track.BitsPerSample)
			}
		})
	}
}

// TestParseStsdAlacWaveWrappedCookie checks the QuickTime layout: a v1 sound
// entry with the cookie inside a 'wave' wrapper, after the four extra v1 fields.
func TestParseStsdAlacWaveWrappedCookie(t *testing.T) {
	plain := mkAlacStsdPayload(24)
	cookie := plain[44:80]
	b := make([]byte, 8+36+16+8+len(cookie))
	be := binary.BigEndian
	copy(b, plain[:44])                           // stsd header + fixed entry fields
	be.PutUint32(b[8:12], uint32(len(b)-8))       // entry size
	be.PutUint16(b[24:26], 1)                     // sound-entry version 1
	be.PutUint32(b[60:64], uint32(8+len(cookie))) // wave box size
	copy(b[64:68], "wave")
	copy(b[68:], cookie)
	d := parseStsdPayload(t, b)
	if d.track.BitsPerSample != 24 {
		t.Errorf("BitsPerSample = %d, want 24 (cookie inside wave)", d.track.BitsPerSample)
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

// TestParseStsdAlacCookieSampleRate: the ALAC magic cookie is the decoder's configuration,
// so its rate wins over the entry's 16.16 field, which cannot hold a rate above 65535. The
// digest salt keeps the raw entry value, so stored ALAC digests are unchanged.
func TestParseStsdAlacCookieSampleRate(t *testing.T) {
	for _, c := range []struct {
		name      string
		entryRate uint32
	}{
		{"muxer wrote 0", 0},
		{"muxer wrote a plausible but wrong rate", 44100},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := parseStsdPayload(t, mkAlacStsdPayloadRates(24, c.entryRate, 96000, 2))
			if d.track.SampleRate != 96000 {
				t.Errorf("SampleRate = %d, want 96000 (from the magic cookie)", d.track.SampleRate)
			}
			if d.cfg.sampleRate != c.entryRate {
				t.Errorf("cfg.sampleRate = %d, want the raw entry value %d", d.cfg.sampleRate, c.entryRate)
			}
		})
	}
}

// TestParseStsdAlacCookieChannels: the cookie's channel count also wins.
func TestParseStsdAlacCookieChannels(t *testing.T) {
	d := parseStsdPayload(t, mkAlacStsdPayloadRates(24, 44100, 44100, 6))
	if d.track.Channels != 6 {
		t.Errorf("Channels = %d, want 6 (from the magic cookie)", d.track.Channels)
	}
	if d.cfg.channels != 2 {
		t.Errorf("cfg.channels = %d, want the raw entry value 2", d.cfg.channels)
	}
}

// TestParseStsdAlacCookieImplausibleRateKeepsEntry: a zero or all-ones cookie rate is not
// a rate. The all-ones case would convert to a negative int on a 32-bit build.
func TestParseStsdAlacCookieImplausibleRateKeepsEntry(t *testing.T) {
	for _, rate := range []uint32{0, 0xFFFFFFFF} {
		d := parseStsdPayload(t, mkAlacStsdPayloadRates(24, 44100, rate, 2))
		if d.track.SampleRate != 44100 {
			t.Errorf("cookie rate %d: SampleRate = %d, want the entry value 44100", rate, d.track.SampleRate)
		}
	}
}

// TestParseStsdFlacStreamInfoGeometry: the FLAC-in-ISOBMFF spec makes dfLa's STREAMINFO
// the reader's source of truth, so it fills the whole geometry, block-size bounds and MD5
// included. The entry keeps the codec four-cc and the digest salt keeps its raw fields.
func TestParseStsdFlacStreamInfoGeometry(t *testing.T) {
	d := parseStsdPayload(t, mkFlacStsdPayload(0, mkDfLa(0, mkStreamInfo(96000, 2, 24, 480000))))
	if d.track.SampleRate != 96000 || d.track.Channels != 2 || d.track.BitsPerSample != 24 {
		t.Errorf("geometry = %d Hz / %d ch / %d bit, want 96000/2/24",
			d.track.SampleRate, d.track.Channels, d.track.BitsPerSample)
	}
	if d.track.MinBlockSize != 4096 || d.track.MaxBlockSize != 4096 {
		t.Errorf("block sizes = %d/%d, want 4096/4096", d.track.MinBlockSize, d.track.MaxBlockSize)
	}
	var wantMD5 [16]byte
	for i := range wantMD5 {
		wantMD5[i] = byte(i + 1)
	}
	if d.track.MD5 != wantMD5 {
		t.Errorf("MD5 = % x, want % x", d.track.MD5, wantMD5)
	}
	if got := string(d.cfg.codec[:]); got != "fLaC" {
		t.Errorf("cfg.codec = %q, want fLaC", got)
	}
	if d.track.Codec != "fLaC" {
		t.Errorf("Codec = %q, want the entry four-cc fLaC", d.track.Codec)
	}
	if d.cfg.sampleRate != 0 || d.cfg.channels != 2 || d.cfg.sampleSize != 16 {
		t.Errorf("cfg = %d Hz / %d ch / %d bit, want the raw entry values 0/2/16",
			d.cfg.sampleRate, d.cfg.channels, d.cfg.sampleSize)
	}
}

// TestParseStsdFlacDfLaBeyondPrefixStillRead: STREAMINFO sits at a fixed offset inside
// dfLa, so a box declaring more bytes than the bounded prefix read returned is still
// usable.
func TestParseStsdFlacDfLaBeyondPrefixStillRead(t *testing.T) {
	dfla := mkDfLa(0, mkStreamInfo(96000, 2, 24, 480000))
	binary.BigEndian.PutUint32(dfla[0:4], 1<<20) // a padding block the prefix read never reaches
	d := parseStsdPayload(t, mkFlacStsdPayload(0, dfla))
	if d.track.SampleRate != 96000 {
		t.Errorf("SampleRate = %d, want 96000", d.track.SampleRate)
	}
}

// TestParseStsdFlacDfLaFirstBlockNotStreamInfoIgnored: STREAMINFO must come first, so a
// dfLa starting with another block type leaves the entry's own values standing.
func TestParseStsdFlacDfLaFirstBlockNotStreamInfoIgnored(t *testing.T) {
	d := parseStsdPayload(t, mkFlacStsdPayload(44100, mkDfLa(1, make([]byte, 34))))
	if d.track.SampleRate != 44100 || d.track.BitsPerSample != 16 {
		t.Errorf("geometry = %d Hz / %d bit, want the entry values 44100/16",
			d.track.SampleRate, d.track.BitsPerSample)
	}
}

// TestParseStsdFlacDfLaTruncatedIgnored: a STREAMINFO body short of its 34 bytes is not
// decodable, so the entry's own values stand.
func TestParseStsdFlacDfLaTruncatedIgnored(t *testing.T) {
	full := mkFlacStsdPayload(44100, mkDfLa(0, mkStreamInfo(96000, 2, 24, 480000)))
	p := full[:len(full)-10]
	binary.BigEndian.PutUint32(p[8:12], uint32(len(p)-8))
	d := parseStsdPayload(t, p)
	if d.track.SampleRate != 44100 || d.track.BitsPerSample != 16 {
		t.Errorf("geometry = %d Hz / %d bit, want the entry values 44100/16",
			d.track.SampleRate, d.track.BitsPerSample)
	}
}

// TestParseStsdV2Geometry: a version 2 sound entry carries its rate as a float64 and its
// channel count as a uint32, so a hi-res .mov reports them instead of nothing. The digest
// salt stays the four-cc plus zero geometry, as it was when v2 entries were skipped.
func TestParseStsdV2Geometry(t *testing.T) {
	d := parseStsdPayload(t, mkV2StsdPayload("lpcm", 96000, 2, 24, nil))
	if d.track.SampleRate != 96000 || d.track.Channels != 2 || d.track.BitsPerSample != 24 {
		t.Errorf("geometry = %d Hz / %d ch / %d bit, want 96000/2/24",
			d.track.SampleRate, d.track.Channels, d.track.BitsPerSample)
	}
	if d.cfg.channels != 0 || d.cfg.sampleSize != 0 || d.cfg.sampleRate != 0 {
		t.Errorf("v2 entry must leave the digest salt geometry zero: %+v", d.cfg)
	}
	_, salt := Codec{}.EssenceExtent(&core.Media{Native: d})
	if want := append([]byte("lpcm"), make([]byte, 8)...); !bytes.Equal(salt, want) {
		t.Errorf("essence salt = % x, want % x", salt, want)
	}
}

// TestParseStsdV2RejectsBadRate: NaN, an infinity, a negative, and an absurd magnitude are
// not rates; int(NaN) is implementation-defined and a wild rate poisons the reported track.
func TestParseStsdV2RejectsBadRate(t *testing.T) {
	for _, rate := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, 1e300} {
		d := parseStsdPayload(t, mkV2StsdPayload("lpcm", rate, 2, 24, nil))
		if d.track.SampleRate != 0 {
			t.Errorf("rate %v: SampleRate = %d, want 0", rate, d.track.SampleRate)
		}
	}
}

// TestParseStsdV2AlacCookieAfterStruct: a v2 'alac' entry's extensions start after the
// declared struct size, so the magic cookie is found there.
func TestParseStsdV2AlacCookieAfterStruct(t *testing.T) {
	cookie := mkAlacStsdPayloadRates(24, 0, 96000, 2)[44:80]
	d := parseStsdPayload(t, mkV2StsdPayload("alac", 0, 0, 0, cookie))
	if d.track.SampleRate != 96000 || d.track.Channels != 2 || d.track.BitsPerSample != 24 {
		t.Errorf("geometry = %d Hz / %d ch / %d bit, want 96000/2/24 (from the cookie)",
			d.track.SampleRate, d.track.Channels, d.track.BitsPerSample)
	}
}

// TestParseStsdV2WaveWrappedCookie: ffmpeg writes a .mov ALAC cookie inside a 'wave'
// wrapper on a v2 entry.
func TestParseStsdV2WaveWrappedCookie(t *testing.T) {
	cookie := mkAlacStsdPayloadRates(24, 0, 96000, 2)[44:80]
	wave := make([]byte, 8+len(cookie))
	binary.BigEndian.PutUint32(wave[0:4], uint32(len(wave)))
	copy(wave[4:8], "wave")
	copy(wave[8:], cookie)
	d := parseStsdPayload(t, mkV2StsdPayload("alac", 0, 0, 0, wave))
	if d.track.SampleRate != 96000 || d.track.BitsPerSample != 24 {
		t.Errorf("geometry = %d Hz / %d bit, want 96000/24 (cookie inside wave)",
			d.track.SampleRate, d.track.BitsPerSample)
	}
}

// TestParseStsdNonAlacEntryNotScanned: only the codecs whose configuration carries the
// real geometry are scanned, so an AAC entry keeps its sample-entry values.
func TestParseStsdNonAlacEntryNotScanned(t *testing.T) {
	p := mkAlacStsdPayloadRates(24, 44100, 96000, 6)
	copy(p[12:16], "mp4a")
	d := parseStsdPayload(t, p)
	if d.track.SampleRate != 44100 || d.track.Channels != 2 || d.track.BitsPerSample != 16 {
		t.Errorf("geometry = %d Hz / %d ch / %d bit, want the entry values 44100/2/16",
			d.track.SampleRate, d.track.Channels, d.track.BitsPerSample)
	}
}

// TestParseStsdV2HugeStructSizeRejected: the declared struct size is attacker-controlled,
// and a value near MaxInt32 overflows the extension offset to a negative on a 32-bit
// build, indexing out of range. It must be rejected as malformed instead.
func TestParseStsdV2HugeStructSizeRejected(t *testing.T) {
	for _, size := range []uint32{0x7FFFFFFF, 0x80000000, 0xFFFFFFFF, 1 << 20} {
		p := mkV2StsdPayload("alac", 96000, 2, 24, nil)
		binary.BigEndian.PutUint32(p[44:48], size)
		if d := parseStsdPayload(t, p); d.track.SampleRate != 0 {
			t.Errorf("sizeOfStructOnly %#x: SampleRate = %d, want the entry left unread", size, d.track.SampleRate)
		}
	}
}

// TestParseStsdFlacDfLaTooSmallForStreamInfo: a dfLa declaring less than a STREAMINFO
// block must not take its geometry from the bytes of the box that follows it.
func TestParseStsdFlacDfLaTooSmallForStreamInfo(t *testing.T) {
	short := make([]byte, 12) // box header + FullBox version/flags, no metadata block
	binary.BigEndian.PutUint32(short[0:4], uint32(len(short)))
	copy(short[4:8], "dfLa")
	next := make([]byte, 60)
	binary.BigEndian.PutUint32(next[0:4], uint32(len(next)))
	copy(next[4:8], "junk")
	for i := 8; i < len(next); i++ {
		next[i] = 0x55
	}

	d := parseStsdPayload(t, mkFlacStsdPayload(44100, append(short, next...)))
	if d.track.SampleRate != 44100 || d.track.Channels != 2 || d.track.BitsPerSample != 16 {
		t.Errorf("geometry = %d Hz / %d ch / %d bit, want the entry values 44100/2/16",
			d.track.SampleRate, d.track.Channels, d.track.BitsPerSample)
	}
	if d.track.MinBlockSize != 0 || d.track.MD5 != [16]byte{} {
		t.Errorf("block sizes / MD5 were taken from the neighbouring box: %d/%x", d.track.MinBlockSize, d.track.MD5)
	}
}
