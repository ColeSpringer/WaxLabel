# Changelog

All notable changes to this project are documented here.

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
