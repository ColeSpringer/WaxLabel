# Changelog

All notable changes to this project are documented here.

## [1.6.2]

### Fixed

- Musepack SV8 chapter packets are read, where the reference decoder reads them: after
  the seek table the seek-offset packet points at, else the run that ends at the end
  marker. A transcode or `copy` out of an `.mpc` now carries its chapters instead of
  dropping them silently; `dump`, `diff`, and `lint` see them. They are preserved by
  every rewrite, which copies the stream verbatim, and not written: the capability reads
  `read full, write none`, a chapter edit is refused (or dropped under the unsupported-drop
  option, warned `chapters-unsupported` with read-only wording), and `copy` into an `.mpc`
  grades chapters dropped as "cannot write". A packet stream whose header declares a
  stream version other than 8 is refused, as the reference decoder refuses it.
- WMA Marker Object entries are read as chapters, on the playback timeline (the preroll
  subtracted, as for the duration), so a WMA audiobook's chapters carry out of it.
- A chapter clear on a file whose chapters WaxLabel reads but cannot write is refused
  rather than planned as a silent no-op that keeps them. The cover-art drop gate takes
  the same shape, so clearing the cover a WebM file carries is dropped with a warning
  under the unsupported-drop option instead of reaching the writer's refusal.
- A caller-supplied source that answers a zero-length read at its end with EOF, as
  `bytes.Reader` does, no longer fails a parse that reads an empty element there.
- The `element-cap` warning code is exported as `WarnElementCap`, so a library caller can
  match it by name like every other code.

## [1.6.1]

### Fixed

- APEv2 cover writes keep item names unique, as the format requires. The Cover Art
  convention has one front and one back item, and the picture set is resolved onto
  those two slots: a front or back cover keeps its own name, any other role is stored
  under a free cover name (reading back as that cover; warned `picture-metadata-dropped`
  and graded lossy by `copy`), and an added cover replaces a same-role one the file
  already had rather than losing to it. A picture with no name left is refused for
  library callers without the unsupported-drop option and otherwise dropped with a
  `picture-unsupported` warning that `--strict` escalates; `copy` grades it dropped. An
  undecodable cover item keeps its slot against spilling roles, and is replaced (warned
  `malformed-tag-entry-dropped`) only when the edit claims its exact name or no other
  slot is free. A back cover with no description now grades as a clean carry.
- The same uniqueness holds for every APEv2 item the rebuild authors: a set on a key
  whose name a binary item occupies replaces that item (`tag-structure-dropped`), a
  cover write displaces a text item squatting on its Cover Art name (`value-dropped`),
  and a text value under a `Cover Art` name is refused outright like a reserved name,
  with `copy` grading such a key dropped. Collisions a file already carried are still
  preserved as found, and the post-write warning set recomputes its per-item findings
  from the written items so a replaced item's parse-time warning does not outlive it.

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
  survived. `--set DATE` plus `--set RECORDINGDATE` was already noted; one spelling twice
  was not.
- `copy --strict`, which refuses (exit 2) when the projection is not lossless or when
  writing the destination would itself lose metadata, writing nothing. `set --strict`
  already did the second half.
- `dump --native` shows the description a `COMM`, `USLT`, or `TXXX` frame carries, so a
  described frame is identifiable instead of a bare four-character id and a size.
- FLAC truncation and trailing-junk detection, from a walk over the frame headers at the
  end of the audio region: a stream stopping short of STREAMINFO's declared sample count
  is flagged truncated, and bytes past the final frame's CRC-located end are flagged and
  carved out of the audio extent (still copied verbatim on rewrites), so a junk-appended
  rip dedup-matches its clean twin under `verify`. The extent name becomes
  `flac-frames-v2`; persisted `flac-frames-v1` digests stay labeled v1.
- QuickTime `keys`/`mdta` metadata, the shape `ffmpeg -movflags +use_metadata_tags`
  writes: the `keys` index is read and its items decoded, so such a file reports tags
  instead of nothing, and an edit writes keys entries rather than four-character atoms.
  Apple's `com.apple.quicktime.*` names and ffmpeg's bare ones both resolve.
