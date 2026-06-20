# WaxLabel

A pure-Go library for reading and writing audio-file metadata (tags + embedded
cover art), reimplemented from public specifications.

> **Status: v0.x.** The core model with FLAC, Ogg Vorbis/Opus, MP3, WAV, MP4/M4A,
> AAC, AIFF, and Matroska/WebM read/write are implemented and tested. Other
> formats are in progress; codecs stay internal until v1.0, when validated ones
> are promoted to public `waxlabel/<fmt>` packages. See
> [CHANGELOG.md](CHANGELOG.md) for release notes.

WaxLabel is preservation-first: it treats the file's native metadata as the
base and rewrites only the fields you actually change, copying the audio
verbatim. It is designed for tools that fill in complete, accurate metadata for
large libraries - including files acquired from transcoders, which arrive
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

fmt.Println(plan)                     // full human-readable preview of what Execute will do
_, res, err := plan.Execute(ctx, waxlabel.SaveBack())
if err != nil { log.Fatal(err) }
fmt.Println("committed:", res.Committed)
```

Human-readable text output - `Plan.String()` (printed above), `WriteReport.String()`,
and the CLI's default rendering - is sanitized for the terminal: control and escape
bytes in untrusted tag values are shown as visible `\xNN` escapes, so a hostile file
cannot inject ANSI sequences into your output. For the exact, unmodified bytes, read
the structured accessors (`plan.Changes()`, `doc.Tags()`) or use the CLI's `--json`.

`Document` is immutable and detached - it holds no file descriptor and has no
`Close`, so you can scan, cache, and discard it freely. Save destinations are
[`SaveBack`] (atomic in-place rewrite; a no-op writes nothing), [`SaveAsFile`],
and [`WriteTo`] (stream to any `io.Writer`; pass the source to copy, or `nil` for
a `ParseFile`/`OpenSource` document to use its own).

## Command-line tool

A `waxlabel` CLI lives in `cmd/waxlabel` and dogfoods the library:

```
go run ./cmd/waxlabel dump track.flac                    # tags, properties, pictures, warnings
go run ./cmd/waxlabel plan track.flac --set TITLE=New    # preview a write (writes nothing)
go run ./cmd/waxlabel set  track.flac --set TITLE=New --add ARTIST=Featured --clear ENCODER
go run ./cmd/waxlabel lint track.flac                    # report metadata issues (--fix the safe ones)
go run ./cmd/waxlabel verify track.flac                  # audio-essence identity for dedup
go run ./cmd/waxlabel caps  --format flac                # what a format can store and edit
go run ./cmd/waxlabel copy  track.flac track.m4a         # copy metadata across formats
go run ./cmd/waxlabel diff  a.flac b.flac                # compare canonical metadata
```

Install the binary with `go install github.com/colespringer/waxlabel/cmd/waxlabel@latest`.

- **`dump <file>...`** - tags, audio properties, pictures, and warnings. `--native`
  adds the native blocks and the per-source (family) view. `--recursive` walks
  directory arguments.
- **`plan <file>...`** - the dry-run preview for `set`: resolve edits into a write
  plan and print exactly what `set` would do (including a field-level change
  preview), without touching the file (the report and the write share state).
- **`set <file>...`** - apply edits and save: atomic in-place by default, `-o` writes a
  new file (single input only), a no-op writes nothing. `--verify` checks the written
  audio essence. `--strip-encoder` clears the transcoder stamp; `--recursive` walks
  directory arguments. `--padding N` / `--no-padding` control the free space reserved
  after the metadata (default 8 KiB, reused in place; `--preset minimal` also writes
  none); both `set` and `plan` accept them.
- **`lint <file>...`** - report metadata issues (stale legacy tags, encoder noise,
  conflicting families, bad pictures, malformed dates, missing audio). `--fix`
  applies only the safe, non-destructive remediations and saves; pictures are never
  dropped automatically. `--recursive` walks directory arguments.
- **`verify <file>...`** - the tag-independent audio-essence digest; `--whole-file`
  adds the whole-file digest. `--recursive` walks directory arguments.
- **`caps <file>... | --format <name>`** - what a format can store and edit: per
  known key the read/write level, native representation, fidelity, and cardinality
  (single- vs multi-valued), plus picture/chapter limits. `--all` includes
  read-only keys.
- **`copy <source> <dest>`** - copy `source`'s canonical metadata onto `dest`
  (across formats), rewriting `dest` in place. Each value is carried, downgraded,
  or dropped per `dest`'s capabilities; that loss report prints first. The copy
  overlays the source (keys only in `dest` are kept). `--dry-run` previews the
  result without writing.
- **`diff <a> <b>`** - compare two files' canonical metadata (added/removed/changed
  keys, picture/chapter deltas). `--quiet` reports through the exit code only.

Edits: `--set KEY=VALUE` (replace), `--add KEY=VALUE` (append, for multi-value),
`--clear KEY` (remove), `--strip-encoder`, `--add-cover FILE` (`--force` to embed a
file whose header is not a recognized image), `--remove-pictures`. By default `set`
and `plan` note an unknown key (written as a custom field) or a single-valued key
given multiple values on stderr and continue; `--strict` makes either one fail
(exit 2) instead. Write policy:
`--preset preserve|compatible|canonical|minimal`, `--legacy ...`. The read commands
(`dump`, `verify`, `lint`, and a `diff` operand) accept a single `-` to read
standard input; `dump`, `verify`, and `lint` (like `set` and `plan`) walk directory
arguments with `--recursive`. Every command accepts `--json` for scriptable output:
the list commands (`dump`, `verify`, `lint`, `set`, `plan`, and `caps` over files)
emit a JSON array - one element per input, `[]` when none - so a consumer iterates
(or `jq '.[]'`) regardless of count, while `diff`, `copy`, and `caps --format` emit
a single object.

`ENCODER` is the canonical key for the encoding software/tool (the transcoder
stamp, e.g. ID3 `TSSE` or MP4 `©too`), distinct from `ENCODEDBY` (the encoding
person). A single `--clear ENCODER` or `--strip-encoder` reaches the stamp on
every format.

Exit codes for `dump`/`plan`/`set`/`verify`/`caps`/`copy`: `0` success; `1` error;
`2` usage/invalid key; `3` unsupported format; `4` invalid data; `5` source
changed; `6` I/O; `130` canceled/timeout. **`lint` and `diff` follow the
linter / diff(1) convention instead:** `0` clean/identical; `1` issues found /
differs; `>=2` a structural error (using the same `2`-`6` classes, which outranks
a `1` in a multi-file run). (cobra's built-in `help` and `completion` follow
cobra's own conventions.)

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
- **Public, writable canonical key vocabulary** (`tag.Key`); `tag.KnownKeys()`
  enumerates it. Unknown canonical keys pass through unchanged. Keys are
  Vorbis-permissive (normalized to uppercase; spaces and punctuation are allowed),
  so a key naming characters ID3/MP4 cannot represent may not round-trip to those
  formats.
- **Preservation-first.** The native document is the base; an edit rewrites only
  the affected field. Legacy containers (stray ID3, APE) are preserved and
  warned by default, never stripped silently.
- **Prepare -> Report -> Execute.** The plan and the write share state, so the
  report cannot disagree with what is written.
- **Versioned audio identity.** `AudioDigest` carries an algorithm and a named,
  versioned extent so persisted dedup hashes stay interpretable library-wide.

## Format support

| Container | Codec | Read | Write | Notes |
|-----------|-------|:----:|:-----:|-------|
| FLAC | FLAC | yes | yes | Vorbis comments, pictures, stray-ID3 + CUESHEET/SEEKTABLE preserved |
| Ogg | Vorbis | yes | yes | Vorbis comments + `METADATA_BLOCK_PICTURE`; setup header preserved; audio packets byte-identical |
| Ogg | Opus | yes | yes | OpusTags (+ padding) + pictures; R128 `output_gain` distinct from ReplayGain |
| MP3 | ID3v2/v1 | yes | yes | ID3v2.2/2.3/2.4 read+write (version preserved); ID3v1/APEv2 read into the family view; numeric genre; VBR length |
| WAV | RIFF | yes | yes | LIST/INFO + embedded `id3 ` chunk; id3 authoritative when present, else INFO; pictures via id3; all chunks preserved; RF64/BW64 out of scope |
| MP4 | AAC/ALAC | yes | yes | iTunes `moov.udta.meta.ilst` (text, trkn/disk, covr art, `----` freeform long tail); `free`-atom reuse + all-track `stco`/`co64` fixups; `chpl` preserved; fragmented (moof) rejected |
| Matroska | FLAC/Opus/Vorbis/AAC/... | yes | yes | `.mka`/`.webm`/`.mkv`; scope-aware SimpleTag projection (album/track/edition/chapter) + `Info.Title` + cover-art attachments; canonical edits written at album scope and removed from any other scope that held the key (unedited scoped tags preserved verbatim); size change absorbed into a reserved Void (else tail shifted with Cues/SeekHead/CRC fixups), clusters byte-identical; cover write refused for WebM; chapters and cluster rewrite out of scope |
| AIFF | PCM (AIFF-C) | yes | yes | native NAME/AUTH/`(c) `/ANNO chunks + embedded `ID3 ` chunk; ID3 authoritative when present, else native; pictures via ID3; 80-bit COMM rate; AIFF-C + `id3 ` variant; all chunks preserved |

Ogg writes preserve audio *packet payloads* byte-for-byte (Ogg re-pagination is
allowed); chained/multiplexed streams are read best-effort and reported, but
writing them is refused.

## Audio identity

Three levels answer different questions:

- `HashAudioEssence` - encoded-essence identity: the audio packets plus the
  codec's decoder-critical config (FLAC STREAMINFO; the Vorbis identification +
  setup headers; the Opus head with its channel mapping and output_gain). "Is
  this the same audio?", independent of tags. The extent can be several
  byte ranges, so Ogg's audio page bodies (interleaved with page headers) hash
  correctly.
- `HashFile` - whole-file identity.
- decoded-PCM identity - needs a decoder; test-only.

## Lint

`Document.Lint()` (CLI: `waxlabel lint`) reports issues a tagger would want to
surface or fix: stale legacy tags, inherited encoder noise, conflicting families,
duplicate or invalid pictures, malformed dates, missing audio (a tag-only file),
and truncated audio - a file that declares more audio than it contains. Truncation
is flagged only where it can be told reliably: WAV/AIFF/MP4 against the container's
declared essence size, and a VBR MP3 when its surviving bytes are far too few for
the duration its Xing/Info header declares - so few the implied bitrate falls below
what MPEG can encode. A partial loss that still implies a plausible bitrate, a
mid-stream FLAC cut, and a Xing-less CBR MP3 are indistinguishable from a valid file
without decoding, so they are left unflagged rather than risk a false alarm.
`lint --fix` applies only the safe, non-destructive remediations
(clearing the encoder stamp, stripping legacy containers) and saves, then
re-lints the saved file so what it reports as "fixed" or still "not auto-fixed" is
the truth on disk, not the fixer's intent. Pictures are never dropped
automatically.

## Discovering editable metadata

`tag.KnownKeys()` enumerates the canonical vocabulary, and each `tag.Key` reports
its `Description()` and `Multivalued()` cardinality. `waxlabel.CapabilitiesFor(format)`
answers what a format can store and edit with no file in hand (the file-aware
`Document.Capabilities()` answers it for a parsed file); both feed the
`waxlabel caps` command. Together they let a UI render an edit form, or a script
discover fields, without hard-coding the key list.

## Safety

All input is treated as untrusted: allocations and recursion are bounded
(`waxerr.ErrSizeTooLarge`, `waxerr.ErrTooDeep`) and the parser never panics
(verified by `FuzzParse`). Saves are durable (temp -> fsync -> rename -> dir
fsync) and detect a file that changed since parse (`waxerr.ErrSourceChanged`).

The atomic save writes a temp file in the target's directory and renames it into
place. Three consequences worth knowing:

- **Symlinks are followed.** Editing through a symlink resolves and rewrites the
  *target*, leaving the link itself in place - it is not replaced by a regular file.
- **Hard links are broken.** The rename swaps the directory entry, so any other
  hard link to the original inode keeps the pre-edit contents. This is inherent to
  atomic-rename saves.
- **Read-only files are still rewritten** when their directory is writable (the
  replacement is a fresh file), and the original file's mode is preserved onto it.

## License

MIT. All code is reimplemented from public specifications; see
[PROVENANCE.md](PROVENANCE.md).
