package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// editPrecedenceHelp documents per-key edit flag rules. --set replaces; --add appends.
// Same key in --set/--add and --clear is an error (exit 2). patch() enforces this.
const editPrecedenceHelp = "For one key, --set replaces the key and --add appends to it. Giving the same\n" +
	"key to both --set/--add and --clear is an error (they conflict); order on the\n" +
	"command line does not change this."

// editFlags holds tag/picture edit flags and write-shaping options for plan and set.
// Binds to cobra; compiles to [tag.TagPatch], picture mutations, and [wl.WriteOption].
type editFlags struct {
	set                []string // KEY=VALUE, replace
	add                []string // KEY=VALUE, append (multi-value)
	clear              []string // KEY, remove
	addCover           []string // image file path, added as a front cover
	addPicture         []string // ROLE=PATH, added with that cover-art role (repeatable)
	pictureDescription string   // description applied to every picture added this run
	removePicture      []string // SELECTOR (role name or 1-based dump index), repeatable
	rmPics             bool
	force              bool // embed --add-cover/--add-picture input even when it is not a recognized image

	addChapter    []string // TIMESTAMP=Title, appended to the chapter list (repeatable)
	clearChapters bool     // remove all chapters

	syncedLyricsFile  string   // --synced-lyrics-file: LRC file authoring one synced-lyrics set
	addSyncedLyric    []string // TIMESTAMP=Text timed lines, authoring one synced-lyrics set (repeatable)
	syncedLyricsLang  string   // ISO-639-2 language for the authored synced-lyrics set
	clearSyncedLyrics bool     // remove all synced lyrics

	stripEncoder bool // clear the ENCODER software stamp

	preset string
	legacy string
	// id3Multi is raw --id3-multi; "" means unset.
	id3Multi string

	// padding is raw --padding (bytes); "" unset. noPadding is --no-padding.
	// Both unset keeps the default 8 KiB policy.
	padding   string
	noPadding bool

	// numericGenre writes genre as numeric ref (TCON) via WithNumericGenre.
	// Like --force, resolved in writeOptions() for plan and set.
	numericGenre bool

	// outputGain is raw --output-gain in dB; "" unset. keepR128 skips R128 rebase; needs --output-gain.
	outputGain string
	keepR128   bool

	strict bool // promote selected write notes, including unknown-key and dropped-value notes, to errors
}

// bind registers the edit and write-option flags on cmd.
func (e *editFlags) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringArrayVar(&e.set, "set", nil, "set KEY=VALUE, replacing the key (repeatable)")
	f.StringArrayVar(&e.add, "add", nil, "append KEY=VALUE to a key (repeatable, for multi-value fields)")
	f.StringArrayVar(&e.clear, "clear", nil, "remove KEY (repeatable)")
	f.StringArrayVar(&e.addCover, "add-cover", nil, "add a front-cover picture from an image file, replacing any existing front cover (repeatable, last wins); --add-picture front-cover=PATH instead appends a front cover")
	f.StringArrayVar(&e.addPicture, "add-picture", nil, "add a picture ROLE=PATH, e.g. back-cover=back.jpg (repeatable; ROLE is a cover-art role such as front-cover, back-cover, artist)")
	f.StringVar(&e.pictureDescription, "picture-description", "", "set the description on every picture added this run (--add-picture/--add-cover)")
	f.StringArrayVar(&e.removePicture, "remove-picture", nil, "remove pictures by role name or 1-based dump index, e.g. back-cover or 2 (repeatable; removals apply before adds)")
	f.BoolVar(&e.rmPics, "remove-pictures", false, "remove all embedded pictures")
	f.BoolVar(&e.force, "force", false, "embed --add-cover/--add-picture input even if it is not a recognized image ("+wl.RecognizedImageFormats+"); unrecognized bytes are stored as "+wl.UnrecognizedMIME+". The check is header-only, not a full image decode")
	f.StringArrayVar(&e.addChapter, "add-chapter", nil, "add a chapter TIMESTAMP=Title (e.g. 1:30=Verse; repeatable); formats with chapter-count caps reject over-limit lists (255 for ID3 and MP4, 1000 for FLAC/Ogg). CLI-created chapters have no end time, so replacing a Matroska list that had explicit ends (--clear-chapters plus this flag) drops them; a plain --add-chapter keeps existing chapters, and their ends where the format stores them (FLAC/Ogg CHAPTERxxx store none), except that a start-only insert overlapping an existing chapter truncates that chapter's end to the new start (reported as [chapter-overlap-reconciled])")
	f.BoolVar(&e.clearChapters, "clear-chapters", false, "remove all chapters (applied before --add-chapter, so combining them keeps only the added chapters)")
	f.StringVar(&e.syncedLyricsFile, "synced-lyrics-file", "", "set synced lyrics from an LRC file, replacing any existing synced lyrics (MP3/AAC/AIFF/WAV keep the language; FLAC/Ogg drop it)")
	f.StringArrayVar(&e.addSyncedLyric, "add-synced-lyric", nil, "add synced lyric line TIMESTAMP=Text (e.g. 1:30=Verse; repeatable); combined lines replace any existing synced lyrics")
	f.StringVar(&e.syncedLyricsLang, "synced-lyrics-lang", "", "a 3-letter language code (the ISO-639-2 shape, e.g. eng) for synced lyrics authored by --synced-lyrics-file or --add-synced-lyric; the shape is validated, not registry membership")
	f.BoolVar(&e.clearSyncedLyrics, "clear-synced-lyrics", false, "remove all synced lyrics")
	f.BoolVar(&e.stripEncoder, "strip-encoder", false, "clear ENCODER, the software stamp an encoder or transcoder leaves behind, wherever the format stores it (a WAV ISFT item and a FLAC/Ogg vendor string included)")
	f.StringVar(&e.preset, "preset", "", "write policy preset: preserve|compatible|minimal")
	f.StringVar(&e.legacy, "legacy", "", "legacy-tag policy: preserve|strip. strip removes ID3v1/APEv2/stray-ID3 containers unconditionally, warning when one holds the only copy of a value (--strict then refuses)")
	f.StringVar(&e.id3Multi, "id3-multi", "", "how an ID3v2.3 tag (MP3) stores a multi-valued field: null (NUL-separated, the default, a de-facto extension some readers do not split), repeat (one frame per value), or slash (values joined with a slash). ID3v2.4 tags (WAV/AIFF/AAC) separate values natively and ignore it")
	f.StringVar(&e.padding, "padding", "", "reserve at least N bytes of padding after the metadata, with the same size suffixes as --max-size (e.g. 8KiB; FLAC default 8192; MP3/AAC/MP4 reuse the existing region; 0 writes none, like --no-padding)")
	f.BoolVar(&e.noPadding, "no-padding", false, "write no padding after the metadata (no effect on Ogg/WAV/AIFF/Matroska, which have no padding region)")
	f.BoolVar(&e.numericGenre, "numeric-genre", false, "write a recognized genre as its numeric reference instead of its name: ID3's TCON on MP3/AAC/AIFF, MP4's gnre atom, and on WAV only where an 'id3 ' chunk exists or the same edit creates one (LIST/INFO IGNR stores the name literally). FLAC, Ogg, and Matroska have no numeric genre representation, so it has no effect there")
	f.StringVar(&e.outputGain, "output-gain", "", "set the decoder-applied output gain the stream header declares, in decibels (e.g. -3.5), rounded to the Q7.8 step the field stores. Only Ogg Opus has one; elsewhere it is dropped with a warning (--strict then refuses) and a read-only file fails. RFC 7845 applies R128_TRACK_GAIN and R128_ALBUM_GAIN on top of it, so they are rebased by the same change unless this invocation sets or clears them (--keep-r128 leaves them and warns)")
	f.BoolVar(&e.keepR128, "keep-r128", false, "with --output-gain, leave R128_TRACK_GAIN and R128_ALBUM_GAIN as they are instead of rebasing them by the same change, and warn for each one kept. Use it when the stored values are stale and will be replaced separately")
	f.BoolVar(&e.strict, "strict", false, "fail (exit 2), instead of just noting it, on an unknown key or any edit the destination format cannot store faithfully: a value dropped, coerced, or reduced in precision; a single-valued key given multiple values; a dropped picture, chapter, or synced-lyrics field; or a truncated chapter title or clamped timestamp")
}

