// Package waxlabel is a pure-Go library for reading and writing audio-file
// metadata: tags, embedded pictures, chapters, and synced lyrics.
//
// # Scope
//
// Design goals: preservation-first edits, a public writable canonical key
// vocabulary, plan-before-write ([Editor.Prepare] produces a [Plan] whose
// [Plan.Report] matches [Plan.Execute]), and versioned audio-essence identity
// for library dedup within a container ([Document.HashAudioEssence]; digests
// are container-scoped).
//
// Typical inputs are sparse or inconsistently tagged (source metadata, transcoder
// stamps like "encoder=Lavf..."). Inherited and generated metadata is data to
// read, preserve, override, or deduplicate.
//
// # Formats
//
// Read/write: FLAC, Ogg Vorbis, Ogg Opus, Ogg FLAC, MP3, WAV (RF64/BW64),
// MP4/M4A, raw AAC/ADTS, Matroska/WebM, AIFF/AIFF-C, WavPack, Monkey's Audio,
// Musepack. WMA/ASF is read-only.
//
// # Object model
//
// [Parse], [ParseFile], and [OpenSource] return an immutable, detached
// [Document]: no OS resources, no Close. Accessors return deep copies
// (including [Picture] payloads). [Document.Inspect] skips picture bytes for
// bulk scans.
//
// Edit via [Document.Edit] -> [Editor.Prepare] -> [Plan] against a
// [Destination]. Cross-file copy uses [Document.Transfer].
//
// # Frozen contracts
//
// Stable across v1; other surface may still evolve:
//
//   - Document is immutable, detached, and serializable.
//   - Presence-aware [tag.TagSet]/[tag.TagPatch] are authoritative; typed
//     [tag.Tags] is a convenience projection.
//   - Canonical key vocabulary ([tag.Key]) is public and writable.
//   - Preservation-first: native document is the base; unaffected data
//     (including legacy tags) is preserved and warned, never stripped silently.
//   - Prepare and Execute share state so plan and write cannot disagree; a
//     no-op SaveBack writes nothing.
//   - [AudioDigest] carries algorithm and versioned extent so persisted digests
//     stay interpretable. Extent is container-scoped; remuxing changes the digest.
//
// # Acknowledgements
//
// Reimplemented from public specs (ID3v2, Vorbis comments, FLAC, ISO/IEC
// 14496-12, RIFF/WAVE, EBU Tech 3306, APEv2, WavPack, Monkey's Audio, Musepack,
// ASF, RFC 3533/7845/9559). Reference implementations informed design but were
// not copied; see the README acknowledgements.
package waxlabel