- Classic QuickTime `moov.udta` text atoms (`(c)nam`, `(c)swr`, ...), a plain `.mov`'s
  whole tag store, are read and written. Multi-language entries keep their translations,
  and a value the file also keeps in an `ilst` is rewritten to match rather than left to
  disagree. `dump --native` lists `moov.udta`.
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

- A WAV or RF64 LIST/INFO list missing a word-alignment pad byte lost every item past the
  first odd-size one. The reader stepped over a byte that was not there, stopped on the
  garbage without a word, and the next rewrite rebuilt the chunk from what it had read. The
  list now re-synchronizes; a region still unreadable is warned about on read and reported
  as dropped on write.
- A Vorbis comment entry with no `=` was dropped at parse and erased by the next rewrite.
  The entry is framed by its own length prefix, so it is now kept verbatim and rendered
  back unchanged, across FLAC, Ogg Vorbis, Ogg Opus and Ogg FLAC.
- An ID3 frame whose declared size overruns the tag stopped the frame walk silently, and
  the unread remainder was counted as free padding. It is now warned about, `dump` no
  longer reports padding the file does not have, and a rewrite says what it could not
  carry. A front tag header declaring more bytes than the whole file is warned about too,
  instead of reading as no tag at all.
- The `--strict` refusal said `(omit --strict to write anyway)`, which is false for the
  discard family: without the flag the item is dropped either way, and for something the
  format cannot store no bytes are written at all. It now says `(omit --strict to continue
  with a warning)`.
- A WAV or AIFF chunk declaring the `0xFFFFFFFF` size-unknown value left no signal. The
  clamp takes the rest of the file as that chunk, so a LIST/INFO after a sentinel-sized
  `data` chunk is swallowed into the audio extent and the file reads as untagged. The size
  is still clamped; the condition is now reported.
- An MP4 whose writer emitted a leading `free`, `skip` or `wide` box before `ftyp` was
  unsupported (exit 3), though the parser handles it. Detection steps over such a box
  inside its 64-byte window.
- A LIST/INFO item declaring bytes past its NUL terminator lost them on rewrite in silence:
  the writer emits the value up to the terminator plus one NUL. They are counted and
  reported now. A run of alignment zeros still is not, since a rewrite re-creates it.
- An RF64/BW64 chunk whose `ds64` entry is missing or unusable read as the plain-RIFF
  streaming sentinel, which exempted a truncated file from `truncated-audio`. Inside those
  containers the 32-bit `0xFFFFFFFF` is always the `ds64` marker, so such a chunk keeps its
  own size and clamps like any other overrun.
- Warnings that quote file-derived text - an inherited encoder stamp, an unreadable comment
  entry, a key the vocabulary cannot represent - elide an oversized value instead of
  splicing it whole. A comment list full of unreadable entries is one warning with a count,
  not one per entry.
- The family view graded each native item against the whole authoritative value list, so a
  tag container at the element cap cost quadratic time. It indexes once per key.
- `dump --native` accounts for the region of an ID3v2 tag the frame walk could not read,
  the way it does for a LIST/INFO chunk, so the block size no longer disagrees with the
  frames listed under it.
- A stray leading ID3v2 whose frame walk stopped early now counts as opaque legacy content,
  so `lint --fix` keeps it instead of stripping a region nothing could read.
- An MP4's final chapter kept its real end. The QuickTime reader canonicalized an end
  landing on the movie duration back to open, so `dump` showed `null` where ffprobe showed
  the duration and `SetChapters(End: duration)` did not round-trip. The end is now reported
  verbatim. A chapter starting past the file end still reads open: that tail is a
  placeholder our own writer invents, not a value the file states.
- `copy` graded a final chapter running to the source's own end of file as lossy while the
  transfer opened it and lost nothing. Both now read one rule.
