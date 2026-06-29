# WaxLabel

WaxLabel is a pure-Go library and command-line tool for reading and writing
audio-file metadata: tags, embedded pictures, chapters, and synced lyrics where
the format supports them. It is preservation-first: edits are planned against the
parsed native structure, metadata is rewritten only where needed, and audio bytes
are copied rather than transcoded.

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
| `dump <file>...` | Show tags, audio properties, pictures, chapters, synced lyrics, and parse warnings. `--native` also shows native blocks and source families. |
| `plan <file>...` | Preview an edit without writing. |
| `set <file>...` | Apply edits and save. Use `-o` for a new output file. |
| `lint <file>...` | Report metadata issues. `--fix` applies only safe fixes, such as clearing encoder noise or stripping legacy containers. |
| `verify <file>...` | Print tag-independent audio-essence digests. `--whole-file` also hashes every byte. |
| `caps <file>...` or `caps --format <name>` | Show what a file or format can store and edit. |
| `keys` | List the canonical tag vocabulary and cardinality. |
| `copy <source> <dest>` | Overlay source metadata onto the destination, reporting values that carry, downgrade, or drop. |
| `diff <a> <b>` | Compare canonical tags, pictures, chapters, and synced lyrics. |

Common edit flags:

- `--set KEY=VALUE` replaces a value.
- `--add KEY=VALUE` appends a value.
- `--clear KEY` removes a key.
- `--strip-encoder` removes inherited encoder stamps where the format allows it.
- `--add-cover FILE` and `--add-picture ROLE=FILE` embed pictures.
- `--remove-picture SELECTOR` and `--remove-pictures` remove pictures.
- `--add-chapter TIMESTAMP=TITLE` and `--clear-chapters` edit chapters. A chapter
  `TIMESTAMP` is `[H:]MM:SS[.fff]` (for example `1:02:03.500` or `02:03`) or a bare
  number of seconds (`123` or `123.5`); when present, the seconds field's fractional
  part is 1 to 3 digits (millisecond resolution).
- `--synced-lyrics-file FILE.lrc`, `--add-synced-lyric TIMESTAMP=TEXT`,
  `--synced-lyrics-lang eng`, and `--clear-synced-lyrics` edit synced lyrics.
  File input and added lines are combined into one set that replaces any existing
  synced lyrics. MP3/AAC/AIFF/WAV keep `--synced-lyrics-lang` as the ID3v2 `SYLT`
  ISO-639-2 language code; FLAC/Ogg drop it because `SYNCEDLYRICS` stores LRC text
  without a language field. The `TIMESTAMP` grammar matches `--add-chapter`.
- `--preset preserve|compatible|minimal`, `--legacy preserve|strip`,
  `--padding N`, and `--no-padding` shape the write.
