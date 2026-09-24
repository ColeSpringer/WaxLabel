package id3

import (
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// maxFrameSize is the largest a v2.4 frame body or the whole frame region can be: the
// sync-safe 28-bit size field caps both just under 256 MiB. A larger value would
// truncate the size field and corrupt the tag.
const maxFrameSize = 1<<28 - 1

// CheckSize rejects a frame list that cannot be encoded: more than maxElements frames, or
// a frame region overflowing the sync-safe size fields. The element cap is checked first
// so a write never mints a tag the read path would refuse to re-parse. An over-large
// embedded picture reports ErrPictureTooLarge; anything else is ErrSizeTooLarge.
//
// The cap test is a strict len(frames) > maxElements, not bits.CheckElementCap: the
// reader calls CheckElementCap with the pre-append count, so it accepts exactly
// maxElements frames, and a final-count CheckElementCap would reject a tag the reader
// reads back fine. Pass bits.DefaultLimits.MaxElements, the read-path default.
func CheckSize(writeVersion byte, frames []Frame, maxElements int) error {
	if maxElements > 0 && len(frames) > maxElements {
		return fmt.Errorf("%w: ID3v2 tag has %d frames, exceeding the %d-frame limit",
			waxerr.ErrSizeTooLarge, len(frames), maxElements)
	}
	var total int64 // int64 so a sum of large frames cannot wrap on 32-bit
	for _, f := range frames {
		fl := len(f.Body)
		if writeVersion == 4 && fl > maxFrameSize {
			return sizeErr(f, fl)
		}
		total += 10 + int64(fl)
	}
	if total > maxFrameSize {
		return fmt.Errorf("%w: ID3v2 tag is %s, exceeding the 28-bit size field limit %s",
			waxerr.ErrSizeTooLarge, bits.HumanBytes(total), bits.HumanBytes(int64(maxFrameSize)))
	}
	return nil
}

func sizeErr(f Frame, n int) error {
	if f.ID == "APIC" {
		return fmt.Errorf("%w: APIC frame is %s (max %s)", waxerr.ErrPictureTooLarge, bits.HumanBytes(int64(n)), bits.HumanBytes(int64(maxFrameSize)))
	}
	return fmt.Errorf("%w: %s frame is %s (max %s)", waxerr.ErrSizeTooLarge, f.ID, bits.HumanBytes(int64(n)), bits.HumanBytes(int64(maxFrameSize)))
}

// Frame is one ID3v2 frame. ID is always the 4-character v2.3/v2.4 identifier (a v2.2
// 3-character ID is upgraded on read). A normal frame's Body is the clean, re-renderable
// payload (de-unsynchronised, grouping byte and data-length indicator stripped) and Flags
// is zero. An opaque frame (compressed, encrypted, or otherwise unsafe to reinterpret)
// keeps Body and Flags exactly as read so it round-trips byte-for-byte.
type Frame struct {
	ID     string
	Flags  [2]byte
	Body   []byte
	Opaque bool
}

// Clone returns a deep copy of the frame.
func (f Frame) Clone() Frame {
	f.Body = slices.Clone(f.Body)
	return f
}

// v2.3 frame format-flag bits (the second flag byte).
const (
	v23Compression = 0x80
	v23Encryption  = 0x40
	v23Grouping    = 0x20
)

// v2.4 frame format-flag bits (the second flag byte).
const (
	v24Grouping    = 0x40
	v24Compression = 0x08
	v24Encryption  = 0x04
	v24Unsync      = 0x02
	v24DataLen     = 0x01
)

// parseFrames walks the frame region, decoding each frame and stopping at padding (a zero
// ID byte), an invalid identifier, or truncation. major selects the header geometry;
// tagUnsync (v2.4) forces per-frame de-unsynchronisation even when a frame does not set
// its own flag. rest is the byte count after the last frame read: free padding when the
// walk stopped on a zero ID, the unread remainder when it stopped on a malformed frame.
// malformed names the frame whose declared size ran past the end of the tag, and is empty
// when the walk stopped cleanly; it tells ParseTag whether rest is padding or unreadable.
func parseFrames(body []byte, major byte, tagUnsync bool, maxElements int) (frames []Frame, rest int, malformed string, err error) {
	pos := 0
	hdr := 10
	idLen := 4
	if major == 2 {
		hdr, idLen = 6, 3
	}
	for pos+hdr <= len(body) {
		if body[pos] == 0 {
			break // padding
		}
		// The body is bounded by the caller's MaxAllocBytes, but a body full of
		// minimum-size (6/10 B) frames still amplifies into one Frame descriptor each;
		// cap the count.
		if err := bits.CheckElementCap(len(frames), maxElements, "ID3 frames"); err != nil {
			return nil, 0, "", err
		}
		id := string(body[pos : pos+idLen])
		if !validFrameID(id) {
			break
		}
		var size int64
		var flags [2]byte
		switch major {
		case 2:
			size = int64(body[pos+3])<<16 | int64(body[pos+4])<<8 | int64(body[pos+5])
		case 3:
			size = int64(body[pos+4])<<24 | int64(body[pos+5])<<16 | int64(body[pos+6])<<8 | int64(body[pos+7])
			flags = [2]byte{body[pos+8], body[pos+9]}
		default: // 4
			// v2.4 sizes are sync-safe; some buggy encoders write plain sizes, but
			// sync-safe is the spec and what Render emits.
			size = syncSafe(body[pos+4 : pos+8])
			flags = [2]byte{body[pos+8], body[pos+9]}
		}
		start := pos + hdr
		// Compare in int64: a v2.3 plain 32-bit size can be up to 0xFFFFFFFF, which
		// on a 32-bit platform would wrap to a negative int and slip past the guard.
		if size < 0 || int64(start)+size > int64(len(body)) {
			// The frame declares more bytes than the tag holds. Stop and name it: what
			// follows is unreadable, not padding. Report the id the rest of the output uses,
			// so a v2.2 tag does not warn about a "TAL" frame that dump lists as TALB.
			return frames, len(body) - pos, reportedFrameID(id, major), nil
		}
		raw := body[start : start+int(size)]
		pos = start + int(size)

		frames = append(frames, decodeFrame(id, flags, raw, major, tagUnsync))
	}
	return frames, len(body) - pos, "", nil
}

// reportedFrameID renders a frame identifier the way the decoded frame list does: a v2.2
// id is upgraded to its v2.3/v2.4 spelling, or space-padded when the table does not know
// it. [decodeFrame] applies the same two rules, so a diagnostic cannot spell a frame
// differently from the listing beside it.
func reportedFrameID(id string, major byte) string {
	if major != 2 {
		return id
	}
	if up, ok := upgradeV22ID(id); ok {
		return up
	}
	return padID(id)
}

// decodeFrame normalises one raw frame: it upgrades a v2.2 identifier, converts
// a v2.2 PIC body to APIC form, and either cleans the body (stripping grouping
// and data-length bytes and de-unsynchronising) or marks it opaque when it is
// compressed or encrypted.
func decodeFrame(id string, flags [2]byte, raw []byte, major byte, tagUnsync bool) Frame {
	if major == 2 {
		up, ok := upgradeV22ID(id)
		if !ok {
			// Unknown v2.2 frame: keep it opaque under a best-effort upgraded ID so
			// it is preserved rather than dropped.
			return Frame{ID: padID(id), Body: slices.Clone(raw), Opaque: true}
		}
		if id == "PIC" {
			raw = convertPICtoAPIC(raw)
		}
		return Frame{ID: up, Body: slices.Clone(raw)}
	}

	compressed, encrypted, grouping, unsync, dataLen := decodeFrameFlags(flags, major)
	// Preserve verbatim (opaque) when the body cannot be reinterpreted (compressed or
	// encrypted) or the ID is not spec-conformant. A non-conformant ID is a space-padded
	// v2.2 upgrade ("TXY " from padID) rendered into this v2.3/2.4 tag and now re-read.
	// Marking it opaque here, the single decode entry point, keeps it out of the projection
	// and the managed-frame rebuild, since every f.Opaque short-circuit (projection,
	// rebuild, DecodeText) covers it.
	if compressed || encrypted || !conformantFrameID(id) {
		// v2.4 unsync still applies to an opaque body. Render writes a tag header without
		// tag-level unsync, so normalize the body here and clear the frame-level unsync bit when
		// it no longer applies.
		if unsync || (major == 4 && tagUnsync) {
			raw = deunsync(raw)
			flags[1] &^= v24Unsync
		}
		return Frame{ID: id, Flags: flags, Body: slices.Clone(raw), Opaque: true}
	}
	// De-unsynchronise first: per the v2.4 spec the transform covers the whole frame-data
	// region, including the group byte and data-length indicator. Stripping first would
	// strand a 0x00 stuffing byte in the payload as an extra empty value. v2.3 whole-tag
	// unsync is undone in ParseTag, so only v2.4 frames reach deunsync here.
	b := raw
	if unsync || (major == 4 && tagUnsync) {
		b = deunsync(b)
	}
	if grouping && len(b) >= 1 {
		b = b[1:] // drop the group identity byte
	}
	if dataLen && len(b) >= 4 {
		b = b[4:] // drop the sync-safe data-length indicator
	}
	return Frame{ID: id, Body: slices.Clone(b)}
}

// decodeFrameFlags interprets the second format-flag byte for the version.
func decodeFrameFlags(flags [2]byte, major byte) (compressed, encrypted, grouping, unsync, dataLen bool) {
	f := flags[1]
	if major == 3 {
		return f&v23Compression != 0, f&v23Encryption != 0, f&v23Grouping != 0, false, false
	}
	return f&v24Compression != 0, f&v24Encryption != 0, f&v24Grouping != 0, f&v24Unsync != 0, f&v24DataLen != 0
}

// validFrameID reports whether id looks like a frame identifier: it begins with A-Z or a
// digit, and the rest are A-Z, digits, or spaces. The trailing-space allowance keeps a
// three-character ID padded to four ("TT2 ") from ending the scan and dropping every
// later frame. A leading space or NUL still stops the scan.
func validFrameID(id string) bool {
	if len(id) == 0 {
		return false
	}
	if !isUpperAlnum(id[0]) {
		return false
	}
	for i := 1; i < len(id); i++ {
		if c := id[i]; !isUpperAlnum(c) && c != ' ' {
			return false
		}
	}
	return true
}

// isUpperAlnum reports whether c is an uppercase ASCII letter or a digit, the character
// class of an ID3 frame identifier. validFrameID, conformantFrameID, and rawFrameIDKey
// share it.
func isUpperAlnum(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// conformantFrameID reports whether id is a spec-conformant 4-character frame identifier:
// exactly four bytes, each A-Z or 0-9. Unlike validFrameID it rejects a trailing space, so
// an unknown ID3v2.2 frame preserved under a space-padded ID ("TXY " from padID) fails.
// decodeFrame uses it to mark such a re-read frame opaque; rawFrameIDKey builds on it.
func conformantFrameID(id string) bool {
	if len(id) != 4 {
		return false
	}
	for i := 0; i < 4; i++ {
		if !isUpperAlnum(id[i]) {
			return false
		}
	}
	return true
}

// padID widens a 3-character identifier to four characters so it fits the
// v2.3/v2.4 header when an unknown v2.2 frame must be preserved.
func padID(id string) string {
	for len(id) < 4 {
		id += " "
	}
	return id[:4]
}

// clampPadding bounds padding so the rendered tag's payload (frames plus padding, after
// the 10-byte header) fits the sync-safe 28-bit size field. nonPad is the rendered
// non-padding size including that header (RenderedSize). It returns the clamped padding
// and whether a clamp was applied. An over-limit frame set, which CheckSize rejects
// first, yields maxPad 0 rather than a negative padding.
func clampPadding(nonPad, padSize int64) (int64, bool) {
	maxPad := max(0, int64(maxFrameSize)-(nonPad-10))
	if padSize > maxPad {
		return maxPad, true
	}
	return padSize, false
}

// Render assembles a full ID3v2 tag: the 10-byte header (no unsynchronisation,
// no extended header), the frames, and padding zeros. writeVersion is 3 or 4.
func Render(writeVersion byte, frames []Frame, padding int) []byte {
	var fb []byte
	for _, f := range frames {
		fb = append(fb, renderFrame(writeVersion, f)...)
	}
	// Clamp for direct Render callers: padding must not push frames+padding past the
	// 28-bit size field. RenderFrontTag clamps earlier, WAV/AIFF pass padding=0, and
	// CheckSize checks the frame bytes.
	if clamped, _ := clampPadding(int64(len(fb))+10, int64(padding)); clamped < int64(padding) {
		padding = int(clamped)
	}
	total := len(fb) + padding
	out := make([]byte, 0, 10+total)
	out = append(out, 'I', 'D', '3', writeVersion, 0, 0)
	var sz [4]byte
	putSyncSafe(sz[:], int64(total))
	out = append(out, sz[:]...)
	out = append(out, fb...)
	out = append(out, make([]byte, padding)...)
	return out
}

// RenderedSize returns the on-disk length [Render] would emit for frames with no padding:
// a 10-byte tag header plus each frame's 10-byte header and body. Codecs that size
// padding by reuse-in-place use it instead of rendering the whole tag a second time. The
// length is independent of the write version; only the size field's encoding differs.
func RenderedSize(frames []Frame) int64 {
	total := int64(10) // tag header
	for _, f := range frames {
		total += int64(10 + len(f.Body))
	}
	return total
}

// FrameNote is the dump --native note for one frame: what it is beyond its four-character
// id. A described frame (COMM, USLT, TXXX) carries its identity in a description the id
// does not show, so a bare "COMM 20 B" line cannot tell an iTunes normalization block
// from a real comment, or show which descriptions [mapping.ID3TechnicalCommentDesc] keeps
// out of the canonical view. The description is a stored string, so it goes through
// [tag.SanitizeLine]. MP3 and AAC share it.
func FrameNote(f Frame) string {
	if f.Opaque {
		return "preserved (opaque)"
	}
	var desc string
	switch f.ID {
	case "COMM":
		desc, _, _ = decodeCommentFrame(f.Body)
	case "TXXX":
		desc, _, _ = decodeUserText(f.Body)
	case "USLT":
		desc, _, _ = decodeLangText(f.Body)
	}
	if desc == "" {
		return ""
	}
	return "description: " + tag.SanitizeLine(desc)
}

// FramesNote is the native-view note for a described ID3v2 tag: its frame count, plus the
// byte count of a region the frame walk could not read (see [Tag.MalformedTail]). Padding
// is not counted as unparsed, but a malformed tail is not padding either; without it the
// block size would disagree with the frames listed under it. All four ID3-backed codecs
// render through it.
func FramesNote(t *Tag) string {
	if t == nil {
		return "0 frames"
	}
	_, tail := t.MalformedTail()
	return fmt.Sprintf("%d frames", len(t.Frames())) + core.UnparsedNote(tail)
}

// FrontTagPadding reports the free padding inside a front ID3v2 region, or 0 for a file
// with no front tag. ID3 padding lives inside the tag's declared size and has no block of
// its own, so this is what Document.Padding reports for the ID3-fronted codecs. The number
// is [Tag.Padding]: measured at parse, and restamped by RenderFrontTag on a rewrite, so a
// read of the written file and the plan that wrote it report the same region.
func FrontTagPadding(t *Tag) int64 {
	if t == nil {
		return 0
	}
	return t.Padding()
}

// renderFrame renders one frame's on-disk bytes for the write version. An opaque
// frame keeps its preserved flags; a normal frame writes cleared flags.
func renderFrame(writeVersion byte, f Frame) []byte {
	out := make([]byte, 0, 10+len(f.Body))
	out = append(out, f.ID[:4]...)
	var sz [4]byte
	if writeVersion == 4 {
		putSyncSafe(sz[:], int64(len(f.Body)))
	} else {
		sz[0] = byte(len(f.Body) >> 24)
		sz[1] = byte(len(f.Body) >> 16)
		sz[2] = byte(len(f.Body) >> 8)
		sz[3] = byte(len(f.Body))
	}
	out = append(out, sz[:]...)
	out = append(out, f.Flags[0], f.Flags[1])
	out = append(out, f.Body...)
	return out
}