- `Lavc`/`libavcodec` counts as a transcoder stamp alongside `Lavf`/`libavformat`, so an
  `ENCODER` naming the codec is flagged and removed rather than kept. `lint --fix` now
  discards a value like `Lavc61.19.101 libopus`, which `--strip-encoder` also removes.
- Ogg Vorbis reports its measured average bitrate, like every other format, instead of the
  identification header's `bitrate_nominal` encoder target, which read several times high
  on a VBR file. Nominal is kept only when nothing can be measured.
- A WAV or AIFF whose header declares less than its own chunks occupy is re-walked against
  the file size, recovering tags and audio previously reported as missing - including a
  `LIST`/`NAME` appended without updating the header, which a rewrite used to duplicate. A
  truncated or genuinely tag-appended file keeps its `no-audio`/`trailing-bytes` verdict.
- `--padding` accepts the size suffixes `--max-size` does (`8KiB`, `32k`, `31.5KiB`), and
  rejects a value that would truncate rather than turning `0.4` into a silent `--no-padding`.
- An empty value for a key ID3 cannot store one for (`GENRE=`, `TRACKNUMBER=`, `DISCNUMBER=`,
  `MOVEMENT=`) was dropped silently, and genre also left a stub `TCON` frame behind. The drop
  is reported and no stub is written; the plain text frames still store a present-empty value.
- A plan whose edit was wholly discarded (cover art on WebM) reported "no changes (already
  up to date)", which says the file holds what was asked for. It now says the edit was
  discarded, from the same predicate `--strict` gates on.
- An MP4 key held by both its `ilst` and its `moov.udta` atoms reports the ilst value once,
  with the udta value surfaced as a family entry. Merging the two doubled the value and let an
  unrelated edit store both in the ilst, after which the disagreement no longer showed.
- An APE tag whose footer claimed a header record that is not there moved the tag's
  start 32 bytes into the audio, so a rewrite wrote the tag over it while `--verify`
  passed (both sides derived the extent from the same wrong offset). The record is now
  confirmed before the flag is believed.
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
  and synced lyrics, as it already did for tags, instead of exiting 0 with a warning
  about the format's storage.
- `set KEY=` stores an empty value on WavPack, Monkey's Audio, and Musepack instead of
  removing the item, matching every other writable format, and `dump` reports it as a
  present-empty value rather than an absent key, as LIST/INFO already did. `--clear` still
  removes it. A zero-length item in an MP3's trailing APEv2 stays uncounted in the legacy
  view, as ID3v1's blank fields are, so it does not block `lint --fix` from stripping an
  otherwise redundant container.
- `malformed-date` says which fault it found. `"2001-13-01" is not YYYY, YYYY-MM, or
  YYYY-MM-DD` was false: the shape is right and the month does not exist. Such a value now
  reads `is not a real date`, and a genuine shape fault keeps the old wording.
- `set` with thousands of keys printed one `note:` line per key, for unknown keys and for
  per-value advisories alike. Each surface now lists ten and counts the rest. The
  `--strict` failure message still names every key: a strict run writes nothing, so that
  list is the only account of what to fix.
- A malformed value says which fault it found where a category has more than one. `BPM`
  and the MP4 integer keys (`MEDIATYPE`, `ITUNESADVISORY`, `MOVEMENT`, `MOVEMENTTOTAL`)
  said "is not a non-negative number" for a plainly non-negative number that merely
  exceeded the atom's ceiling; they now name the ceiling, which differs per key. A
  negative ReplayGain *peak* is reported as negative rather than as an unrecognized
  ReplayGain value.
- `dump --json` on an RF64 or BW64 file reports `"subformat": "RF64"`/`"BW64"` rather than
  `"WAV"`, which the parser already tracked and preserved on write. `properties.container`
  carries the same value for library callers.
- `--legacy strip` says what it destroys. It removes legacy containers unconditionally, so
  a value living only there died silently, contradicting the frozen contract that unaffected
  data is preserved and warned about, never stripped. `set`, `copy`, and `--preset minimal`
  now warn naming the lost keys, and `--strict` refuses the write. The warning judges the
  edit's own tags, so a strip that is also writing the value does not claim to be losing it.
  `lint --fix` cannot reach it: it chooses the strip only when nothing would be lost.
  On WAV the same flag consolidates LIST/INFO into the `id3 ` chunk, which cannot carry an
  item with no canonical key (`IENG`, `ISBJ`); that drop is now reported too.
