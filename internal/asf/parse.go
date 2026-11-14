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

// maxHeaderBytes bounds the leading read when the Header Object declares a bogus size.
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
	if fatal := bits.Fatal(err); fatal != nil {
		return nil, fatal
	}
	if err != nil || len(head) < objectHeaderLen {
		return nil, fmt.Errorf("%w: ASF file shorter than an object header", waxerr.ErrInvalidData)
	}
	headerLen := int64(binary.LittleEndian.Uint64(head[16:24]))
	// Bogus header size: leave audio extent unknown (avoid fingerprinting whole file).
	if headerLen < 30 || headerLen > size {
		headerLen = 0
	}
	// A declared header is read whole or refused; a bogus one is read up to a fixed window.
	window := headerLen
	if window <= 0 {
		window = min(size, maxHeaderBytes)
	}
	buf, err := bits.ReadSlice(src, 0, window, limit)
	if err != nil {
		return nil, err
	}
	children, err := parseHeaderObject(buf)
	if err != nil {
		return nil, err
	}

	d := &doc{size: size, headerEnd: headerLen}
	if d.dataStart, d.dataEnd, err = locateData(src, headerLen, size, limit); err != nil {
		return nil, err
	}
	r := &reading{seen: map[[2]string]bool{}, maxElements: opts.Limits.MaxElements}

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
			d.readContentDescription(o.body, r)
		case guidExtContentDesc:
			d.readExtendedContentDescription(o.body, r)
		case guidHeaderExt:
			d.readHeaderExtension(o.body, r)
		case guidMarker:
			d.readMarkers(o.body, r)
		}
		d.objects = append(d.objects, objectSummary{id: o.id, size: len(o.body) + objectHeaderLen})
	}

	ts := core.BuildTagSet(r.contribs)
	// WM/TrackNumber may be "3/12"; shared split for consistent pairs.
	tag.NormalizeNumberPairs(&ts)

	media := &core.Media{
		Format:     core.FormatWMA,
		Native:     d,
		Tags:       ts,
		Families:   core.BuildFamilies(r.contribs, core.FamilyASF),
		Pictures:   d.pictures,
		Chapters:   d.chapters(),
		AudioStart: d.dataStart,
		AudioEnd:   d.dataEnd,
		Properties: core.Properties{Container: "ASF", Tracks: d.tracks()},
	}
	warnings := r.warnings
	for _, k := range []tag.Key{tag.Encoder, tag.EncodedBy} {
		if vals, ok := ts.Get(k); ok {
			for _, v := range vals {
				if core.IsTranscoderStamp(v) {
					warnings = core.WarnKeyed(warnings, core.WarnInheritedEncoder, "inherited encoder stamp: "+core.WarnSnippet(v), k)
				}
			}
		}
	}

	for _, name := range r.invalidKeys {
		warnings = core.WarnInvalidKey(warnings, name)
	}
	media.Warnings = r.cuts.warnings(warnings)
	if err := core.SetIdentity(src, media); err != nil {
		return nil, err
	}
	core.SyncTextItems(media)
	return media, nil
}

// reading is what one parse gathers beside the document it fills: the tag values, the warnings, and
// what it keeps only to word them.
type reading struct {
	contribs    []core.Contribution
	seen        map[[2]string]bool // the values gathered, by key
	warnings    []core.Warning
	invalidKeys []string  // descriptors the canonical vocabulary cannot represent
	cuts        cutCounts // texts read up to a NUL that more than padding follows
	maxElements int
}

// emit gathers value for key, once: ffmpeg often writes the Content Description and repeats it as
// Extended Content Description, and an exact (key, value) duplicate folds. Different values still
// contribute.
func (r *reading) emit(key tag.Key, value, source string) {
	if key == "" || value == "" {
		return
	}
	if pair := [2]string{string(key), value}; !r.seen[pair] {
		r.seen[pair] = true
		r.contribs = append(r.contribs, core.Contribution{Key: key, Value: value, Source: source})
	}
}

// cutCounts is the texts cut at a NUL: descriptor names and values, with the keys that read them,
// and marker descriptions.
type cutCounts struct {
	descriptors int
	keys        []tag.Key // in the order met
	listed      map[tag.Key]bool
	markers     int
}

// note counts the cut texts of one descriptor, which key reads when it is not "".
func (c *cutCounts) note(key tag.Key, cut ...bool) {
	n := 0
	for _, b := range cut {
		if b {
			n++
		}
	}
	if c.descriptors += n; n == 0 || key == "" || c.listed[key] {
		return
	}
	if c.listed == nil {
		c.listed = map[tag.Key]bool{}
	}
	c.listed[key] = true
	c.keys = append(c.keys, key)
}

// warnings reports the cut texts ([core.WarnBehindNUL]).
func (c cutCounts) warnings(ws []core.Warning) []core.Warning {
	if c.descriptors > 0 {
		ws = core.WarnBehindNUL(ws, fmt.Sprintf("%d descriptor name(s) or value(s)", c.descriptors), c.keys...)
	}
	if c.markers > 0 {
		ws = core.WarnBehindNUL(ws, fmt.Sprintf("%d marker description(s)", c.markers))
	}
	return ws
}

