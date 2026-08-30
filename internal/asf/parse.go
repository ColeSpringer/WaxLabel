package asf

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"time"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// maxHeaderBytes bounds the leading region read into memory. The ASF header holds
// every object this codec reads, cover art included, and is small in any real file;
// the cap stops a declared size from pulling a whole multi-gigabyte file into memory.
const maxHeaderBytes = 64 << 20

// hundredNS is the tick of ASF's duration fields.
const hundredNS = 100 * time.Nanosecond

// parse reads a WMA/ASF file's metadata into a neutral Media. Everything comes from
// the leading Header Object: the audio geometry from Stream Properties and File
// Properties, and the tags from Content Description, Extended Content Description,
// and the Metadata objects nested in the Header Extension.
//
// WMA is read-only, so there is no native structure to preserve for a rewrite - the
// document records only what the reader found.
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes

	head, err := bits.ReadSlice(src, 0, min(size, objectHeaderLen), limit)
	if err != nil || len(head) < objectHeaderLen {
		return nil, fmt.Errorf("%w: ASF file shorter than an object header", waxerr.ErrInvalidData)
	}
	headerLen := int64(binary.LittleEndian.Uint64(head[16:24]))
	// A bogus header size leaves the audio extent unknown rather than declaring the whole
	// file to be header: AudioStart == size would send the fingerprint's head-read branch
	// over every byte of the file, on top of the header window already read.
	if headerLen < 30 || headerLen > size {
		headerLen = 0
	}
	window := headerLen
	if window <= 0 {
		window = size
	}
	buf, err := bits.ReadSlice(src, 0, min(window, min(limit, maxHeaderBytes)), limit)
	if err != nil {
		return nil, err
	}
	children, err := parseHeaderObject(buf)
	if err != nil {
		return nil, err
	}

	d := &doc{size: size, headerEnd: headerLen}
	d.dataStart, d.dataEnd = locateData(src, headerLen, size, limit)
	var contribs []core.Contribution
	var warnings []core.Warning
	// ASF routinely stores one value twice: the ffmpeg family writes the five Content
	// Description fields and then repeats them as Extended Content Description
	// descriptors. Those are one value with two spellings, not a multi-valued field, so
	// an exact (key, value) repeat is folded. A DIFFERENT value under the same key still
	// contributes, so a genuine disagreement between the two objects is still surfaced
	// (and flagged unselected) by the family view.
	seen := map[[2]string]bool{}
	emit := func(key tag.Key, value, source string) {
		if key == "" || value == "" {
			return
		}
		if pair := [2]string{string(key), value}; !seen[pair] {
			seen[pair] = true
			contribs = append(contribs, core.Contribution{Key: key, Value: value, Source: source})
		}
	}

	for _, o := range children {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch o.id {
		case guidFileProps:
			d.readFileProperties(o.body)
		case guidStreamProps:
			d.readStreamProperties(o.body)
		case guidContentDesc:
			readContentDescription(o.body, emit)
		case guidExtContentDesc:
			d.readExtendedContentDescription(o.body, emit, &warnings, opts.Limits.MaxElements)
		case guidHeaderExt:
			d.readHeaderExtension(o.body, emit, &warnings, opts.Limits.MaxElements)
		}
		d.objects = append(d.objects, objectSummary{id: o.id, size: len(o.body) + objectHeaderLen})
	}

	ts := core.BuildTagSet(contribs)
	// ASF stores "3/12" in WM/TrackNumber just as the text codecs do, so the shared
	// split runs here too and a WMA source reads back the same pair as any other file.
	tag.NormalizeNumberPairs(&ts)

	media := &core.Media{
		Format:     core.FormatWMA,
		Native:     d,
		Tags:       ts,
		Families:   core.BuildFamilies(contribs, core.FamilyASF),
		Pictures:   d.pictures,
		AudioStart: d.dataStart,
		AudioEnd:   d.dataEnd,
		Properties: core.Properties{Container: "ASF", Tracks: d.tracks()},
	}
	// The transcoder stamp an acquired file inherits, reported the way every other
	// codec reports it.
	for _, k := range []tag.Key{tag.Encoder, tag.EncodedBy} {
		if vals, ok := ts.Get(k); ok {
			for _, v := range vals {
				if core.IsTranscoderStamp(v) {
					warnings = core.Warn(warnings, core.WarnInheritedEncoder, "inherited encoder stamp: "+core.WarnSnippet(v))
				}
			}
		}
	}

	for _, name := range d.invalidKeys {
		warnings = core.WarnInvalidKey(warnings, name)
	}
	media.Warnings = warnings
	media.Identity = core.Identity{Size: size}
	media.Identity.Fingerprint, media.Identity.HasFinger = core.Fingerprint(src, media, limit)
	return media, nil
}

