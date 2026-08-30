package ogg

import (
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/waxerr"
)

// The FLAC-in-Ogg mapping. The first header packet is a fixed prologue followed
// by the native FLAC stream marker and the STREAMINFO block; every later header
// packet carries exactly one FLAC metadata block. Audio packets are FLAC frames.
//
// This is the one Ogg codec whose cover art is not a METADATA_BLOCK_PICTURE
// comment: FLAC's native PICTURE block travels in a header packet of its own.
const (
	// flacIDFixed is the length of the identification packet up to and including
	// the STREAMINFO body: "\x7FFLAC"(5) version(2) headerPackets(2) "fLaC"(4)
	// block header(4) STREAMINFO(34).
	flacIDFixed = 5 + 2 + 2 + 4 + 4 + vorbis.StreamInfoLen
	// flacHeaderCountOff is where the 16-bit big-endian count of header packets
	// after this one sits in the identification packet.
	flacHeaderCountOff = 7
	// flacStreamInfoOff is where the STREAMINFO body starts in the identification packet.
	flacStreamInfoOff = 17

	// FLAC metadata block type codes, from internal/vorbis so the Ogg mapping and the
	// native FLAC stream cannot disagree about them. Every code this file does not name
	// is preserved verbatim in its own header packet.
	flacBlkStreamInfo    = vorbis.BlockStreamInfo
	flacBlkPadding       = vorbis.BlockPadding
	flacBlkVorbisComment = vorbis.BlockVorbisComment
	flacBlkPicture       = vorbis.BlockPicture
	flacBlkInvalid       = vorbis.BlockInvalid

	flacMaxBlockBody = vorbis.MaxBlockBody
)

var (
	flacID     = []byte("\x7FFLAC")
	flacMarker = []byte("fLaC")
)

// fblock is one FLAC metadata block from a header packet, body verbatim so an
// untouched block round-trips byte-for-byte.
type fblock struct {
	code byte
	body []byte
}

// packet renders the block back into a header packet: the 4-byte block header
// (last-block flag in bit 7, type code in bits 0-6) followed by the body.
func (b fblock) packet(last bool) []byte {
	pkt := make([]byte, 4, 4+len(b.body))
	pkt[0] = b.code & 0x7F
	if last {
		pkt[0] |= 0x80
	}
	n := len(b.body)
	pkt[1] = byte(n >> 16)
	pkt[2] = byte(n >> 8)
	pkt[3] = byte(n)
	return append(pkt, b.body...)
}

func flacBlockName(code byte) string { return vorbis.BlockName(code) }

// checkFLACID validates the identification packet's fixed prologue. Only mapping
// version 1.x is defined; a later major version may reshape the packet, so it is
// refused rather than misread.
func checkFLACID(pkt []byte) error {
	if len(pkt) < flacIDFixed {
		return fmt.Errorf("%w: Ogg FLAC identification packet is %d bytes, need %d", waxerr.ErrInvalidData, len(pkt), flacIDFixed)
	}
	if pkt[5] != 1 {
		return fmt.Errorf("%w: Ogg FLAC mapping version %d.%d", waxerr.ErrUnsupportedFormat, pkt[5], pkt[6])
	}
	if string(pkt[9:13]) != string(flacMarker) {
		return fmt.Errorf("%w: Ogg FLAC identification packet is missing the fLaC marker", waxerr.ErrInvalidData)
	}
	if pkt[13]&0x7F != flacBlkStreamInfo || int(binary.BigEndian.Uint32(append([]byte{0}, pkt[14:17]...))) != vorbis.StreamInfoLen {
		return fmt.Errorf("%w: Ogg FLAC identification packet does not carry a %d-byte STREAMINFO", waxerr.ErrInvalidData, vorbis.StreamInfoLen)
	}
	return nil
}

