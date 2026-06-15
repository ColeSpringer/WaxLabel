# Provenance

WaxLabel is licensed MIT. All code is reimplemented from public format
specifications. Reference implementations under `*-temp/` (which are
git-ignored and not distributed) were consulted for design and for
cross-checking behavior, **not** copied:

- `mutagen` (GPL-2.0+) — FLAC block layout and Vorbis comment structure.
- `TagLib` (LGPL-2.1 / MPL-1.1) — FLAC metadata handling.
- `bogem/id3v2`, `sentriz/go-taglib` (MIT) — Go API ergonomics.

Because mutagen and TagLib are copyleft, no line-by-line porting was done; the
implementation follows the specifications below directly.

## Codec → specification matrix (M0)

| Codec | Status | Specification implemented | Reference consulted |
|-------|--------|---------------------------|---------------------|
| FLAC  | read + write | FLAC format (metadata blocks, STREAMINFO, VORBIS_COMMENT, PICTURE, PADDING; CUESHEET/SEEKTABLE/APPLICATION preserved verbatim) | mutagen `flac.py`, TagLib `flac/` |
| Vorbis comment | read + write | Ogg Vorbis I — comment field format (little-endian lengths, no FLAC framing bit) | mutagen `_vorbis.py` |

The Ogg CRC table used by `internal/bits` (for the upcoming Ogg milestone) is
generated from the polynomial in the Ogg specification and validated in tests
against libogg's published `crc_lookup` values — it is the non-reflected
variant, distinct from Go's `hash/crc32`.

## Vendored data

None yet. When the ID3v1/Winamp numeric genre table is added (MP3 milestone),
it will be documented here as data (not expression) with its source.
