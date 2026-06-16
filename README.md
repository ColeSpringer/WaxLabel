# WaxLabel

A pure-Go library for reading and writing audio-file metadata (tags + embedded
cover art), reimplemented from public specifications.

> **Status: v0.x.** The core model with FLAC, Ogg Vorbis/Opus, MP3, and WAV
> read/write are implemented and tested. Other formats are in progress; codecs
> stay internal until v1.0, when validated ones are promoted to public
> `waxlabel/<fmt>` packages.

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

## Command-line tool

A `waxlabel` CLI lives in `cmd/waxlabel` and dogfoods the library:

```
go run ./cmd/waxlabel dump track.flac                    # tags, properties, pictures, warnings
go run ./cmd/waxlabel plan track.flac --set TITLE=New    # preview a write (writes nothing)
go run ./cmd/waxlabel set  track.flac --set TITLE=New --add ARTIST=Featured --clear ENCODER
go run ./cmd/waxlabel verify track.flac                  # audio-essence identity for dedup
```

Install the binary with `go install github.com/colespringer/waxlabel/cmd/waxlabel@latest`.

- **`dump <file>...`** — tags, audio properties, pictures, and warnings. `--native`
  adds the native blocks and the per-source (family) view.
- **`plan <file>`** — resolve edits into a write plan and print exactly what `set`
  would do, without touching the file (the report and the write share state).
- **`set <file>`** — apply edits and save: atomic in-place by default, `-o` writes a
  new file, a no-op writes nothing. `--verify` checks the written audio essence.
- **`verify <file>...`** — the tag-independent audio-essence digest; `--whole-file`
  adds the whole-file digest.

Edits: `--set KEY=VALUE` (replace), `--add KEY=VALUE` (append, for multi-value),
`--clear KEY` (remove), `--add-cover FILE`, `--remove-pictures`. Write policy:
`--preset preserve|compatible|canonical|minimal`, `--legacy …`. Every command
accepts `--json` for scriptable output.

Exit codes for `dump`/`plan`/`set`/`verify`: `0` success · `1` error · `2`
usage/invalid key · `3` unsupported format · `4` invalid data · `5` source
changed · `6` I/O · `130` canceled/timeout. (cobra's built-in `help` and
`completion` follow cobra's own conventions.)

> The **library** has no third-party dependencies. The CLI (package `main` under
> `cmd/`) uses `spf13/cobra`; thanks to Go module-graph pruning, code that imports
> only `github.com/colespringer/waxlabel` never compiles or downloads it.

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
| Ogg | Vorbis | ✅ | ✅ | Vorbis comments + `METADATA_BLOCK_PICTURE`; setup header preserved; audio packets byte-identical |
| Ogg | Opus | ✅ | ✅ | OpusTags (+ padding) + pictures; R128 `output_gain` distinct from ReplayGain |
| MP3 | ID3v2/v1 | ✅ | ✅ | ID3v2.2/2.3/2.4 read+write (version preserved); ID3v1/APEv2 read into the family view; numeric genre; VBR length |
| WAV | RIFF | ✅ | ✅ | LIST/INFO + embedded `id3 ` chunk; id3 authoritative when present, else INFO; pictures via id3; all chunks preserved; RF64/BW64 out of scope |
| MP4 | AAC/ALAC | — | — | planned |
| Matroska | — | — | — | planned (read-only) |
| AIFF | — | — | — | planned |

Ogg writes preserve audio *packet payloads* byte-for-byte (Ogg re-pagination is
allowed); chained/multiplexed streams are read best-effort and reported, but
writing them is refused.

## Audio identity

Three levels answer different questions:

- `HashAudioEssence` — encoded-essence identity: the audio packets plus the
  codec's decoder-critical config (FLAC STREAMINFO; the Vorbis identification +
  setup headers; the Opus head with its channel mapping and output_gain). "Is
  this the same audio?", independent of tags. The extent can be several
  byte ranges, so Ogg's audio page bodies (interleaved with page headers) hash
  correctly.
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
