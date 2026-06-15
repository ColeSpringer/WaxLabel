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
// The headline consumer is a music-organization tool that fills in complete,
// accurate metadata for libraries sourced from YouTube. Such files are
// sparsely and inconsistently tagged (not blank): source metadata propagates
// and transcoders stamp an "encoder=Lavf..." comment. WaxLabel therefore
// treats inherited and generated metadata as a real case to read, preserve,
// override, and dedupe.
//
// # Object model
//
// [Parse], [ParseFile], and [OpenSource] return an immutable, detached
// [Document]: it holds no OS resources and has no Close method, so a caller
// may scan, cache, and discard it freely. Accessors return detached deep
// copies of structural data; only [Picture] payload bytes are shared
// read-only. [Document.Inspect] skips picture bytes entirely for bulk scans.
//
// Editing flows through [Document.Edit], which yields an [Editor]. The editor
// records mutations against a presence-aware canonical [tag.TagSet]; calling
// [Editor.Prepare] resolves them into a [Plan]. Executing the plan against a
// [Destination] streams the result.
//
// # Frozen contracts
//
// The following contracts are stable; everything else may change during v0.x:
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
// # Provenance
//
// All code is reimplemented from public specifications (ID3v2, the Vorbis
// comment format, FLAC, ISO/IEC 14496-12, RIFF/WAVE, RFC 3533/7845/9559).
// Reference implementations were consulted for design but not copied; see
// PROVENANCE.md.
package waxlabel
