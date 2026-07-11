# WaxLabel

WaxLabel is a pure-Go library and command-line tool for reading and writing
audio-file metadata: tags, embedded pictures, chapters, and synced lyrics. It is
preservation-first: edits are planned against the parsed native structure, metadata
is rewritten only where needed, and audio bytes are copied rather than transcoded.

It reads and writes FLAC, Ogg Vorbis, Ogg Opus, MP3, WAV, MP4/M4A, raw AAC/ADTS,
Matroska/WebM, and AIFF/AIFF-C.

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
	if err != nil {
		log.Fatal(err)
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
leading bytes, not its extension. All data commands accept `--json`. `-o` writes
atomically and refuses an existing target unless `--overwrite` is given.

`lint --json` findings carry a machine-readable `code` and `severity`; the exit code
reflects the highest-precedence result. See `waxlabel <command> --help` and the
package documentation for the exit-code table and finding codes.

## Format Support

| Format | Metadata | Notes |
| --- | --- | --- |
| FLAC | read/write | Vorbis comments, FLAC pictures, `CHAPTERxxx` chapters, `SYNCEDLYRICS` (LRC); padding is fully controllable. |
| Ogg Vorbis / Opus | read/write | Vorbis comments, `METADATA_BLOCK_PICTURE`, `CHAPTERxxx` chapters, `SYNCEDLYRICS` (LRC). |
| MP3 | read/write | ID3v2 (`CHAP`/`CTOC` chapters, `SYLT` lyrics); new tags are ID3v2.3. ID3v1/APEv2 are surfaced as legacy. |
| WAV | read/write | RIFF LIST/INFO plus embedded `id3 ` (chapters and lyrics); chunks are preserved. |
| MP4 / M4A / M4B | read/write | iTunes `ilst`, cover art, Nero and QuickTime chapters. Fragmented MP4 is rejected. |
| Matroska / WebM | read/write | Scoped SimpleTags, segment title, attachments, default-edition chapters. WebM cannot write cover attachments. |
| AAC (ADTS) | read/write | Front ID3v2 tag (new tags are ID3v2.4) plus ADTS frames. |
| AIFF / AIFF-C | read/write | Native text chunks plus embedded `ID3 `; chunks are preserved. |

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
| Ogg Opus | read full, write full · METADATA_BLOCK_PICTURE | read full, write full · VorbisComment CHAPTERxxx | read full, write full · SYNCEDLYRICS comment (LRC) |
| Ogg Vorbis | read full, write full · METADATA_BLOCK_PICTURE | read full, write full · VorbisComment CHAPTERxxx | read full, write full · SYNCEDLYRICS comment (LRC) |
| WAV | read full, write full · APIC (id3 chunk) | read full, write full · ID3v2 CHAP/CTOC frames (id3 chunk) | read full, write full · ID3v2 SYLT frame |
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
