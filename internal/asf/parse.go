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

// maxHeaderBytes caps the leading Header read (stops a huge declared size).
const maxHeaderBytes = 64 << 20

// hundredNS is the ASF duration tick.
const hundredNS = 100 * time.Nanosecond

// parse reads WMA/ASF metadata from the Header Object into a Media.
// Read-only: document records what was found, no rewrite base.
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
	// Bogus header size: leave audio extent unknown (avoid fingerprinting whole file).
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
	// ffmpeg often writes Content Description and repeats as Extended Content
	// Description. Fold exact (key, value) duplicates; different values still contribute.
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
		case guidMarker:
			d.readMarkers(o.body, &warnings, opts.Limits.MaxElements)
		}
		d.objects = append(d.objects, objectSummary{id: o.id, size: len(o.body) + objectHeaderLen})
	}

	ts := core.BuildTagSet(contribs)
	// WM/TrackNumber may be "3/12"; shared split for consistent pairs.
	tag.NormalizeNumberPairs(&ts)

	media := &core.Media{
		Format:     core.FormatWMA,
		Native:     d,
		Tags:       ts,
		Families:   core.BuildFamilies(contribs, core.FamilyASF),
		Pictures:   d.pictures,
		Chapters:   d.chapters(),
		AudioStart: d.dataStart,
		AudioEnd:   d.dataEnd,
		Properties: core.Properties{Container: "ASF", Tracks: d.tracks()},
	}
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

// locateData finds the Data Object after the Header and returns its packet extent.
// Bound at declared end so trailing Simple Index is not folded into the digest.
// Unknown header length or missing Data Object yields a zero extent.
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
	// Body opens with File ID (16) + packet count (8) + reserved (2).
	const dataHeaderLen = objectHeaderLen + 16 + 8 + 2
	// Check with subtraction: headerEnd+objLen can overflow int64.
	if objLen < dataHeaderLen || objLen > size-headerEnd {
		return 0, 0
	}
	return headerEnd + dataHeaderLen, headerEnd + objLen
}

// readFileProperties: play duration, preroll, declared file size.
// Play duration includes preroll; player length is the difference.
func (d *doc) readFileProperties(b []byte) {
	if len(b) < 80 {
		return
	}
	d.fileSize = binary.LittleEndian.Uint64(b[16:24])
	// Range-check before Duration conversion (overflow / negative duration).
	play, playOK := scaledDuration(binary.LittleEndian.Uint64(b[40:48]), hundredNS)
	preroll, prerollOK := scaledDuration(binary.LittleEndian.Uint64(b[56:64]), time.Millisecond)
	if prerollOK {
		d.preroll = preroll
	}
	if playOK && prerollOK && play > preroll {
		d.duration = play - preroll
	}
	d.maxBitrate = binary.LittleEndian.Uint32(b[76:80])
}

// scaledDuration converts ticks; ok=false when the product does not fit int64 ns.
func scaledDuration(ticks uint64, unit time.Duration) (time.Duration, bool) {
	if ticks > uint64(math.MaxInt64)/uint64(unit) {
		return 0, false
	}
	return time.Duration(ticks) * unit, true
}

// markerEntryLen is the fixed part of a marker entry.
const markerEntryLen = 30

// readMarkers decodes the Marker Object. Steps by description length (ignores
// entry length field, like ffmpeg). Times stored as-is; preroll applied in
// chapters(). Short lists and out-of-range times are warned.
func (d *doc) readMarkers(b []byte, warnings *[]core.Warning, maxElements int) {
	if len(b) < 24 {
		return
	}
	count := binary.LittleEndian.Uint32(b[16:20])
	pos := 24 + int(binary.LittleEndian.Uint16(b[22:24]))
	short := func(read uint32) {
		*warnings = core.Warn(*warnings, core.WarnMalformedTagEntry,
			fmt.Sprintf("the Marker Object declares %d markers but ends after %d; the rest are not read", count, read))
	}
	for i := uint32(0); i < count; i++ {
		if bits.CheckElementCap(int(i), maxElements, "ASF markers") != nil {
			*warnings = core.Warn(*warnings, core.WarnElementCap,
				"the Marker Object has more markers than the element limit allows; the rest are not read")
			return
		}
		if pos+markerEntryLen > len(b) {
			short(i)
			return
		}
		at, ok := scaledDuration(binary.LittleEndian.Uint64(b[pos+8:pos+16]), hundredNS)
		descLen := uint64(binary.LittleEndian.Uint32(b[pos+26:pos+30])) * 2
		pos += markerEntryLen
		desc := b[pos:]
		cut := descLen > uint64(len(desc))
		if !cut {
			desc = desc[:descLen]
		}
		pos += len(desc)
		if ok {
			d.markers = append(d.markers, marker{at: at, desc: markerDescription(desc)})
		} else {
			*warnings = core.Warn(*warnings, core.WarnMalformedTagEntry,
				fmt.Sprintf("marker %d has a presentation time past any duration; skipped", i+1))
		}
		if cut {
			if i+1 < count {
				short(i + 1)
			}
			return
		}
	}
}

