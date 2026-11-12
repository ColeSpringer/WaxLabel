package testfile

import (
	"bytes"
	"encoding/binary"
	"slices"
)

func MP4Atom(name string, body []byte) []byte {
	return slices.Concat(BE32(8+len(body)), []byte(name), body)
}

// MP4Data is a "data" atom: version 0, a 24-bit type, locale 0, then the value.
func MP4Data(typ uint32, value []byte) []byte {
	return MP4Atom("data", slices.Concat(BE32(int(typ&0x00FFFFFF)), []byte{0, 0, 0, 0}, value))
}

// MP4Text is an ilst text item: one UTF-8 data atom.
func MP4Text(name, value string) []byte {
	return MP4Atom(name, MP4Data(1, []byte(value)))
}

func MP4Meta(children ...[]byte) []byte {
	return MP4Atom("meta", append([]byte{0, 0, 0, 0}, slices.Concat(children...)...))
}

func MP4Ilst(items ...[]byte) []byte { return MP4Atom("ilst", slices.Concat(items...)) }

func MP4HdlrMdir() []byte {
	return MP4Atom("hdlr", slices.Concat(make([]byte, 8), []byte("mdirappl"), make([]byte, 9)))
}

// MP4HdlrMdta is the handler of a meta whose ilst items are keyed by index into a sibling keys
// box, as ffmpeg's "+use_metadata_tags" writes.
func MP4HdlrMdta() []byte {
	return MP4Atom("hdlr", slices.Concat(make([]byte, 8), []byte("mdta"), make([]byte, 12)))
}

// MP4Keys is a keys box of names under the "mdta" namespace; entry i is ilst index i+1.
func MP4Keys(names ...string) []byte {
	body := slices.Concat([]byte{0, 0, 0, 0}, BE32(len(names)))
	for _, n := range names {
		body = slices.Concat(body, BE32(8+len(n)), []byte("mdta"), []byte(n))
	}
	return MP4Atom("keys", body)
}

// MP4KeyItem is an mdta ilst item of one UTF-8 value, named by its 1-based keys index.
func MP4KeyItem(index int, value string) []byte {
	return MP4KeyItemData(index, MP4Data(1, []byte(value)))
}

// MP4KeyItemData is MP4KeyItem over rendered data atoms.
func MP4KeyItemData(index int, data []byte) []byte {
	return slices.Concat(BE32(8+len(data)), BE32(index), data)
}

// MP4MdtaFile is a file whose meta holds an mdta handler, a keys box of names and one item of
// values[i] for each.
func MP4MdtaFile(names, values []string) []byte {
	items := make([][]byte, 0, len(values))
	for i, v := range values {
		items = append(items, MP4KeyItem(i+1, v))
	}
	return MP4Assemble(MP4HdlrMdta(), MP4Keys(names...), MP4Ilst(items...))
}

func MP4HdlrSoun() []byte {
	return MP4Atom("hdlr", slices.Concat(make([]byte, 8), []byte("soun"), make([]byte, 12)))
}

// MP4Mdhd is a media header declaring the given timescale and a one-second duration.
func MP4Mdhd(timescale int) []byte {
	return MP4Atom("mdhd", slices.Concat([]byte{0, 0, 0, 0}, make([]byte, 8), BE32(timescale), BE32(timescale)))
}

// MP4StsdAudio is an stsd with a single mp4a entry: stereo, 16-bit, 44100 Hz.
func MP4StsdAudio() []byte {
	return MP4Stsd(MP4StsdEntry("mp4a", 2, 16, 44100))
}

func MP4Stsd(entries ...[]byte) []byte {
	return MP4Atom("stsd", slices.Concat([]byte{0, 0, 0, 0}, BE32(len(entries)), slices.Concat(entries...)))
}

