# WaxLabel

Pure-Go library and CLI for reading and writing audio metadata: tags, embedded
pictures, chapters, and synced lyrics. Preservation-first: edits are planned
against the parsed native structure, metadata is rewritten only where needed,
and audio bytes are copied rather than transcoded.

Read/write: FLAC, Ogg Vorbis, Ogg Opus, Ogg FLAC, MP3, WAV (RF64/BW64), MP4/M4A,
raw AAC/ADTS, Matroska/WebM, AIFF/AIFF-C, WavPack, Monkey's Audio, Musepack.
Read-only: WMA/ASF.

Public API: `github.com/colespringer/waxlabel` and
`github.com/colespringer/waxlabel/tag`. Codec packages are internal.

## Install

```sh
go get github.com/colespringer/waxlabel            # library
go install github.com/colespringer/waxlabel/cmd/waxlabel@latest   # CLI
```

Requires Go 1.26+. Library uses only the standard library; CLI uses Cobra.

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
	// Failed write: err != nil AND Committed false. err with Committed true means
	// bytes landed but a later step failed; the edit is applied and the plan is spent.
	if err != nil && !result.Committed {
		log.Fatal(err)
	}
	if err != nil {
		log.Println("warning:", err)
	}
	fmt.Println("committed:", result.Committed)
}
```

`Parse`, `ParseFile`, and `OpenSource` return an immutable `Document` with no open
file descriptor. Edit with `Document.Edit()`, resolve with `Editor.Prepare()`,
write by executing the `Plan`. Destinations:

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
| `dump <file>...` | Tags, audio properties, pictures, chapters, synced lyrics, warnings. `--native` adds native blocks. |
| `plan <file>...` | Preview an edit without writing. |
| `set <file>...` | Apply edits and save. `-o` writes a new file. |
| `lint <file>...` | Report metadata issues. `--fix` applies safe, non-destructive fixes; a legacy container is stripped only when fully redundant with canonical tags. |
| `verify <file>...` | Tag-independent audio-essence digests. `--whole-file` hashes every byte. |
| `clean <dir>...` | List `.waxlabel-*.tmp` leftovers from interrupted writes; `--remove` deletes them, `--all` includes files newer than an hour. |
| `caps <file>` or `caps --format <name>` | What a file or format can store and edit. |
| `keys` | Canonical tag vocabulary and cardinality. |
| `copy <source> <dest>` | Overlay source metadata onto dest; report what carries, downgrades, or drops. `--strict` refuses a non-lossless transfer. |
| `diff <a> <b>` | Compare canonical tags, pictures, chapters, and synced lyrics. |
| `export-picture <file>` | Write one embedded picture to `-o` FILE. `--picture` selects by role or index. |

Edits use `--set KEY=VALUE`, `--add KEY=VALUE`, `--clear KEY`, plus picture
(`--add-cover`, `--add-picture`, `--remove-picture`), chapter
(`--add-chapter`, `--clear-chapters`), synced-lyric
(`--synced-lyrics-file`, `--add-synced-lyric`, `--synced-lyrics-lang`), and
`--output-gain` (Ogg Opus header gain). Write shaping: `--preset`, `--legacy`,
`--padding`. `--id3-multi null|repeat|slash` controls ID3v2.3 multi-value storage.
See `waxlabel <command> --help`. `--legacy strip` (and `--preset minimal`) removes
ID3v1/APEv2/stray-ID3 unconditionally; when one holds the only copy of a value, the
plan says so and `--strict` refuses the write.

Read commands accept `-` for stdin. `dump`, `verify`, `lint`, `plan`, and `set`
walk directories with `--recursive`. Format comes from leading bytes, not extension,
except under `--recursive` (extension filter first): a valid FLAC named `noext` is
skipped recursively but works when named directly. An unreadable directory is an
`io` error for that path (exit 6); the rest of the tree continues. All data commands
accept `--json`. `-o` writes atomically and refuses an existing target unless
`--overwrite`.

`lint --json` findings include `code`, `severity`, and `fixable` (whether
`--fix` acts). Exit code reflects the highest-precedence result. Finding codes
are in `waxlabel <command> --help` and package docs.

### Exit codes

Every failure has a stable machine `code` (JSON error envelope `code` field) and
an exit status:

| Exit | Machine code | Meaning |
| --- | --- | --- |
| 0 | none, or `broken-pipe` | Success, or a closed output pipe (`... \| head`) |
| 1 | `error` | Unclassified failure |
| 2 | `usage`, `invalid-key`, `needs-file` | Bad invocation, invalid canonical key, or `--strict` refusal |
| 3 | `unsupported-format`, `unsupported-tag`, `unsupported-stream`, `unsupported-alignment`, `unsupported-fragmentation`, `picture-too-large` | Unsupported format, or file reads but format refuses the write |
| 4 | `invalid-data` | Corrupt file or format violation |
| 5 | `source-changed` | File changed between read and save-back |
| 6 | `not-found`, `io` | Wrong path, or local I/O failure |
| 7 | `input-too-large` | Streamed input exceeded `--max-size` |
| 130 | `canceled`, `timeout` | Interrupted, or deadline expired |

Multi-file runs exit with the most-severe class (not numeric max):
`canceled`/`timeout` > `source-changed` > `invalid-data` > `input-too-large` >
`unsupported-format` > `unsupported-tag` > `unsupported-stream` >
`unsupported-alignment` > `unsupported-fragmentation` > `picture-too-large` >
`io` > `not-found` > `usage`/`invalid-key`/`needs-file` > `error` > `broken-pipe`.
A corrupt file (exit 4) outranks a mistyped path (exit 6).

## Format Support

| Format | Metadata | Notes |
| --- | --- | --- |
| FLAC | read/write | Vorbis comments, FLAC pictures, `CHAPTERxxx` chapters, `SYNCEDLYRICS` (LRC); padding fully controllable. |
| Ogg Vorbis / Opus | read/write | Vorbis comments, `METADATA_BLOCK_PICTURE`, `CHAPTERxxx`, `SYNCEDLYRICS` (LRC). Opus also has `OpusHead` output gain (`--output-gain`); rebases `R128_TRACK_GAIN`/`R128_ALBUM_GAIN` per RFC 7845. |
| Ogg FLAC (`.oga`) | read/write | Vorbis comments and chapters as above; cover art is a native FLAC `PICTURE` block, not a comment. |
| MP3 | read/write | ID3v2 (`CHAP`/`CTOC`, `SYLT`); new tags are ID3v2.3. ID3v1/APEv2 surfaced as legacy. |
| WAV / RF64 / BW64 | read/write | RIFF LIST/INFO plus embedded `id3 `; chunks preserved. RF64/BW64 form kept on save-back; `ds64` recomputed. |
| MP4 / M4A / M4B / MOV | read/write | iTunes `ilst`, `mdta` keys (ffmpeg `+use_metadata_tags`), classic `moov.udta` text, cover art, Nero and QuickTime chapters. Fragmented MP4 (`moof`) is read-only; `moov` with `mvex` but no fragment writes normally. |
| Matroska / WebM | read/write | Scoped SimpleTags, segment title, attachments, default-edition chapters. WebM cannot write cover attachments. |
| AAC (ADTS) | read/write | Front ID3v2 (new tags ID3v2.4) plus ADTS frames. HE-AAC reports played rate, channels, and profile from frames when the header cannot. |
| AIFF / AIFF-C | read/write | Native text chunks plus embedded `ID3 `; chunks preserved. |
| WavPack | read/write | APEv2 and `Cover Art` convention; trailing ID3v1 as legacy. |
| Monkey's Audio | read/write | APEv2 as above; SV3.98+ and older inline header both read. |
| Musepack | read/write | APEv2 for SV7 and SV8. SV8 chapter packets read and preserved, not written. Leading ID3v2 as legacy. |
| WMA / ASF | read-only | Content Description, `WM/*`, `WM/Picture`, Marker Object chapters. No ASF writes. |

When `set` authors a structural edit a format cannot store (e.g. cover art on WebM,
chapters with no chapter store), it drops that item with a warning and applies the
rest. `set --strict` promotes drops to failures (exit 2). `copy --strict` refuses a
non-lossless transfer, or when writing the destination would itself lose metadata.
Copy onto a read-only destination (WMA, fragmented MP4) is refused at exit 3 after
the per-field report; not a silent no-op.

Table below is generated from the same capability model as `waxlabel caps`.

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
| Musepack | read full, write full · APEv2 Cover Art item | read full, write none · SV8 chapter packets | read none, write none |
| Ogg FLAC | read full, write full · FLAC PICTURE block | read full, write full · VorbisComment CHAPTERxxx | read full, write full · SYNCEDLYRICS comment (LRC) |
| Ogg Opus | read full, write full · METADATA_BLOCK_PICTURE | read full, write full · VorbisComment CHAPTERxxx | read full, write full · SYNCEDLYRICS comment (LRC) |
| Ogg Vorbis | read full, write full · METADATA_BLOCK_PICTURE | read full, write full · VorbisComment CHAPTERxxx | read full, write full · SYNCEDLYRICS comment (LRC) |
| WAV | read full, write full · APIC (id3 chunk) | read full, write full · ID3v2 CHAP/CTOC frames (id3 chunk) | read full, write full · ID3v2 SYLT frame |
| WMA | read full, write none · WM/Picture descriptor | read full, write none · Marker Object | read none, write none |
| WavPack | read full, write full · APEv2 Cover Art item | read none, write none | read none, write none |
<!-- END caps -->

Some limits are intentional (MP4 cover drops picture description; ID3 chapters store
no per-chapter language; Matroska writes random UIDs so chapter/attachment rewrites
are not byte-reproducible). Documented in package docs and surfaced as write-time
warnings.

## Safety

Input is untrusted: bounded allocation and recursion, fuzz coverage, and human
output that sanitizes terminal-control bytes and invisible/reordering Unicode
(bidi controls, ZWSP, word joiner, BOM, line/paragraph separators). Line breaks
print as indented continuations only for LYRICS, COMMENT, DESCRIPTION, and
LONGDESCRIPTION; elsewhere as `\x0a` (JSON keeps exact values).

Save-back: temp file in the target directory, fsync, rename. SIGKILL mid-write can
leave the temp; recursive commands note leftovers and `waxlabel clean` lists or
removes them. If the source changed since parse, `SaveBack()` refuses with
`waxerr.ErrSourceChanged`. Atomic rename consequences: editing through a symlink
rewrites the target and leaves the link; other hard links keep the old inode; a
read-only file can be replaced when its directory is writable (mode preserved).

## License

MIT.

## Acknowledgements

Mutagen, TagLib, bogem/id3v2, sentriz/go-taglib, and libogg influenced design and
test cross-checks. Implementation follows public specifications and does not copy
their code.