// locateData finds the Data Object that follows the Header Object and returns the
// extent of its media packets. Bounding the essence at the object's declared end
// matters because an ASF file can carry Simple Index objects after it: taking the
// rest of the file would fold a rebuilt index into the audio digest, so two files
// with identical packets would report different identities.
//
// A header of unknown length, or a Data Object that is not where it should be, yields
// a zero extent - the honest "the essence was not located" answer, which the digest
// and fingerprint paths already handle.
func locateData(src core.ReaderAtSized, headerEnd, size, limit int64) (start, end int64) {
	if headerEnd <= 0 || headerEnd+objectHeaderLen > size {
		return 0, 0
	}
	head, err := bits.ReadSlice(src, headerEnd, objectHeaderLen, limit)
	if err != nil {
		return 0, 0
	}
	var id guid
	copy(id[:], head[0:16])
	if id != guidData {
		return 0, 0
	}
	objLen := int64(binary.LittleEndian.Uint64(head[16:24]))
	// The Data Object's own body opens with a 16-byte File ID, an 8-byte total packet
	// count, and two reserved bytes before the packets themselves.
	const dataHeaderLen = objectHeaderLen + 16 + 8 + 2
	if objLen < dataHeaderLen || headerEnd+objLen > size {
		return 0, 0
	}
	return headerEnd + dataHeaderLen, headerEnd + objLen
}

// readFileProperties decodes the play duration, preroll, and declared file size. The
// play duration includes the preroll - the decoder's start-up buffer - so a player's
// reported length is the difference.
func (d *doc) readFileProperties(b []byte) {
	if len(b) < 80 {
		return
	}
	d.fileSize = binary.LittleEndian.Uint64(b[16:24])
	// Both fields are 64-bit and unvalidated on disk. Converting them straight into a
	// time.Duration overflows its int64 nanoseconds, and a wrapped-negative preroll would
	// pass a play > preroll test and wrap again on the subtraction, publishing a negative
	// duration. Range-check each before the conversion instead.
	play, playOK := scaledDuration(binary.LittleEndian.Uint64(b[40:48]), hundredNS)
	preroll, prerollOK := scaledDuration(binary.LittleEndian.Uint64(b[56:64]), time.Millisecond)
	if playOK && prerollOK && play > preroll {
		d.duration = play - preroll
	}
	d.maxBitrate = binary.LittleEndian.Uint32(b[76:80])
}

// scaledDuration converts a tick count to a duration, reporting ok=false when the
// product would not fit an int64 nanosecond count.
func scaledDuration(ticks uint64, unit time.Duration) (time.Duration, bool) {
	if ticks > uint64(math.MaxInt64)/uint64(unit) {
		return 0, false
	}
	return time.Duration(ticks) * unit, true
}

// readStreamProperties decodes an audio stream's WAVEFORMATEX. Only the first audio
// stream is described; a WMA file with several is rare and the first is the one a
// player selects.
func (d *doc) readStreamProperties(b []byte) {
	if len(b) < 54 {
		return
	}
	var streamType guid
	copy(streamType[:], b[0:16])
	if streamType != guidAudioMedia || d.haveAudio {
		return
	}
	typeLen := int(binary.LittleEndian.Uint32(b[40:44]))
	if typeLen < 16 || 54+typeLen > len(b) {
		return
	}
	w := b[54 : 54+typeLen]
	d.haveAudio = true
	d.formatTag = binary.LittleEndian.Uint16(w[0:2])
	d.channels = int(binary.LittleEndian.Uint16(w[2:4]))
	d.sampleRate = int(binary.LittleEndian.Uint32(w[4:8]))
	d.byteRate = int(binary.LittleEndian.Uint32(w[8:12]))
	d.bitsPerSample = int(binary.LittleEndian.Uint16(w[14:16]))
}

// contentDescriptionFields are the five fixed strings of the Content Description
// object, in the order their lengths are declared.
var contentDescriptionFields = [5]string{"Title", "Author", "Copyright", "Description", "Rating"}

// readContentDescription decodes the five fixed text fields. Rating is free text with
// no agreed scale, so the name table suppresses it; it is still walked past, because
// its length is needed to find the field after it.
func readContentDescription(b []byte, emit func(tag.Key, string, string)) {
	if len(b) < 10 {
		return
	}
	pos := 10
	for i, name := range contentDescriptionFields {
		n := int(binary.LittleEndian.Uint16(b[i*2 : i*2+2]))
		if n < 0 || pos+n > len(b) {
			return
		}
		if key, ok := mapping.CanonicalASF(name); ok {
			emit(key, utf16String(b[pos:pos+n]), name)
		}
		pos += n
	}
}

// readExtendedContentDescription decodes the open-ended "WM/*" descriptor list: the
// tag store for everything beyond the five fixed fields, cover art included.
func (d *doc) readExtendedContentDescription(b []byte, emit func(tag.Key, string, string), warnings *[]core.Warning, maxElements int) {
	if len(b) < 2 {
		return
	}
	count := int(binary.LittleEndian.Uint16(b[0:2]))
	pos := 2
	for i := 0; i < count; i++ {
		if bits.CheckElementCap(i, maxElements, "ASF descriptors") != nil {
			return
		}
		if pos+2 > len(b) {
			return
		}
		nameLen := int(binary.LittleEndian.Uint16(b[pos : pos+2]))
		pos += 2
		if nameLen < 0 || pos+nameLen+4 > len(b) {
			return
		}
		name := utf16String(b[pos : pos+nameLen])
		pos += nameLen
		valueType := binary.LittleEndian.Uint16(b[pos : pos+2])
		valueLen := int(binary.LittleEndian.Uint16(b[pos+2 : pos+4]))
		pos += 4
		if valueLen < 0 || pos+valueLen > len(b) {
			return
		}
		d.descriptor(name, valueType, b[pos:pos+valueLen], emit, warnings)
		pos += valueLen
	}
}

