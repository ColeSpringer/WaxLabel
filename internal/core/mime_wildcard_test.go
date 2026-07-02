package core

import "testing"

// TestMIMERepresentableWildcard covers the image/* cap: a "type/*" entry matches any subtype
// of that top-level type (case-insensitively, mirroring a reader's lowercase HasPrefix gate),
// while an exact list still matches only its own entries so MP4's fixed list is unaffected.
func TestMIMERepresentableWildcard(t *testing.T) {
	wild := Capability{PictureMIMEs: []string{"image/*"}}
	for _, mime := range []string{"image/png", "image/jpeg", "image/tiff", "image/x-icon", "IMAGE/PNG"} {
		if !MIMERepresentable(wild, mime) {
			t.Errorf("image/* should represent %q", mime)
		}
	}
	for _, mime := range []string{"application/octet-stream", "audio/mpeg", "text/plain", "imagexpng", ""} {
		if MIMERepresentable(wild, mime) {
			t.Errorf("image/* should not represent %q", mime)
		}
	}

	// An exact list (MP4's shape) is unaffected: no /* suffix, so only listed MIMEs match.
	exact := Capability{PictureMIMEs: []string{"image/png", "image/jpeg"}}
	if !MIMERepresentable(exact, "image/png") || !MIMERepresentable(exact, "image/jpeg") {
		t.Error("exact list should represent its own listed entries")
	}
	if MIMERepresentable(exact, "image/tiff") {
		t.Error("exact list must not represent an unlisted subtype")
	}

	// A nil/empty list imposes no MIME restriction.
	if !MIMERepresentable(Capability{}, "application/octet-stream") {
		t.Error("empty PictureMIMEs should represent any MIME")
	}
}

// TestExactMIMEMatchReliesOnSniffNormalization locks the reason the exact-list match stays
// case-sensitive: EffectiveMIME normalizes a *sniffable* image to a lowercase canonical MIME,
// so a real cover matches an exact (lowercase) list regardless of its stored MIME case; only a
// non-sniffable blob keeps an uppercase stored MIME, and that MUST drop - MP4's write gate
// (coverMIMESupported) is likewise case-sensitive and errors on a mismatch, so making this
// match case-insensitive would grade such a cover Carried and then fail the write, breaking
// the report==write invariant.
func TestExactMIMEMatchReliesOnSniffNormalization(t *testing.T) {
	mp4 := Capability{PictureMIMEs: []string{"image/jpeg", "image/png", "image/bmp"}}
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")

	real := Picture{MIME: "IMAGE/PNG", Data: png} // uppercase stored MIME, but valid PNG bytes
	if got := real.EffectiveMIME(); got != "image/png" {
		t.Fatalf("EffectiveMIME(real PNG) = %q, want image/png (sniff normalizes case)", got)
	}
	if !Representable(mp4, real) {
		t.Error("a real PNG stored under an uppercase MIME must grade representable for MP4")
	}

	blob := Picture{MIME: "IMAGE/PNG", Data: []byte("not an image")} // non-sniffable
	if Representable(mp4, blob) {
		t.Error("a non-sniffable blob must NOT grade representable for MP4 (its write gate would error)")
	}
}
