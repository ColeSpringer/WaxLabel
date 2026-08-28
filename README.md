# WaxLabel

WaxLabel is a pure-Go library and command-line tool for reading and writing
audio-file metadata: tags, embedded pictures, chapters, and synced lyrics. It is
preservation-first: edits are planned against the parsed native structure, metadata
is rewritten only where needed, and audio bytes are copied rather than transcoded.

It reads and writes FLAC, Ogg Vorbis, Ogg Opus, Ogg FLAC, MP3, WAV (including RF64/BW64),
MP4/M4A, raw AAC/ADTS, Matroska/WebM, AIFF/AIFF-C, WavPack, Monkey's Audio, and Musepack,
and reads WMA/ASF.

The public API lives in `github.com/colespringer/waxlabel` and
`github.com/colespringer/waxlabel/tag`; codec packages are internal.

## Install

```sh
go get github.com/colespringer/waxlabel            # library
go install github.com/colespringer/waxlabel/cmd/waxlabel@latest   # CLI
```

WaxLabel requires Go 1.26 or newer. The library uses only the standard library; the
CLI uses Cobra.

## Library

```go
package main

import (
	"context"
	"fmt"
	"log"

	waxlabel "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

func main() {
	ctx := context.Background()

	doc, err := waxlabel.ParseFile(ctx, "track.flac")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(doc.Fields().Title)

	plan, err := doc.Edit().
		Set(tag.Title, "New Title").
		Set(tag.Artist, "Lead", "Featured").
		Clear(tag.Encoder).
		Prepare()
	if err != nil {
		log.Fatal(err)
	}

	_, result, err := plan.Execute(ctx, waxlabel.SaveBack())
	// A failed write is an error AND Committed false. An error with Committed true
	// means the bytes landed and a step after them did not; the edit is applied and
	// the plan is spent, so it is a warning, not a reason to abort.
	if err != nil && !result.Committed {
		log.Fatal(err)
	}
	if err != nil {
		log.Println("warning:", err)
	}
	fmt.Println("committed:", result.Committed)
}
```

`Parse`, `ParseFile`, and `OpenSource` return an immutable `Document` that holds no
open file descriptor. Editing starts with `Document.Edit()`, resolves through
`Editor.Prepare()`, and writes only when the resulting `Plan` is executed. Write
destinations:

- `SaveBack()` atomically rewrites the parsed file in place (a no-op writes nothing).
- `SaveAsFile(path)` writes a complete new file.
- `WriteTo(w, source)` streams a complete output to an `io.Writer`.

## CLI

```sh
waxlabel dump track.flac
waxlabel plan track.flac --set TITLE="New Title"
waxlabel set track.flac --set TITLE="New Title" --add ARTIST=Featured
waxlabel lint track.flac --fix
waxlabel verify track.flac
waxlabel caps --format flac
waxlabel keys
waxlabel copy source.flac dest.m4a
waxlabel diff before.flac after.flac
waxlabel export-picture track.flac -o cover.jpg
```

| Command | Purpose |
| --- | --- |
| `dump <file>...` | Show tags, audio properties, pictures, chapters, synced lyrics, and warnings. `--native` also shows native blocks. |
| `plan <file>...` | Preview an edit without writing. |
| `set <file>...` | Apply edits and save. Use `-o` for a new output file. |
| `lint <file>...` | Report metadata issues. `--fix` applies only safe, non-destructive fixes; a legacy container is stripped only when fully redundant with the canonical tags. |
| `verify <file>...` | Print tag-independent audio-essence digests. `--whole-file` hashes every byte. |
| `caps <file>` or `caps --format <name>` | Show what a file or format can store and edit. |
| `keys` | List the canonical tag vocabulary and cardinality. |
| `copy <source> <dest>` | Overlay source metadata onto the destination, reporting what carries, downgrades, or drops. |
| `diff <a> <b>` | Compare canonical tags, pictures, chapters, and synced lyrics. |
| `export-picture <file>` | Write one embedded picture to `-o` FILE. `--picture` selects by role or index. |

