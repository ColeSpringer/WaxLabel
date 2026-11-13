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
| `diff <a> <b>` | Compare canonical tags, pictures, chapters, and synced lyrics, and comment and lyrics descriptions and languages where both formats store them. |
| `export-picture <file>` | Write one embedded picture to `-o` FILE. `--picture` selects by role or index. |

Edits use `--set KEY=VALUE`, `--add KEY=VALUE`, `--clear KEY`, plus picture
(`--add-cover`, `--add-picture`, `--remove-picture`), chapter
(`--add-chapter`, `--clear-chapters`), synced-lyric
(`--synced-lyrics-file`, `--add-synced-lyric`, `--synced-lyrics-lang`), and
`--output-gain` (Ogg Opus header gain). `--comment-desc`, `--comment-lang`,
`--lyrics-desc` and `--lyrics-lang` give the `COMMENT` or `LYRICS` values an
invocation sets or adds a description and language; ID3 stores both, Matroska the
language, and other formats drop them with a warning. Write shaping: `--preset`, `--legacy`,
`--padding`. `--id3-multi null|repeat|slash` sets how an ID3v2.3 tag stores multiple values:
a new MP3 tag, or an existing v2.3 tag in any container; ID3v2.4 tags ignore it.
See `waxlabel <command> --help`. `--legacy strip` (and `--preset minimal`) removes
ID3v1/Lyrics3/APEv2/stray-ID3 unconditionally; when one holds the only copy of a value, the
plan says so and `--strict` refuses the write. On WAV and AIFF it moves the LIST/INFO and
native text chunk values into the ID3 chunk and removes those chunks, with the same warning
for a value or item that cannot move.

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
| 3 | `unsupported-format`, `unsupported-tag`, `unsupported-stream`, `unsupported-alignment`, `unsupported-fragmentation`, `picture-too-large`, `content-too-large` | Unsupported format, or file reads but format refuses the write |
| 4 | `invalid-data` | Corrupt file or format violation |
| 5 | `source-changed` | File changed between read and save-back |
| 6 | `not-found`, `io` | Wrong path, or local I/O failure |
| 7 | `input-too-large` | Streamed input exceeded `--max-size` |
| 130 | `canceled`, `timeout` | Interrupted, or deadline expired |

Multi-file runs exit with the most-severe class (not numeric max):
`canceled`/`timeout` > `source-changed` > `invalid-data` > `input-too-large` >
`unsupported-format` > `unsupported-tag` > `unsupported-stream` >
`unsupported-alignment` > `unsupported-fragmentation` >
`picture-too-large`/`content-too-large` > `io` > `not-found` >
`usage`/`invalid-key`/`needs-file` > `error` > `broken-pipe`.
A corrupt file (exit 4) outranks a mistyped path (exit 6).

## Format Support

