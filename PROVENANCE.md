# Provenance

WaxLabel is licensed MIT. All code is reimplemented from public format
specifications. Reference implementations were consulted for design and for
cross-checking behavior, **not** copied:

- `mutagen` (GPL-2.0+) - FLAC block layout, Vorbis comment structure, Ogg page
  handling (its explicit CRC bit-swap confirmed the Ogg CRC is non-reflected),
  ID3 frame translation tables and the numeric-genre list, MP4 atom structure
  and the iTunes ilst tag conventions.
- `TagLib` (LGPL-2.1 / MPL-1.1) - FLAC and ID3 metadata handling.
- `bogem/id3v2`, `sentriz/go-taglib` (MIT) - Go API ergonomics.

Because mutagen and TagLib are copyleft, no line-by-line porting was done; the
implementation follows the specifications below directly.

## Codec -> specification matrix

| Codec | Status | Specification implemented | Reference consulted |
|-------|--------|---------------------------|---------------------|
| FLAC  | read + write | FLAC format (metadata blocks, STREAMINFO, VORBIS_COMMENT, PICTURE, PADDING; CUESHEET/SEEKTABLE/APPLICATION preserved verbatim) | mutagen `flac.py`, TagLib `flac/` |
| Vorbis comment | read + write | Ogg Vorbis I - comment field format (little-endian lengths; FLAC framing bit / Ogg framing handled per container) | mutagen `_vorbis.py` |
| Ogg framing | read + write | RFC 3533 (Ogg bitstream): page structure, lacing, non-reflected CRC-32; packet-payload-preserving re-pagination | mutagen `ogg.py` |
| Ogg Vorbis | read + write | Vorbis I identification + setup headers (preserved verbatim); audio packets copied byte-for-byte | mutagen `oggvorbis.py` |
| Ogg Opus | read + write | RFC 7845 (Ogg Opus): OpusHead (pre-skip, R128 output_gain), OpusTags with preserved padding | mutagen `oggopus.py` |
| METADATA_BLOCK_PICTURE | read + write | base64-encoded FLAC PICTURE block, shared with FLAC | mutagen `_vorbis.py` |
| ID3v2 | read + write | ID3v2.2 / v2.3 / v2.4: header (sync-safe sizes), unsynchronisation, frames (text encodings, TXXX, COMM, USLT, APIC/PIC, UFID), v2.2->v2.3 identifier upgrade, TYER/TDAT/TIME <-> TDRC date (de)composition | mutagen `id3/`, bogem/id3v2 |
| ID3v1 | read | the 128-byte trailer (v1.0 / v1.1 track byte) | mutagen `_id3v1.py` |
| APEv2 | read | header/footer + items, for the family view and verbatim preservation | mutagen `apev2.py` |
| MP3 | read + write | MPEG-1/2/2.5 Layer I-III frame headers; Xing/Info/VBRI VBR length; ID3v2 front + audio + trailing APEv2/ID3v1 layout; audio frames copied byte-for-byte | mutagen `mp3.py` |
| WAV / RIFF | read + write | RIFF/WAVE chunk structure (`fmt ` geometry, `data` extent, word alignment, RIFF size); LIST/INFO tag block; embedded `id3 ` chunk (shared ID3v2 codec); all chunks preserved verbatim; RF64/BW64 rejected | mutagen `wave.py`, `aiff.py`, TagLib `riff/` |
| MP4 / M4A | read + write | ISO/IEC 14496-12 atom structure (32/64-bit sizes, `meta` FullBox, sample tables); iTunes `moov.udta.meta.ilst` tags (`data` type codes - text, integer, JPEG/PNG cover, `trkn`/`disk`, `gnre`, `----` freeform); all-track `stco`/`co64` chunk-offset fixups; `free`-atom padding reuse; `chpl` preserved verbatim; fragmented (`moof`) rejected | mutagen `mp4/` |
| Matroska / WebM | read | RFC 8794 (EBML element/VINT structure, unknown-size handling) + RFC 9559 (Matroska): Segment.Info (TimestampScale/Duration/Title), Segment.Tracks audio geometry, Segment.Tags target-scoped SimpleTags, Segment.Attachments cover art; cluster media skipped by size | TagLib `matroska/` |
| AIFF / AIFF-C | read + write | AIFF / AIFF-C (EA IFF 85 FORM container, big-endian sizes, word alignment): `COMM` geometry with the 80-bit IEEE-754 extended sample rate and the AIFF-C compression type; `SSND` sample-frame extent; native NAME/AUTH/`(c) `/ANNO text chunks; embedded `ID3 ` chunk (shared ID3v2 codec, `ID3 `/`id3 ` variants); all chunks preserved verbatim | mutagen `aiff.py`, TagLib `riff/aiff/` |

The Ogg CRC table in `internal/bits` is generated from the polynomial in the Ogg
specification and validated in tests against libogg's published `crc_lookup`
values - it is the non-reflected variant, distinct from Go's `hash/crc32`. Audio
pages are renumbered on write by patching this CRC (it is linear: init 0, no
final XOR), validated against a full recomputation.

## Vendored data

- **ID3v1 / Winamp numeric genre table** (`internal/id3/genres.go`) - the 192
  genre names indexed 0-191. Entries 0-79 are from the ID3v1 specification;
  80-191 are the de-facto Winamp extensions. This is reference *data* (a list of
  factual genre names), not expression; it is reproduced for numeric-genre
  resolution. The same list appears verbatim across mutagen, TagLib, and the
  ID3v1 documentation.