Edits are driven by `--set KEY=VALUE`, `--add KEY=VALUE`, and `--clear KEY`, plus
picture (`--add-cover`, `--add-picture`, `--remove-picture`), chapter
(`--add-chapter`, `--clear-chapters`), and synced-lyric
(`--synced-lyrics-file`, `--add-synced-lyric`, `--synced-lyrics-lang`) flags. Write
shaping is controlled by `--preset`, `--legacy`, and `--padding`. Run
`waxlabel <command> --help` for the full flag list.

Read commands accept `-` for standard input, and `dump`, `verify`, `lint`, `plan`,
and `set` can walk directories with `--recursive`. Format is detected from a file's
leading bytes, not its extension, except under `--recursive`: the walker picks its
candidates by extension first, so a valid FLAC named `noext` is skipped by a recursive
run while working normally when named directly. All data commands accept `--json`. `-o`
writes atomically and refuses an existing target unless `--overwrite` is given.

`lint --json` findings carry a machine-readable `code` and `severity`; the exit code
reflects the highest-precedence result. See `waxlabel <command> --help` and the
package documentation for the finding codes.

### Exit codes

Every failure carries a stable machine `code` (in `--json`, the error envelope's
`code` field) alongside its exit status:

| Exit | Machine code | Meaning |
| --- | --- | --- |
| 0 | none, or `broken-pipe` | Success, or a closed output pipe (`... \| head`) |
| 1 | `error` | Unclassified failure |
| 2 | `usage`, `invalid-key`, `needs-file` | Bad invocation, or an invalid canonical key |
| 3 | `unsupported-format`, `unsupported-tag`, `unsupported-stream`, `unsupported-alignment`, `unsupported-fragmentation`, `picture-too-large` | The format is unsupported, or the file reads but the requested write is refused |
| 4 | `invalid-data` | The file is corrupt or violates its format |
| 5 | `source-changed` | The file changed between the read and the save-back |
| 6 | `not-found`, `io` | A wrong path, or a local I/O failure |
| 7 | `input-too-large` | A streamed input exceeded `--max-size` |
| 130 | `canceled`, `timeout` | Interrupted, or the deadline expired |

A multi-file run exits with the most-severe class it saw, which is not the numeric
maximum: `canceled`/`timeout` > `source-changed` > `invalid-data` > `input-too-large`
> `unsupported-format` > `unsupported-tag` > `unsupported-stream` >
`unsupported-alignment` > `unsupported-fragmentation` > `picture-too-large` >
`io` > `not-found` >
`usage`/`invalid-key`/`needs-file` > `error` > `broken-pipe`. So a corrupt file
(exit 4) outranks a mistyped path (exit 6).

## Format Support

| Format | Metadata | Notes |
| --- | --- | --- |
| FLAC | read/write | Vorbis comments, FLAC pictures, `CHAPTERxxx` chapters, `SYNCEDLYRICS` (LRC); padding is fully controllable. |
| Ogg Vorbis / Opus | read/write | Vorbis comments, `METADATA_BLOCK_PICTURE`, `CHAPTERxxx` chapters, `SYNCEDLYRICS` (LRC). |
| Ogg FLAC (`.oga`) | read/write | Vorbis comments and chapters as above; cover art is a native FLAC `PICTURE` block, not a comment. |
| MP3 | read/write | ID3v2 (`CHAP`/`CTOC` chapters, `SYLT` lyrics); new tags are ID3v2.3. ID3v1/APEv2 are surfaced as legacy. |
| WAV / RF64 / BW64 | read/write | RIFF LIST/INFO plus embedded `id3 ` (chapters and lyrics); chunks are preserved. The 64-bit RF64/BW64 form is kept on save-back, with `ds64` recomputed. |
| MP4 / M4A / M4B / MOV | read/write | iTunes `ilst`, cover art, Nero and QuickTime chapters. Fragmented MP4 (a `moof`) is read-only; a `moov` declaring `mvex` with no fragment present is written normally. |
| Matroska / WebM | read/write | Scoped SimpleTags, segment title, attachments, default-edition chapters. WebM cannot write cover attachments. |
| AAC (ADTS) | read/write | Front ID3v2 tag (new tags are ID3v2.4) plus ADTS frames. |
| AIFF / AIFF-C | read/write | Native text chunks plus embedded `ID3 `; chunks are preserved. |
| WavPack | read/write | APEv2 items and the `Cover Art` convention; a trailing ID3v1 is surfaced as legacy. |
| Monkey's Audio | read/write | APEv2 as above; SV3.98+ and the older inline header are both read. |
| Musepack | read/write | APEv2 as above, for both SV7 and SV8. A leading ID3v2 is surfaced as legacy. |
| WMA / ASF | read-only | Content Description, `WM/*` descriptors, and `WM/Picture` cover art. WaxLabel does not write ASF. |

