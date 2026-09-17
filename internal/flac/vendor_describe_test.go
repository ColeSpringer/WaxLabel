package flac

import (
	"slices"
	"testing"
)

// TestDescribePerBlockVendor: each VORBIS_COMMENT block gets its own vendor note.
func TestDescribePerBlockVendor(t *testing.T) {
	d := &doc{blocks: []block{
		{code: blkVorbisComment, body: renderVorbisComment("Vendor-A", nil)},
		{code: blkVorbisComment, body: renderVorbisComment("Vendor-B", nil)},
	}}
	d.vendor = "Vendor-A" // parser keeps first block's vendor

	var notes []string
	for _, e := range d.Describe() {
		if e.Kind == blockName(blkVorbisComment) {
			notes = append(notes, e.Note)
		}
	}
	want := []string{"vendor=Vendor-A", "vendor=Vendor-B"}
	if !slices.Equal(notes, want) {
		t.Errorf("VORBIS_COMMENT notes = %v, want %v (each block's own vendor)", notes, want)
	}
}

// TestVendorOfBounds: short or overrun bodies fall back to remaining bytes.
func TestVendorOfBounds(t *testing.T) {
	if got := vendorOf(nil); got != "" {
		t.Errorf("vendorOf(nil) = %q, want empty", got)
	}
	if got := vendorOf([]byte{0x01, 0x02}); got != "\x01\x02" {
		t.Errorf("vendorOf(short) = %q, want the raw 2 bytes", got)
	}
	overrun := []byte{100, 0, 0, 0, 'x', 'y', 'z'} // length says 100, only 3 bytes follow
	if got := vendorOf(overrun); got != "xyz" {
		t.Errorf("vendorOf(overrun) = %q, want xyz (fallback to remaining bytes)", got)
	}
	if got := vendorOf(renderVorbisComment("Hi", nil)); got != "Hi" {
		t.Errorf("vendorOf(well-formed) = %q, want Hi", got)
	}
}
