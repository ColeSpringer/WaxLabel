package waxlabel_test

import (
	"bytes"
	"slices"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/tag"
)

// wavFmtTag builds a 16-byte "fmt " chunk for an arbitrary WAVE format tag, so a test can
// describe a compressed payload (whose avgBytesPerSec is nominal or zero and whose
// blockAlign is a compressed block, not a sample frame).
func wavFmtTag(format, channels, blockAlign, bits int, sampleRate, byteRate int) []byte {
	b := slices.Concat(wavLE16(format), wavLE16(channels), wavLE32(sampleRate),
		wavLE32(byteRate), wavLE16(blockAlign), wavLE16(bits))
	return wavChunk("fmt ", b)
}

// wavFact builds a "fact" chunk declaring n sample frames.
func wavFact(n int) []byte { return wavChunk("fact", wavLE32(n)) }

// TestWAVADPCMDurationFromFact pins the MS-ADPCM case: avgBytesPerSec is nominal (16000
// against ~22 KB of one second's audio), so the byte-rate formula reads 1.4 s and the
// declared 128 kbps is not what the file holds. The fact chunk's sample count is the real
// length, and the bitrate follows from the bytes actually stored.
func TestWAVADPCMDurationFromFact(t *testing.T) {
	data := wavFile(wavFmtTag(0x0002, 1, 1024, 4, 44100, 16000), wavFact(44100), wavData(22528))
	tr := mustParseBytes(t, data).Properties().First()
	if got := tr.Duration.Round(time.Millisecond); got != time.Second {
		t.Errorf("duration = %v, want 1s", got)
	}
	if tr.TotalSamples != 44100 {
		t.Errorf("totalSamples = %d, want 44100 (not a block count)", tr.TotalSamples)
	}
	if tr.Bitrate != 22528*8 {
		t.Errorf("bitrate = %d, want %d (from the stored bytes, not the nominal byte rate)", tr.Bitrate, 22528*8)
	}
}

// TestWAVMP3InWAVDurationFromFact pins the other failing shape: a WAV carrying MP3 payload
// declares avgBytesPerSec 0, so there is no byte-rate formula at all and both duration and
// bitrate used to read null.
func TestWAVMP3InWAVDurationFromFact(t *testing.T) {
	data := wavFile(wavFmtTag(0x0055, 1, 1152, 0, 44100, 0), wavFact(45205), wavData(8359))
	tr := mustParseBytes(t, data).Properties().First()
	if got := tr.Duration.Round(time.Millisecond); got != 1025*time.Millisecond {
		t.Errorf("duration = %v, want 1.025s", got)
	}
	if tr.Bitrate == 0 {
		t.Error("bitrate = 0; a resolved duration should yield an average bitrate")
	}
}

// TestWAVHostileFactFallsBack is the sanity gate: dwSampleLength is attacker-controlled, so
// a 0xFFFFFFFF count on a 22 KB file must not report a 27-hour duration. The byte-rate
// estimate takes over instead.
func TestWAVHostileFactFallsBack(t *testing.T) {
	data := wavFile(wavFmtTag(0x0002, 1, 1024, 4, 44100, 16000), wavChunk("fact", []byte{0xFF, 0xFF, 0xFF, 0xFF}), wavData(22528))
	tr := mustParseBytes(t, data).Properties().First()
	if tr.Duration > 10*time.Second {
		t.Errorf("duration = %v; a garbage sample count must not be trusted", tr.Duration)
	}
	if tr.TotalSamples != 0 {
		t.Errorf("totalSamples = %d, want 0 (the declared count was rejected)", tr.TotalSamples)
	}
}

// TestWAVHostileFactFallsBackWithoutByteRate covers the same gate on the path with no
// byte-rate estimate to compare against, where the bound is the data itself.
func TestWAVHostileFactFallsBackWithoutByteRate(t *testing.T) {
	data := wavFile(wavFmtTag(0x0055, 1, 1152, 0, 44100, 0), wavChunk("fact", []byte{0xFF, 0xFF, 0xFF, 0xFF}), wavData(8359))
	tr := mustParseBytes(t, data).Properties().First()
	if tr.Duration != 0 {
		t.Errorf("duration = %v, want 0 (no usable length)", tr.Duration)
	}
	if tr.Bitrate != 0 {
		t.Errorf("bitrate = %d, want 0", tr.Bitrate)
	}
}

// TestWAVAbsurdSampleRateFactRejected: with no byte rate to compare against, a declared
// count and a declared rate can be nonsense together and stay inside every ratio. 800000
// frames at 1 Hz is 9.3 days of audio stored in 100 KB, which is 1 bit per second - the
// bitrate floor is what sees it.
func TestWAVAbsurdSampleRateFactRejected(t *testing.T) {
	data := wavFile(wavFmtTag(0x0002, 1, 1024, 4, 1, 0), wavFact(800000), wavData(100000))
	tr := mustParseBytes(t, data).Properties().First()
	if tr.Duration != 0 {
		t.Errorf("duration = %v, want 0; no audio is stored at 1 bit per second", tr.Duration)
	}
	if tr.TotalSamples != 0 {
		t.Errorf("totalSamples = %d, want 0", tr.TotalSamples)
	}
}

// TestWAVCompressedWithoutFactHasNoSampleCount: without a fact chunk, blockAlign is a
// compressed block, so reporting dataLen/blockAlign would be a block count three orders of
// magnitude off. The duration still falls back to the nominal byte rate.
func TestWAVCompressedWithoutFactHasNoSampleCount(t *testing.T) {
	data := wavFile(wavFmtTag(0x0002, 1, 1024, 4, 44100, 16000), wavData(22528))
	tr := mustParseBytes(t, data).Properties().First()
	if tr.TotalSamples != 0 {
		t.Errorf("totalSamples = %d, want 0 (a block count is not a sample count)", tr.TotalSamples)
	}
	if tr.Duration <= 0 {
		t.Errorf("duration = %v, want the byte-rate estimate", tr.Duration)
	}
}

// TestWAVPCMIgnoresFact: for a constant-rate family the fmt byte rate is exact, so the
// declared count changes nothing. A fact chunk in a PCM file must not move the numbers.
func TestWAVPCMIgnoresFact(t *testing.T) {
	plain := mustParseBytes(t, wavFile(wavFmtPCM(), wavData(176400))).Properties().First()
	withFact := mustParseBytes(t, wavFile(wavFmtPCM(), wavFact(1), wavData(176400))).Properties().First()
	if plain.Duration != withFact.Duration || plain.Bitrate != withFact.Bitrate || plain.TotalSamples != withFact.TotalSamples {
		t.Errorf("a fact chunk changed PCM properties: %+v vs %+v", plain, withFact)
	}
	if withFact.Duration.Round(time.Millisecond) != time.Second {
		t.Errorf("duration = %v, want 1s", withFact.Duration)
	}
}

// TestWAVFactSurvivesEdit: the fact chunk is preserved verbatim like every other non-tag
// chunk, so the count still describes the audio after a metadata rewrite.
func TestWAVFactSurvivesEdit(t *testing.T) {
	data := wavFile(wavFmtTag(0x0002, 1, 1024, 4, 44100, 16000), wavFact(44100), wavData(22528))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, wavFact(44100)) {
		t.Error("the fact chunk did not survive a metadata edit")
	}
	if got := mustParseBytes(t, out).Properties().First().Duration.Round(time.Millisecond); got != time.Second {
		t.Errorf("duration after edit = %v, want 1s", got)
	}
}
