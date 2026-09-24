# Changelog

All notable changes to this project are documented here.

## [1.8.0]

### Fixed

- QuickTime and ISOBMFF audio fourccs (`.mp3`, `sowt`, `lpcm`, `ipcm`, `fl32`, `fpcm`,
  `ulaw`, `ima4`, ...) report the canonical codec with the fourcc as `codecProfile`, so a
  `.mov` MP3 reads `MP3`/`.mp3` like its `mp4a` twin. A QuickTime `ms` + WAVE-format-tag
  fourcc names the codec that tag names in a WAV or WMA file.
- Uncompressed MP4 entries report their real bit width, not the fixed 16 the sample entry
  stores: from the fourcc for `in24`/`in32`/`fl32`/`fl64`/`ulaw`/`alaw`/`ima4`, the `pcmC`
  box for `ipcm`/`fpcm`, and a v2 entry's format flags for a float `lpcm` stream.
- A hi-res ISOBMFF `ipcm`/`fpcm` track reports its sample rate. The 16.16 entry field holds
  nothing above 65535 and these entries carry no configuration, so the rate came out as 0.
- AIFF-C compression types report the same codec and `codecProfile` as the matching
  `.mov` fourcc: `sowt` reads `PCM`/`sowt`, `ima4` reads `IMA ADPCM`/`ima4`, `.mp3` reads
  `MP3`/`.mp3`, and a `ms` + WAVE-format-tag type names that tag's codec. The AIFF-only
  names (`AIFF-C XYZ!`, `PCM (little-endian)`) are gone.
- WAV and WMA name format tag 0x0050 `MP2`.
- WMA Lossless bit depth comes from the codec extra bytes, where decoders read it, not
  from the decorative `wBitsPerSample`.
- Declared lengths in a WMA file and a TIFF cover image are bounds-checked against the
  remaining bytes. Adding them to an offset first let a crafted length overflow negative and
  pass the check, which panicked a 32-bit build. A WMA Data Object declaring an impossible
  length reports an unknown audio extent on any platform.
- AIFF-C `ima4` and MACE report their real length. COMM's `numSampleFrames` counts packets
  for those types, so an `ima4` file read 64 times too short. Sample count, duration and
  bitrate follow the packet layout; `ima4` reads 4-bit and `alaw`/`ulaw` 8-bit whatever
  COMM says; `mac3` reads as `MAC3`, as ffmpeg does; a packing the reader cannot size keeps
  the declared count and reports no bitrate.
- An AIFF whose layout the reader can size never reports more audio than its SSND chunk
  holds, and reports `truncated-audio` when COMM overstates. This also covers a well-formed
  file with an overstated COMM, such as an `ima4` written with the spec-literal frame count,
  as WAV does. The document a write returns agrees with a fresh parse of its output.
- A hostile AIFF COMM whose bitrate exceeds `int32` reports the saturated maximum rather
  than an intermediate clamp divided down again.
- The document an MP4 write returns carries the `invalid-tag-key` warnings a fresh parse of
  the output raises for the preserved freeform items.
- An AIFF sample width that is not a whole number of bytes reports the bitrate of the stored
  bytes, as ffprobe does: a 20-bit stream stores 3 bytes per sample, so stereo 44100 Hz
  reads 2116800 bps, not the 1764000 the declared width implied.

### Changed

- **The ASF essence extent is `asf-packets-v2`**, salting the WAVEFORMATEX fixed fields as
  stored, including the full-width byte rate and the block align. v1 kept 16 bits of the
  byte rate, so streams whose rates differed by a multiple of 65536 salted alike.
  `verify` labels the change; stored `asf-packets-v1` digests stay labeled v1 and never
  compare equal, so every WMA digest needs a rehash.
- `dump` shows no bit depth for lossy WMA (v1, v2, Pro, Voice).

## [1.7.0]

### Added

- `Document.Transfer()`, a builder for a metadata copy with a replacement timeline:
  `SetChapters` and `SetSyncedLyrics` write the given list in place of the source's, so a
  caller that has remapped a cut copies in one write. A replacement is written as given
  against the destination's timeline, so a final chapter meant to run to the destination's
  end carries a zero `End`; passing none clears the destination's own.
  `PlanTransfer`/`PrepareTransfer` are unchanged.
- The Ogg Opus output gain, the decoder-applied gain `OpusHead` declares. It reads into
  `Properties` and shows in `dump` (`gain -3.50 dB`, `outputGainDb` in `--json`, an
  `OpusHead` note in `--native`), `caps` (`output gain`), and `diff`, which no longer calls
  two files identical when only their header gain differs. `Editor.SetOutputGain` and
  `set --output-gain DB` write it, leaving every audio page byte for byte, and touch page 0
  alone when no R128 rebase accompanies it. The unit is the raw Q7.8 integer the spec and
  the R128 tags use; the CLI takes decibels. RFC 7845 applies `R128_TRACK_GAIN` and
  `R128_ALBUM_GAIN` on top of the header, so a gain edit rebases them by the same change and
  playback loudness is unchanged, unless the same edit sets, clears, or keeps them
  (`WithKeepR128Gains`, `--keep-r128`). A rebase outside the signed 16-bit range refuses the
  edit. New codes: `output-gain-unsupported`, for a format that stores none (`--strict`
  escalates it), and `output-gain-r128-tags`, an advisory for an R128 tag a gain change left
  alone, either kept by request or not a Q7.8 integer.
- QuickTime version 2 sound sample entries, the shape ffmpeg writes into a hi-res `.mov`:
  their float64 rate and 32-bit channel count are read, so such a file reports its geometry.
- HEIF/HEIC, AVIF and JPEG XL in the image sniffer, so such a cover embeds under its own
  type instead of as `application/octet-stream` under `--force`. The image-sequence brands
  report their registered types (`image/heic-sequence` and siblings), and a JPEG XL
  codestream reports its canvas size. Cover file names built from a MIME (a Matroska
  `cover.<ext>` attachment, an APE Cover Art item) cover every format the sniffer knows, so
  a WebP or TIFF cover is no longer named `.jpg` or left without an extension.
- `UnrecognizedMIME`, `LinkMIME` and `RecognizedImageFormats` name the MIME a junk cover
  reads under, the `-->` URL-link sentinel, and the formats `IsRecognizedImage` accepts, so
  callers need not hardcode them.
- `Finding.Fixable` and the `fixable` field of `lint --json` say whether `lint --fix` acts on a
  finding, decided by the same gates the fix applies.