// readHeaderExtension walks the nested objects inside the Header Extension, where
// the Metadata and Metadata Library objects live. Their records use a different
// header shape from the Extended Content Description but the same value encoding.
func (d *doc) readHeaderExtension(b []byte, emit func(tag.Key, string, string), warnings *[]core.Warning, maxElements int) {
	// GUID(16) + reserved word(2) + data size(4) precede the nested objects.
	if len(b) < 22 {
		return
	}
	dataLen := int(binary.LittleEndian.Uint32(b[18:22]))
	if dataLen < 0 || 22+dataLen > len(b) {
		dataLen = len(b) - 22
	}
	for _, o := range walkObjects(b[22:22+dataLen], 0) {
		switch o.id {
		case guidMetadata, guidMetadataLibrary:
			d.readMetadataRecords(o.body, emit, warnings, maxElements)
		}
	}
}

// readMetadataRecords decodes a Metadata or Metadata Library object. Both carry
// per-stream descriptor records: a language or reserved word, the stream number, then
// the name and value. The per-stream scoping is not modeled - WaxLabel's canonical
// set is file-level - so records are read as plain descriptors.
func (d *doc) readMetadataRecords(b []byte, emit func(tag.Key, string, string), warnings *[]core.Warning, maxElements int) {
	if len(b) < 2 {
		return
	}
	count := int(binary.LittleEndian.Uint16(b[0:2]))
	pos := 2
	for i := 0; i < count; i++ {
		if bits.CheckElementCap(i, maxElements, "ASF metadata records") != nil {
			return
		}
		if pos+12 > len(b) {
			return
		}
		nameLen := int(binary.LittleEndian.Uint16(b[pos+4 : pos+6]))
		valueType := binary.LittleEndian.Uint16(b[pos+6 : pos+8])
		valueLen := int(binary.LittleEndian.Uint32(b[pos+8 : pos+12]))
		pos += 12
		if nameLen < 0 || valueLen < 0 || pos+nameLen > len(b) || pos+nameLen+valueLen > len(b) {
			return
		}
		name := utf16String(b[pos : pos+nameLen])
		pos += nameLen
		d.descriptor(name, valueType, b[pos:pos+valueLen], emit, warnings)
		pos += valueLen
	}
}

// descriptor routes one decoded descriptor: cover art into the picture set,
// everything else through the name mapping into the canonical model. A byte-array
// value that is not a picture has no text form and is left out of the canonical set
// rather than rendered as a hex blob.
func (d *doc) descriptor(name string, valueType uint16, value []byte, emit func(tag.Key, string, string), warnings *[]core.Warning) {
	if isPictureName(name) {
		p, err := decodePicture(value)
		if err != nil {
			*warnings = core.Warn(*warnings, core.WarnInvalidPicture, err.Error())
			return
		}
		d.pictures = append(d.pictures, p)
		return
	}
	if valueType == valBytes {
		return
	}
	text, ok := descriptorText(valueType, value)
	if !ok {
		return
	}
	key, ok := mapping.CanonicalASF(name)
	if !ok {
		if mapping.ASFUnrepresentable(name) {
			d.invalidKeys = append(d.invalidKeys, name)
		}
		return
	}
	emit(key, text, name)
}

// tracks builds the audio track list from the decoded stream and file properties.
func (d *doc) tracks() []core.AudioTrack {
	if !d.haveAudio {
		return nil
	}
	t := core.AudioTrack{
		Codec:         codecName(d.formatTag),
		SampleRate:    d.sampleRate,
		Channels:      d.channels,
		BitsPerSample: d.bitsPerSample,
		Duration:      d.duration,
	}
	// The WAVEFORMATEX byte rate is the stream's own average and is more accurate than
	// a size-over-duration estimate, which would fold in the header and packet padding.
	// It still runs through the shared helper so a crafted rate cannot publish a bitrate
	// outside the range every other codec's is clamped to.
	switch {
	case d.byteRate > 0:
		t.Bitrate = core.AverageBitrate(int64(d.byteRate), 1)
	case d.maxBitrate > 0:
		t.Bitrate = core.AverageBitrate(int64(d.maxBitrate)/8, 1)
	}
	// ASF stores a duration, not a sample count, so the count is derived. Guard the
	// float conversion: an out-of-range product is implementation-defined in Go and
	// would publish a garbage total.
	if samples := t.Duration.Seconds() * float64(t.SampleRate); samples > 0 && samples < math.MaxInt64 {
		t.TotalSamples = uint64(samples)
	}
	return []core.AudioTrack{t}
}