// MP4StsdEntry is a version 0 AudioSampleEntry with the given codec configuration boxes as
// children.
func MP4StsdEntry(fourcc string, channels, sampleSize, rate int, children ...[]byte) []byte {
	rateField := 0
	if rate > 0 && rate <= 0xFFFF {
		rateField = rate << 16
	}
	return MP4Atom(fourcc, slices.Concat(
		make([]byte, 6), []byte{0, 1}, // reserved + data_ref_index
		make([]byte, 8),                                      // reserved
		BE16(channels), BE16(sampleSize), []byte{0, 0, 0, 0}, // predefined + reserved
		BE32(rateField),
		slices.Concat(children...),
	))
}

// MP4AlacCookie is the ALAC magic cookie box: a FullBox header and the 24-byte ALACSpecificConfig.
func MP4AlacCookie(rate, channels, depth int) []byte {
	cfg := make([]byte, 24)
	binary.BigEndian.PutUint32(cfg[0:4], 4096) // frameLength
	cfg[5] = byte(depth)
	cfg[6], cfg[7], cfg[8] = 40, 10, 14 // pb, mb, kb
	cfg[9] = byte(channels)
	binary.BigEndian.PutUint16(cfg[10:12], 255) // maxRun
	binary.BigEndian.PutUint32(cfg[20:24], uint32(rate))
	return MP4Atom("alac", slices.Concat([]byte{0, 0, 0, 0}, cfg))
}

// MP4EsdsShort is an MPEG-4 audio esds with one-byte descriptor lengths.
func MP4EsdsShort(asc []byte) []byte { return MP4EsdsRaw(0x40, asc, MP4DescrShort) }

// MP4EsdsRaw is an esds with the given objectTypeIndication and descriptor length encoding; a nil
// asc leaves out the DecoderSpecificInfo.
func MP4EsdsRaw(oti byte, asc []byte, descr func(tag byte, body []byte) []byte) []byte {
	var dsi []byte
	if asc != nil {
		dsi = descr(0x05, asc)
	}
	// objectTypeIndication, streamType, bufferSizeDB, maxBitrate, avgBitrate.
	decoderCfg := descr(0x04, slices.Concat([]byte{oti, 0x15}, make([]byte, 11), dsi))
	es := descr(0x03, slices.Concat([]byte{0, 1, 0}, decoderCfg)) // ES_ID, no optional fields
	return MP4Atom("esds", slices.Concat([]byte{0, 0, 0, 0}, es))
}

// MP4DescrShort is a descriptor with a one-byte length, valid for a body under 128 bytes.
func MP4DescrShort(tag byte, body []byte) []byte {
	return slices.Concat([]byte{tag, byte(len(body))}, body)
}

func MP4Stco(offset uint32) []byte {
	return MP4Atom("stco", slices.Concat([]byte{0, 0, 0, 0}, BE32(1), BE32(int(offset))))
}

// MP4MoovStsd is a moov holding one audio trak, with the given stsd and media timescale, a stco
// entry at stcoOff and stblExtra in its stbl, then udta and moovExtra.
func MP4MoovStsd(udta []byte, stcoOff uint32, stblExtra, moovExtra, stsd []byte, timescale int) []byte {
	stbl := MP4Atom("stbl", slices.Concat(stsd, MP4Stco(stcoOff), stblExtra))
	minf := MP4Atom("minf", stbl)
	mdia := MP4Atom("mdia", slices.Concat(MP4HdlrSoun(), MP4Mdhd(timescale), minf))
	trak := MP4Atom("trak", mdia)
	return MP4Atom("moov", slices.Concat(trak, udta, moovExtra))
}

func MP4Ftyp() []byte {
	return MP4Atom("ftyp", slices.Concat([]byte("M4A "), BE32(0), []byte("M4A mp42isom")))
}

// MP4AssembleUdta is ftyp, moov(trak, udta(udtaKids)) and mdat, the stco entry pointing at the mdat
// payload.
func MP4AssembleUdta(udtaKids ...[]byte) []byte {
	return MP4AssembleExtra(nil, nil, udtaKids...)
}

