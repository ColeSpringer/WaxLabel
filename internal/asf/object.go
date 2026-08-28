package asf

import (
	"encoding/binary"
	"fmt"
	"unicode/utf16"

	"github.com/colespringer/waxlabel/waxerr"
)

// Every ASF object is a 16-byte GUID followed by a 64-bit total size that includes
// the 24-byte prefix itself.
const objectHeaderLen = 24

// object is one ASF object: its type and the body bytes after the 24-byte prefix.
type object struct {
	id   guid
	body []byte
}

// walkObjects splits a region into consecutive ASF objects. It stops cleanly at the
// first object that will not fit, so a truncated tail leaves the objects before it
// readable; count, when non-zero, caps how many are returned (the Header Object
// declares its child count).
func walkObjects(b []byte, count int) []object {
	var out []object
	for pos := 0; pos+objectHeaderLen <= len(b); {
		size := binary.LittleEndian.Uint64(b[pos+16 : pos+24])
		if size < objectHeaderLen || size > uint64(len(b)-pos) {
			break
		}
		var o object
		copy(o.id[:], b[pos:pos+16])
		o.body = b[pos+objectHeaderLen : pos+int(size)]
		out = append(out, o)
		pos += int(size)
		if count > 0 && len(out) >= count {
			break
		}
	}
	return out
}

// parseHeaderObject validates the file's leading Header Object and returns its child
// objects. The 30-byte prefix is the object header plus a child count and two
// reserved bytes.
func parseHeaderObject(b []byte) ([]object, error) {
	if len(b) < 30 {
		return nil, fmt.Errorf("%w: ASF file shorter than a Header Object", waxerr.ErrInvalidData)
	}
	var id guid
	copy(id[:], b[0:16])
	if id != guidHeader {
		return nil, fmt.Errorf("%w: missing the ASF Header Object GUID", waxerr.ErrInvalidData)
	}
	size := binary.LittleEndian.Uint64(b[16:24])
	if size < 30 {
		return nil, fmt.Errorf("%w: ASF Header Object declares %d bytes", waxerr.ErrInvalidData, size)
	}
	if size > uint64(len(b)) {
		size = uint64(len(b)) // truncated: read what is present rather than refusing outright
	}
	count := int(binary.LittleEndian.Uint32(b[24:28]))
	return walkObjects(b[30:size], count), nil
}

// utf16String decodes a UTF-16LE string, dropping a trailing NUL terminator. ASF
// stores every text value this way, terminator included in the declared length.
func utf16String(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, binary.LittleEndian.Uint16(b[i:i+2]))
	}
	for len(u) > 0 && u[len(u)-1] == 0 {
		u = u[:len(u)-1]
	}
	return string(utf16.Decode(u))
}

// Descriptor value types, shared by the Extended Content Description, Metadata, and
// Metadata Library objects.
const (
	valUnicode = 0
	valBytes   = 1
	valBool    = 2
	valDWord   = 3
	valQWord   = 4
	valWord    = 5
)

// descriptorText renders a descriptor value as the text the canonical model stores.
// The numeric types are rendered as decimal, which is how every ASF-aware tagger
// displays them; a byte array has no text form and is reported as unrepresentable so
// the caller can route it (cover art) or skip it.
func descriptorText(valueType uint16, v []byte) (string, bool) {
	switch valueType {
	case valUnicode:
		return utf16String(v), true
	case valBool:
		// Officially a 32-bit value; some writers emit 16 bits. Accept either rather
		// than dropping a flag over a width nobody agrees on.
		var n uint64
		switch len(v) {
		case 2:
			n = uint64(binary.LittleEndian.Uint16(v))
		case 4:
			n = uint64(binary.LittleEndian.Uint32(v))
		default:
			return "", false
		}
		if n != 0 {
			return "1", true
		}
		return "0", true
	case valDWord:
		if len(v) < 4 {
			return "", false
		}
		return fmt.Sprint(binary.LittleEndian.Uint32(v)), true
	case valQWord:
		if len(v) < 8 {
			return "", false
		}
		return fmt.Sprint(binary.LittleEndian.Uint64(v)), true
	case valWord:
		if len(v) < 2 {
			return "", false
		}
		return fmt.Sprint(binary.LittleEndian.Uint16(v)), true
	}
	return "", false // valBytes and anything unrecognized
}