When `set` authors a structural edit a format cannot store (e.g. cover art on WebM,
or chapters on a format with no chapter store), it drops that item with a warning and
applies the rest of the edit. `set --strict` promotes such drops to failures.

The table below is generated from the same capability model used by `waxlabel caps`.

<!-- BEGIN caps (generated from codec Capabilities; see tests/capability_test.go) -->
| Format | Pictures | Chapters | Synced Lyrics |
| --- | --- | --- | --- |
| AAC (ADTS) | read full, write full · APIC frame | read full, write full · ID3v2 CHAP/CTOC frames | read full, write full · ID3v2 SYLT frame |
| AIFF | read full, write full · APIC (ID3 chunk) | read full, write full · ID3v2 CHAP/CTOC frames (ID3 chunk) | read full, write full · ID3v2 SYLT frame |
| FLAC | read full, write full · FLAC PICTURE block | read full, write full · VorbisComment CHAPTERxxx | read full, write full · SYNCEDLYRICS comment (LRC) |
| MP3 | read full, write full · APIC frame | read full, write full · ID3v2 CHAP/CTOC frames | read full, write full · ID3v2 SYLT frame |
| MP4 | read full, write full · covr atom (JPEG/PNG/BMP) | read full, write full · Nero chpl and a QuickTime chapter text track | read none, write none |
| Matroska | read full, write full · AttachedFile (image attachment) | read full, write full · Chapters > EditionEntry > ChapterAtom (default edition) | read none, write none |
| Monkey's Audio | read full, write full · APEv2 Cover Art item | read none, write none | read none, write none |
| Musepack | read full, write full · APEv2 Cover Art item | read none, write none | read none, write none |
| Ogg FLAC | read full, write full · FLAC PICTURE block | read full, write full · VorbisComment CHAPTERxxx | read full, write full · SYNCEDLYRICS comment (LRC) |
| Ogg Opus | read full, write full · METADATA_BLOCK_PICTURE | read full, write full · VorbisComment CHAPTERxxx | read full, write full · SYNCEDLYRICS comment (LRC) |
| Ogg Vorbis | read full, write full · METADATA_BLOCK_PICTURE | read full, write full · VorbisComment CHAPTERxxx | read full, write full · SYNCEDLYRICS comment (LRC) |
| WAV | read full, write full · APIC (id3 chunk) | read full, write full · ID3v2 CHAP/CTOC frames (id3 chunk) | read full, write full · ID3v2 SYLT frame |
| WMA | read full, write none · WM/Picture descriptor | read none, write none | read none, write none |
| WavPack | read full, write full · APEv2 Cover Art item | read none, write none | read none, write none |
<!-- END caps -->

Some format-specific limits are intentional (for example, MP4 cover art drops the
picture description, ID3 chapters store no per-chapter language, and Matroska writes
random UIDs so chapter/attachment rewrites are not byte-reproducible). These are
documented in the package documentation and surfaced as warnings at write time.

## Safety

Input is treated as untrusted: parsers use bounded allocation and recursion limits,
fuzz tests cover arbitrary input, and human output sanitizes terminal-control bytes
(JSON output uses exact machine-readable values).

Save-back writes go to a temp file in the target directory, are fsync'd, and renamed
into place. If the source changed since parse, `SaveBack()` refuses with
`waxerr.ErrSourceChanged` rather than overwriting newer bytes. Atomic renames have
normal filesystem consequences: editing through a symlink rewrites the target and
leaves the link, other hard links keep pointing at the old inode, and a read-only
file can be replaced when its directory is writable (its mode is preserved).

## License

MIT.

## Acknowledgements

Mutagen, TagLib, bogem/id3v2, sentriz/go-taglib, and libogg were direct influences on
WaxLabel's design and test cross-checks. WaxLabel's implementation follows public
specifications and does not copy their code.