// splitFLACBlocks turns the header packets after the identification packet into
// metadata blocks, one per packet, and reports the full packet bytes of the
// first VORBIS_COMMENT block so the shared comment decoder can read it.
func splitFLACBlocks(packets [][]byte, maxElements int, limit int64) (blocks []fblock, comment []byte, dups []core.DuplicateContent, err error) {
	for _, pkt := range packets {
		if err := bits.CheckElementCap(len(blocks), maxElements, "Ogg FLAC metadata blocks"); err != nil {
			return nil, nil, nil, err
		}
		if len(pkt) < 4 {
			return nil, nil, nil, fmt.Errorf("%w: Ogg FLAC header packet is %d bytes, too short for a metadata block", waxerr.ErrInvalidData, len(pkt))
		}
		code := pkt[0] & 0x7F
		if code == flacBlkInvalid {
			return nil, nil, nil, fmt.Errorf("%w: invalid FLAC block type 127", waxerr.ErrInvalidData)
		}
		n := int(pkt[1])<<16 | int(pkt[2])<<8 | int(pkt[3])
		if len(pkt)-4 < n {
			return nil, nil, nil, fmt.Errorf("%w: truncated %s block (%d bytes declared, %d present)",
				waxerr.ErrInvalidData, flacBlockName(code), n, len(pkt)-4)
		}
		b := fblock{code: code, body: pkt[4 : 4+n]}
		switch {
		case code != flacBlkVorbisComment:
		case comment == nil:
			comment = pkt[:4+n]
		default:
			// Only the first comment block survives a rewrite, matching native FLAC. Record
			// what each extra holds so the writer can warn when dropping it loses something.
			if _, extra, _, err := vorbis.ParseCommentList(b.body, limit, maxElements); err == nil {
				lose, _ := vorbis.Project(extra)
				dups = append(dups, core.DuplicateContent{Tags: lose})
			}
		}
		blocks = append(blocks, b)
	}
	return blocks, comment, dups, nil
}

// flacHeaderPackets renders the header packets that follow the identification
// packet: one per metadata block, with the last-block flag on the final one.
func flacHeaderPackets(blocks []fblock) [][]byte {
	out := make([][]byte, len(blocks))
	for i, b := range blocks {
		out[i] = b.packet(i == len(blocks)-1)
	}
	return out
}

// rebuildFLACBlocks assembles the new metadata block list, preserving order and
// raw bytes for everything untouched. It mirrors the native FLAC writer: the
// first VORBIS_COMMENT block is re-rendered (extras are dropped), pictures are
// re-emitted at the position of the first PICTURE block, and every other block
// is cloned verbatim.
func rebuildFLACBlocks(d *doc, vendor string, comments []vorbis.Comment, pictures []core.Picture, commentsChanged, picturesChanged bool) (out []fblock, dupDropped bool) {
	out = make([]fblock, 0, len(d.flacBlocks)+len(pictures))
	commentHandled := false
	picturesEmitted := false
	commentReRendered := false

	// A picture edit re-emits every cover as a native block. If the source carried a
	// comment-embedded cover, cloning the comment block verbatim would keep that now-stale
	// METADATA_BLOCK_PICTURE alongside the fresh native block, so force a re-render.
	dropPictureComment := picturesChanged && len(d.commentPictures) > 0

	emitPictures := func() {
		for _, p := range pictures {
			out = append(out, fblock{code: flacBlkPicture, body: vorbis.RenderPicture(p)})
		}
		for _, body := range d.malformedPictureBlocks {
			out = append(out, fblock{code: flacBlkPicture, body: body})
		}
		picturesEmitted = true
	}

	for _, b := range d.flacBlocks {
		switch b.code {
		case flacBlkVorbisComment:
			if commentHandled {
				dupDropped = true
				continue // only the first comment block survives a rewrite
			}
			commentHandled = true
			if !commentsChanged && !dropPictureComment {
				out = append(out, b)
				continue
			}
			out = append(out, fblock{code: flacBlkVorbisComment, body: vorbis.RenderCommentList(vendor, comments)})
			commentReRendered = true
		case flacBlkPicture:
			if picturesChanged {
				if !picturesEmitted {
					emitPictures()
				}
				continue
			}
			out = append(out, b)
		case flacBlkPadding:
			// Ogg re-paginates the header region on every rewrite, so a padding block
			// buys nothing here; drop it rather than carry dead bytes through pages.
			continue
		default:
			out = append(out, b)
		}
	}

	if !commentHandled {
		// The mapping requires a VORBIS_COMMENT block immediately after STREAMINFO,
		// so a file missing one gets it at the front on rewrite.
		out = append([]fblock{{code: flacBlkVorbisComment, body: vorbis.RenderCommentList(vendor, comments)}}, out...)
		commentReRendered = true
	}
	if picturesChanged && !picturesEmitted && (len(pictures) > 0 || len(d.malformedPictureBlocks) > 0) {
		emitPictures()
	}
	// Materialize comment-sourced covers when the comment block was re-rendered (which
	// strips the METADATA_BLOCK_PICTURE entry) but pictures were not separately re-emitted.
	// Without this a tag-only edit would silently drop the cover.
	if commentReRendered && !picturesChanged {
		for _, p := range d.commentPictures {
			out = append(out, fblock{code: flacBlkPicture, body: vorbis.RenderPicture(p)})
		}
	}
	return out, dupDropped
}