- `copy` onto a read-only destination exits 3 with the codec's own refusal, after printing
  the per-field drops. It reported every field dropped and then exited 0, because nothing
  was set on the destination editor and the write collapsed into a no-op before the codec
  could refuse. A WMA keeps `unsupported-format` and a fragmented MP4 keeps
  `unsupported-fragmentation`. A transfer with nothing to carry still exits 0, and a
  writable destination that drops an item it cannot store is unaffected.
- A described ID3 `COMM` frame is read as `COMMENT`. Such a frame (Windows Explorer and
  CDDB-era taggers write one) was invisible to `dump`, `lint` and `diff`, and `copy`
  reported a clean lossless carry while leaving it behind. It is now also managed on write:
  a single described frame keeps its description and language across an edit, and a merge
  that cannot keep one warns (`comment-description-dropped`, which `--strict` escalates).
  Machine descriptions (`iTunNORM`, `iTunSMPB`, ReplayGain) stay unprojected and untouched.
  With several comment frames, the first frame's language now wins rather than the last.
- WAV `ISFT` is `ENCODER` on both sides. A stock ffmpeg WAV showed no `ENCODER` under
  `dump` while `ffprobe` showed `encoder=Lavf`, and writing `ENCODER` created an `id3 `
  chunk for a value LIST/INFO has a slot for. Three consequences worth knowing: clearing
  `ENCODER` (which `--strip-encoder` does) now removes the `ISFT` item, since that is where
  the key lives; a WAV whose `id3 ` chunk and `ISFT` disagree reports `conflicting-families`
  and the two are brought into agreement on the next write, as they already were for every
  other key both containers hold; and an inherited transcoder stamp is never promoted into
  an `id3 ` chunk a write creates, so an unrelated edit does not author a second copy of the
  noise the linter flags.
- `lint --fix` no longer restructures a LIST/INFO-only WAV. Reading `IPRT=4/9` splits it
  into a track number and a total, and the total had no INFO slot, so the fix spawned an
  `id3 ` chunk holding it and rewrote `IPRT` to a bare `4`. The pair is recombined into the
  one item it came from, so the round trip is byte-stable. `DISCNUMBER` has no INFO
  identifier at all and still promotes the file.
- WAV duration and bitrate for a compressed payload come from the `fact` chunk's sample
  count, which is now parsed. A one-second MS-ADPCM file reported 1.408 s at the nominal
  128 kbps, and a WAV carrying MP3 payload reported both as null. The declared count is
  sanity-checked before it is trusted, so a hostile `0xFFFFFFFF` falls back instead of
  reporting 27 hours. `totalSamples` for such a format is now 0 rather than a block count,
  which was wrong by three orders of magnitude. PCM, IEEE float, A-law and mu-law are
  unchanged: their byte rate is exact.
- APEv2 no longer writes the item names the specification reserves (`ID3`, `TAG`, `OggS`,
  `MP+`), each of which is magic another structure is found by. Such a key is dropped with
  a warning that `--strict` escalates, and `copy` grades it dropped rather than carrying it.
  A file that already holds such an item keeps it, whether the edit leaves it alone or tries
  to change it, so a refused write never costs the value the file already had on top of the
  one it could not store.
- A canonical key can no longer contain `~` (0x7E). The rule is the intersection of every
  format's key syntax, and the Vorbis comment specification stops at 0x7D, so the promise
  that a valid key is representable everywhere was false. `~` in a key is now exit 2
  (`invalid-key`); a file already carrying one is preserved verbatim, as any unrepresentable
  native key is.