- `set` and `copy` take `--id3-multi null|repeat|slash`, the CLI form of `WithID3MultiValue`,
  so an MP3 can be written with one frame per value for readers that do not split the
  NUL-separated form.
- `waxlabel clean DIR...` lists the `.waxlabel-*.tmp` files an interrupted write left beside
  its target and deletes them with `--remove`; files newer than an hour are skipped unless
  `--all`. `--recursive` follows a symlinked root and reports an unreadable subtree as an
  `io` error (exit 6) while cleaning the rest. Recursive `dump`/`plan`/`set`/`lint`/`verify`
  count the leftovers they pass by the same rule, so the note never points at a command that
  then finds nothing. `IsTempFileName`, `TempFilePrefix` and `TempFileSuffix` expose the
  naming rule.
- RIFF INFO `ITCH` (ffmpeg's `encoded_by`) and `IENG` read and write as `ENCODEDBY` and
  `ENGINEER`. An MP4 mdta name that is not a valid key as spelled (`custom_key`,
  `com.apple.quicktime.author`, `org.example.thing`) folds to a custom key (`CUSTOM_KEY`,
  `AUTHOR`, `ORG.EXAMPLE.THING`) so `copy` and `diff` see it; this surfaces a phone
  recording's `MAKE`, `MODEL` and `LOCATION.ISO6709`. An edit rewrites the file's entry
  under its original spelling. `tag.FoldKey` is the folding rule.

### Changed

- `durationMs`, chapter `startMs`/`endMs`, synced-lyric `timeMs`, and the human chapter and
  lyric timestamps round to the nearest millisecond instead of truncating. `diff` still
  compares the exact stored offsets, so a Musepack chapter starting between two milliseconds
  (it stores sample offsets) still differs from its copy into a whole-millisecond store
  (ID3 `CHAP`).
- Every LRC line the parser drops counts in `synced-lyrics-line-dropped`, including a bare
  `[Chorus]`-style section header; only LRC metadata tags and blank lines are exempt.
  `--strict` therefore refuses an LRC file with section headers.
- Human output escapes the Unicode bidirectional controls, zero width space, word joiner,
  byte order mark and line/paragraph separators as `\uXXXX`, and `dump`'s tag table
  prints a line break as `\x0a` for every key except LYRICS, COMMENT, DESCRIPTION and
  LONGDESCRIPTION, whose values keep the indented multi-line display.
- **The Ogg Opus essence extent is `ogg-opus-packets-v2`**, hashing the `OpusHead` with its
  `output_gain` masked, so a gain edit keeps the digest and two copies differing only in
  gain dedup. `verify` labels the change; stored `ogg-opus-packets-v1` digests stay labeled
  v1 and never compare equal, so every Opus digest needs a rehash.

### Fixed

- A picture whose bytes no decoder can read reports the unrecognized MIME with no
  dimensions, whatever its container declared (an APIC or FLAC PICTURE claiming `image/png`
  over junk), so `lint` flags it, `export-picture` labels it as such, and a transfer never
  re-labels it as the claimed type. `Editor.AddPicture` applies the same rule to a caller's
  MIME. A `-->` URL-link picture keeps its declaration, since it describes its payload
  rather than claiming an image format, so a picture edit no longer rewrites an ID3 or FLAC
  link as a broken cover holding a URL.
- A cover in an image format the sniffer does not know is carried into Matroska again. Its
  picture capability claimed only `image/*`, while its writer and reader handle the
  unrecognized MIME under the cover-art name, so a transfer dropped a cover the write would
  have stored.
- A WAV's LIST/INFO items untouched by an edit are copied verbatim rather than regenerated
  from the merged tag set, so an INFO value the id3 chunk disagrees with, a second
  identifier for the same key (`IPRT` and `ITRK`), and duplicate items survive an unrelated
  edit, which no longer spawns an id3 chunk to hold INFO's duplicates. An explicit set of a
  conflicting key re-renders the INFO item and reports
  `LIST/INFO conflict resolved (KEY)`; a track edit updates every identifier that carried it.
  AIFF's `NAME`, `AUTH`, `(c) ` and `ANNO` chunks follow the same rule, reporting
  `native text chunk conflict resolved (KEY)`.
- WAV and AIFF report what a write does to their native tag container: an edit that clears
  every value reports the deletion (`LIST/INFO drop`, `native text chunk drop`, and the same
  for an emptied id3 chunk), and a container left byte for byte no longer claims a rewrite.
  The rewrite line tracks bytes, not values, so a chunk that only loses an unreadable tail
  or moves in a regrouping still reports one.
- `--legacy strip` on an AIFF reports the native text values it destroys. The strip
  consolidates into the ID3 chunk, and a chunk value the projection did not select has no
  canonical key to carry it, so it was deleted without warning.
- `set --set ENCODER=<the stamp the file already carries>` on a WAV keeps the value. The
  writer judged authorship from the value diff alone, so setting the key to what `ISFT`
  already held read as unauthored, and the inherited-stamp strip the CLI enables for any
  `ENCODER` edit removed the value the edit wrote. Both read the same signal: an edit
  naming the key.
- An MP4 whose `stsd` runs past the caller's allocation limit no longer parses with no
  codec and no geometry, which gave the same bytes a different `mp4-mdat-v3` digest per
  limit. The prefix read is clamped to the limit rather than refused.
- Hi-res ALAC and FLAC-in-MP4 report their real sample rate. The sample entry's 16.16 field
  cannot hold a rate above 65535, so a 96 kHz file read back as 65535, 0, or 48000. The
  rate, channel count, bit depth, and FLAC block-size bounds come from the codec
  configuration: the ALAC magic cookie, or the `dfLa` `STREAMINFO` the FLAC-in-ISOBMFF spec
  makes authoritative. The digest salt keeps the raw entry values, so stored MP4 digests
  are unchanged.
- Hi-res AAC-in-MP4 too, from the `esds` AudioSpecificConfig. The config describes the
  core coder while an SBR stream plays at twice its rate, so the entry still matters: a
  config declaring SBR gives the played rate outright (downsampled SBR, whose extension
  rate equals the core rate, is not doubled); a config silent about SBR whose entry rate is
  exactly double an AAC LC core rate is an implicitly signalled stream the muxer already
  decoded, so the entry stands; otherwise the core rate wins, even over a config that
  denies SBR. Raw ADTS is a separate entry below. `codecProfile` reads the object type
  (`AAC LC`, `HE-AAC`, `HE-AAC v2`, `xHE-AAC`) from a config, the `mp4a` fourcc without
  one. Digests unchanged.
- An MP4 `mp4a` entry whose `esds` declares MPEG-1 or MPEG-2 audio reports `MP3` rather than
  `AAC`.
- A Matroska track's `OutputSamplingFrequency` is read, so an HE-AAC `.mka` reports the rate
  a player produces instead of half of it. An `A_AAC*` `CodecPrivate` names the object type
  and, when the track declares no output frequency, supplies the played rate and channel
  count; where it says nothing about SBR the `CodecID` suffix (`/SBR`, `/LC`, ...) names the
  profile. The digest salt stays on `SamplingFrequency`.
- An MP4 `dfLa` box declaring more bytes than its sample entry holds no longer decodes
  `STREAMINFO` out of the bytes that follow the entry.
- `set --strict --set R128_TRACK_GAIN=0`, which `--output-gain`'s help text recommends, is
  accepted rather than refused as an unknown key. The two RFC 7845 keys are validated
  (`lint` reports `malformed-number` for a value that is not a signed 16-bit integer) and
  trimmed like the `REPLAYGAIN_*` keys, and `lint` no longer calls them `custom-key`.
- `copy` excludes `R128_TRACK_GAIN` and `R128_ALBUM_GAIN`, like the `REPLAYGAIN_*` keys:
  they describe the source's audio, so the destination keeps its own.
- An ID3 picture frame missing its description terminator no longer splits inside the
  image at the PNG header's first NUL: the description reads empty and the image is whole,
  for v2.2 `PIC` and v2.3/v2.4 `APIC` alike (`APIC` dropped the frame).
- An ID3v2.2 tag with the compression flag set is ignored, as the spec directs for a scheme
  it never defined; the parse warns `malformed-tag-entry` and a rewrite warns
  `malformed-tag-entry-dropped` (`--strict` refuses).
- `--recursive` reports an unreadable directory as an `io` error entry (exit 6) and
  continues the walk.
- A chapter authored past the media duration reads back with no end on MP3, WAV, AIFF and
  AAC, as on MP4 and the start-only stores; the ID3 `CHAP` frame still carries the bounded
  `end == start` a player needs.
- `tag.Merge` with `Union` treats empty and whitespace-only values as absent, as `FillEmpty`
  does.
- Raw ADTS HE-AAC reports the played rate, channel count and profile, as the same audio in
  MP4 does, instead of the core coder's (22050 Hz, AAC LC for a 44100 Hz HE-AAC stream;
  1 channel for HE-AAC v2). The frames are parsed (AAC-LC syntax through the SBR fill
  element, and a mono core's SBR payload through its parametric stereo extension), so both
  containers agree with ffprobe. Digests are unchanged.

## [1.6.2]

### Fixed

- Musepack SV8 chapter packets are read, where the reference decoder reads them: after
  the seek table the seek-offset packet points at, else the run ending at the end marker.
  A transcode or `copy` out of an `.mpc` carries its chapters, and `dump`, `diff`, and
  `lint` see them. Every rewrite preserves them by copying the stream verbatim; none
  writes them: the capability reads `read full, write none`, a chapter edit is refused (or
  dropped under the unsupported-drop option, warned `chapters-unsupported` with read-only
  wording), and `copy` into an `.mpc` grades chapters dropped as "cannot write". A packet
  stream declaring a stream version other than 8 is refused, as the reference decoder
  refuses it.
- WMA Marker Object entries are read as chapters, on the playback timeline (the preroll
  subtracted, as for the duration), so a WMA audiobook's chapters carry out of it.
- A chapter clear on a file whose chapters WaxLabel reads but cannot write is refused,
  not planned as a no-op that keeps them. The cover-art drop gate takes the same shape,
  so clearing a WebM's cover is dropped with a warning under the unsupported-drop option
  instead of reaching the writer's refusal.
- A caller-supplied source that answers a zero-length read at its end with EOF, as
  `bytes.Reader` does, no longer fails a parse that reads an empty element there.
- The `element-cap` warning code is exported as `WarnElementCap`, so a library caller can
  match it by name like every other code.

## [1.6.1]

### Fixed

- APEv2 cover writes keep item names unique, as the format requires. The Cover Art
  convention has one front and one back item, and the picture set resolves onto those
  two slots: a front or back cover keeps its name, any other role takes a free cover name
  (reading back as that cover; warned `picture-metadata-dropped` and graded lossy by
  `copy`), and an added cover replaces a same-role one the file already had. A picture
  with no name left is refused for library callers without the unsupported-drop option
  and otherwise dropped with a `picture-unsupported` warning that `--strict` escalates;
  `copy` grades it dropped. An undecodable cover item keeps its slot against spilling
  roles and is replaced (warned `malformed-tag-entry-dropped`) only when the edit claims
  its exact name or no other slot is free. A back cover with no description grades as a
  clean carry.
- The same uniqueness holds for every APEv2 item the rebuild authors: a set on a key
  whose name a binary item occupies replaces that item (`tag-structure-dropped`), a
  cover write displaces a text item on its Cover Art name (`value-dropped`), and a text
  value under a `Cover Art` name is refused like a reserved name, with `copy` grading the
  key dropped. Collisions a file already carried are preserved as found, and the
  post-write warnings are recomputed from the written items, so a replaced item's
  parse-time warning does not outlive it.

## [1.6.0]

### Added

- WavPack (`.wv`), Monkey's Audio (`.ape`), and Musepack (`.mpc`), written through APEv2,
  which is now a writable container: items, multi-values, and the `Cover Art` convention.
- Ogg FLAC (`.oga`), sharing the Ogg page layer. Cover art is a native `PICTURE` block.
- WMA/ASF (`.wma`, `.asf`), read-only: Content Description, `WM/*`, and `WM/Picture`.
- RF64 and BW64. `ds64` is recomputed on save-back and the form is never downgraded.
- `.m4r`, `.mpga`, `.adts`, and `.mov` as claimed extensions.
- `dump` reports `paddingBytes` wherever a padding region exists, not only on FLAC: MP3
  and AAC (inside the ID3v2 tag), MP4 (the `free` atom next to `ilst`), and Ogg Opus
  (RFC 7845 comment padding). The figure matches what `plan` reports for an in-place
  write. An Ogg FLAC `PADDING` block is not counted: every Ogg rewrite drops it.
- A note when one `--set` key is given twice with different values, naming the value that
  survived. `--set DATE` plus `--set RECORDINGDATE` was already noted.
- `copy --strict`, which refuses (exit 2) when the projection is not lossless or when
  writing the destination would itself lose metadata, writing nothing. `set --strict`
  already did the second half.
- `dump --native` shows the description a `COMM`, `USLT`, or `TXXX` frame carries, so a
  described frame is identifiable.
- FLAC truncation and trailing-junk detection, from a walk over the frame headers at the
  end of the audio region: a stream stopping short of STREAMINFO's declared sample count
  is flagged truncated, and bytes past the final frame's CRC-located end are flagged and
  carved out of the audio extent (still copied verbatim on rewrites), so a junk-appended
  rip dedup-matches its clean twin under `verify`. The extent name becomes
  `flac-frames-v2`; persisted `flac-frames-v1` digests stay labeled v1.
- QuickTime `keys`/`mdta` metadata, the shape `ffmpeg -movflags +use_metadata_tags`
  writes: the `keys` index is read and its items decoded, so such a file reports its
  tags, and an edit writes keys entries rather than four-character atoms. Apple's
  `com.apple.quicktime.*` names and ffmpeg's bare ones both resolve.
- Classic QuickTime `moov.udta` text atoms (`(c)nam`, `(c)swr`, ...), a plain `.mov`'s
  whole tag store, are read and written. Multi-language entries keep their translations,
  and a value the file also keeps in an `ilst` is rewritten to match. `dump --native`
  lists `moov.udta`.
- `duplicate-tag-block-dropped`, a write-time warning for a rewrite that discards a
  duplicate tag container holding content the written set does not, across WAV, AIFF, FLAC
  and Ogg FLAC. `--strict` escalates it; a redundant duplicate stays silent. Ogg FLAC also
  gains the read-side `multiple-vorbis-comment` warning native FLAC already had.
- `malformed-tag-entry`, one read-side warning for an entry a tag container holds but no
  reader can interpret: a RIFF INFO list missing a word-alignment pad byte, a Vorbis
  comment with no `=`, an ID3 frame or tag header whose declared size overruns. `lint`
  reports it as a warning.
- `malformed-tag-entry-dropped`, its write-side counterpart, for a rewrite that cannot
  carry a region the parser never read. `--strict` escalates it.
- `unknown-chunk-size`, for a WAV or AIFF chunk declaring the `0xFFFFFFFF` size-unknown
  value. `lint` reports it at info severity, so a piped capture still exits 0.
- `lint --fix` reports anything its rewrite destroyed as `lost in the rewrite` (`lost` in
  `--json`). Re-linting cannot show it: the condition is gone from the output.

### Fixed

- A WAV or RF64 LIST/INFO list missing a word-alignment pad byte re-synchronizes; a region
  still unreadable is warned about on read and reported as dropped on write. The reader
  stepped over a byte that was not there and lost every item past the first odd-size one,
  and the next rewrite rebuilt the chunk from what it had read.
- A Vorbis comment entry with no `=` is kept verbatim and rendered back unchanged, across
  FLAC, Ogg Vorbis, Ogg Opus and Ogg FLAC; its own length prefix frames it. It was dropped
  at parse and erased by the next rewrite.
- An ID3 frame whose declared size overruns the tag is warned about, `dump` no longer
  counts the unread remainder as padding, and a rewrite says what it could not carry. A
  front tag header declaring more bytes than the whole file is warned about too, instead
  of reading as no tag.
- The `--strict` refusal says `(omit --strict to continue with a warning)`. The old
  `(omit --strict to write anyway)` was false for the discard family: without the flag the
  item is dropped either way, and for an item the format cannot store nothing is written
  at all.
- A WAV or AIFF chunk declaring the `0xFFFFFFFF` size-unknown value is reported. The clamp
  takes the rest of the file as that chunk, so a LIST/INFO after a sentinel-sized `data`
  chunk is swallowed into the audio extent and the file reads as untagged; the size is
  still clamped.
- An MP4 with a leading `free`, `skip` or `wide` box before `ftyp` is detected; the sniff
  steps over such a box inside its 64-byte window. It was unsupported (exit 3) though the
  parser handles it.
- A LIST/INFO item declaring bytes past its NUL terminator reports them as lost on
  rewrite: the writer emits the value up to the terminator plus one NUL. A run of
  alignment zeros is not reported, since a rewrite re-creates it.
- An RF64/BW64 chunk whose `ds64` entry is missing or unusable keeps its own size and
  clamps like any other overrun, so a truncated file gets `truncated-audio`. Inside those
  containers the 32-bit `0xFFFFFFFF` is always the `ds64` marker, not the plain-RIFF
  streaming sentinel.
- Warnings that quote file-derived text (an inherited encoder stamp, an unreadable comment
  entry, a key the vocabulary cannot represent) elide an oversized value. A comment list
  full of unreadable entries is one warning with a count.
- The family view indexes once per key. Grading each native item against the whole
  authoritative value list cost quadratic time at the element cap.
- `dump --native` accounts for the region of an ID3v2 tag the frame walk could not read,
  as it does for a LIST/INFO chunk, so the block size agrees with the frames listed under
  it.
- A stray leading ID3v2 whose frame walk stopped early counts as opaque legacy content, so
  `lint --fix` keeps it rather than stripping a region nothing could read.
- An MP4's final chapter end is reported verbatim. The QuickTime reader canonicalized an
  end on the movie duration back to open, so `dump` showed `null` where ffprobe showed the
  duration and `SetChapters(End: duration)` did not round-trip. A chapter starting past
  the file end still reads open: that tail is a placeholder the writer invents, not a
  value the file states.
- `copy` and the transfer read one rule for a final chapter running to the source's end of
  file: the transfer opens it and loses nothing, so `copy` no longer grades it lossy.
- `Lavc`/`libavcodec` counts as a transcoder stamp alongside `Lavf`/`libavformat`, so an
  `ENCODER` naming the codec is flagged. `lint --fix` and `--strip-encoder` discard a
  value like `Lavc61.19.101 libopus`.
- Ogg Vorbis reports its measured average bitrate, like every other format, instead of the
  identification header's `bitrate_nominal` encoder target, which read several times high
  on a VBR file. Nominal is kept only when nothing can be measured.
- A WAV or AIFF whose header declares less than its chunks occupy is re-walked against the
  file size, recovering tags and audio, including a `LIST`/`NAME` appended without updating
  the header, which a rewrite duplicated. A truncated or tag-appended file keeps its
  `no-audio`/`trailing-bytes` verdict.
- `--padding` accepts the size suffixes `--max-size` does (`8KiB`, `32k`, `31.5KiB`), and
  rejects a value that would truncate, such as `0.4`, rather than treating it as
  `--no-padding`.
- An empty value for a key ID3 cannot store one for (`GENRE=`, `TRACKNUMBER=`, `DISCNUMBER=`,
  `MOVEMENT=`) reports its drop, and genre no longer leaves a stub `TCON` frame behind; the
  plain text frames still store a present-empty value.
- A plan whose edit was wholly discarded (cover art on WebM) says so, from the same
  predicate `--strict` gates on. It reported "no changes (already up to date)", which
  claims the file holds what was asked for.
- An MP4 key held by both its `ilst` and its `moov.udta` atoms reports the ilst value once,
  with the udta value as a family entry. Merging the two doubled the value and let an
  unrelated edit store both in the ilst, hiding the disagreement.
- The header flag in an APE footer is confirmed before it is believed. A footer claiming
  a header record that is not there moved the tag's start 32 bytes into the audio, so a
  rewrite wrote the tag over it while `--verify` passed, both sides deriving the extent
  from the same wrong offset.
- An APE slash pair (`Track=3/12`) is rewritten as a number plus a total on an edit to
  either half, so clearing a total takes effect and an unrelated edit no longer appends
  a total item the file never had.
- An APE tag whose item list was cut short by the element limit is no longer rewritten,
  which would have deleted every item the parse did not read.
- An APE text item whose bytes are not valid UTF-8 (APEv1's code page) reads as Latin-1
  with a warning instead of poisoning a later `copy`, and its raw bytes still round-trip.
  An APEv1 tag keeps its version and footer-only shape on write.
- WavPack bit depth comes from the storage width, not the magnitude field, which tracks
  how loud the recording is; a 32-bit stream reported 13-bit. A streamed file's
  "unknown" sample count is recognized, instead of reporting 27 hours.
- An absurd declared header size in a Monkey's Audio or WavPack file no longer hides the
  file's real APEv2 tag behind a second one appended on write.
- RF64 truncation is reported when the `ds64` size happens to equal `0xFFFFFFFF`, the
  streaming sentinel that only applies to a size nothing resolved.
- ASF duration, bitrate, and sample count are range-checked, and the audio extent is
  bounded at the Data Object, so a rebuilt index no longer changes a WMA's audio digest.
- An edit to a read-only file reports the format's own refusal for chapters, pictures,
  and synced lyrics, as for tags, instead of exiting 0 with a storage warning.
- `set KEY=` stores an empty value on WavPack, Monkey's Audio, and Musepack, matching
  every other writable format, and `dump` reports it as present-empty, as for LIST/INFO.
  `--clear` still removes the item. A zero-length item in an MP3's trailing APEv2 stays
  uncounted in the legacy view, as ID3v1's blank fields are, so it does not block
  `lint --fix` from stripping an otherwise redundant container.
- `malformed-date` says which fault it found. `"2001-13-01" is not YYYY, YYYY-MM, or
  YYYY-MM-DD` was false: the shape is right and the month does not exist. Such a value
  reads `is not a real date`; a shape fault keeps the old wording.
- `set` with thousands of unknown keys or per-value advisories lists ten `note:` lines and
  counts the rest. The `--strict` failure message still names every key: a strict run
  writes nothing, so that list is the only account of what to fix.
- A malformed value says which fault it found where a category has more than one. `BPM`
  and the MP4 integer keys (`MEDIATYPE`, `ITUNESADVISORY`, `MOVEMENT`, `MOVEMENTTOTAL`)
  name the per-key ceiling a non-negative number exceeds, instead of "is not a
  non-negative number". A negative ReplayGain *peak* is reported as negative rather than
  as an unrecognized ReplayGain value.
- `dump --json` on an RF64 or BW64 file reports `"subformat": "RF64"`/`"BW64"` rather than
  `"WAV"`, which the parser already tracked and preserved on write. `properties.container`
  carries the same value for library callers.
- `--legacy strip` says what it destroys. It removes legacy containers unconditionally, so
  a value living only there died without a word, against the contract that unaffected data
  is preserved and warned about, never stripped. `set`, `copy`, and `--preset minimal` warn
  naming the lost keys, and `--strict` refuses the write. The warning judges the edit's own
  tags, so a strip that also writes the value does not claim to lose it. `lint --fix`
  chooses the strip only when nothing would be lost. On WAV the same flag consolidates
  LIST/INFO into the `id3 ` chunk, which cannot carry an item with no canonical key
  (`IENG`, `ISBJ`); that drop is reported too.
- `copy` onto a read-only destination exits 3 with the codec's own refusal, after printing
  the per-field drops. It exited 0 because nothing was set on the destination editor and
  the write collapsed into a no-op before the codec could refuse. A WMA keeps
  `unsupported-format` and a fragmented MP4 keeps `unsupported-fragmentation`. A transfer
  with nothing to carry still exits 0, and a writable destination that drops an unstorable
  item is unaffected.
- A described ID3 `COMM` frame (Windows Explorer and CDDB-era taggers write one) is read
  as `COMMENT`; it was invisible to `dump`, `lint` and `diff`, and `copy` left it behind
  while reporting a clean carry. On write, a single described frame keeps its description
  and language across an edit, and a merge that cannot keep one warns
  (`comment-description-dropped`, which `--strict` escalates). Machine descriptions
  (`iTunNORM`, `iTunSMPB`, ReplayGain) stay unprojected and untouched. With several
  comment frames, the first frame's language wins rather than the last.
- WAV `ISFT` is `ENCODER` on both sides. A stock ffmpeg WAV showed no `ENCODER` under
  `dump` while `ffprobe` showed `encoder=Lavf`, and writing `ENCODER` created an `id3 `
  chunk for a value LIST/INFO has a slot for. Consequences: clearing `ENCODER` (which
  `--strip-encoder` does) removes the `ISFT` item; a WAV whose `id3 ` chunk and `ISFT`
  disagree reports `conflicting-families` and the next write reconciles them, as for every
  other key both containers hold; and an inherited transcoder stamp is never promoted into
  an `id3 ` chunk a write creates, so an unrelated edit does not author a second copy of
  the stamp the linter flags.
- `lint --fix` no longer restructures a LIST/INFO-only WAV. Reading `IPRT=4/9` splits it
  into a track number and a total; the total had no INFO slot, so the fix spawned an `id3 `
  chunk for it and rewrote `IPRT` to a bare `4`. The pair recombines into the item it came
  from, so the round trip is byte-stable. `DISCNUMBER` has no INFO identifier and still
  promotes the file.
- WAV duration and bitrate for a compressed payload come from the `fact` chunk's sample
  count. A one-second MS-ADPCM file reported 1.408 s at the nominal 128 kbps, and a WAV
  carrying MP3 payload reported both as null. The declared count is sanity-checked first,
  so a hostile `0xFFFFFFFF` falls back instead of reporting 27 hours. `totalSamples` for
  such a format is 0 rather than a block count, which was wrong by three orders of
  magnitude. PCM, IEEE float, A-law and mu-law are unchanged: their byte rate is exact.
- APEv2 no longer writes the item names the specification reserves (`ID3`, `TAG`, `OggS`,
  `MP+`), each the magic another structure is found by. Such a key is dropped with a
  warning that `--strict` escalates, and `copy` grades it dropped. A file that already
  holds such an item keeps it, whether the edit leaves it alone or changes it, so a refused
  write never costs the existing value as well.
- A canonical key cannot contain `~` (0x7E): the rule is the intersection of every format's
  key syntax, and the Vorbis comment specification stops at 0x7D. `~` in a key is exit 2
  (`invalid-key`); a file already carrying one is preserved verbatim, as any unrepresentable
  native key is.
- Every format that holds string keys reports one it cannot represent (`invalid-tag-key`):
  APEv2 items, ID3 `TXXX` descriptions, MP4 freeform names, Matroska `SimpleTag` names and
  ASF `WM/*` descriptors, alongside Vorbis comments. Such a value is preserved on disk but
  never reaches the canonical set, so it was absent from `dump`, `lint` and `diff` while
  `copy` reported a clean carry. Intended exclusions (Matroska's `BPS`/`NUMBER_OF_*`
  statistics, ASF's technical descriptors) are not reported, since nothing is lost there.
- An ID3v2.3 date stored in full but read back respelled (`2001-02-03 10:20` comes back as
  `2001-02-03T10:20`, since the frames store neither separator) is reported as a coercion
  and escalated by `--strict`; it was neither a drop nor a precision loss, so nothing
  reported it. The three date fates come from one predicted-read-back rule.
- `dump`'s human `format:` line names the container rather than the codec family where the
  two differ, so an RF64, BW64, AIFC or WebM file is no longer reported as WAV, AIFF or
  Matroska. `--json` already reported it as `subformat`; `copy` and `caps` say it too.
- `dump`'s `paddingBytes` and `plan`'s padding agree in three more places. A FLAC holding
  several `PADDING` blocks under-reported by four bytes per extra block, which a rewrite
  reclaims when it collapses them. An MP4 whose `free` atom uses the 64-bit largesize form
  under-reported by eight, which a rewrite reclaims by re-rendering the atom with a 32-bit
  header. And a chapters-only MP4 edit planned "padding: none" for a `free` atom the write
  leaves untouched.

### Changed

- `--recursive` descends into `.mov`, `.m4r`, `.mpga`, and `.adts`, and reports each `.wma`
  it finds as failed (exit 3), since WMA cannot be written.
- `caps --format` refuses an extension claimed by more than one format, naming both.
  `.oga` and `.ogg` are now Ogg Vorbis and Ogg FLAC alike; pass `oggflac` to pick FLAC.
  `ogg` still names Ogg Vorbis, as it always has.
- The legacy-conflict warning is gated on a container being legacy, not on its name. An
  APEv2-native file no longer warns about its own tag; a FLAC whose stray leading ID3v2
  disagrees with an edit does.
- An APE `DATE` resolves to `RECORDINGDATE`, and a slashed `Track` splits into the
  canonical number/total pair.
- **`lint` reports more, and some files that were clean will now exit 1.** The new
  findings, all warnings: `chapter-past-duration` and `duplicate-chapter` (raised only at
  write time before, while `set --help` pointed at `lint`); `chained-stream` (which `dump`
  already reported); `trailing-bytes`, for a region of a WAV, AIFF or Ogg belonging to no
  chunk or page (the bytes were already preserved); `oversized-chunk`, which `dump`
  reported but `lint` did not, and which is also how bytes appended after an MP4's last
  atom read; `invalid-tag-key` on four more formats; and `non-conforming-icon`, for a
  file-icon picture that is not the 32x32 PNG ID3v2 requires. Mapping WAV `ISFT` to
  `ENCODER` also makes a WAV whose `id3 ` chunk and `ISFT` disagree a `conflicting-families`
  finding, which `lint --fix` resolves. FLAC gained its own trailing-region detection
  later; see the frame-tail entry above.
- **New refusals, where a write that could not happen used to exit 0 or name the wrong
  fault.** `copy` onto a read-only destination (WMA, a fragmented MP4) is exit 3 instead of
  exit 0, and `~` in a canonical key is exit 2 instead of accepted; both are described
  under Fixed. Authoring a second file-icon picture is exit 3 (`unsupported-tag`) instead
  of exit 4 (`invalid-data`), which called the file corrupt when only the write was
  impossible, and it no longer outranks a corrupt file in a batch run. FLAC and Ogg cap
  chapters at 1000, the size of the `CHAPTERxxx` 3-digit namespace; a file already holding
  more becomes chapter-uneditable at exit 3 while its tag edits keep working, and a `copy`
  from such a source drops the chapter set, as the 255-chapter formats already do.

## [1.5.0]

### Added

- Eight canonical iTunes keys (writable): `ITUNESADVISORY` (content advisory; integer 0-255,
  1 = explicit, 2 = clean, 0 = none, legacy 4 = explicit), `ITUNESGAPLESS` and `SHOWMOVEMENT`
  (booleans), `BPM` (non-negative decimal up to 65535, fractions accepted), `WORK`,
  `MOVEMENTNAME`, and the `MOVEMENT`/`MOVEMENTTOTAL` pair (integers 0-65535, no pair syntax
  at the tag level). Stored as the structured MP4 atoms (`rtng`, `pgap`, `shwm`, `tmpo`,
  `©wrk`, `©mvn`, `©mvi`, `©mvc`), on ID3 as `TBPM`, `MVNM`, and one `MVIN` `n/total` frame
  plus `TXXX` user frames for the rest (`WORK` as Picard's `TXXX:WORK`), and under their own
  names on Vorbis, Matroska, and APE. `ENCODEDBY` now maps to the MP4 `©enc` atom.

### Changed

- The MP4 atoms above project and rebuild like any owned atom; they survived edits only as
  preserved unknown items, invisible to dump, diff, copy, and `Get`.
- ID3 `TBPM` projects as `BPM` instead of the custom key `TBPM`. Consumers keyed on `TBPM`
  must move.
- A freeform `----:ITUNESADVISORY`-style MP4 representation migrates to the structured atom
  on the next edit; a stale ID3 `TXXX:BPM` migrates to `TBPM` on the next edit that changes
  the key. `ENCODEDBY`'s MP4 write spelling moves from a freeform to `©enc`.
- Recognized boolean words canonicalize to `1`/`0` on ID3 `TXXX` frames for boolean keys, so
  `ITUNESGAPLESS=yes` stores `1` on MP3 as it does on FLAC and MP4.
- These names no longer draw the custom-key lint info, and their values are validated: an
  invalid or out-of-range value drops with a warning on MP4 writes (escalated by
  `--strict`) instead of writing a freeform, and a copy into MP4 excludes it (the
  destination keeps its own). A fractional `BPM` rounds to nearest on MP4 with a coercion
  warning, which `--strict` escalates to exit 2 like a drop; a copy of one into MP4 grades
  lossy. An MP4 edit that makes one of these values unstorable keeps the stored value, with
  the warning, matching the track/disc slots.
- `lint` flags a malformed value on these keys (`malformed-number`/`malformed-boolean`,
  exit 1) instead of passing it as custom text. A library carrying free-text BPM values
  ("120-125", "Unknown") flips a lint gate on upgrade.
- ffmpeg folds both `©too` and `©enc` onto its single `encoder` tag, so ffprobe reports
  whichever atom comes later when a file carries `ENCODER` and `ENCODEDBY` together; iTunes
  and Mp3tag keep them distinct.
- Setting a raw mapped frame ID as a key (`--set TBPM=128`) writes the value to that frame,
  which reads back under the canonical key; it wrote nothing once the frame joined the
  mapping table. Same for the other mapped frame IDs.
- Giving a structured single-atom MP4 key several values warns that only the first is
  stored, and a copy grades it lossy; the surplus vanished without a warning (pre-existing
  for `MEDIATYPE`/`COMPILATION` and the track/disc slots).
- Matroska canonicalizes boolean words to `1`/`0` on write like FLAC, ID3, and MP4, so
  `ITUNESGAPLESS=yes` stores `1` on MKA too.
- The Matroska native tag spellings (`PART_NUMBER`, `TOTAL_PARTS`, `TOTAL_DISCS`,
  `LEAD_PERFORMER`, `DATE_RECORDED`, `DATE_RELEASED`, `DATE_RELEASE`, `DATE_ORIGINAL`,
  `ORIGINAL_DATE`, `ENCODED_BY`, `CATALOG_NUMBER`, `PUBLISHER`, `REMIXED_BY`,
  `CONTENT_GROUP`) are aliases of their canonical keys, so `--set PART_NUMBER=x` replaces
  the value; it wrote a custom field that projected back onto `TRACKNUMBER`, an append that
  left a single-valued key holding conflicting values. The aliases apply on every format,
  and Vorbis-family, ID3 `TXXX`, MP4 freeform, and APE reads fold these spellings onto the
  canonical keys, so a set replaces a foreign field stored under the spelling.
- `--strict` escalates the numeric-genre coercion with or without `--numeric-genre`:
  `--set GENRE=17` stores a reference that reads back as `Rock` on MP3, AAC, AIFF, and WAV
  files carrying an `id3 ` chunk, and the same loss failed with the flag but passed without
  it. It exits 2 either way, including when the write no-ops because the file already
  projects the coerced name, so `--strict` runs that set a bare numeric genre change from
  exit 0 to exit 2. WAV files whose genre stays literal in `LIST/INFO` are unaffected.

### Fixed

- `copy` grades a bare numeric genre reference (`GENRE=17`) lossy, with the reason, onto
  MP3, AAC, AIFF, and WAV-with-id3 destinations, which read it back as the genre name;
  spelled-out genres and MP4 destinations still carry clean.
- With `--numeric-genre`, setting a bare numeric genre warns once: the `[value-reduced]`
  capability reduction is suppressed when the `[numeric-genre]` warning already names the
  loss.
- Setting one of Matroska's reserved technical tag names (`DURATION`, `BPS`, `NUMBER_OF_*`,
  `_STATISTICS_*`) drops the value with a keyed warning (escalated by `--strict`); it
  reported an empty plan but wrote the element into the native store, where no read path
  surfaces it. A plan whose rendered result equals the file is a no-op instead of a tags
  rewrite with no changes.
- Writing a canonical key that Matroska carries in more than one target scope keeps a
  still-wanted value at its scope, drops removed values from every scope, and writes only
  values new to the file at album scope. It collapsed every scope into the album-scope
  `Tag` block, relocating a per-track value to per-album scope (reachable through
  `lint --fix`).
- `copy` prints the carried line beside the lossy one for a chapter, picture, or
  synced-lyrics set that split into carried and lossy parts; a two-chapter transfer showed
  `lossy chapters (1)` alone and read as a chapter gone missing. The `TransferReport` doc
  comment attributed splits to pictures alone; chapter and synced-lyrics sets split the
  same way.

## [1.4.2]

### Fixed

- ALAC files reported 16 bits per sample whatever the real depth: the MP4 sample entry pins
  that field at 16 by convention. The depth is now read from the ALAC magic cookie.

## [1.4.1]

### Fixed

- `--numeric-genre` rewrites whenever the stored representation differs, on MP3, AAC, AIFF,
  MP4, and on WAV files carrying an `id3 ` chunk; it did nothing when the stored genre
  already matched the requested value, so a bulk normalisation run re-encoded only files
  whose genre also changed. A file that cannot be rewritten, such as a fragmented MP4,
  reports that refusal rather than no changes.
- On MP4 the stored genre encoding is kept unless the genre's value changes; an unrelated
  edit converted a numeric `gnre` atom back to the text one, undoing an earlier
  `--numeric-genre` run.
- `--set GENRE=17 --numeric-genre` reaches the same stored form on ID3v2.3 as
  `--set GENRE=Rock`; it stored a bare `17` where the name stored `(17)`, so one pass could
  leave a library holding both.
- An oversized picture exits 3 with the new `picture-too-large` code instead of 4
  (`invalid-data`, "the file is corrupt") for a healthy file. Behavior change for scripts
  branching on the exit code.
- In-place writes work on Windows. The library held its own read handle on the source across
  the rename that replaces it, which Windows refuses with `Access is denied`, so `set`,
  `copy`, and `lint --fix` failed in the shipped Windows binaries, as did `SaveBack` for
  library callers. The handle is released once the copy is done.
- The post-rename directory fsync is a no-op on Windows, where the filesystem journals the
  rename; it always failed there, so even a successful save returned an error.
- A write whose bytes were written but whose post-commit step failed counts as changed,
  names the step in a warning on stderr (a `postWriteWarning` field under `--json`), and
  leaves the exit code clean, since the edit is applied and the plan cannot run again. It
  was reported as a per-file failure and counted unchanged. Behavior change, on Linux too,
  where an ENOSPC or EIO from the directory fsync produced the same wrong report.
- `Plan.Execute` returns a nil Document for a failed save, matching every other failure
  path; it returned one describing the untouched original. Behavior change for library
  callers.
- Editing a read-only file works on Windows, where a rename refuses such a target: the
  attribute is cleared for the rename and carried over to the rewritten file.
- `waxlabel dump --recursive DIR | head` exits 0 on Windows instead of 6: the broken-pipe
  check tested only `EPIPE`, which Windows never returns.
- A missing file's human message read `The system cannot find the file specified.` on Windows
  while `--json` said `no such file or directory`. Both now use the canonical wording.
- A per-file `--json` error and its stderr line agree; they stated one failure two ways:
  `open a.flac: permission denied` against `a.flac: permission denied`, and `canceled`
  against `context canceled`. The JSON message drops the path already in `file` and Go's
  syscall verb, and an interrupted or timed-out run's human line reads `canceled` or
  `operation timed out`.
- A directory whose fsync answers `EINVAL`, as some FUSE and network mounts do, counts as
  the platform having no such step, alongside `ENOSYS`/`ENOTSUP`, instead of warning after
  every save. `ENOSPC`, `EIO`, and `EDQUOT` still surface.
- `--preserve-mtime` updated the timestamp on a file dated before 1970 instead of keeping it.
- A failed atomic commit names the destination alone, as the temp-create failure already
  did, instead of the internal temp file: a Windows sharing violation read
  `rename C:\...\.waxlabel-2559819126.tmp C:\...\track.flac: Access is denied.`.
- lint's per-file loop handles a closed output pipe as the other list commands do:
  `lint --recursive DIR | head` printed a per-file line for every file it had not reached,
  and `lint --json ... | head` exited 0 even when a file carried an error-severity finding.

## [1.4.0]

### Added

- Fragmented MP4 (a top-level `moof`) is read: tags in the initial movie box project
  normally, the file carries a `fragmented` warning, and only the write is
  refused, with the new `waxerr.ErrFragmented` sentinel (exit 3, `unsupported-fragmentation`).
  Such a file reports read-only in `caps`.
- MP4 `saio` sample-auxiliary offset tables are collected and shifted on a rewrite. They were
  never patched before, so a growing edit corrupted them.
- README documents the exit codes and their aggregate precedence.

### Fixed

- MP4 files whose `moov` declares `mvex` but carries no fragment read and write normally as
  ordinary progressive files; they were rejected.
- MP4 offset-table collection resolves the `moov > trak > mdia > minf > stbl` path instead of
  searching the whole `moov` by name, where an `ilst` item named `stco`, or one named `stbl`
  holding a crafted table, decoded as a real offset table and failed the parse or corrupted
  a write.
- An `iloc` is refused wherever it sits, not only in `moov.udta.meta`. The spec's usual
  placement is a top-level `meta`, where a growing edit shifted the media out from under its
  extents while reporting success.
- A shrinking MP4 rewrite (clearing chapters) refuses a chunk offset that points inside the
  replaced metadata region as invalid data; it was written to a `co64` as a ~18-exabyte
  value and the write reported success.
- MP4 files this codec cannot rewrite (one carrying an `iloc`, or a `saio` of an
  unrecognized version) report read-only, matching the fragmented case. A transfer onto one
  reports per-item drops instead of failing the whole plan.

## [1.3.0]

### Added

- Three canonical release-detail keys (writable): `RELEASECOUNTRY`, `RELEASESTATUS`, and
  `RELEASETYPE`, the last multivalued (one primary release-group type plus any secondary
  types). Stored natively on Vorbis and Matroska,
  as `TXXX:MusicBrainz Album Release Country` / `... Album Status` / `... Album Type` on ID3,
  as those same names in MP4 `com.apple.iTunes` freeforms, and on APE as `RELEASECOUNTRY` /
  `MUSICBRAINZ_ALBUMSTATUS` / `MUSICBRAINZ_ALBUMTYPE`, the last two also accepted as aliases.
  `RELEASECOUNTRY` takes a two-letter code (ISO 3166-1 alpha-2, plus MusicBrainz's `XW`/`XE`),
  checked by the new `malformed-country` lint code.

### Changed

- Several spellings project under the canonical keys instead of as custom fields, so
  consumers keyed on the old names must move: on ID3 the frames above (previously
  `MUSICBRAINZ ALBUM RELEASE COUNTRY` / `... STATUS` / `... TYPE`), on MP4 the equivalent
  atoms (which did not project at all), and `MUSICBRAINZ_ALBUMSTATUS` / `MUSICBRAINZ_ALBUMTYPE`
  on every format. Editing under an alias spelling writes the canonical key, so
  `--set MUSICBRAINZ_ALBUMTYPE=album` stores `RELEASETYPE`. A file carrying two spellings of
  one key lints `single-valued-multi`; on ID3 and Matroska the next edit touching the key
  collapses them to one element, while MP4 merges them into one multi-value atom on any
  write, which keeps both values and the lint.

## [1.2.0]

### Added

- Six canonical contributor-role keys (writable, multivalued): `PRODUCER`, `ENGINEER`,
  `MIXER`, `ARRANGER`, `WRITER`, and `DJMIXER`. On ID3 the first five are stored in the
  involved-people list (`TIPL` in v2.4, `IPLS` in v2.3) using the de-facto Picard involvement
  strings (`producer`/`engineer`/`mix`/`arranger`/`DJ-mix`), so they interoperate with
  MusicBrainz Picard; `WRITER` uses a `TXXX:Writer` user frame. Cross-format parity: MP4
  `com.apple.iTunes` freeforms, Vorbis/Matroska native identity, and APE. Unmodeled
  involvements already present in a `TIPL`/`IPLS` frame (e.g. `mastering`) are preserved when
  a role is edited.

## [1.1.0]

### Added

- Canonical `LYRICIST` tag key (writable, multivalued), modeled on `COMPOSER`, with
  cross-format parity: ID3 `TEXT` frame, MP4 `com.apple.iTunes` freeform, Vorbis/Matroska
  native identity, WAV/AIFF via embedded ID3, and APE. A legacy `TXXX:LYRICIST` frame reads
  onto `LYRICIST` and re-renders as the conformant `TEXT` frame on the next edit that
  touches it.