// MP4AssembleExtra is MP4AssembleUdta with an stbl child (a saio, say) and a moov child (an mvex),
// built in the same two passes so the stco entry and every box size stay right.
func MP4AssembleExtra(stblExtra, moovExtra []byte, udtaKids ...[]byte) []byte {
	return MP4AssembleLeading(nil, stblExtra, moovExtra, udtaKids...)
}

// MP4AssembleLeading is MP4AssembleExtra with bytes placed before ftyp, for the free/skip/wide box
// some writers reserve there.
func MP4AssembleLeading(leading, stblExtra, moovExtra []byte, udtaKids ...[]byte) []byte {
	return MP4AssembleStsd(MP4StsdAudio(), leading, stblExtra, moovExtra, 44100, udtaKids...)
}

// MP4AssembleStsd is MP4AssembleLeading with the audio track's stsd and media timescale supplied.
func MP4AssembleStsd(stsd, leading, stblExtra, moovExtra []byte, timescale int, udtaKids ...[]byte) []byte {
	mdatPayload := bytes.Repeat([]byte{0xA7}, 120)
	build := func(stcoOff uint32) []byte {
		udta := MP4Atom("udta", slices.Concat(udtaKids...))
		return slices.Concat(leading, MP4Ftyp(),
			MP4MoovStsd(udta, stcoOff, stblExtra, moovExtra, stsd, timescale), MP4Atom("mdat", mdatPayload))
	}
	return build(uint32(bytes.Index(build(0), []byte("mdat")) + 4))
}

// MP4Assemble is a file whose udta holds one meta box of metaKids.
func MP4Assemble(metaKids ...[]byte) []byte {
	return MP4AssembleUdta(MP4Meta(metaKids...))
}

// MP4Tagged is a file whose meta holds an mdir hdlr and an ilst of items.
func MP4Tagged(items ...[]byte) []byte {
	return MP4Assemble(MP4HdlrMdir(), MP4Ilst(items...))
}

// MP4AudioTrakChap is an audio trak (track_id 1) referencing the chapter text track through a tref
// "chap", with its single stco entry at audioStco.
func MP4AudioTrakChap(chapTrackID int, audioStco uint32, stblExtra ...[]byte) []byte {
	stbl := MP4Atom("stbl", slices.Concat(MP4StsdAudio(), MP4Stco(audioStco), slices.Concat(stblExtra...)))
	minf := MP4Atom("minf", stbl)
	mdia := MP4Atom("mdia", slices.Concat(MP4HdlrSoun(), MP4Mdhd(44100), minf))
	return MP4Atom("trak", slices.Concat(mp4Tkhd(1), MP4TrefChap(chapTrackID), mdia))
}

// MP4SoundTrak is an audio trak with one chunk at stco; extra goes between its tkhd and mdia, where
// a tref does.
func MP4SoundTrak(trackID int, stco uint32, extra ...[]byte) []byte {
	stbl := MP4Atom("stbl", slices.Concat(MP4StsdAudio(), MP4Stco(stco)))
	mdia := MP4Atom("mdia", slices.Concat(MP4HdlrSoun(), MP4Mdhd(44100), MP4Atom("minf", stbl)))
	return MP4Atom("trak", slices.Concat(mp4Tkhd(trackID), slices.Concat(extra...), mdia))
}

// MP4VideoTrak is MP4SoundTrak for a video trak.
func MP4VideoTrak(trackID int, stco uint32, extra ...[]byte) []byte {
	hdlr := MP4Atom("hdlr", slices.Concat(make([]byte, 8), []byte("vide"), make([]byte, 12)))
	mdia := MP4Atom("mdia", slices.Concat(hdlr, MP4Mdhd(600), MP4Atom("minf", MP4Atom("stbl", MP4Stco(stco)))))
	return MP4Atom("trak", slices.Concat(mp4Tkhd(trackID), slices.Concat(extra...), mdia))
}