| Format | Metadata | Notes |
| --- | --- | --- |
| FLAC | read/write | Vorbis comments, FLAC pictures, `CHAPTERxxx` chapters, `SYNCEDLYRICS` (LRC); padding fully controllable. Leading ID3v2, trailing ID3v1, APEv2 and Lyrics3 as legacy. |
| Ogg Vorbis / Opus | read/write | Vorbis comments, `METADATA_BLOCK_PICTURE`, `CHAPTERxxx`, `SYNCEDLYRICS` (LRC). Opus also has `OpusHead` output gain (`--output-gain`); rebases `R128_TRACK_GAIN`/`R128_ALBUM_GAIN` per RFC 7845. |
| Ogg FLAC (`.oga`) | read/write | Vorbis comments and chapters as above; cover art is a native FLAC `PICTURE` block, not a comment. |
| MP3 | read/write | ID3v2 (`CHAP`/`CTOC`, `SYLT`); new tags are ID3v2.3. Trailing ID3v1, APEv2 and Lyrics3 as legacy. |
| WAV / RF64 / BW64 | read/write | RIFF LIST/INFO plus embedded `id3 `; chunks preserved. RF64/BW64 form kept on save-back; `ds64` recomputed. |
| MP4 / M4A / M4B / MOV | read/write | iTunes `ilst`, `mdta` keys (ffmpeg `+use_metadata_tags`, and the `moov.meta` store Apple's recorders write), classic `moov.udta` text, cover art, Nero and QuickTime chapters. Fragmented MP4 (`moof`) is read-only; `moov` with `mvex` but no fragment writes normally. |
| Matroska / WebM | read/write | Scoped SimpleTags, segment title, attachments, default-edition chapters. Tags of an attachment, chapter, edition or video track stay out of the file's tags. Beside a `TITLE` at level 40 or under, the album-level `TITLE` and `ARTIST` are `ALBUM` and `ALBUMARTIST`. Attachments an edit keeps are copied verbatim. WebM cannot write cover attachments or hidden/disabled chapter flags. |
| AAC (ADTS) | read/write | Front ID3v2 (new tags ID3v2.4) plus ADTS frames. HE-AAC reports played rate, channels, and profile from frames when the header cannot. Trailing ID3v1, APEv2 and Lyrics3 as legacy. |
| AIFF / AIFF-C | read/write | Native text chunks plus embedded `ID3 `; chunks preserved. |
| WavPack | read/write | APEv2 and `Cover Art` convention; leading ID3v2, trailing ID3v1 and Lyrics3 as legacy. |
| Monkey's Audio | read/write | APEv2 and legacy tags as above; SV3.98+ and older inline header both read. |
| Musepack | read/write | APEv2 for SV7 and SV8. SV8 chapter packets read and preserved, not written. Leading ID3v2, trailing ID3v1 and Lyrics3 as legacy. |
| WMA / ASF | read-only | Content Description, `WM/*`, `WM/Picture`, Marker Object chapters. No ASF writes. |

When `set` authors a structural edit a format cannot store (cover art on WebM,
chapters with no chapter store), it drops that item with a warning and applies the
rest. `set --strict` promotes drops to failures (exit 2). `copy --strict` refuses a
non-lossless transfer, or one whose destination write would itself lose metadata.
Copy onto a read-only destination (WMA, fragmented MP4) exits 3 after the per-field
report.

Table below is generated from the same capability model as `waxlabel caps`.

<!-- BEGIN caps (generated from codec Capabilities; see tests/capability_test.go) -->
| Format | Pictures | Chapters | Synced Lyrics | Comment and Lyrics Qualifiers |
| --- | --- | --- | --- | --- |
| AAC (ADTS) | read full, write full · APIC frame | read full, write full · ID3v2 CHAP/CTOC frames | read full, write full · ID3v2 SYLT frame | description, language |
| AIFF | read full, write full · APIC (ID3 chunk) | read full, write full · ID3v2 CHAP/CTOC frames (ID3 chunk) | read full, write full · ID3v2 SYLT frame | description, language |
| FLAC | read full, write full · FLAC PICTURE block | read full, write full · VorbisComment CHAPTERxxx | read full, write full · SYNCEDLYRICS comment (LRC) | none |
| MP3 | read full, write full · APIC frame | read full, write full · ID3v2 CHAP/CTOC frames | read full, write full · ID3v2 SYLT frame | description, language |
| MP4 | read full, write full · covr atom, or the artwork key of an mdta keys store (JPEG/PNG/BMP) | read full, write full · Nero chpl and a QuickTime chapter text track | read none, write none | none |
| Matroska | read full, write full · AttachedFile (image attachment) | read full, write full · Chapters > EditionEntry > ChapterAtom (default edition) | read none, write none | language |
| Monkey's Audio | read full, write full · APEv2 Cover Art item | read none, write none | read none, write none | none |
| Musepack | read full, write full · APEv2 Cover Art item | read full, write none · SV8 chapter packets | read none, write none | none |
| Ogg FLAC | read full, write full · FLAC PICTURE block | read full, write full · VorbisComment CHAPTERxxx | read full, write full · SYNCEDLYRICS comment (LRC) | none |
| Ogg Opus | read full, write full · METADATA_BLOCK_PICTURE | read full, write full · VorbisComment CHAPTERxxx | read full, write full · SYNCEDLYRICS comment (LRC) | none |
| Ogg Vorbis | read full, write full · METADATA_BLOCK_PICTURE | read full, write full · VorbisComment CHAPTERxxx | read full, write full · SYNCEDLYRICS comment (LRC) | none |
| WAV | read full, write full · APIC (id3 chunk) | read full, write full · ID3v2 CHAP/CTOC frames (id3 chunk) | read full, write full · ID3v2 SYLT frame | description, language |
| WMA | read full, write none · WM/Picture descriptor | read full, write none · Marker Object | read none, write none | none |
| WavPack | read full, write full · APEv2 Cover Art item | read none, write none | read none, write none | none |
<!-- END caps -->

Some limits are intentional: MP4 covers drop the picture description, ID3 chapters
store no per-chapter language, ID3 stores a comment or lyric written without a
language as `eng`, and Matroska writes random UIDs, so chapter and
attachment rewrites are not byte-reproducible. Package docs describe them and writes
warn about them.

## Safety

Input is untrusted: bounded allocation and recursion, fuzz coverage, and human
output that sanitizes terminal-control bytes and invisible/reordering Unicode
(bidi controls, ZWSP, word joiner, BOM, line/paragraph separators). Line breaks
print as indented continuations only for LYRICS, COMMENT, DESCRIPTION, and
LONGDESCRIPTION; elsewhere as `\x0a` (JSON keeps exact values).

Save-back: temp file in the target directory, fsync, rename. SIGKILL mid-write can
leave the temp; recursive commands note leftovers and `waxlabel clean` lists or
removes them. `SaveBack()` refuses with `waxerr.ErrSourceChanged` when the file
changed since parse: a different file at the path, its size, metadata fingerprint,
mtime, or (Unix) change time, so a `chmod` or an xattr write counts as a change too;
so does a deleted file. The check runs again after the copy and, through the path,
just before the rename, so a competitor that renamed its own file over the path keeps
it. Concurrent saves of one file run one at a time under an advisory `flock` (Linux,
macOS, BSD) and the later one refuses; a save that waits on another program's lock
says so after a second. Without the lock (Windows, filesystems that refuse locks) two
saves that both pass the last check can still race to the rename.

A rewrite keeps the replaced file's mode, its owner and group where the caller may
set them, and on Linux its `user.*` attributes and POSIX access ACL; Windows swaps
the file with `ReplaceFile`, which keeps its ACL and attributes. A new file's mode
follows the umask. `-o` without `--overwrite` also refuses a target created while
the write runs, except at the last moment on a filesystem without hard links (FAT,
exFAT, some network shares). Atomic rename consequences: editing through a symlink rewrites the
target and leaves the link; other hard links keep the old inode; a read-only file
can be replaced when its directory is writable (mode preserved).

## License

MIT.

## Acknowledgements

Mutagen, TagLib, bogem/id3v2, sentriz/go-taglib, and libogg influenced design and
test cross-checks. Implementation follows public specifications and does not copy
their code.
