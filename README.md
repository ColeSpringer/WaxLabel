# WaxLabel

A pure-Go library for reading and writing audio-file metadata (tags + embedded
cover art), reimplemented from public specifications.

> **Status: v0.x.** The core model and FLAC read/write are implemented and
> tested. Other formats are in progress; codecs stay internal until v1.0, when
> validated ones are promoted to public `waxlabel/<fmt>` packages.

WaxLabel is preservation-first: it treats the file's native metadata as the
base and rewrites only the fields you actually change, copying the audio
verbatim. It is designed for tools that fill in complete, accurate metadata for
large libraries — including files acquired from transcoders, which arrive
sparsely and inconsistently tagged rather than blank.

## Install

```
go get github.com/colespringer/waxlabel
```

## Quick start

```go
ctx := context.Background()

// Read.
doc, err := waxlabel.ParseFile(ctx, "track.flac")
if err != nil { log.Fatal(err) }
fmt.Println(doc.Fields().Title, doc.Fields().Artists)

// Edit (nothing is written until Execute).
plan, err := doc.Edit().
    Set(tag.Title, "New Title").
    Set(tag.Artist, "Lead", "Featured").  // multi-valued
    AddPicture(waxlabel.Picture{Type: waxlabel.PicFrontCover, Data: jpegBytes}).
    Prepare()
if err != nil { log.Fatal(err) }

fmt.Println(plan.Report())            // exactly what Execute will do
_, res, err := plan.Execute(ctx, waxlabel.SaveBack())
if err != nil { log.Fatal(err) }
fmt.Println("committed:", res.Committed)
```

`Document` is immutable and detached — it holds no file descriptor and has no
`Close`, so you can scan, cache, and discard it freely. Save destinations are
[`SaveBack`] (atomic in-place rewrite; a no-op writes nothing), [`SaveAsFile`],
and [`WriteTo`] (stream to any `io.Writer`).

## Design

A small set of contracts is stable:

- **Immutable, detached `Document`.** Accessors return deep copies; only
  `Picture` payload bytes are shared read-only. `Inspect()` skips them for bulk
  scans.
- **Presence-aware `tag.TagSet`/`tag.TagPatch` are authoritative**, so *absent*,
  *present-but-empty*, and *present-with-values* are all distinguishable. The
  typed `tag.Tags` struct is a convenience projection.
- **Public, writable canonical key vocabulary** (`tag.Key`). Unknown canonical
  keys pass through unchanged.
- **Preservation-first.** The native document is the base; an edit rewrites only
  the affected field. Legacy containers (stray ID3, APE) are preserved and
  warned by default, never stripped silently.
- **Prepare → Report → Execute.** The plan and the write share state, so the
  report cannot disagree with what is written.
- **Versioned audio identity.** `AudioDigest` carries an algorithm and a named,
  versioned extent so persisted dedup hashes stay interpretable library-wide.

## Format support

| Container | Codec | Read | Write | Notes |
|-----------|-------|:----:|:-----:|-------|
| FLAC | FLAC | ✅ | ✅ | Vorbis comments, pictures, stray-ID3 + CUESHEET/SEEKTABLE preserved |
| Ogg | Vorbis / Opus | — | — | planned |
| MP3 | ID3v2/v1 | — | — | planned |
| WAV | RIFF | — | — | planned |
| MP4 | AAC/ALAC | — | — | planned |
| Matroska | — | — | — | planned (read-only) |
| AIFF | — | — | — | planned |

## Audio identity

Three levels answer different questions:

- `HashAudioEssence` — encoded-essence identity: the audio packets plus
  decoder-critical config (sample rate, channels, bit depth). "Is this the same
  audio?", independent of tags.
- `HashFile` — whole-file identity.
- decoded-PCM identity — needs a decoder; test-only.

## Lint

`Document.Lint()` reports issues a tagger would want to surface or fix: stale
legacy tags, inherited encoder noise, conflicting families, duplicate or invalid
pictures, and malformed dates.

## Safety

All input is treated as untrusted: allocations and recursion are bounded
(`waxerr.ErrSizeTooLarge`, `waxerr.ErrTooDeep`) and the parser never panics
(verified by `FuzzParse`). Saves are durable (temp → fsync → rename → dir
fsync) and detect a file that changed since parse (`waxerr.ErrSourceChanged`).

## License

MIT. All code is reimplemented from public specifications; see
[PROVENANCE.md](PROVENANCE.md).