func mp4Tkhd(trackID int) []byte {
	return MP4Atom("tkhd", slices.Concat([]byte{0, 0, 0, 0}, make([]byte, 8), BE32(trackID), make([]byte, 4)))
}

// MP4Mvhd is a version 1 movie header of the given timescale and duration, whose next track id
// is 0 (unset).
func MP4Mvhd(timescale uint32, duration uint64) []byte {
	p := make([]byte, 112)
	p[0] = 1
	binary.BigEndian.PutUint32(p[20:], timescale)
	binary.BigEndian.PutUint64(p[24:], duration)
	return MP4Atom("mvhd", p)
}

// MP4Edts is an edit list of version 0 entries, each a segment duration in movie units and a
// media time, -1 for an empty edit.
func MP4Edts(entries ...[2]int64) []byte {
	p := slices.Concat([]byte{0, 0, 0, 0}, BE32(len(entries)))
	for _, e := range entries {
		p = slices.Concat(p, BE32(int(e[0])), BE32(int(e[1])), BE32(0x00010000))
	}
	return MP4Atom("edts", MP4Atom("elst", p))
}

// MP4TrefChap is a tref whose one "chap" entry names ids.
func MP4TrefChap(ids ...int) []byte {
	var list []byte
	for _, id := range ids {
		list = append(list, BE32(id)...)
	}
	return MP4Atom("tref", MP4Atom("chap", list))
}

// MP4TracksFile is ftyp, a moov of the traks handed back and an mdat of 120 audio bytes, the chapter
// samples of titles and 40 thumbnail bytes; traks is told where each of the three starts.
func MP4TracksFile(titles []string, traks func(audio, text, thumbs uint32) []byte) []byte {
	audio, thumbs := bytes.Repeat([]byte{0xA7}, 120), bytes.Repeat([]byte{0xD8}, 40)
	samples := MP4SamplesBlob(titles)
	build := func(at uint32) []byte {
		text := at + uint32(len(audio))
		return slices.Concat(MP4Ftyp(), MP4Atom("moov", traks(at, text, text+uint32(len(samples)))),
			MP4Atom("mdat", slices.Concat(audio, samples, thumbs)))
	}
	return build(uint32(bytes.Index(build(0), []byte("mdat")) + 4))
}

// MP4TextTrak is a chapter text trak: one sample per title at the given media timescale, its chunk
// offset at textStco.
func MP4TextTrak(trackID, timescale int, startsMS []int, titles []string, textStco uint32, stblExtra ...[]byte) []byte {
	stco := MP4Atom("stco", slices.Concat([]byte{0, 0, 0, 0}, BE32(1), BE32(int(textStco))))
	return textTrak(trackID, timescale, startsMS, titles, stco, stblExtra...)
}

// MP4TextTrakCo64 is MP4TextTrak with its chunk offset in a 64-bit co64 table.
func MP4TextTrakCo64(trackID, timescale int, startsMS []int, titles []string, textCo64 uint64) []byte {
	co64 := MP4Atom("co64", slices.Concat([]byte{0, 0, 0, 0}, BE32(1), binary.BigEndian.AppendUint64(nil, textCo64)))
	return textTrak(trackID, timescale, startsMS, titles, co64)
}

