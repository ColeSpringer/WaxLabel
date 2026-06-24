// Package waxlabel is a pure-Go library for reading and writing audio-file
// metadata (tags plus embedded cover art).
//
// # Scope
//
// WaxLabel is the metadata member of the "Wax" family. Its design goals are
// preservation-first editing, a public writable canonical key vocabulary, a
// plan-before-write workflow ([Editor.Prepare] producing a [Plan] whose
// [Plan.Report] matches exactly what [Plan.Execute] will do), and versioned
// audio-essence identity for library-wide deduplication.
//
// WaxLabel is built for music-organization tools that need complete metadata
// for libraries sourced from uneven inputs such as YouTube. Those files are
// usually sparse or inconsistently tagged rather than blank: source metadata
// propagates, and transcoders often stamp an "encoder=Lavf..." comment.
// WaxLabel treats inherited and generated metadata as data to read, preserve,
// override, and deduplicate.
//
// # Object model
//
// [Parse], [ParseFile], and [OpenSource] return an immutable, detached
// [Document]: it holds no OS resources and has no Close method, so a caller
// may scan, cache, and discard it freely. Accessors return detached deep
// copies of structural data - [Picture] payloads included, so a caller may
// mutate anything an accessor returns without affecting the [Document] or a
// later call. [Document.Inspect] skips picture bytes entirely for bulk scans.
//
// Editing flows through [Document.Edit], which yields an [Editor]. The editor
// records mutations against a presence-aware canonical [tag.TagSet]; calling
// [Editor.Prepare] resolves them into a [Plan]. Executing the plan against a
// [Destination] streams the result.
//
// # Frozen contracts
//
// The following contracts are stable across the v1 line; other surface may
// still evolve:
//
//   - The Document is immutable, detached, and serializable.
//   - The presence-aware canonical [tag.TagSet]/[tag.TagPatch] is
//     authoritative; the typed [tag.Tags] struct is a convenience projection.
//   - The canonical key vocabulary ([tag.Key]) is public and writable.
//   - Editing is preservation-first: the native document is the base and
//     unaffected data (including legacy tags) is preserved and warned, never
//     stripped silently.
//   - Prepare then Execute share state so the plan and the write cannot
//     disagree; a no-op SaveBack writes nothing.
//   - [AudioDigest] carries an algorithm and a versioned extent so persisted
//     dedup hashes survive across library-wide refinements.
//
// # Acknowledgements
//
// All code is reimplemented from public specifications (ID3v2, the Vorbis
// comment format, FLAC, ISO/IEC 14496-12, RIFF/WAVE, RFC 3533/7845/9559).
// Reference implementations were consulted for design but not copied; see the
// README acknowledgements.
package waxlabel