// nonEditFlags are set/output flags that are not edits on their own.
// Every other set flag counts as an edit, so new edit flags need no manual listing.
var nonEditFlags = map[string]bool{
	"output": true, "overwrite": true, "verify": true, "preserve-mtime": true,
	"recursive": true, "quiet": true, "json": true,
	"force": true, "picture-description": true, "strict": true,
	// --synced-lyrics-lang only labels authored lyrics; alone it is not an edit.
	"synced-lyrics-lang": true,
}

// editFlagsEmpty reports whether no edit was requested (only non-edit flags or none).
// In-place set is a usage error; with -o it is a verbatim copy.
// Uses Visit (changed flags only), so new flags do not need a manual list.
func editFlagsEmpty(cmd *cobra.Command) bool {
	empty := true
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if !nonEditFlags[f.Name] {
			empty = false
		}
	})
	return empty
}

// quotingHint detects unquoted spaced values: --set/--add with a stray bare-word
// positional beside a real input (--set TITLE=Two Words -> file + Words).
// Needs a resolved sibling to avoid false positives on lone missing extensionless paths.
// Shared by plan (advisory) and set (refuses before write).
func quotingHint(ef *editFlags, realOf func(string) string, args []string) (hint string, ok bool) {
	if len(ef.set) == 0 && len(ef.add) == 0 {
		return "", false
	}
	// Pre-pass: skip stat unless some positional looks like a stray bare word.
	hasBareWord := false
	for _, a := range args {
		if a != stdinArg && looksLikeBareWord(a) {
			hasBareWord = true
			break
		}
	}
	if !hasBareWord {
		return "", false
	}
	// Stat only after a bare-word candidate exists.
	resolves := func(a string) bool {
		if a == stdinArg {
			return true
		}
		_, err := os.Stat(realOf(a))
		return err == nil
	}
	hasRealInput, hasStrayWord := false, false
	for _, a := range args {
		switch {
		case resolves(a):
			hasRealInput = true // present input (incl. existing extensionless file)
		case looksLikeBareWord(a):
			hasStrayWord = true // missing bare word, likely split value
		}
	}
	if hasRealInput && hasStrayWord {
		return "a value containing spaces must be quoted, e.g. --set 'TITLE=Two Words'", true
	}
	return "", false
}

// refuseUnquotedValue refuses unquoted spaced values (quotingHint). Exit 2 for plan and set.
// writes adds "; nothing was written" for set. Shared so wording stays aligned.
func refuseUnquotedValue(ef *editFlags, realOf func(string) string, args []string, writes bool) error {
	hint, ok := quotingHint(ef, realOf, args)
	if !ok {
		return nil
	}
	if writes {
		return usagef("%s; nothing was written", hint)
	}
	return usagef("%s", hint)
}

// rejectEmptyScalarFlags rejects empty scalar flags (empty vs unset is ambiguous).
// Uses Changed so set and plan share one check.
func rejectEmptyScalarFlags(cmd *cobra.Command) error {
	for _, name := range []string{"preset", "legacy", "id3-multi", "padding", "synced-lyrics-file"} {
		if cmd.Flags().Changed(name) {
			if v, _ := cmd.Flags().GetString(name); v == "" {
				return usagef("--%s cannot be empty", name)
			}
		}
	}
	return nil
}

