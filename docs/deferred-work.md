# Deferred work

## FLAC cannot detect a trailing region or a truncation

WAV, AIFF, Ogg and MP4 all report bytes belonging to no chunk, page or atom. FLAC reports
nothing, because it cannot tell such a region from audio: the format declares no encoded
essence length anywhere (STREAMINFO holds decoded `TotalSamples`, not bytes), so
`internal/flac/parse.go` sets `audioEnd = size` and appended junk is indistinguishable from
more frames. The same gap hides a mid-stream truncation with an intact STREAMINFO, which
that file's own comment records as a known limitation.

Both need the same thing: a walk over FLAC frame headers - the 14-bit sync code plus the
per-header CRC-8, with variable block sizes - to find where valid frames stop. A per-byte
bitrate floor was considered as a cheap substitute and rejected: lossless silence
legitimately compresses to tens of bps, so it would false-flag valid audio.

Worth doing as its own change. It closes two findings at once, and it is a decoder-adjacent
walk this codec does not otherwise have.
