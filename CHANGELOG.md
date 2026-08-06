# Changelog

All notable changes to this project are documented here.

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
