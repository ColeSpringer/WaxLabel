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
waxlabel export-picture track.flac -o cover.jpg
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
| `export-picture <file>` | Write one embedded picture to a file with `-o`. `--picture` selects by role or 1-based index; the default is the sole front cover. The input is never modified. |

Common edit flags:

- `--set KEY=VALUE` replaces a value.
- `--add KEY=VALUE` appends a value.
- `--clear KEY` removes a key.
- `--strip-encoder` removes inherited encoder/transcoder stamps where the format allows
  it. It clears the canonical `ENCODER` tag, drops a transcoder-stamp WAV `ISFT`, and
  rewrites a transcoder-stamped FLAC/Ogg vendor string to `WaxLabel`. The vendor field is
  mandatory in those formats, so it is neutralized instead of removed.
- `--add-cover FILE` and `--add-picture ROLE=FILE` embed pictures. `--add-cover`
  replaces any existing front cover, while `--add-picture front-cover=FILE` appends one
  (like every other `--add-picture ROLE=`).
- `--remove-picture SELECTOR` and `--remove-pictures` remove pictures.
- `--add-chapter TIMESTAMP=TITLE` and `--clear-chapters` edit chapters. A chapter
  `TIMESTAMP` is `[H:]MM:SS[.fff]` (for example `1:02:03.500` or `02:03`) or a bare
  number of seconds (`123` or `123.5`); when present, the seconds field's fractional
  part is 1 to 3 digits (millisecond resolution).
- `--synced-lyrics-file FILE.lrc`, `--add-synced-lyric TIMESTAMP=TEXT`,
  `--synced-lyrics-lang eng`, and `--clear-synced-lyrics` edit synced lyrics.
  File input and added lines are combined into one set that replaces the existing
  synced-lyric *lines*. On MP3/AAC/AIFF/WAV the ID3v2 `SYLT` ISO-639-2 language comes
  from `--synced-lyrics-lang` when given; without it, an existing set's language is
  preserved (a line-only edit does not discard it), and a first set authored with no
  flag is left undefined. Setting the language to `xxx` (the ISO-639-2 "undefined"
  marker) stores it but reads back with no language, and warns. FLAC/Ogg have no
  language field, so `--synced-lyrics-lang` is dropped there. The `TIMESTAMP` grammar
  matches `--add-chapter`. A set holds at most 65,536 lines (about 18 hours at one line
  per second); a longer set drops the extra lines on read and warns.
- `--preset preserve|compatible|minimal`, `--legacy preserve|strip`, and
  `--no-padding` shape the write; `--padding N` reserves *at least* N bytes of padding
  after the metadata (FLAC defaults to 8192; MP3/AAC/MP4 reuse the existing region; `0`
  writes none, like `--no-padding`).
