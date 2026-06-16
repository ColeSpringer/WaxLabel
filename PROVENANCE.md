# Provenance

WaxLabel is licensed MIT. All code is reimplemented from public format
specifications. Reference implementations were consulted for design and for
cross-checking behavior, **not** copied:

- `mutagen` (GPL-2.0+) — FLAC block layout, Vorbis comment structure, Ogg page
  handling (its explicit CRC bit-swap confirmed the Ogg CRC is non-reflected).
- `TagLib` (LGPL-2.1 / MPL-1.1) — FLAC metadata handling.
- `bogem/id3v2`, `sentriz/go-taglib` (MIT) — Go API ergonomics.

Because mutagen and TagLib are copyleft, no line-by-line porting was done; the
implementation follows the specifications below directly.

## Codec → specification matrix

| Codec | Status | Specification implemented | Reference consulted |
|-------|--------|---------------------------|---------------------|
| FLAC  | read + write | FLAC format (metadata blocks, STREAMINFO, VORBIS_COMMENT, PICTURE, PADDING; CUESHEET/SEEKTABLE/APPLICATION preserved verbatim) | mutagen `flac.py`, TagLib `flac/` |
| Vorbis comment | read + write | Ogg Vorbis I — comment field format (little-endian lengths; FLAC framing bit / Ogg framing handled per container) | mutagen `_vorbis.py` |
| Ogg framing | read + write | RFC 3533 (Ogg bitstream): page structure, lacing, non-reflected CRC-32; packet-payload-preserving re-pagination | mutagen `ogg.py` |
| Ogg Vorbis | read + write | Vorbis I identification + setup headers (preserved verbatim); audio packets copied byte-for-byte | mutagen `oggvorbis.py` |
| Ogg Opus | read + write | RFC 7845 (Ogg Opus): OpusHead (pre-skip, R128 output_gain), OpusTags with preserved padding | mutagen `oggopus.py` |
| METADATA_BLOCK_PICTURE | read + write | base64-encoded FLAC PICTURE block, shared with FLAC | mutagen `_vorbis.py` |

The Ogg CRC table in `internal/bits` is generated from the polynomial in the Ogg
specification and validated in tests against libogg's published `crc_lookup`
values — it is the non-reflected variant, distinct from Go's `hash/crc32`. Audio
pages are renumbered on write by patching this CRC (it is linear: init 0, no
final XOR), validated against a full recomputation.

## Vendored data

None yet. When the ID3v1/Winamp numeric genre table is added (MP3 milestone),
it will be documented here as data (not expression) with its source.