- Every format that holds string keys now reports one it cannot represent
  (`invalid-tag-key`): APEv2 items, ID3 `TXXX` descriptions, MP4 freeform names, Matroska
  `SimpleTag` names and ASF `WM/*` descriptors, alongside the Vorbis comments that already
  did. Such a value is preserved on disk but never reaches the canonical set, so without
  this it was absent from `dump`, `lint` and `diff` while `copy` reported a clean lossless
  carry. Deliberate exclusions - Matroska's `BPS`/`NUMBER_OF_*` statistics, ASF's technical
  descriptors - are not reported, since nothing is lost there.
- An ID3v2.3 date stored in full but read back respelled (`2001-02-03 10:20` comes back as
  `2001-02-03T10:20`, since the frames store neither separator) is reported as a coercion
  and escalated by `--strict`. It was neither a drop nor a precision loss, so nothing
  reported it. The three date fates now come from one predicted-read-back rule rather than
  separate scanners.
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

- `--recursive` now descends into `.mov`, `.m4r`, `.mpga`, and `.adts`, and reports each
  `.wma` it finds as failed (exit 3) rather than skipping it, since WMA cannot be written.
- `caps --format` refuses an extension claimed by more than one format, naming both.
  `.oga` and `.ogg` are now Ogg Vorbis and Ogg FLAC alike; pass `oggflac` to pick FLAC.
  `ogg` still names Ogg Vorbis, as it always has.
- The legacy-conflict warning is gated on a container being legacy, not on its name. An
  APEv2-native file no longer warns about its own tag; a FLAC whose stray leading ID3v2
  disagrees with an edit now does.
- An APE `DATE` resolves to `RECORDINGDATE`, and a slashed `Track` splits into the
  canonical number/total pair.
- **`lint` reports more, and some files that were clean will now exit 1.** The full list of
  new findings, all warnings: `chapter-past-duration` and `duplicate-chapter` (previously
  raised only at write time, while `set --help` pointed at `lint`); `chained-stream` (which
  `dump` already reported); `trailing-bytes`, for a region of a WAV, AIFF or Ogg belonging
  to no chunk or page (the bytes were already preserved, the silence was the gap);
  `oversized-chunk`, which `dump` reported but `lint` did not, and which is also how bytes
  appended after an MP4's last atom read; `invalid-tag-key` on four more formats; and
  `non-conforming-icon`, for a file-icon picture that is not the 32x32 PNG ID3v2 requires.
  Mapping WAV `ISFT` to `ENCODER` also means a WAV whose `id3 ` chunk and `ISFT` disagree is
  a new `conflicting-families` finding, which `lint --fix` resolves. FLAC gained its own
  trailing-region detection later; see the frame-tail entry above.
- **New refusals, where a write that could not happen used to exit 0 or name the wrong
  fault.** `copy` onto a read-only destination (WMA, a fragmented MP4) is exit 3 instead of
  a silent exit 0, and `~` in a canonical key is exit 2 instead of accepted; both are
  described under Fixed. Authoring a second file-icon picture was exit 4 (`invalid-data`),
  which said the file was corrupt when only the write was impossible; it is now exit 3
  (`unsupported-tag`), and no longer outranks a genuinely corrupt file in a batch run. FLAC
  and Ogg cap chapters at 1000, the size of the `CHAPTERxxx` 3-digit namespace; a file
  already holding more becomes chapter-uneditable at exit 3, while its tag edits keep
  working, and a `copy` from such a source now drops the chapter set rather than carrying
  it (the same rule the 255-chapter formats already follow).

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

- The MP4 atoms above previously survived edits only as preserved unknown items and were
  invisible to dump, diff, copy, and `Get`; they now project and rebuild like any owned atom.
- ID3 `TBPM` previously round-tripped as the custom key `TBPM`; it now projects as `BPM`.
  Consumers keyed on `TBPM` must move.
- A freeform `----:ITUNESADVISORY`-style MP4 representation migrates to the structured atom
  on the next edit; a stale ID3 `TXXX:BPM` migrates to `TBPM` on the next edit that changes
  the key. `ENCODEDBY`'s MP4 write spelling moves from a freeform to `©enc`.