// textTrak is a chapter text trak whose stbl ends with chunkOffsets and then stblExtra.
func textTrak(trackID, timescale int, startsMS []int, titles []string, chunkOffsets []byte, stblExtra ...[]byte) []byte {
	var stts, sizes []byte
	for i, t := range titles {
		delta := 1000
		if i+1 < len(startsMS) {
			delta = startsMS[i+1] - startsMS[i]
		}
		stts = append(stts, slices.Concat(BE32(1), BE32(delta))...)
		sizes = append(sizes, BE32(2+len(t))...)
	}
	n := len(titles)
	stbl := MP4Atom("stbl", slices.Concat(
		MP4Atom("stts", slices.Concat([]byte{0, 0, 0, 0}, BE32(n), stts)),
		MP4Atom("stsc", slices.Concat([]byte{0, 0, 0, 0}, BE32(1), BE32(1), BE32(n), BE32(1))),
		MP4Atom("stsz", slices.Concat([]byte{0, 0, 0, 0}, BE32(0), BE32(n), sizes)),
		chunkOffsets,
		slices.Concat(stblExtra...)))
	mdhd := MP4Atom("mdhd", slices.Concat([]byte{0, 0, 0, 0}, make([]byte, 8), BE32(timescale), BE32(0)))
	hdlr := MP4Atom("hdlr", slices.Concat(make([]byte, 8), []byte("text"), make([]byte, 12)))
	mdia := MP4Atom("mdia", slices.Concat(hdlr, mdhd, MP4Atom("minf", stbl)))
	return MP4Atom("trak", slices.Concat(mp4Tkhd(trackID), mdia))
}

// MP4SamplesBlob is the length-prefixed UTF-8 text samples the text track's stco points at.
func MP4SamplesBlob(titles []string) []byte {
	var blob []byte
	for _, t := range titles {
		blob = append(blob, slices.Concat(BE16(len(t)), []byte(t))...)
	}
	return blob
}

// MP4QTFile is a file with a QuickTime chapter text track (and optional udta children, a chpl say),
// both stco offsets pointing at their data in the trailing mdat.
func MP4QTFile(startsMS []int, titles []string, udtaKids ...[]byte) []byte {
	return MP4QTFileTS(1000, startsMS, titles, udtaKids...)
}

// MP4QTFileTS is MP4QTFile with the chapter track's media timescale supplied.
func MP4QTFileTS(timescale int, startsMS []int, titles []string, udtaKids ...[]byte) []byte {
	audioFiller := bytes.Repeat([]byte{0xA7}, 120)
	samples := MP4SamplesBlob(titles)
	build := func(audioStco, textStco uint32) []byte {
		kids := slices.Concat(MP4AudioTrakChap(2, audioStco), MP4TextTrak(2, timescale, startsMS, titles, textStco))
		if len(udtaKids) > 0 {
			kids = append(kids, MP4Atom("udta", slices.Concat(udtaKids...))...)
		}
		return slices.Concat(MP4Ftyp(), MP4Atom("moov", kids), MP4Atom("mdat", slices.Concat(audioFiller, samples)))
	}
	audioOff := bytes.Index(build(0, 0), []byte("mdat")) + 4
	return build(uint32(audioOff), uint32(audioOff+len(audioFiller)))
}

// MP4SampleTables is a track's stts, stsc, stsz and stco: chunk i lies at offs[i] and holds
// samples of the sizes in sizes[i].
func MP4SampleTables(offs []uint32, sizes [][]int) []byte {
	var stsc, stsz, stco []byte
	n := 0
	for i, chunk := range sizes {
		stsc = slices.Concat(stsc, BE32(i+1), BE32(len(chunk)), BE32(1))
		for _, size := range chunk {
			stsz = append(stsz, BE32(size)...)
		}
		stco = append(stco, BE32(int(offs[i]))...)
		n += len(chunk)
	}
	return slices.Concat(
		MP4Atom("stts", slices.Concat([]byte{0, 0, 0, 0}, BE32(1), BE32(n), BE32(1024))),
		MP4Atom("stsc", slices.Concat([]byte{0, 0, 0, 0}, BE32(len(sizes)), stsc)),
		MP4Atom("stsz", slices.Concat([]byte{0, 0, 0, 0}, BE32(0), BE32(n), stsz)),
		MP4Atom("stco", slices.Concat([]byte{0, 0, 0, 0}, BE32(len(offs)), stco)))
}