// locateData finds the Data Object after the Header and returns its packet extent.
// Bound at declared end so trailing Simple Index is not folded into the digest.
// Unknown header length or missing Data Object yields a zero extent; a read that fails fatally
// ([bits.Fatal]) is the error.
func locateData(src core.ReaderAtSized, headerEnd, size, limit int64) (start, end int64, err error) {
	if headerEnd <= 0 || headerEnd+objectHeaderLen > size {
		return 0, 0, nil
	}
	head, err := bits.ReadSlice(src, headerEnd, objectHeaderLen, limit)
	if err != nil {
		return 0, 0, bits.Fatal(err)
	}
	var id guid
	copy(id[:], head[0:16])
	if id != guidData {
		return 0, 0, nil
	}
	objLen := int64(binary.LittleEndian.Uint64(head[16:24]))
	// Body opens with File ID (16) + packet count (8) + reserved (2).
	const dataHeaderLen = objectHeaderLen + 16 + 8 + 2
	// Check with subtraction: headerEnd+objLen can overflow int64.
	if objLen < dataHeaderLen || objLen > size-headerEnd {
		return 0, 0, nil
	}
	return headerEnd + dataHeaderLen, headerEnd + objLen, nil
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
func (d *doc) readMarkers(b []byte, r *reading) {
	warnings, maxElements := &r.warnings, r.maxElements
	if len(b) < 24 {
		return
	}
	count := binary.LittleEndian.Uint32(b[16:20])
	pos := 24 + int(binary.LittleEndian.Uint16(b[22:24]))
	short := func(read uint32) {
		*warnings = core.Warn(*warnings, core.WarnMalformedTagEntry,
			fmt.Sprintf("the Marker Object declares %d markers but ends after %d; the rest are not read", count, read))
	}
	for i := range count {
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
			// The string up to its NUL, or all of it where the terminator is left out.
			text, behindNUL := utf16String(desc)
			if behindNUL {
				r.cuts.markers++
			}
			d.markers = append(d.markers, marker{at: at, desc: text})
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
func (d *doc) readContentDescription(b []byte, r *reading) {
	if len(b) < 10 {
		return
	}
	pos := 10
	for i, name := range contentDescriptionFields {
		n := int(binary.LittleEndian.Uint16(b[i*2 : i*2+2]))
		if n < 0 || pos+n > len(b) {
			return
		}
		text, cut := utf16String(b[pos : pos+n])
		key, _ := mapping.CanonicalASF(name)
		r.emit(key, text, name)
		r.cuts.note(key, cut)
		pos += n
	}
}

// readExtendedContentDescription: open-ended "WM/*" descriptors (cover art included).
func (d *doc) readExtendedContentDescription(b []byte, r *reading) {
	if len(b) < 2 {
		return
	}
	count := int(binary.LittleEndian.Uint16(b[0:2]))
	pos := 2
	for i := range count {
		if bits.CheckElementCap(i, r.maxElements, "ASF descriptors") != nil {
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
		name, nameCut := utf16String(b[pos : pos+nameLen])
		pos += nameLen
		valueType := binary.LittleEndian.Uint16(b[pos : pos+2])
		valueLen := int(binary.LittleEndian.Uint16(b[pos+2 : pos+4]))
		pos += 4
		if valueLen < 0 || pos+valueLen > len(b) {
			return
		}
		key, valueCut := d.descriptor(name, valueType, b[pos:pos+valueLen], r)
		r.cuts.note(key, nameCut, valueCut)
		pos += valueLen
	}
}

// readHeaderExtension walks nested Metadata / Metadata Library objects.
func (d *doc) readHeaderExtension(b []byte, r *reading) {
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
			d.readMetadataRecords(o.body, r)
		}
	}
}

// readMetadataRecords: per-stream descriptors; scoping not modeled (file-level set).
func (d *doc) readMetadataRecords(b []byte, r *reading) {
	if len(b) < 2 {
		return
	}
	count := int(binary.LittleEndian.Uint16(b[0:2]))
	pos := 2
	for i := range count {
		if bits.CheckElementCap(i, r.maxElements, "ASF metadata records") != nil {
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
		name, nameCut := utf16String(b[pos : pos+nameLen])
		pos += nameLen
		key, valueCut := d.descriptor(name, valueType, b[pos:pos+valueLen], r)
		r.cuts.note(key, nameCut, valueCut)
		pos += valueLen
	}
}

// descriptor: pictures into the picture set; else name mapping. Non-picture
// byte arrays omitted (no hex blob). It returns the key the descriptor reads as, "" for none, and
// whether its text was cut at a NUL ([utf16String]).
func (d *doc) descriptor(name string, valueType uint16, value []byte, r *reading) (key tag.Key, cut bool) {
	if isPictureName(name) {
		p, err := decodePicture(value)
		if err != nil {
			r.warnings = core.Warn(r.warnings, core.WarnInvalidPicture, err.Error())
			return "", false
		}
		d.pictures = append(d.pictures, p)
		return "", false
	}
	if valueType == valBytes {
		return "", false
	}
	text, cut, ok := descriptorText(valueType, value)
	if !ok {
		return "", false
	}
	key, ok = mapping.CanonicalASF(name)
	if !ok {
		if mapping.ASFUnrepresentable(name) {
			r.invalidKeys = append(r.invalidKeys, name)
		}
		return "", cut
	}
	r.emit(key, text, name)
	return key, cut
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
	// Derive sample count from duration; bound the float conversion.
	if samples := t.Duration.Seconds() * float64(t.SampleRate); samples > 0 && samples < math.MaxInt64 {
		t.TotalSamples = uint64(samples)
	}
	return []core.AudioTrack{t}
}
