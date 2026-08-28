# Changelog

All notable changes to this project are documented here.

## [Unreleased]

### Added

- WavPack (`.wv`), Monkey's Audio (`.ape`), and Musepack (`.mpc`), written through APEv2,
  which is now a writable container: items, multi-values, and the `Cover Art` convention.
- Ogg FLAC (`.oga`), sharing the Ogg page layer. Cover art is a native `PICTURE` block.
- WMA/ASF (`.wma`, `.asf`), read-only: Content Description, `WM/*`, and `WM/Picture`.
- RF64 and BW64. `ds64` is recomputed on save-back and the form is never downgraded.
- `.m4r`, `.mpga`, `.adts`, and `.mov` as claimed extensions.

### Fixed

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
