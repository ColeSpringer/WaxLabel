package asf

import (
	"encoding/binary"
	"fmt"
	"slices"
	"strings"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// WM/Picture: type, image length, UTF-16LE MIME and description (NUL-terminated),
// then image bytes. Type codes match ID3 APIC / core.PictureType.
const pictureName = "wm/picture"

// isPictureName: case-insensitive; Metadata Library uses the same name.
func isPictureName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), pictureName)
}

// decodePicture decodes a WM/Picture descriptor value.
func decodePicture(b []byte) (core.Picture, error) {
	if len(b) < 5 {
		return core.Picture{}, fmt.Errorf("%w: WM/Picture value is %d bytes, too short for its header", waxerr.ErrInvalidData, len(b))
	}
	p := core.Picture{Type: core.PictureType(b[0])}
	dataLen := int64(binary.LittleEndian.Uint32(b[1:5]))
	pos := 5

	mime, n, ok := cutUTF16(b[pos:])
	if !ok {
		return core.Picture{}, fmt.Errorf("%w: WM/Picture MIME type is not NUL-terminated", waxerr.ErrInvalidData)
	}
	pos += n
	desc, n, ok := cutUTF16(b[pos:])
	if !ok {
		return core.Picture{}, fmt.Errorf("%w: WM/Picture description is not NUL-terminated", waxerr.ErrInvalidData)
	}
	pos += n

	// Declared length is advisory: trim trailing padding only.
	img := b[pos:]
	if dataLen >= 0 && dataLen < int64(len(img)) {
		img = img[:dataLen]
	}
	if len(img) == 0 {
		return core.Picture{}, fmt.Errorf("%w: WM/Picture carries no image bytes", waxerr.ErrInvalidData)
	}
	p.MIME, p.Description, p.Data = mime, desc, slices.Clone(img)
	// Reconcile stored MIME with bytes; failed sniff keeps the stored label.
	p.SniffInto()
	return p, nil
}

// cutUTF16 reads a NUL-terminated UTF-16LE string; n includes the two-byte terminator.
func cutUTF16(b []byte) (string, int, bool) {
	for i := 0; i+1 < len(b); i += 2 {
		if b[i] == 0 && b[i+1] == 0 {
			return utf16String(b[:i]), i + 2, true
		}
	}
	return "", 0, false
}