// MP4Trak is a trak of the given handler and track id whose stbl holds stbl, with a one-second
// media header; extra goes between its tkhd and mdia.
func MP4Trak(handler string, trackID int, stbl []byte, extra ...[]byte) []byte {
	hdlr := MP4Atom("hdlr", slices.Concat(make([]byte, 8), []byte(handler), make([]byte, 12)))
	mdia := MP4Atom("mdia", slices.Concat(hdlr, MP4Mdhd(44100), MP4Atom("minf", MP4Atom("stbl", stbl))))
	return MP4Atom("trak", slices.Concat(mp4Tkhd(trackID), slices.Concat(extra...), mdia))
}

// MP4AVFile is ftyp, a moov of a movie header, a sound track, a video track when video is given
// and moovKids, then one mdat interleaving the chunks, each audio chunk ahead of the video chunk
// of its index. An audio chunk is two samples.
func MP4AVFile(audio, video [][]byte, moovKids ...[]byte) []byte {
	build := func(at uint32) []byte {
		var mdat []byte
		var aOffs, vOffs []uint32
		var aSizes, vSizes [][]int
		for i := range max(len(audio), len(video)) {
			if i < len(audio) {
				aOffs, aSizes = append(aOffs, at+uint32(len(mdat))), append(aSizes, []int{len(audio[i]) / 2, len(audio[i]) - len(audio[i])/2})
				mdat = append(mdat, audio[i]...)
			}
			if i < len(video) {
				vOffs, vSizes = append(vOffs, at+uint32(len(mdat))), append(vSizes, []int{len(video[i])})
				mdat = append(mdat, video[i]...)
			}
		}
		kids := slices.Concat(MP4Mvhd(1000, 1000), MP4Trak("soun", 1, slices.Concat(MP4StsdAudio(), MP4SampleTables(aOffs, aSizes))))
		if len(video) > 0 {
			kids = append(kids, MP4Trak("vide", 2, MP4SampleTables(vOffs, vSizes))...)
		}
		return slices.Concat(MP4Ftyp(), MP4Atom("moov", slices.Concat(kids, slices.Concat(moovKids...))), MP4Atom("mdat", mdat))
	}
	return build(uint32(bytes.Index(build(0), []byte("mdat")) + 4))
}

// MP4FrameCountedFile is a file whose sound track counts audio frames, as QuickTime writes
// uncompressed and fixed-ratio audio: one stts entry of duration 1, and an stsz that gives one
// size for all. Its two chunks of frames samples each lie around 300 bytes that are no audio;
// chunkBytes is what each chunk really holds. It returns where the first chunk starts, where the
// bytes between start, and where the second chunk starts.
func MP4FrameCountedFile(entry []byte, frames, stszSize, chunkBytes int) (data []byte, first, between, second int) {
	build := func(at uint32) []byte {
		stbl := slices.Concat(MP4Stsd(entry),
			MP4Atom("stts", slices.Concat([]byte{0, 0, 0, 0}, BE32(1), BE32(2*frames), BE32(1))),
			MP4Atom("stsc", slices.Concat([]byte{0, 0, 0, 0}, BE32(1), BE32(1), BE32(frames), BE32(1))),
			MP4Atom("stsz", slices.Concat([]byte{0, 0, 0, 0}, BE32(stszSize), BE32(2*frames))),
			MP4Atom("stco", slices.Concat([]byte{0, 0, 0, 0}, BE32(2), BE32(int(at)), BE32(int(at)+chunkBytes+300))))
		mdat := slices.Concat(bytes.Repeat([]byte{0xA1}, chunkBytes), bytes.Repeat([]byte{0xEE}, 300), bytes.Repeat([]byte{0xA2}, chunkBytes))
		return slices.Concat(MP4Ftyp(), MP4Atom("moov", MP4Trak("soun", 1, stbl)), MP4Atom("mdat", mdat))
	}
	at := bytes.Index(build(0), []byte("mdat")) + 4
	return build(uint32(at)), at, at + chunkBytes, at + chunkBytes + 300
}