- `--numeric-genre` writes a recognized genre as its numeric reference where the
  format supports one (ID3's `TCON`). It converts only on a genuine genre change; when
  the canonical genre is unchanged it is a no-op (an existing numeric or text genre is
  left as it is, not rewritten).

The read commands accept a single `-` for standard input; `set -` also works when
paired with `-o`. `dump`, `verify`, `lint`, `plan`, and `set` can walk directories
with `--recursive`. A file's format is detected from its leading bytes, not from
its extension: extensions only filter which files a `--recursive` walk visits.
A walk skips hidden directories (those whose name begins with `.`) unless one is
named as the root. A direct file argument whose bytes match no supported
container is unsupported (exit 3), regardless of extension.

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

In `set`, `plan`, and `lint --fix` JSON, `changes` and `operations` have
different meanings. `changes` is the canonical tag-level diff: which keys are
added, removed, or replaced. `operations` is the structural write list, such as
an ID3v2 frame rewrite, an encoder-stamp strip, or a chapter-track rewrite. Some
fixes touch only native structure, so they can report an empty `changes` list
with non-empty `operations`. Empty `changes` does not mean nothing was written.

Exit code summary:

- `0`: success, clean lint, or identical diff.
- `1`: generic error, lint warnings found, or files differ.
- `2`: usage error, such as a bad `--format` flag value, giving the same key to
  both `--set`/`--add` and `--clear` (they conflict), or a bare directory argument
  without `--recursive`.
- `3`: an input file whose bytes match no supported container signature (unsupported
  regardless of its extension), or an unsupported metadata operation.
- `4`: invalid or contradictory data, including a recognized container whose contents
  are corrupt.
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
| FLAC | read/write | Vorbis comments, FLAC pictures, `CHAPTERxxx` chapters, and `SYNCEDLYRICS` (LRC) synced lyrics; padding is fully controllable. |
| Ogg Vorbis / Opus | read/write | Vorbis comments, `METADATA_BLOCK_PICTURE`, `CHAPTERxxx` chapters, and `SYNCEDLYRICS` (LRC) synced lyrics; audio packet payloads are preserved. |
| MP3 | read/write | ID3v2 is writable, including `CHAP`/`CTOC` chapters and `SYLT` synced lyrics. A new tag is written as ID3v2.3; ID3v1 and APEv2 are surfaced as legacy families. |
| WAV | read/write | RIFF LIST/INFO plus embedded `id3 `, including `CHAP`/`CTOC` chapters and `SYLT` synced lyrics; chunks are preserved. |
| MP4 / M4A / M4B | read/write | iTunes `ilst`, cover art, Nero chapters, and QuickTime chapter text tracks. Timed-text lyric tracks are outside the metadata model. Fragmented MP4 is rejected. |
| Matroska / WebM | read/write | Scoped SimpleTags, segment title, attachments, and default-edition chapters. Subtitle lyric tracks are outside the metadata model. WebM cannot write cover attachments. |
| AAC (ADTS) | read/write | Front ID3v2 tag plus ADTS frames. A new tag is written as ID3v2.4, including `CHAP`/`CTOC` chapters and `SYLT` synced lyrics. |
| AIFF / AIFF-C | read/write | Native text chunks plus embedded `ID3 `, including `CHAP`/`CTOC` chapters and `SYLT` synced lyrics; chunks are preserved. |

The capability table below is generated from the same codec capability model used
by `waxlabel caps`.

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

**Known issue: Matroska reproducibility.** Matroska expects FileUID and ChapterUID
values to be random. WaxLabel follows that rule for new attachments and chapters, so a
write that creates or rebuilds those IDs will not be byte-identical across runs. The
audio bytes are still preserved. A tag-only rewrite that mints no new ID can remain
deterministic, and a future option could derive IDs from a stable seed or content hash.

### Known limitations

These limits are intentional for now; each is bounded and does not affect the common
path:

- **Matroska essence digest, interleaved metadata.** The audio-essence digest hashes a
  single contiguous cluster span. If a muxer places a non-cluster element such as Cues
  or Tags between clusters, that element is included in the digest. Re-rendering it
  could change the digest even when the audio bytes are unchanged. The multi-range
  essence model already exists (used by MP4 and Ogg); closing this means populating it
  from the Matroska cluster runs and bumping the digest version.
- **Malformed ID3v2.2 `PIC` with no description terminator.** A non-conformant embedded
  picture whose description is missing its terminating NUL is parsed with an empty
  description and the remaining bytes as image data. That reading is persisted on
  rewrite, so the original malformed bytes do not round-trip. Conformant pictures are
  unaffected.
- **Present-but-valueless fields collapse to absent.** A field present in the source
  with no value reads back as absent rather than present-empty; this is consistent
  across formats and matches how `--clear` and a set-empty value are distinguished
  elsewhere.
- **Native-cue chapters are not read.** MP3/AAC/AIFF/WAV chapters use ID3v2 `CHAP`/`CTOC`,
  and FLAC/Ogg use the VorbisComment `CHAPTERxxx` convention. A FLAC ripped with a
  `CUESHEET` block, or a WAV carrying native `cue `/`adtl` chapters, projects no chapters
  (those bytes are preserved verbatim, never edited). ID3 `CHAP` stores start, end, and
  title but no per-chapter language or hidden/disabled flags; `CHAPTERxxx` stores start and
  title only. Copying chapters that carry the dropped fields reports a lossy carry.
- **Synced lyrics are metadata-only.** MP3/AAC/AIFF/WAV use ID3v2 `SYLT`, which
  keeps the language, descriptor, and millisecond timestamps. FLAC/Ogg use an LRC
  document in a `SYNCEDLYRICS` Vorbis comment, which keeps the timed text but has
  no language or descriptor field; copying a set with either field to FLAC/Ogg
  reports a lossy carry. WaxLabel reads only `SYLT` entries that use millisecond
  timestamps and the lyrics content type. MPEG-frame timestamps and non-lyric
  content types are skipped with warnings and preserved verbatim. `SYLT` timestamps
  are 32-bit milliseconds (about 49.7 days), so later lines are clamped with a
  warning. Authored synced-lyrics languages must be 3-letter ISO-639-2 codes such
  as `eng`. LRC is line-based: a newline inside one lyric line is flattened to a
  space on FLAC/Ogg, while `SYLT` keeps it. MP4 and Matroska represent synced
  lyrics as timed-text or subtitle tracks, not metadata, so WaxLabel does not model
  them. LRC `[offset:]` follows foobar2000 behavior: effective timestamp =
  timestamp - offset.

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
