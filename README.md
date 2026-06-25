# WaxLabel

WaxLabel is a pure-Go library and command-line tool for reading and writing
audio-file metadata: tags, embedded pictures, and chapters where the format
supports them. It is preservation-first: edits are planned against the parsed
native structure, metadata is rewritten only where needed, and audio bytes are
copied rather than transcoded.

The public API lives in `github.com/colespringer/waxlabel` and
`github.com/colespringer/waxlabel/tag`. Codec packages are internal implementation
details.

WaxLabel reads and writes FLAC, Ogg Vorbis, Ogg Opus, MP3, WAV, MP4/M4A, raw
AAC/ADTS, Matroska/WebM, and AIFF/AIFF-C.

## Install

Library:

```sh
go get github.com/colespringer/waxlabel
```

CLI:

```sh
go install github.com/colespringer/waxlabel/cmd/waxlabel@latest
```

WaxLabel requires Go 1.26 or newer. The library packages use only the standard
library; the CLI uses Cobra.

## Library Example

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

	fmt.Println(plan)

	_, result, err := plan.Execute(ctx, waxlabel.SaveBack())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("committed:", result.Committed)
}
```

`Parse`, `ParseFile`, and `OpenSource` return an immutable `Document`. A document
does not hold an open file descriptor, and accessors return detached data. Editing
starts with `Document.Edit()`, resolves through `Editor.Prepare()`, and writes only
when the resulting `Plan` is executed.

Write destinations:

- `SaveBack()` atomically rewrites the parsed file in place. A no-op writes nothing.
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
```

Commands:

| Command | Purpose |
| --- | --- |
| `dump <file>...` | Show tags, audio properties, pictures, chapters, and parse warnings. `--native` also shows native blocks and source families. |
| `plan <file>...` | Preview an edit without writing. |
| `set <file>...` | Apply edits and save. Use `-o` for a new output file. |
| `lint <file>...` | Report metadata issues. `--fix` applies only safe fixes, such as clearing encoder noise or stripping legacy containers. |
| `verify <file>...` | Print tag-independent audio-essence digests. `--whole-file` also hashes every byte. |
| `caps <file>...` or `caps --format <name>` | Show what a file or format can store and edit. |
| `keys` | List the canonical tag vocabulary and cardinality. |
| `copy <source> <dest>` | Overlay source metadata onto the destination, reporting values that carry, downgrade, or drop. |
| `diff <a> <b>` | Compare canonical tags, pictures, and chapters. |

Common edit flags:

- `--set KEY=VALUE` replaces a value.
- `--add KEY=VALUE` appends a value.
- `--clear KEY` removes a key.
- `--strip-encoder` removes inherited encoder stamps where the format allows it.
- `--add-cover FILE` and `--add-picture ROLE=FILE` embed pictures.
- `--remove-picture SELECTOR` and `--remove-pictures` remove pictures.
- `--add-chapter TIMESTAMP=TITLE` and `--clear-chapters` edit chapters.
- `--preset preserve|compatible|minimal`, `--legacy preserve|strip`,
  `--padding N`, and `--no-padding` shape the write.

The read commands accept a single `-` for standard input; `set -` also works when
paired with `-o`. `dump`, `verify`, `lint`, `plan`, and `set` can walk directories
with `--recursive`; walked files are selected by extension, while direct file
arguments are content-sniffed, and a `--recursive` walk skips hidden directories
(those whose name begins with `.`) unless one is named as the root.