// checkFLACBlockSizes rejects any block whose body exceeds the 24-bit length
// field, which would otherwise be silently truncated into a corrupt file.
func checkFLACBlockSizes(blocks []fblock) error {
	for _, b := range blocks {
		if len(b.body) <= flacMaxBlockBody {
			continue
		}
		if b.code == flacBlkPicture {
			return fmt.Errorf("%w: picture block is %s (max %s)",
				waxerr.ErrPictureTooLarge, bits.HumanBytes(int64(len(b.body))), bits.HumanBytes(int64(flacMaxBlockBody)))
		}
		return fmt.Errorf("%w: %s block is %s, exceeding the 24-bit limit %s",
			waxerr.ErrInvalidData, flacBlockName(b.code), bits.HumanBytes(int64(len(b.body))), bits.HumanBytes(int64(flacMaxBlockBody)))
	}
	return nil
}

// streamInfo returns the STREAMINFO body carried at the tail of the FLAC
// mapping's identification packet, or nil for the other two kinds.
func (d *doc) streamInfo() []byte {
	if d.kind != kindFLAC || len(d.idPacket) < flacIDFixed {
		return nil
	}
	return d.idPacket[flacStreamInfoOff : flacStreamInfoOff+vorbis.StreamInfoLen]
}

// clone deep-copies the block, so a Document accessor cannot alias a body the
// document still owns.
func (b fblock) clone() fblock { return fblock{code: b.code, body: slices.Clone(b.body)} }

// decodeFLACBlockPictures splits a FLAC block list into the covers it holds and the
// PICTURE bodies that would not decode. It is the one derivation both the parser and
// the post-write result use, so a rewritten document reports the same picture set a
// fresh parse of its bytes would.
func decodeFLACBlockPictures(blocks []fblock, limit int64) (pics []core.Picture, malformed [][]byte, warnings []core.Warning) {
	for _, b := range blocks {
		if b.code != flacBlkPicture {
			continue
		}
		p, err := vorbis.ParsePicture(b.body, limit)
		if err != nil {
			warnings = core.Warn(warnings, core.WarnInvalidPicture, err.Error())
			malformed = append(malformed, b.body)
			continue
		}
		pics = append(pics, p)
	}
	return pics, malformed, warnings
}

// commentSourcedPictures decodes the covers a comment list still carries as
// METADATA_BLOCK_PICTURE entries. It is how the post-write document re-derives the
// field the parser fills, so a comment block preserved verbatim keeps its covers
// materializable and a re-rendered one reports none.
func commentSourcedPictures(comments []vorbis.Comment, limit int64) []core.Picture {
	var out []core.Picture
	for _, cm := range comments {
		if !vorbis.IsPictureComment(cm.Name) {
			continue
		}
		if p, err := vorbis.DecodePictureComment(cm.Value, limit); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// origMaxHeaderPacket is the largest header packet this document was parsed with:
// the comment packet for every mapping, plus the Vorbis setup packet and each FLAC
// metadata block's packet. The write-side allocation guard floors at it so bytes
// already in the file - read within the (possibly raised) parse limit - stay writable
// under a lower write limit, whether or not the edit touches them.
func (d *doc) origMaxHeaderPacket() int64 {
	n := d.origCommentPacketLen
	if sz := int64(len(d.setupPacket)); sz > n {
		n = sz
	}
	for _, b := range d.flacBlocks {
		if sz := int64(len(b.body)) + 4; sz > n {
			n = sz
		}
	}
	return n
}

// headerPacketName names a header packet for the size-guard error, so a refusal says
// which packet was too large rather than always blaming the comment header.
func headerPacketName(k kind, pkt []byte) string {
	if k != kindFLAC {
		return "comment header"
	}
	if len(pkt) > 0 {
		return flacBlockName(pkt[0]&0x7F) + " block"
	}
	return "header packet"
}

// flacIDWithCount returns the identification packet with its header-packet count
// updated to n. Everything else - the mapping version and the STREAMINFO block -
// is carried verbatim, so the decoder-critical bytes are untouched. The count is
// the one field a metadata rewrite can invalidate: adding or removing a picture
// changes how many header packets follow.
func flacIDWithCount(id []byte, n int) []byte {
	out := make([]byte, len(id))
	copy(out, id)
	if n > 0xFFFF {
		n = 0 // 0 means "unknown", the spec's escape for a count that does not fit
	}
	binary.BigEndian.PutUint16(out[flacHeaderCountOff:flacHeaderCountOff+2], uint16(n))
	return out
}