// markerDescription: string up to NUL, or all bytes if terminator omitted.
func markerDescription(b []byte) string {
	if s, _, ok := cutUTF16(b); ok {
		return s
	}
	return utf16String(b)
}

// waveFormatWMALossless: depth lives in codec extra bytes, not fixed WAVEFORMATEX.
const waveFormatWMALossless = 0x0163

// readStreamProperties decodes the first audio stream's WAVEFORMATEX.
// WMA Lossless also reads codec extra for decode depth (16 or 24 only).
func (d *doc) readStreamProperties(b []byte) {
	if len(b) < 54 {
		return
	}
	var streamType guid
	copy(streamType[:], b[0:16])
	if streamType != guidAudioMedia || d.haveAudio {
		return
	}
	// Bounds-check by subtraction (uint32 near 2GiB overflows add-then-check on 32-bit).
	typeLen := int(binary.LittleEndian.Uint32(b[40:44]))
	if typeLen < 16 || typeLen > len(b)-54 {
		return
	}
	w := b[54 : 54+typeLen]
	d.haveAudio = true
	copy(d.waveFormat[:], w[:16])
	d.formatTag = binary.LittleEndian.Uint16(w[0:2])
	d.channels = int(binary.LittleEndian.Uint16(w[2:4]))
	d.sampleRate = int(binary.LittleEndian.Uint32(w[4:8]))
	d.byteRate = int(binary.LittleEndian.Uint32(w[8:12]))
	d.bitsPerSample = int(binary.LittleEndian.Uint16(w[14:16]))
	if d.formatTag == waveFormatWMALossless && typeLen >= 18 {
		if cbSize := int(binary.LittleEndian.Uint16(w[16:18])); cbSize >= 18 && 18+cbSize <= typeLen {
			if depth := int(binary.LittleEndian.Uint16(w[18:20])); depth == 16 || depth == 24 {
				d.losslessDepth = depth
			}
		}
	}
}

var contentDescriptionFields = [5]string{"Title", "Author", "Copyright", "Description", "Rating"}

// readContentDescription: five fixed fields. Rating is suppressed by the name
// table but still walked (length needed for later fields).
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

// readExtendedContentDescription: open-ended "WM/*" descriptors (cover art included).
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

// readHeaderExtension walks nested Metadata / Metadata Library objects.
func (d *doc) readHeaderExtension(b []byte, emit func(tag.Key, string, string), warnings *[]core.Warning, maxElements int) {
	// GUID(16) + reserved(2) + data size(4) precede nested objects.
	if len(b) < 22 {
		return
	}
	dataLen := int(binary.LittleEndian.Uint32(b[18:22]))
	if dataLen < 0 || dataLen > len(b)-22 {
		dataLen = len(b) - 22
	}
	for _, o := range walkObjects(b[22:22+dataLen], 0) {
		switch o.id {
		case guidMetadata, guidMetadataLibrary:
			d.readMetadataRecords(o.body, emit, warnings, maxElements)
		}
	}
}

// readMetadataRecords: per-stream descriptors; scoping not modeled (file-level set).
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
		// Value bound only after nameLen fits (nameLen is uint16, never negative).
		if valueLen < 0 || nameLen > len(b)-pos || valueLen > len(b)-pos-nameLen {
			return
		}
		name := utf16String(b[pos : pos+nameLen])
		pos += nameLen
		d.descriptor(name, valueType, b[pos:pos+valueLen], emit, warnings)
		pos += valueLen
	}
}

// descriptor: pictures into the picture set; else name mapping. Non-picture
// byte arrays omitted (no hex blob).
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

// tracks builds the audio track list from stream and file properties.
func (d *doc) tracks() []core.AudioTrack {
	if !d.haveAudio {
		return nil
	}
	// WMA Lossless: codec extra depth wins over fixed field.
	depth := d.bitsPerSample
	if d.losslessDepth > 0 {
		depth = d.losslessDepth
	}
	t := core.AudioTrack{
		Codec:         codecName(d.formatTag),
		SampleRate:    d.sampleRate,
		Channels:      d.channels,
		BitsPerSample: depth,
		Duration:      d.duration,
	}
	// WAVEFORMATEX byte rate is the stream average (shared clamp).
	switch {
	case d.byteRate > 0:
		t.Bitrate = core.AverageBitrate(int64(d.byteRate), 1)
	case d.maxBitrate > 0:
		t.Bitrate = core.AverageBitrate(int64(d.maxBitrate)/8, 1)
	}
	// Derive sample count from duration; guard float conversion.
	if samples := t.Duration.Seconds() * float64(t.SampleRate); samples > 0 && samples < math.MaxInt64 {
		t.TotalSamples = uint64(samples)
	}
	return []core.AudioTrack{t}
}