`-o` writes atomically (a temp file in the target's directory, then a rename), so it
must name a regular file in a writable directory. It is not a discard sink, and
`-o /dev/null` fails. To write nothing, omit `-o` or use `plan` to preview the edit.

All data commands accept `--json`. Commands that process many inputs return an
array, one element per input. Single-result commands such as `diff`, `copy`,
`caps --format`, `keys`, and `version` return one object.

In `dump` and `caps` JSON, the top-level `format` is the codec family, such as
`Matroska` or `AIFF`. `subformat` is the exact container subtype, such as `WebM`
or `AIFC`. For plain formats the two values are the same; in `dump`, `subformat`
matches `properties.container`.

The `warnings` array reported by `set` and `plan` describes the write plan: what
the write will change, downgrade, or drop. It does not include post-write
cleanliness findings such as an inherited encoder stamp or a malformed value. Run
`lint` on the saved file to check those.

Exit code summary:

- `0`: success, clean lint, or identical diff.
- `1`: generic error, lint warnings found, or files differ.
- `2`: usage error.
- `3`: unsupported format or unsupported metadata operation.
- `4`: invalid or contradictory data.
- `5`: source changed since parse.
- `6`: I/O or not-found error.
- `130`: canceled or timed out.

For multi-file commands, a more severe file error determines the process exit code.

## Core Concepts

**Canonical tags.** WaxLabel projects native metadata into a format-neutral
`tag.TagSet`. Known keys live in `tag.KnownKeys()`, but unknown uppercase keys are
preserved as custom fields when a format can carry them.

**Presence-aware edits.** WaxLabel distinguishes absent keys, present empty-string
values, and keys present with no values. Formats cannot store every distinction, so
the plan reports what will actually be written after codec projection.

**Planning before writing.** `Prepare()` builds a `Plan` and a `WriteReport` from
the same state used by `Execute()`. The preview is not a separate guess.

**Preservation.** Legacy or secondary metadata containers are preserved and warned
by default. Use `WithLegacyPolicy(LegacyStrip)` or the CLI's `--legacy strip` when
you want them removed.

**Terminal-safe text.** Human text renderers sanitize untrusted tag values, paths,
and warning strings so control bytes cannot inject terminal escapes. JSON output
uses the exact machine-readable values.

**Audio identity.** `Document.HashAudioEssence` hashes encoded audio plus
decoder-critical configuration, independent of tags. `Document.HashFile` hashes the
entire file.

## Format Support

| Format | Metadata | Notes |
| --- | --- | --- |
| FLAC | read/write | Vorbis comments and FLAC pictures; padding is fully controllable. |
| Ogg Vorbis / Opus | read/write | Vorbis comments and `METADATA_BLOCK_PICTURE`; audio packet payloads are preserved. |
| MP3 | read/write | ID3v2 is writable (a new tag is written as ID3v2.3); ID3v1 and APEv2 are surfaced as legacy families. |
| WAV | read/write | RIFF LIST/INFO plus embedded `id3 `; chunks are preserved. |
| MP4 / M4A / M4B | read/write | iTunes `ilst`, cover art, Nero chapters, and QuickTime chapter text tracks. Fragmented MP4 is rejected. |
| Matroska / WebM | read/write | Scoped SimpleTags, segment title, attachments, and default-edition chapters. WebM cannot write cover attachments. |
| AAC (ADTS) | read/write | Front ID3v2 tag (a new tag is written as ID3v2.4) plus ADTS frames. |
| AIFF / AIFF-C | read/write | Native text chunks plus embedded `ID3 `; chunks are preserved. |

The capability table below is generated from the same codec capability model used
by `waxlabel caps`.

<!-- BEGIN caps (generated from codec Capabilities; see tests/capability_test.go) -->
| Format | Pictures | Chapters |
| --- | --- | --- |
| AAC (ADTS) | read full, write full · APIC frame | read none, write none |
| AIFF | read full, write full · APIC (ID3 chunk) | read none, write none |
| FLAC | read full, write full · FLAC PICTURE block | read none, write none · CUESHEET preserved |
| MP3 | read full, write full · APIC frame | read none, write none · CHAP preserved |
| MP4 | read full, write full · covr atom (JPEG/PNG/BMP) | read full, write full · Nero chpl and a QuickTime chapter text track |
| Matroska | read full, write full · AttachedFile (image attachment) | read full, write full · Chapters > EditionEntry > ChapterAtom (default edition) |
| Ogg Opus | read full, write full · METADATA_BLOCK_PICTURE | read none, write none |
| Ogg Vorbis | read full, write full · METADATA_BLOCK_PICTURE | read none, write none |
| WAV | read full, write full · APIC (id3 chunk) | read none, write none · cue/adtl preserved |
<!-- END caps -->

## Safety

Input is treated as untrusted. Parsers use bounded allocation and recursion limits,
fuzz tests cover arbitrary input, and human output sanitizes terminal-control bytes.

Save-back writes use a temp file in the target directory, fsync it, rename it into
place, and fsync the directory. If the source file changed since parse,
`SaveBack()` refuses with `waxerr.ErrSourceChanged` instead of overwriting newer
bytes.

Atomic rename saves have normal filesystem consequences:

- Editing through a symlink rewrites the symlink target and leaves the link in place.
- Other hard links keep pointing at the old inode.
- A read-only file can still be replaced when its directory is writable; the
  original mode is preserved on the replacement.

## License

MIT.

## Acknowledgements

Mutagen, TagLib, bogem/id3v2, sentriz/go-taglib, and libogg were direct influences
on WaxLabel's design and test cross-checks. WaxLabel's implementation follows
public specifications and does not copy their code.