- Recognized boolean words canonicalize to `1`/`0` on ID3 `TXXX` frames for boolean keys, so
  `ITUNESGAPLESS=yes` stores `1` on MP3 as it does on FLAC and MP4.
- These names no longer draw the custom-key lint info, and their values are validated: an
  invalid or out-of-range value now drops with a warning on MP4 writes (escalated by
  `--strict`) instead of writing a freeform, and a copy into MP4 excludes such a value
  (the destination keeps its own). A fractional `BPM` rounds to nearest on MP4 with a
  coercion warning, which `--strict` escalates to exit 2 like a drop; a copy of one into
  MP4 grades lossy. On an MP4 edit that makes one of these values unstorable, an existing
  stored value is kept (with the warning) rather than deleted, matching the track/disc
  slots.
- `lint` now flags a malformed value on these keys (`malformed-number`/`malformed-boolean`,
  exit 1) where it previously passed them as custom text. A library carrying free-text BPM
  values ("120-125", "Unknown") flips a lint gate on upgrade.
- ffmpeg folds both `©too` and `©enc` onto its single `encoder` tag, so ffprobe reports
  whichever atom comes later when a file carries `ENCODER` and `ENCODEDBY` together; iTunes
  and Mp3tag keep them distinct.
- Setting a raw mapped frame ID as a key (`--set TBPM=128`) silently wrote nothing once the
  frame joined the mapping table; the value is now written to that frame and reads back
  under the canonical key. Same fix for the other mapped frame IDs.
- Giving a structured single-atom MP4 key several values now warns that only the first is
  stored, and a copy grades it lossy; previously the surplus vanished silently
  (pre-existing for `MEDIATYPE`/`COMPILATION` and the track/disc slots).
- Matroska now canonicalizes boolean words to `1`/`0` on write like FLAC, ID3, and MP4, so
  `ITUNESGAPLESS=yes` stores `1` on MKA too.
- The Matroska native tag spellings (`PART_NUMBER`, `TOTAL_PARTS`, `TOTAL_DISCS`,
  `LEAD_PERFORMER`, `DATE_RECORDED`, `DATE_RELEASED`, `DATE_RELEASE`, `DATE_ORIGINAL`,
  `ORIGINAL_DATE`, `ENCODED_BY`, `CATALOG_NUMBER`, `PUBLISHER`, `REMIXED_BY`,
  `CONTENT_GROUP`) are now aliases of their canonical keys. `--set PART_NUMBER=x` previously
  wrote a custom field that projected back onto `TRACKNUMBER`, so a set behaved as an append
  and left a single-valued key holding conflicting values; it now replaces. The aliases apply
  on every format, and Vorbis-family, ID3 `TXXX`, MP4 freeform, and APE reads now fold these
  spellings onto the canonical keys, so a set replaces a foreign field stored under the
  spelling instead of appending beside it.
- `--strict` did not escalate the numeric-genre coercion: `--set GENRE=17` stores a reference
  that reads back as `Rock` on MP3, AAC, AIFF, and WAV files carrying an `id3 ` chunk, and the
  identical loss failed with `--numeric-genre` but passed without it. It now exits 2 either
  way, including when the write no-ops because the file already projects the coerced name;
  existing `--strict` runs that set a bare numeric genre change from exit 0 to exit 2. WAV
  files whose genre stays literal in `LIST/INFO` are unaffected.

### Fixed

- `copy` graded a bare numeric genre reference (`GENRE=17`) as a clean carry onto MP3, AAC,
  AIFF, and WAV-with-id3 destinations even though the destination reads it back as the genre
  name. It now grades lossy with the reason; spelled-out genres and MP4 destinations still
  carry clean.
- With `--numeric-genre`, setting a bare numeric genre warned twice for the same loss: the
  `[numeric-genre]` warning plus a generic `[value-reduced]` capability reduction. The
  capability reduction is now suppressed when the numeric-genre warning already names it.