- `--numeric-genre` writes a recognized genre as its numeric reference where the
  format supports one (ID3's `TCON`). It converts only on a genuine genre change; when
  the canonical genre is unchanged it is a no-op (an existing numeric or text genre is
  left as it is, not rewritten). A file written this way carries a numeric genre, so later
  `dump` and `lint` runs report an informational `[numeric-genre]` note. That is expected
  for an opt-in numeric genre, not a defect.

The read commands accept a single `-` for standard input; `set -` also works when
paired with `-o`. `dump`, `verify`, `lint`, `plan`, and `set` can walk directories
with `--recursive`. A file's format is detected from its leading bytes, not from
its extension: extensions only filter which files a `--recursive` walk visits.
A walk skips hidden directories (those whose name begins with `.`) unless one is
named as the root. It likewise skips hidden files (a leading `.`), which are not
counted among the files passed over for their extension; a hidden file named
directly as an argument is still processed. A direct file argument whose bytes
match no supported container is unsupported (exit 3), regardless of extension.

`-o` writes atomically (a temp file in the target's directory, then a rename), so it
must name a regular file in a writable directory. It is not a discard sink, and
`-o /dev/null` fails. To write nothing, omit `-o` or use `plan` to preview the edit.
By default `-o` refuses an existing target with a usage error (exit 2); pass
`--overwrite` to replace it. The one exception is writing back to the single input
(`set f -o f`), which is allowed without `--overwrite` since it is effectively an
in-place edit.

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

Each `lint --json` finding carries a machine-readable `code`, a `severity`, and a
`message`. A `warning` finding makes `lint` exit `1` (issues found); an `error`-severity
finding exits `4` (invalid-data), so a script can tell contradictory metadata from a mere
warning; an `info` finding never flips the exit. The codes:

- Errors: `no-audio` (no decodable audio frames), `multiple-vorbis-comment` and
  `duplicate-tag-block` (repeated metadata blocks), `duplicate-icon` (a non-unique
  icon/other-icon picture type).
- Warnings: `inherited-encoder` (a transcoder/encoder stamp `--fix` can clear),
  `stray-leading-id3`, `trailing-id3v1`, `legacy-ape` (legacy containers `--fix` can
  strip), `invalid-picture` (a picture stored as `application/octet-stream` - an unsniffable
  or `--force`-embedded cover; the check is the stored MIME, not a re-sniff of the bytes),
  `truncated-audio` (declared audio bytes are missing; detected only where the container or
  codec declares an expected length - WAV, AIFF, and MP4 by declared size, and VBR MP3 by the
  Xing frame count - so a clean lint on FLAC, AAC, Ogg, or Matroska is not a completeness
  guarantee),
  `invalid-tag-key` (a native name mapping to no canonical key), `conflicting-families`
  (a key's native source fields disagree), `duplicate-picture`, `multiple-front-covers`,
  `single-valued-multi` (a single-valued key carrying several values), `malformed-number`,
  `malformed-date`, and `malformed-boolean`.
- Info (never flips the exit): `numeric-genre` (a numeric genre reference resolved to a
  name), `negative-numeric` (a negative value in a numeric field), and `custom-key` (an
  unknown, preserved uppercase key).

In `set`, `plan`, and `lint --fix` JSON, `changes` and `operations` have
different meanings. `changes` is the canonical tag-level diff: which keys are
added, removed, or replaced. `operations` is the structural write list, such as
an ID3v2 frame rewrite, an encoder-stamp strip, or a chapter-track rewrite. Some
fixes touch only native structure, so they can report an empty `changes` list
with non-empty `operations`. Empty `changes` does not mean nothing was written.

Exit code summary:

- `0`: success, clean lint, identical diff, or a closed output pipe (a downstream reader
  ended early, as in `waxlabel dump --recursive DIR | head` - benign and silent).
- `1`: generic error, lint warnings found, or files differ.
- `2`: usage error, such as a bad `--format` flag value, giving the same key to
  both `--set`/`--add` and `--clear` (they conflict), or a bare directory argument
  without `--recursive`.
- `3`: an input file whose bytes match no supported container signature (unsupported
  regardless of its extension), or an unsupported metadata operation.
- `4`: invalid or contradictory data, including a recognized container whose contents
  are corrupt, or a `lint` error-severity finding (`no-audio`, `duplicate-tag-block`,
  `multiple-vorbis-comment`, `duplicate-icon`).
- `5`: source changed since parse.
- `6`: I/O or not-found error.
- `7`: a streamed input (`-`/standard input under `--max-size`) exceeded the configured
  size cap. This is a resource-limit refusal, not corruption: a raw stream carries no
  declared size, so it is kept distinct from the exit-4 invalid-data class.
- `130`: canceled or timed out.

For multi-file commands, WaxLabel returns the code for the highest-precedence
file error, not necessarily the first error encountered. The precedence is:

> canceled/timeout (130) > source-changed (5) > invalid-data (4) > input-too-large
> (7) > unsupported format/tag/stream/alignment (3) > I/O (6) > not-found (6) >
> usage/invalid-key (2) > generic error (1) > broken-pipe (0)

A broken output pipe ranks last, below every real failure, so a genuine per-file error in
the same run still sets the exit code; only when the closed pipe is the sole outcome does the
run exit 0.

The stream size cap (7) sits below invalid-data (4) in this ranking, but it is also enforced
pre-flight: a `-`/standard-input stream over `--max-size` is refused as it is read, before any
named file in the same run is parsed. So in a mixed `-` plus named-file run, exit 7 can
short-circuit a corrupt named file's would-be exit 4. The ranking above governs errors that
parsing surfaces; it does not override that pre-flight refusal.

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
you want them removed. ID3v2 is authoritative and edits are written there, so editing
a file that also carries a legacy ID3v1 (or APEv2) tag leaves that older tag untouched.
It then goes stale: the edit reports `[legacy-conflict]`, and later `lint`/`dump` runs
keep reporting `trailing-id3v1` / `legacy-ape` and `conflicting-families` until you
resolve it with `--legacy strip` (or `--preset minimal`) or `lint --fix`. This is
deliberate. Preservation-first never silently discards a container you did not ask to
remove.

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

When `set` authors a structural edit a format cannot store at all - cover art on a
WebM file (`[picture-unsupported]`), or chapters on a format with no chapter store
(`[chapters-unsupported]`) - it drops that item with a warning and applies the rest of
the edit, the same way a cross-format copy drops what the destination cannot hold. Under
`set`, `--strict` promotes each such drop warning to a failure; `copy` has no `--strict`
flag (passing one is a usage error), so its drops are never escalated.

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

- **Malformed ID3v2.2 `PIC` with no description terminator.** A non-conformant embedded
  picture whose description is missing its terminating NUL is parsed with an empty
  description and the remaining bytes as image data. That reading is persisted on
  rewrite, so the original malformed bytes do not round-trip. Conformant pictures are
  unaffected.
- **Unknown ID3v2.2 frames keep a non-conformant 4-character ID.** An ID3v2.2 frame
  whose 3-character ID is not in the upgrade table is preserved when the tag is written
  as ID3v2.3/2.4 under a best-effort ID formed by padding the original with a trailing
  space (e.g. `TXY` becomes `TXY `). That padded ID is technically non-conformant, but
  the frame's bytes are kept verbatim and it never surfaces as a canonical tag - it is
  skipped on read, in `dump`, and in `diff`, so the preview always equals a fresh
  re-parse. Known ID3v2.2 frames upgrade to their proper v2.3/2.4 IDs and are unaffected.
- **Present-but-valueless fields collapse to absent.** A field present in the source
  with no value reads back as absent rather than present-empty; this is consistent
  across formats and matches how `--clear` and a set-empty value are distinguished
  elsewhere.
- **Native-cue chapters are not read.** MP3/AAC/AIFF/WAV chapters use ID3v2 `CHAP`/`CTOC`,
  and FLAC/Ogg use the VorbisComment `CHAPTERxxx` convention. A FLAC ripped with a
  `CUESHEET` block, or a WAV carrying native `cue `/`adtl` chapters, projects no chapters
  (those bytes are preserved verbatim, never edited). ID3 `CHAP` stores start, end, and
  title but no per-chapter language or hidden/disabled flags; `CHAPTERxxx` stores start and
  title only. Copying chapters that carry the dropped fields reports a lossy carry. Two chapters
  that share an identical start time (already flagged by a `duplicate-chapter` warning) serialize
  with asymmetric ends in ID3 `CHAP`: the interior chapter is left open while the final one is
  filled to the media duration. This is a known, degenerate case with no data loss.
- **Synced lyrics are metadata-only.** MP3/AAC/AIFF/WAV use ID3v2 `SYLT`, which
  keeps the language, descriptor, and millisecond timestamps. Authoring new lines
  replaces the lyric text but preserves the existing `SYLT` language unless
  `--synced-lyrics-lang` overrides it; a first set authored with no language is
  written undefined. A faithful cross-format copy carries no language, so it never
  inherits the destination's. FLAC/Ogg use an LRC document in a `SYNCEDLYRICS`
  Vorbis comment, which keeps the timed text but has no language or descriptor
  field; copying a set with either field to FLAC/Ogg reports a lossy carry.
  WaxLabel reads only `SYLT` entries that use millisecond timestamps and the lyrics
  content type. MPEG-frame timestamps and non-lyric content types are skipped with
  warnings and preserved verbatim. `SYLT` timestamps
  are 32-bit milliseconds (about 49.7 days), so later lines are clamped with a
  warning. An authored synced-lyrics language must be a 3-letter code (the ISO-639-2
  shape, e.g. `eng`); the shape is validated, not registry membership. LRC is
  line-based: a newline inside one lyric line is flattened to a
  space on FLAC/Ogg, while `SYLT` keeps it. A set carrying more than 65,536 lines
  (about 18 hours of one-per-second lines) is truncated to that cap on write with a
  `[synced-lyrics-truncated]` warning that `--strict` promotes to a failure. MP4 and
  Matroska represent synced lyrics as timed-text or subtitle tracks, not metadata, so
  WaxLabel does not model them: `set` drops an authored synced-lyrics set on those
  formats with a `[synced-lyrics-unsupported]` warning (and `--strict` fails) rather
  than refusing the whole edit, so a set that also writes storable tags still applies
  them. LRC `[offset:]` follows foobar2000 behavior: effective timestamp =
  timestamp - offset. WaxLabel writes one space between a line's timestamp and its
  text so a lyric that itself begins with a `[mm:ss]`-shaped string round-trips; an
  externally authored file that omits that separator (`[00:01.00][00:02.00]x`) is
  read as two timestamps, as any LRC player would, and a file already corrupted by
  an older WaxLabel that wrote no separator stays corrupted.
- **MP4 cover art drops the picture description.** The iTunes `covr` atom stores image
  data only, with no role or description field. A cover written to MP4 keeps its bytes, loses
  any description, and reads back as a front cover. A plain front cover with no description
  round-trips losslessly; copying a described or non-front cover to MP4 reports the
  per-picture metadata loss.
- **MP4 stores a multi-valued field as several data atoms.** The iTunes `ilst` writes each
  value of a multi-valued field (e.g. two `ARTIST` values) as a separate `data` atom under one
  item. WaxLabel writes and reads back every value, but many third-party readers surface only the
  first, so an edit that authors such a field reports an informational `[mp4-multi-value]` note.
  It is not a loss (`--strict` is unaffected).
- **MP4 metadata is read only at `moov.udta.meta.ilst`.** An iTunes `ilst` placed
  directly under `moov.meta` (no intervening `udta`) and the QuickTime `mdta`-keys form
  (`moov.meta` with a `keys` table) are not read, so such a file reads as having no tags.
  Editing it writes a fresh `moov.udta.meta.ilst` and preserves the original metadata atom
  verbatim rather than destroying it; the canonical `moov.udta.meta.ilst` layout that
  iTunes and most taggers write is unaffected.
- **An MP4 `trkn`/`disk` of 65535 reads back as `-1` in ffmpeg.** WaxLabel stores a track or
  disc number/total up to the atom's unsigned 16-bit ceiling (65535) and round-trips it
  faithfully. ffprobe/ffmpeg read that same field as a *signed* 16-bit int, so they report the
  maximum, 65535, as `-1`. This is a signed/unsigned interpretation difference in the other tool,
  not a stored-data difference; WaxLabel's unsigned value is the one on disk.
- **An MP4 `trkn`/`disk` of `0` reads back as absent.** MP4 stores the track/disc number and its
  total as a pair of binary `uint16` fields, in which `0` is the structural "absent" sentinel: the
  bytes are written (as `0/N`), but the reader treats a `0` slot as unset, so a `0` never
  round-trips. Setting `TRACKNUMBER=0` (even paired with a real total, as `0/12`) therefore warns
  that the value is treated as unset and reads back as absent, rather than that it cannot be
  represented (an overflow past 65535 or a non-numeric value keeps the latter wording). This is
  MP4-specific: the text-based number/total ID3 formats (MP3/AAC/AIFF/WAV) store the digits
  literally, so a `0` there is preserved.
- **An unstorable MP4 `trkn`/`disk` edit keeps the existing value instead of erasing it.**
  Because a track/disc slot is a fixed binary `uint16`, an edit that sets it to a value the atom
  cannot hold (non-numeric, negative, or past 65535) cannot be stored. Rather than clearing the
  slot and discarding a good existing value, WaxLabel restores the file's current value for that
  slot and still warns that the requested value was dropped (so `--strict` still exits 2). This
  again diverges from the text formats (MP3/AAC/AIFF/WAV), which store the raw string verbatim.
  Clearing the field (`--clear`) still removes it, and a literal `0` still follows the absent-sentinel
  rule above.
- **MP4 QuickTime chapters can misread the final chapter's end in ffmpeg/VLC when the first
  chapter starts after 0:00.** WaxLabel writes the QuickTime chapter text track with a leading empty
  edit (the iTunes and Apple Books form). ffmpeg, ffprobe, and VLC apply that edit as an offset to
  chapter *starts* (correct) but derive the last chapter's *end* from the track's shortened media
  duration, so they report the final chapter ending early when the first chapter does not begin at
  0:00. WaxLabel's own reader and the Nero `chpl` list always read the chapters correctly, and
  iTunes and Apple Books read the edit as intended. No single output form satisfies both stacks:
  extending the media duration to satisfy ffmpeg makes WaxLabel's own read wrong, and dropping the
  empty edit loses the first chapter's start. **Workaround:** if ffmpeg/VLC chapter compatibility
  matters, start the first chapter at 0:00 (label the intro rather than leaving it unlabeled).
  WaxLabel then writes a single normal edit with no empty edit, and ffmpeg reads every chapter,
  including the last one's end, correctly. A future option could add an ffmpeg-compatible chapter
  mode that bounds the text track's last end and teaches the reader to canonicalize the bounded
  form.
- **WAV/AIFF dual tag containers consolidate on edit.** A WAV or AIFF can carry both an
  embedded `id3`/`ID3 ` chunk and native `LIST`/`INFO` (WAV) or text (AIFF) chunks. When they
  hold conflicting values for the same key, the documented precedence applies (the id3 chunk
  wins; the native chunks only fill keys id3 lacks). Any edit rewrites both containers from
  that merged set, so a shadowed native value is dropped; a no-op write leaves the file
  byte-identical.
- **ID3v2 is written without unsynchronization.** WaxLabel writes clean ID3v2 tags with no
  unsynchronization, so a tag may contain a byte pattern that looks like a false MPEG frame
  sync. Compliant decoders skip the tag via its size field, so playback is unaffected; the
  read path still accepts unsynchronized input.
- **FLAC padding is clamped to one maximum block.** A `--padding` request larger than a
  single FLAC `PADDING` block can hold (16,777,215 bytes, the ceiling of its 24-bit length
  field) is clamped to fit and reported with a `[padding-clamped]` warning. The request still
  reserves the largest padding the format allows; it is not silently shrunk toward zero.
- **`--padding` above 64 MiB is a usage error.** A `--padding` value larger than 64 MiB
  (67,108,864 bytes) is rejected up front (exit 2, all formats), not clamped. This is distinct
  from the per-format clamp above: a value below 64 MiB that still exceeds a format's structural
  cap (such as FLAC's ~16 MiB block) is clamped with `[padding-clamped]`, but an absurd value
  that would reserve a multi-gigabyte metadata region is refused as a likely mistake; a clear
  error is friendlier than silently writing a 64 MiB-padded file.
- **EBML nesting is bounded at 64 levels.** A Matroska/WebM file whose elements nest deeper
  than 64 levels fails to parse. The same recursion guard bounds every container against
  hostile input.

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