// patch compiles --set/--add/--clear into a presence-aware patch.
// Malformed input or the same key in write and clear is a usage error (exit 2).
// Clears run last, so set+add vs clear conflicts are rejected up front.
func (e *editFlags) patch() (tag.TagPatch, error) {
	var p tag.TagPatch
	for _, kv := range e.set {
		k, v, err := splitAssign(kv)
		if err != nil {
			return p, err
		}
		p.Set(k, v)
	}
	for _, kv := range e.add {
		k, v, err := splitAssign(kv)
		if err != nil {
			return p, err
		}
		p.Add(k, v)
	}
	// set+add on one key is legal; (set|add) vs clear is not.
	for _, ks := range e.clear {
		k, err := parseEditKey(strings.TrimSpace(ks))
		if err != nil {
			return p, &usageError{msg: err.Error()}
		}
		if p.Writes(k) {
			return p, usagef("%s is given to both --set/--add and --clear; remove one (they conflict)", k)
		}
		p.Clear(k)
	}
	if e.stripEncoder {
		// --strip-encoder clears ENCODER; name that flag in conflict messages.
		if p.Writes(tag.Encoder) {
			return p, usagef("%s is given to both --set/--add and --strip-encoder; remove one (they conflict)", tag.Encoder)
		}
		p.Clear(tag.Encoder)
	}
	return p, nil
}

// splitAssign parses KEY=VALUE. Key is normalized and alias-resolved.
// Value is everything after the first '=', so it may contain '='.
func splitAssign(s string) (tag.Key, string, error) {
	i := strings.IndexByte(s, '=')
	if i < 0 {
		return "", "", usagef("missing '=' in %q (want KEY=VALUE; use --clear to remove a key)", s)
	}
	k, err := parseEditKey(strings.TrimSpace(s[:i]))
	if err != nil {
		return "", "", &usageError{msg: err.Error()}
	}
	v := s[i+1:]
	if err := checkArgText(v, fmt.Sprintf("value for %q", k)); err != nil {
		return "", "", err
	}
	return k, v, nil
}

// parseEditKey validates a tag key and resolves aliases to canonical keys.
// Shared by all CLI key entry points so aliases do not become duplicate custom fields.
func parseEditKey(s string) (tag.Key, error) {
	k, err := tag.ParseKey(s)
	if err != nil {
		return "", err
	}
	return wl.ResolveAlias(k), nil
}

// loadPictures validates --add-cover/--add-picture inputs once for the whole invocation.
// --add-cover replaces front cover; --add-picture front-cover= appends (like other roles).
// --picture-description applies to all added pictures; error if none were added.
func (e *editFlags) loadPictures() ([]wl.Picture, error) {
	var pics []wl.Picture
	// --add-cover is last-wins but every path is validated first.
	var lastCover *wl.Picture
	for _, path := range e.addCover {
		p, err := e.loadPictureFile("cover image", wl.PicFrontCover, path)
		if err != nil {
			return nil, err
		}
		lastCover = &p
	}
	if lastCover != nil {
		pics = append(pics, *lastCover)
	}
	for _, spec := range e.addPicture {
		role, path, ok := strings.Cut(spec, "=")
		if !ok {
			return nil, usagef("--add-picture wants ROLE=PATH, got %q", spec)
		}
		pt, ok := pictureRole(role)
		if !ok {
			return nil, usagef("unknown picture role %q; valid roles: %s", strings.TrimSpace(role), pictureRoleList())
		}
		// Path is verbatim after '=', like --set KEY=VALUE.
		p, err := e.loadPictureFile("picture image", pt, path)
		if err != nil {
			return nil, err
		}
		pics = append(pics, p)
	}
	if e.pictureDescription != "" {
		if err := checkArgText(e.pictureDescription, "--picture-description"); err != nil {
			return nil, err
		}
		if len(pics) == 0 {
			return nil, usagef("--picture-description needs at least one --add-picture or --add-cover")
		}
		for i := range pics {
			pics[i].Description = e.pictureDescription
		}
	}
	return pics, nil
}