- Setting one of Matroska's reserved technical tag names (`DURATION`, `BPS`, `NUMBER_OF_*`,
  `_STATISTICS_*`) reported an empty plan but wrote the element into the native store anyway,
  where no read path would ever surface it. The value is now dropped with a keyed warning
  (escalated by `--strict`), and a plan whose rendered result equals the file is now an honest
  no-op instead of reporting a tags rewrite with no changes.
- Writing a canonical key that Matroska carried in more than one target scope collapsed it
  into the album-scope `Tag` block, silently relocating a per-track value to per-album scope
  (reachable through `lint --fix`). A still-wanted value now stays at the scope that holds it;
  removed values are dropped from every scope, and only values new to the file are written at
  album scope.
- `copy` printed only the lossy line of a chapter, picture, or synced-lyrics set that split
  into carried and lossy parts, so a two-chapter transfer showed `lossy chapters (1)` with no
  counterpart and read as a chapter gone missing. The carried sibling now prints beside it.
  The `TransferReport` doc comment also attributed splits to pictures alone; chapter and
  synced-lyrics sets split the same way.

## [1.4.2]

### Fixed

- ALAC files reported 16 bits per sample whatever the real depth: the MP4 sample entry pins
  that field at 16 by convention. The depth is now read from the ALAC magic cookie.

## [1.4.1]

### Fixed

- `--numeric-genre` did nothing when the stored genre already matched the requested value, so a
  bulk normalisation run re-encoded only the files whose genre also changed. It now rewrites
  whenever the stored representation differs, on MP3, AAC, AIFF, MP4, and on WAV files carrying
  an `id3 ` chunk. A file that cannot be rewritten at all, such as a fragmented MP4, now reports
  that refusal rather than silently reporting no changes.
- On MP4 an unrelated edit converted an existing numeric `gnre` atom back to the text one,
  undoing an earlier `--numeric-genre` run. The stored encoding is now kept unless the genre's
  value changes.
- `--set GENRE=17 --numeric-genre` stored the bare `17` on ID3v2.3 where `--set GENRE=Rock`
  stored `(17)`, so one pass could leave a library holding both. A supplied reference now
  reaches the same stored form as a supplied name.
- An oversized picture exited 4 (`invalid-data`, "the file is corrupt") for a healthy file. It
  now exits 3 with the new `picture-too-large` code. Behavior change for scripts branching on
  the exit code.
- Every in-place write failed on Windows with `Access is denied`. The library held its own read
  handle on the source across the rename that replaces it, which a Windows rename refuses, so
  `set`, `copy`, and `lint --fix` were broken in the shipped Windows binaries, as was `SaveBack`
  for library callers. The handle is now released once the copy is done.
- The post-rename directory fsync always failed on Windows, so even a successful save returned
  an error. It is now a no-op there, where the filesystem already journals the rename.
- A write whose bytes landed but whose post-commit step then failed was reported as a per-file
  failure and counted unchanged. Such a write is applied and its plan is spent, so it is now
  counted as changed and the step is named in a warning on stderr (a `postWriteWarning` field
  under `--json`), leaving the exit code clean. Behavior change; affects Linux too, where an
  ENOSPC or EIO from the directory fsync produced the same wrong report.
- `Plan.Execute` returned a Document for a write that never happened, describing the untouched
  original. A failed save now returns a nil Document, matching every other failure path.
  Behavior change for library callers.
- Editing a read-only file failed on Windows, where a rename refuses such a target. The
  attribute is now cleared for the rename and carried over to the rewritten file.
- `waxlabel dump --recursive DIR | head` exited 6 on Windows instead of exiting 0 silently: the
  broken-pipe check tested only `EPIPE`, which Windows never returns.
- A missing file's human message read `The system cannot find the file specified.` on Windows
  while `--json` said `no such file or directory`. Both now use the canonical wording.
- A per-file `--json` error and its stderr line stated one failure two ways: `open a.flac:
  permission denied` against `a.flac: permission denied`, and `canceled` against `context
  canceled`. They now agree. The JSON message drops the path already in `file` and Go's
  syscall verb, and an interrupted or timed-out run's human line reads `canceled` or
  `operation timed out`.
