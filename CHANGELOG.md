# Changelog

All notable changes to this project are documented here.

## [1.1.0] - 2026-07-17

### Added

- Canonical `LYRICIST` tag key (writable, multivalued), modeled on `COMPOSER`, with
  cross-format parity: ID3 `TEXT` frame, MP4 `com.apple.iTunes` freeform, Vorbis/Matroska
  native identity, WAV/AIFF via embedded ID3, and APE. A legacy `TXXX:LYRICIST` frame reads
  onto `LYRICIST` and re-renders as the conformant `TEXT` frame on the next edit that
  touches it.