// loadPictureFile reads one image into a picture of role pt. label prefixes errors by flag.
// Non-regular sources: usage error (exit 2). Missing file: I/O error (exit 6).
// 0-byte file refused even with --force. Sniffed on load for plan output.
func (e *editFlags) loadPictureFile(label string, pt wl.PictureType, path string) (wl.Picture, error) {
	// Empty path is usage error before os.ReadFile misreports it.
	if path == "" {
		return wl.Picture{}, usagef("%s: image path cannot be empty", label)
	}
	// Pictures have no stdin path (acceptsStdin false).
	if err := checkRegularFile(path, false); err != nil {
		return wl.Picture{}, fmt.Errorf("%s: %w", label, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return wl.Picture{}, &pictureLoadError{label: label, path: path, err: err}
	}
	if len(data) == 0 {
		return wl.Picture{}, usagef("%s: %s: file is empty", label, path)
	}
	if !e.force && !wl.IsRecognizedImage(data) {
		return wl.Picture{}, usagef("%s: %s: not a recognized image (%s); use --force to embed anyway", label, path, wl.RecognizedImageFormats)
	}
	p := wl.Picture{Type: pt, Data: data}
	p.SniffInto()
	return p, nil
}

// pictureLoadError wraps os.ReadFile failures for --add-cover/--add-picture.
// Renders "<label>: <path>: <reason>"; Unwrap preserves I/O classification (exit 6).
type pictureLoadError struct {
	label string
	path  string
	err   error // os.ReadFile failure, usually *fs.PathError
}

func (e *pictureLoadError) Error() string {
	// perFileReason strips PathError framing; this adds label and path.
	return fmt.Sprintf("%s: %s: %s", e.label, e.path, perFileReason(e.err))
}

func (e *pictureLoadError) Unwrap() error { return e.err }

// pictureRoles maps role names to PictureType, derived from PictureType.String()
// (lowercase, spaces to hyphens) so it tracks the enum and disambiguates roles.
var pictureRoles = func() map[string]wl.PictureType {
	m := map[string]wl.PictureType{}
	for i := 0; i < 256; i++ {
		p := wl.PictureType(i)
		name := p.String()
		if name == "reserved" {
			break // past last defined role
		}
		m[strings.ReplaceAll(strings.ToLower(name), " ", "-")] = p
	}
	return m
}()

// pictureRole resolves a role name (case-insensitive, trimmed) to PictureType.
func pictureRole(name string) (wl.PictureType, bool) {
	pt, ok := pictureRoles[strings.ToLower(strings.TrimSpace(name))]
	return pt, ok
}

// pictureRoleList returns the valid role names in sorted order, for a usage error.
func pictureRoleList() string {
	roles := make([]string, 0, len(pictureRoles))
	for r := range pictureRoles {
		roles = append(roles, r)
	}
	slices.Sort(roles)
	return strings.Join(roles, ", ")
}

// resolveRemovals maps --remove-picture selectors to dump-order indices.
// Selector: 1-based index or role name. Bad index/role: usage error.
// Unmatched role removes nothing but is reported in missedRoles (--strict fails).
// missedRoles is deduped in canonical role names.
func resolveRemovals(selectors []string, pics []wl.Picture) (targets map[int]bool, missedRoles []string, err error) {
	targets = map[int]bool{}
	seenMiss := map[string]bool{}
	for _, sel := range selectors {
		s := strings.TrimSpace(sel)
		if n, err := strconv.Atoi(s); err == nil {
			if n < 1 || n > len(pics) {
				return nil, nil, usagef("--remove-picture index %d is out of range (file has %d picture(s))", n, len(pics))
			}
			targets[n-1] = true
			continue
		}
		pt, ok := pictureRole(s)
		if !ok {
			return nil, nil, usagef("--remove-picture wants a role name or a 1-based index, got %q; valid roles: %s", sel, pictureRoleList())
		}
		matched := false
		for i, p := range pics {
			if p.Type == pt {
				targets[i] = true
				matched = true
			}
		}
		if !matched {
			if role := pt.String(); !seenMiss[role] {
				seenMiss[role] = true
				missedRoles = append(missedRoles, role)
			}
		}
	}
	return targets, missedRoles, nil
}

// chapterAdds parses --add-chapter TIMESTAMP=Title once per invocation.
func (e *editFlags) chapterAdds() ([]wl.Chapter, error) {
	var chs []wl.Chapter
	for _, s := range e.addChapter {
		start, title, err := splitChapter(s)
		if err != nil {
			return nil, err
		}
		chs = append(chs, wl.Chapter{Start: start, Title: title})
	}
	return chs, nil
}

// writeOptions resolves preset, legacy, and padding into write options (preset first).
func (e *editFlags) writeOptions() ([]wl.WriteOption, bool, error) {
	opts, err := resolveWriteFlags(e.preset, e.legacy, e.id3Multi)
	if err != nil {
		return nil, false, err
	}
	// Padding after preset/legacy so explicit --padding/--no-padding wins.
	// Edit commands only; copy uses resolveWriteFlags without padding.
	padOpt, padFlag, err := resolvePaddingFlag(e.padding, e.noPadding)
	if err != nil {
		return nil, false, err
	}
	if padOpt != nil {
		opts = append(opts, padOpt)
	}
	// --force skips library picture validation to match loadPictureFile's --force path.
	if e.force {
		opts = append(opts, wl.WithUnrecognizedPictures())
	}
	// --numeric-genre via compile() for plan and set.
	if e.numericGenre {
		opts = append(opts, wl.WithNumericGenre())
	}
	// --keep-r128 opts out of R128 rebase; compile() requires --output-gain.
	if e.keepR128 {
		opts = append(opts, wl.WithKeepR128Gains())
	}
	return opts, padFlag, nil
}

// maxPaddingBytes caps --padding (64 MiB). Without it, reuse+floor could allocate huge regions.
const maxPaddingBytes = 64 << 20

// parsePaddingBytes uses the same size parser as --max-size. Truncating values are rejected.
func parsePaddingBytes(s string) (int64, error) { return parseByteSizeExact(s) }

// resolvePaddingFlag turns --padding/--no-padding into a write option.
// Returns (nil, false) when neither set (default 8 KiB policy).
// Zero spellings ("0", "00", " 0 ") all mean no padding; conflict only for positive --padding.
// Positive N sets Target=N and Min=N (floor, not silent reuse). Max stays 0.
func resolvePaddingFlag(padding string, noPadding bool) (opt wl.WriteOption, flagGiven bool, err error) {
	var value int64
	hasValue := false
	// "" means unset; whitespace-only "   " must parse-fail, not default.
	if padding != "" {
		v, perr := parsePaddingBytes(padding)
		if perr != nil {
			return nil, false, usagef("--padding %v", perr)
		}
		if v > maxPaddingBytes {
			return nil, false, usagef("--padding %d is too large (max %d bytes, 64 MiB)", v, maxPaddingBytes)
		}
		value, hasValue = v, true
	}
	switch {
	case !noPadding && !hasValue:
		return nil, false, nil // default policy
	case noPadding && value > 0:
		return nil, false, usagef("--padding and --no-padding cannot be combined")
	case value > 0:
		return wl.WithPadding(wl.PaddingPolicy{Target: value, Min: value, Max: 0, ReuseInPlace: true}), true, nil
	default:
		// Min 0 would reuse existing padding instead of dropping it.
		return wl.WithPadding(wl.PaddingPolicy{Target: 0, Max: 0}), true, nil
	}
}

// resolveWriteFlags turns preset/legacy/id3-multi into write options (preset, then legacy).
// Shared by plan, set, and copy.
func resolveWriteFlags(preset, legacy, id3Multi string) ([]wl.WriteOption, error) {
	var opts []wl.WriteOption
	if preset != "" {
		opt, ok := presetOptions[strings.ToLower(preset)]
		if !ok {
			return nil, usagef("unknown preset %q (want preserve|compatible|minimal)", preset)
		}
		opts = append(opts, opt)
	}
	if legacy != "" {
		pol, ok := legacyOptions[strings.ToLower(legacy)]
		if !ok {
			return nil, usagef("unknown legacy policy %q (want preserve|strip)", legacy)
		}
		opts = append(opts, wl.WithLegacyPolicy(pol))
	}
	if id3Multi != "" {
		pol, ok := id3MultiOptions[strings.ToLower(id3Multi)]
		if !ok {
			return nil, usagef("unknown --id3-multi %q (want null|repeat|slash)", id3Multi)
		}
		opts = append(opts, wl.WithID3MultiValue(pol))
	}
	return opts, nil
}

var presetOptions = map[string]wl.WriteOption{
	"preserve":   wl.Preserve,
	"compatible": wl.Compatible,
	"minimal":    wl.Minimal,
}

var legacyOptions = map[string]wl.LegacyPolicy{
	"preserve": wl.LegacyPreserve,
	"strip":    wl.LegacyStrip,
}

var id3MultiOptions = map[string]wl.ID3MultiValuePolicy{
	"null":   wl.ID3MultiNullSep,
	"repeat": wl.ID3MultiRepeatFrame,
	"slash":  wl.ID3MultiSlash,
}

// compiledEdit holds invocation-level edit inputs resolved once for bulk runs.
type compiledEdit struct {
	patch         tag.TagPatch
	opts          []wl.WriteOption
	addPics       []wl.Picture // validated at compile time
	replaceFront  bool         // --add-cover replaces front cover (--add-picture front-cover appends)
	removePics    []string     // --remove-picture selectors, resolved per file
	rmPics        bool
	chapters      []wl.Chapter
	clearChapters bool
	// syncedLyrics: 0 or 1 authored set; replaces existing synced lyrics.
	syncedLyrics []wl.SyncedLyrics
	// syncedLyricsDroppedLines: 1-based LRC line numbers dropped at compile time.
	syncedLyricsDroppedLines []int
	clearSyncedLyrics        bool
	unknownKeys              []tag.Key // non-canonical --set/--add keys, first-seen order
	clearKeys                []tag.Key // non-canonical --clear keys, first-seen order
	paddingFlag              bool      // --padding/--no-padding given

	// outputGain is Q7.8 integer; outputGainSet true when flag given (0 is valid).
	outputGain    int
	outputGainSet bool
}

// compile resolves edit flags before any file is parsed. extra adds save-only options.
func (e *editFlags) compile(extra ...wl.WriteOption) (*compiledEdit, error) {
	opts, padFlag, err := e.writeOptions()
	if err != nil {
		return nil, err
	}
	opts = append(opts, extra...)
	// Allow unsupported structural edits to drop with a warning; --strict fails.
	// copy uses resolveWriteFlags and has its own drop behavior.
	opts = append(opts, wl.WithAllowUnsupportedDrop())
	patch, err := e.patch()
	if err != nil {
		return nil, err
	}
	// ENCODER edits also strip inherited vendor/ISFT stamps the tag edit cannot reach.
	if patch.Touches(tag.Encoder) {
		opts = append(opts, wl.WithStripEncoderStamp())
	}
	addPics, err := e.loadPictures()
	if err != nil {
		return nil, err
	}
	chapters, err := e.chapterAdds()
	if err != nil {
		return nil, err
	}
	syncedLyrics, syncedLyricsDropped, err := e.syncedLyricsAdds()
	if err != nil {
		return nil, err
	}
	var outputGain int
	if e.outputGain != "" {
		if outputGain, err = parseOutputGainDB(e.outputGain); err != nil {
			return nil, err
		}
	}
	// --keep-r128 only applies when --output-gain moves the header.
	if e.keepR128 && e.outputGain == "" {
		return nil, usagef("--keep-r128 needs --output-gain")
	}
	return &compiledEdit{
		patch:                    patch,
		opts:                     opts,
		addPics:                  addPics,
		replaceFront:             len(e.addCover) > 0,
		removePics:               e.removePicture,
		rmPics:                   e.rmPics,
		chapters:                 chapters,
		clearChapters:            e.clearChapters,
		syncedLyrics:             syncedLyrics,
		syncedLyricsDroppedLines: syncedLyricsDropped,
		clearSyncedLyrics:        e.clearSyncedLyrics,
		unknownKeys:              e.unknownAssignKeys(),
		clearKeys:                e.unknownClearKeys(),
		paddingFlag:              padFlag,
		outputGain:               outputGain,
		outputGainSet:            e.outputGain != "",
	}, nil
}

// outputGainStepsPerDB is Q7.8 scale (inverse of [wl.OutputGainDecibels]).
const outputGainStepsPerDB = 256

// parseOutputGainDB converts --output-gain dB to signed Q7.8 for the Opus header.
func parseOutputGainDB(s string) (int, error) {
	db, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(db) || math.IsInf(db, 0) {
		return 0, usagef("--output-gain %q is not a decibel value", s)
	}
	// Bounds use shared formatter so ceiling display matches rejection threshold.
	q78 := math.Round(db * outputGainStepsPerDB)
	if q78 < math.MinInt16 || q78 > math.MaxInt16 {
		return 0, usagef("--output-gain %s is outside the range the header stores (%s to %s)",
			s, wl.OutputGainDB(math.MinInt16), wl.OutputGainDB(math.MaxInt16))
	}
	return int(q78), nil
}

// unknownAssignKeys returns non-canonical --set/--add keys, first-seen, deduped.
// Noted on stderr; --strict errors. patch() already validated assignments.
func (e *editFlags) unknownAssignKeys() []tag.Key {
	var keys []tag.Key
	for _, kv := range slices.Concat(e.set, e.add) {
		if k, _, err := splitAssign(kv); err == nil {
			keys = append(keys, k)
		}
	}
	return dedupUnknownKeys(keys)
}

// unknownClearKeys returns non-canonical --clear keys, first-seen, deduped.
// Typo clears are silent no-ops; surfaced as notes. patch() already validated keys.
func (e *editFlags) unknownClearKeys() []tag.Key {
	var keys []tag.Key
	for _, ks := range e.clear {
		if k, err := parseEditKey(strings.TrimSpace(ks)); err == nil {
			keys = append(keys, k)
		}
	}
	return dedupUnknownKeys(keys)
}

// dedupUnknownKeys filters to unknown keys, first-seen, deduped.
// Shared by assign and clear note paths.
func dedupUnknownKeys(keys []tag.Key) []tag.Key {
	var out []tag.Key
	seen := map[tag.Key]bool{}
	for _, k := range keys {
		// R128 gain keys are intentional, not typos.
		if k.Known() || tag.IsR128GainKey(k) || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

// anyInputExists reports whether any path is actionable ("-" always counts).
// Defers cosmetic notes until a real input exists so missing-file runs show not-found first.
// Skips pathErrors entries (directory without --recursive, FIFO, etc.).
// --strict unknown-key checks are not gated on this.
func anyInputExists(realOf func(string) string, paths []string, pathErrors map[string]error) bool {
	for _, p := range paths {
		if pathErrors[p] != nil {
			continue
		}
		if p == stdinArg {
			return true
		}
		if _, err := os.Stat(realOf(p)); err == nil {
			return true
		}
	}
	return false
}

// notifyInvocationNotes emits invocation-level guardrails and notes for set and plan.
// --strict unknown-key runs even without inputs; cosmetic notes wait for anyInputExists.
func notifyInvocationNotes(errOut io.Writer, ce *compiledEdit, ef *editFlags, realOf func(string) string, paths []string, pathErrors map[string]error, asJSON bool) error {
	if len(paths) == 0 {
		return nil
	}
	exists := anyInputExists(realOf, paths, pathErrors)
	if ef.strict || exists {
		if err := notifyUnknownKeys(errOut, ce, ef.strict, asJSON); err != nil {
			return err
		}
	}
	if exists {
		notifyClearKeys(errOut, ce, asJSON)
		notifyValueNotes(errOut, ef, asJSON)
	}
	return nil
}

// guardrailKeys: empty pass; strict errors; JSON suppresses notes; else return for stderr.
func guardrailKeys(keys []tag.Key, strict, asJSON bool, strictErr func([]tag.Key) error) (note []tag.Key, err error) {
	if len(keys) == 0 {
		return nil, nil
	}
	if strict {
		return nil, strictErr(keys)
	}
	if asJSON {
		return nil, nil
	}
	return keys, nil
}

// notifyUnknownKeys: strict usage error; else stderr notes. Suppressed under --json.
func notifyUnknownKeys(errOut io.Writer, ce *compiledEdit, strict, asJSON bool) error {
	note, err := guardrailKeys(ce.unknownKeys, strict, asJSON, func(ks []tag.Key) error {
		return usagef("unknown key(s) not in the canonical vocabulary: %s (omit --strict to write them as custom fields)", keyList(ks))
	})
	notes := &cappedNotes{w: errOut, noun: "unknown key(s)"}
	for _, k := range note {
		notes.printf("note: %s is not a known key; treated as a custom field where the format permits%s\n", k, didYouMean(k))
	}
	notes.done()
	// One trailing hint after per-key lines, not repeated per key.
	if len(note) > 0 {
		fmt.Fprintln(errOut, "note: run 'waxlabel keys' to list the canonical vocabulary")
	}
	return err
}

// didYouMean returns "; did you mean KEY?" via [tag.ClosestKey], or "".
func didYouMean(k tag.Key) string {
	if s, ok := tag.ClosestKey(string(k)); ok {
		return fmt.Sprintf("; did you mean %s?", s)
	}
	return ""
}

// notifyClearKeys notes non-canonical --clear keys. Never escalated under --strict.
// Suppressed under --json.
func notifyClearKeys(errOut io.Writer, ce *compiledEdit, asJSON bool) {
	if asJSON {
		return
	}
	notes := &cappedNotes{w: errOut, noun: "unknown key(s)"}
	defer notes.done()
	for _, k := range ce.clearKeys {
		notes.printf("note: %s is not a known key (clearing affects only a custom field of that exact name)%s\n", k, didYouMean(k))
	}
}

// notifyValueNotes emits advisory notes for --set/--add values (malformed, empty, collisions).
// Notes only; never --strict errors. Suppressed under --json. Order: --set then --add.
func notifyValueNotes(errOut io.Writer, e *editFlags, asJSON bool) {
	if asJSON {
		return
	}
	notes := &cappedNotes{w: errOut, noun: "note(s)"}
	defer notes.done()

	// Track alias collisions and duplicate --set on one key; note at last assignment.
	type seenSet struct {
		first, other string
		value        string
		collided     bool
	}
	seen := map[tag.Key]*seenSet{}
	lastAt := map[tag.Key]int{}
	for i, kv := range e.set {
		if k, _, err := splitAssign(kv); err == nil {
			lastAt[k] = i
		}
	}
	for i, kv := range e.set {
		k, v, err := splitAssign(kv)
		if err != nil {
			continue // patch() already reported malformed assignment
		}
		// Trim like the writer for collision and malformed checks.
		v = tag.TrimTokenValue(k, v)
		// Compare trimmed values; run before empty-value continue.
		spelling := strings.TrimSpace(kv[:strings.IndexByte(kv, '=')])
		prev, ok := seen[k]
		if !ok {
			prev = &seenSet{first: spelling}
			seen[k] = prev
		} else {
			if prev.value != v {
				prev.collided = true
			}
			if prev.other == "" && !strings.EqualFold(prev.first, spelling) {
				prev.other = spelling
			}
		}
		prev.value = v
		if prev.collided && i == lastAt[k] {
			// Alias collision vs duplicate spelling.
			if prev.other == "" {
				notes.printf("note: --set %s was given more than once; last value %q was used\n",
					tag.SanitizeLine(prev.first), tag.SanitizeLine(v))
			} else {
				notes.printf("note: --set %s and --set %s refer to the same field (%s); last value %q was used\n",
					tag.SanitizeLine(prev.first), tag.SanitizeLine(prev.other),
					tag.SanitizeLine(string(k)), tag.SanitizeLine(v))
			}
		}
		if v == "" {
			// Empty --set value; format outcome varies. Distinct from --clear.
			notes.printf("note: %s= writes an empty value (some formats may drop an empty field rather than store it); use --clear %s to remove it\n", k, k)
			continue
		}
		noteMalformedValue(notes, k, v)
	}
	for _, kv := range e.add {
		// Empty --add skips empty-value note; still checks malformed when non-empty.
		if k, v, err := splitAssign(kv); err == nil {
			if v = tag.TrimTokenValue(k, v); v != "" {
				noteMalformedValue(notes, k, v)
			}
		}
	}
}

// noteMalformedValue emits pre-parse advisory for values failing [tag.ValidatorFor].
// Uses same registry as [Document.Lint]. Per-file value-dropped warning is authoritative for drops.
// SanitizeLine prevents control bytes and forged newlines in one-line notes.
func noteMalformedValue(notes *cappedNotes, k tag.Key, v string) {
	ks, vs := tag.SanitizeLine(string(k)), tag.SanitizeLine(v)
	if val, ok := tag.ValidatorFor(k); ok && !val.Valid(k, v) {
		_, detail := val.Details(k, v)
		notes.printf("note: %s=%s %s; kept as text where the format supports it\n", ks, vs, detail)
		return
	}
	// Negative numerics and empty-number/total are valid but unusual; else-if avoids double notes.
	if tag.IsNumericKey(k) && tag.NegativeNumericValue(k, v) {
		notes.printf("note: %s=%s is negative (numbering is normally non-negative); some formats cannot store a negative number and will drop it\n", ks, vs)
	} else if tag.EmptyNumberWithTotal(k, v) {
		notes.printf("note: %s=%s has no number component; the number is left unset\n", ks, vs)
	}
}

// strictEscalatingCodes: plan warnings --strict promotes to exit 2 (edit-caused losses).
// Read from plan warnings, not re-derived from Changes(). Unknown keys use notifyUnknownKeys.
//
// NOT escalated:
//   - WarnID3MultiValue, WarnNativeValueReduced: value fully stored.
//   - WarnChaptersFlattened, WarnPaddingClamped: pre-existing state or padding, not tag loss.
//   - Advisory/read-path codes (duplicate-tag-block, unknown-chunk-size, etc.): file state, not edit loss.
//   - WarnNonConformingIcon: written in full; conformance only.
//   - duplicate-tag-block-dropped IS escalated (write destroyed content).
var strictEscalatingCodes = map[wl.WarningCode]bool{
	wl.WarnValueDropped:              true,
	wl.WarnValueCoerced:              true,
	wl.WarnValueReduced:              true,
	wl.WarnSingleValuedMulti:         true,
	wl.WarnNumericGenre:              true, // ID3 numeric genre reads back as name
	wl.WarnTagStructureDropped:       true,
	wl.WarnPictureMetadataDropped:    true,
	wl.WarnCommentDescriptionDropped: true,
	// Chapter codes: !carried gate applies to editor codes only, not this whole map.
	// copy --strict can escalate on carry (codec never sees the flag).
	wl.WarnChapterEndsDropped:           true,
	wl.WarnChapterTitleTruncated:        true,
	wl.WarnChapterStartOverflow:         true,
	wl.WarnChapterMetadataDropped:       true,
	wl.WarnSyncedLyricsMetadataDropped:  true,
	wl.WarnSyncedLyricsTimestampClamped: true,
	wl.WarnSyncedLyricsTruncated:        true,
	wl.WarnSyncedLyricsUnsupported:      true,
	wl.WarnPictureUnsupported:           true,
	wl.WarnChaptersUnsupported:          true,
	wl.WarnOutputGainUnsupported:        true,
	wl.WarnSyncedLyricsLineDropped:      true,
	wl.WarnPictureSelectorMiss:          true,
	wl.WarnLegacyStripDropped:           true, // policy destroyed data
	wl.WarnDuplicateTagBlockDropped:     true, // write destroyed duplicate container content
	wl.WarnMalformedTagEntryDropped:     true, // write dropped unread parser region
}

// strictWarningGate fails a file at exit 2 when --strict and plan has escalating warnings.
// Per-file error keeps multi-file exit order-independent. Also used by copy.
type strictWarningGate struct {
	strict bool
}

func newStrictWarningGate(strict bool) *strictWarningGate {
	return &strictWarningGate{strict: strict}
}

// check returns usage error for escalating warnings when strict; else nil.
func (g *strictWarningGate) check(plan *wl.Plan) error {
	if !g.strict {
		return nil
	}
	var reasons []string
	for _, w := range plan.Report().Warnings {
		if strictEscalatingCodes[w.Code] {
			reasons = append(reasons, strictWarningReason(w))
		}
	}
	if len(reasons) == 0 {
		return nil
	}
	// Hint must not say "edit"; copy uses this gate too.
	return usagef("%s (omit --strict to continue with a warning)", strings.Join(reasons, "; "))
}

// strictWarningReason renders one escalating warning for --strict errors.
// Some codes echo plan Message to stay aligned with plan body wording.
func strictWarningReason(w wl.Warning) string {
	keys := keyList(w.Keys)
	if keys == "" {
		// Keyless warning: use Message verbatim.
		return w.Message
	}
	switch w.Code {
	case wl.WarnValueDropped, wl.WarnValueReduced, wl.WarnNumericGenre:
		return w.Message
	case wl.WarnLegacyStripDropped:
		return w.Message
	case wl.WarnDuplicateTagBlockDropped:
		return w.Message
	case wl.WarnValueCoerced:
		return fmt.Sprintf("%s: value is not valid for this format and would be stored coerced", keys)
	case wl.WarnSingleValuedMulti:
		return fmt.Sprintf("%s: single-valued but given multiple values", keys)
	case wl.WarnTagStructureDropped:
		return fmt.Sprintf("%s: %s", keys, w.Message)
	case wl.WarnCommentDescriptionDropped:
		return fmt.Sprintf("%s: the rewrite drops a description one of the file's comment frames carried", keys)
	default:
		return keys
	}
}

// paddingNoter notes when --padding/--no-padding has no effect (AccessNone formats).
// Deduped per format. Suppressed under --json. Caller gates on ce.paddingFlag.
type paddingNoter struct {
	asJSON bool
	errOut io.Writer
	seen   map[wl.Format]bool
}

func newPaddingNoter(asJSON bool, errOut io.Writer) *paddingNoter {
	return &paddingNoter{asJSON: asJSON, errOut: errOut, seen: map[wl.Format]bool{}}
}

// note emits once per AccessNone format (Ogg/WAV/AIFF/Matroska).
// AccessFull/Partial honor padding; nuance left to caps and README.
func (n *paddingNoter) note(caps wl.Capabilities) {
	if n.asJSON || n.seen[caps.Format] {
		return
	}
	if caps.Padding != wl.AccessNone {
		return
	}
	n.seen[caps.Format] = true
	fmt.Fprintf(n.errOut, "note: padding control does not apply to %s; --padding/--no-padding has no effect\n", caps.Format)
}

// noteListCap limits per-advisory item lines before aggregating the rest.
const noteListCap = 10

// cappedNotes limits advisory stderr lines; done emits aggregate count. Call done always.
type cappedNotes struct {
	w     io.Writer
	noun  string
	shown int
	held  int
}

func (c *cappedNotes) printf(format string, a ...any) {
	if c.shown >= noteListCap {
		c.held++
		return
	}
	c.shown++
	fmt.Fprintf(c.w, format, a...)
}

func (c *cappedNotes) done() {
	if c.held > 0 {
		fmt.Fprintf(c.w, "note: %d more %s not listed\n", c.held, c.noun)
	}
}

// keyList renders keys comma-separated. Uncapped: used in errors and --json messages.
func keyList(keys []tag.Key) string {
	s := make([]string, len(keys))
	for i, k := range keys {
		s[i] = string(k)
	}
	return strings.Join(s, ", ")
}

// prepare parses realPath (errors use origPath display name), applies compiledEdit, returns plan.
// Removals before adds. --add-cover replaces front cover (clears existing front covers first).
func (ce *compiledEdit) prepare(ctx context.Context, realPath, origPath string) (*wl.Document, *wl.Plan, error) {
	doc, err := parseInput(ctx, realPath, origPath)
	if err != nil {
		return nil, nil, err
	}
	ed := doc.Edit().Apply(ce.patch)
	if ce.rmPics {
		ed.ClearPictures()
	}
	// Remove by index with running counter aligned to dump order.
	if len(ce.removePics) > 0 {
		targets, missedRoles, err := resolveRemovals(ce.removePics, doc.Pictures())
		if err != nil {
			return nil, nil, err
		}
		i := -1
		ed.RemovePictures(func(wl.Picture) bool {
			i++
			return targets[i]
		})
		// Unmatched role: per-file warning (--strict fails).
		ed.NotePictureSelectorMiss(missedRoles...)
	}
	// --add-cover CLI policy: clear existing front covers before add.
	if ce.replaceFront {
		ed.RemovePictures(func(p wl.Picture) bool { return p.Type == wl.PicFrontCover })
	}
	for _, p := range ce.addPics {
		ed.AddPicture(p)
	}
	// --clear-chapters before --add-chapter keeps only added chapters.
	if len(ce.chapters) > 0 {
		base := doc.Chapters()
		if ce.clearChapters {
			base = nil
		}
		// Dedup on Start+Title; ignore parse-only fields and derived End when add.End==0.
		merged := slices.Clone(base)
		for _, add := range ce.chapters {
			dup := slices.ContainsFunc(merged, func(c wl.Chapter) bool {
				return c.Start == add.Start && c.Title == add.Title && (add.End == 0 || c.End == add.End)
			})
			if !dup {
				merged = append(merged, add)
			}
		}
		ed.SetChapters(merged...)
	} else if ce.clearChapters {
		ed.ClearChapters()
	}
	// Authored synced lyrics replace existing sets. Clear before author when both given.
	if len(ce.syncedLyrics) > 0 {
		if ce.clearSyncedLyrics {
			ed.ClearSyncedLyrics()
		}
		ed.SetSyncedLyrics(ce.syncedLyrics...)
		// Propagate LRC dropped lines for plan warning/--strict.
		ed.NoteSyncedLyricsDropped(ce.syncedLyricsDroppedLines...)
	} else if ce.clearSyncedLyrics {
		ed.ClearSyncedLyrics()
	}
	if ce.outputGainSet {
		ed.SetOutputGain(ce.outputGain)
	}
	plan, err := ed.Prepare(ce.opts...)
	if err != nil {
		return nil, nil, err
	}
	return doc, plan, nil
}