- A directory whose fsync answers `EINVAL`, as some FUSE and network mounts do, warned after
  every save. It now counts as the platform having no such step, alongside `ENOSYS`/`ENOTSUP`.
  `ENOSPC`, `EIO`, and `EDQUOT` still surface.
- `--preserve-mtime` updated the timestamp on a file dated before 1970 instead of keeping it.
- A failed atomic commit named the internal temp file. A Windows sharing violation read
  `rename C:\...\.waxlabel-2559819126.tmp C:\...\track.flac: Access is denied.`; it now
  names the destination alone, the way the temp-create failure already did.
- `lint --recursive DIR | head` printed a per-file line for every file it had not reached
  instead of stopping silently, and `lint --json ... | head` exited 0 even when a file
  carried an error-severity finding. lint's per-file loop now handles a closed output pipe
  the way the other list commands already did.

## [1.4.0]

### Added

- Fragmented MP4 (a top-level `moof`) is now read instead of rejected: tags in the initial
  movie box project normally, the file carries a `fragmented` warning, and only the write is
  refused, with the new `waxerr.ErrFragmented` sentinel (exit 3, `unsupported-fragmentation`).
  Such a file reports read-only in `caps`.
- MP4 `saio` sample-auxiliary offset tables are collected and shifted on a rewrite. They were
  never patched before, so a growing edit corrupted them.
- README documents the exit codes and their aggregate precedence.

### Fixed

- MP4 files whose `moov` declares `mvex` but carries no fragment are ordinary progressive
  files; they were rejected outright and now read and write normally.
- MP4 offset-table collection resolves the `moov > trak > mdia > minf > stbl` path instead of
  searching by name. It previously recursed the whole `moov`, so an `ilst` item named `stco`,
  or one named `stbl` holding a crafted table, was decoded as a real offset table, failing the
  parse or corrupting a write.
- An `iloc` is now refused wherever it sits, not only in `moov.udta.meta`. The spec's usual
  placement is a top-level `meta`, and a growing edit there shifted the media out from under
  its extents while reporting success.
- A shrinking MP4 rewrite (clearing chapters) no longer wraps a chunk offset that points
  inside the replaced metadata region. Such an offset used to be written to a `co64` as a
  ~18-exabyte value and the write reported success; it is now refused as invalid data.
- MP4 files this codec cannot rewrite at all - one carrying an `iloc`, or a `saio` whose
  version is unrecognized - now report read-only, matching the fragmented case. A transfer
  onto one reports per-item drops instead of failing the whole plan.

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

- Several spellings now project under the canonical keys instead of as custom fields, so
  consumers keyed on the old names must move: on ID3 the frames above (previously
  `MUSICBRAINZ ALBUM RELEASE COUNTRY` / `... STATUS` / `... TYPE`), on MP4 the equivalent
  atoms (which did not project at all), and `MUSICBRAINZ_ALBUMSTATUS` / `MUSICBRAINZ_ALBUMTYPE`
  on every format. Editing under an alias spelling likewise writes the canonical key, so
  `--set MUSICBRAINZ_ALBUMTYPE=album` now stores `RELEASETYPE`. A file carrying two spellings
  of one key lints `single-valued-multi`; on ID3 and Matroska the next edit that touches the
  key collapses them to one element, while MP4 merges them into a single multi-value atom on
  any write, which keeps both values and so keeps the lint.

## [1.2.0]

### Added

- Six canonical contributor-role keys (writable, multivalued): `PRODUCER`, `ENGINEER`,
  `MIXER`, `ARRANGER`, `WRITER`, and `DJMIXER`. On ID3 the first five are stored in the
  involved-people list (`TIPL` in v2.4, `IPLS` in v2.3) using the de-facto Picard involvement
  strings (`producer`/`engineer`/`mix`/`arranger`/`DJ-mix`), so they interoperate with
  MusicBrainz Picard; `WRITER` rides a `TXXX:Writer` user frame. Cross-format parity: MP4
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
