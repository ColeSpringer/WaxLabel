package mapping

import (
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestRIFFTrackNumberAliases checks that IPRT and ITRK both read as TrackNumber,
// while the write mapping stays deterministic by choosing IPRT.
func TestRIFFTrackNumberAliases(t *testing.T) {
	for _, id := range []string{"IPRT", "ITRK"} {
		k, ok := RIFFInfoKey(id)
		if !ok || k != tag.TrackNumber {
			t.Errorf("RIFFInfoKey(%q) = %s,%v, want TrackNumber,true", id, k, ok)
		}
	}
	if id, ok := RIFFKeyInfo(tag.TrackNumber); !ok || id != "IPRT" {
		t.Errorf("RIFFKeyInfo(TrackNumber) = %q,%v, want IPRT,true", id, ok)
	}
}

// TestRIFFEncoderIsISFT pins the software stamp to ENCODER on both sides, so a WAV read
// agrees with ffprobe's encoder= and an ENCODER write has an INFO slot to land in.
func TestRIFFEncoderIsISFT(t *testing.T) {
	if k, ok := RIFFInfoKey("ISFT"); !ok || k != tag.Encoder {
		t.Errorf("RIFFInfoKey(ISFT) = %s,%v, want ENCODER,true", k, ok)
	}
	if id, ok := RIFFKeyInfo(tag.Encoder); !ok || id != "ISFT" {
		t.Errorf("RIFFKeyInfo(ENCODER) = %q,%v, want ISFT,true", id, ok)
	}
}

// TestRIFFTechnicianAndEngineerMapped: ITCH is ffmpeg's encoded_by and IENG its engineer;
// both have canonical keys, so they read and write like the other INFO items.
func TestRIFFTechnicianAndEngineerMapped(t *testing.T) {
	for id, want := range map[string]tag.Key{"ITCH": tag.EncodedBy, "IENG": tag.Engineer} {
		if k, ok := RIFFInfoKey(id); !ok || k != want {
			t.Errorf("RIFFInfoKey(%q) = %q, %v; want %q", id, k, ok, want)
		}
		if got, ok := RIFFKeyInfo(want); !ok || got != id {
			t.Errorf("RIFFKeyInfo(%q) = %q, %v; want %q", want, got, ok, id)
		}
	}
}
