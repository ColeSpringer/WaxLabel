package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// editPrecedenceHelp documents how the edit flags combine for one key. --set
// replaces and --add appends; giving the same key to both a write (--set/--add)
// and a removal (--clear) is rejected as a conflict (exit 2, nothing written) -
// there is no silent precedence, so the rule users rely on is "don't give one key
// to both." patch() enforces this.
const editPrecedenceHelp = "For one key, --set replaces the key and --add appends to it. Giving the same\n" +
	"key to both --set/--add and --clear is an error (they conflict); order on the\n" +
	"command line does not change this."

// editFlags holds the tag- and picture-editing options shared by the plan and
// set commands, plus the write-shaping options (preset, legacy policy). It binds
// onto a command's flag set and compiles into a presence-aware [tag.TagPatch], a
// picture mutation, and a list of [wl.WriteOption].
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

	stripEncoder bool // clear the ENCODER software stamp

	preset string
	legacy string

	// padding is the raw --padding value (a byte count); "" means unset, mirroring
	// the preset/legacy empty-string sentinel. noPadding is --no-padding. Both unset
	// leaves the default 8 KiB policy untouched.
	padding   string
	noPadding bool

	// numericGenre writes a recognized genre as its numeric reference (ID3's TCON)
	// instead of its name, via wl.WithNumericGenre(). It is a write-encoding choice,
	// so - like --force - it resolves in writeOptions() and is shared by plan and set.
	numericGenre bool

	strict bool // promote the unknown-key and single-valued-multi notes to errors
}

// bind registers the edit and write-option flags on cmd.
func (e *editFlags) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringArrayVar(&e.set, "set", nil, "set KEY=VALUE, replacing the key (repeatable)")
	f.StringArrayVar(&e.add, "add", nil, "append KEY=VALUE to a key (repeatable, for multi-value fields)")
	f.StringArrayVar(&e.clear, "clear", nil, "remove KEY (repeatable)")
	f.StringArrayVar(&e.addCover, "add-cover", nil, "add a front-cover picture from an image file, replacing any existing front cover (shorthand for --add-picture front-cover=PATH; repeatable)")
	f.StringArrayVar(&e.addPicture, "add-picture", nil, "add a picture ROLE=PATH, e.g. back-cover=back.jpg (repeatable; ROLE is a cover-art role such as front-cover, back-cover, artist)")
	f.StringVar(&e.pictureDescription, "picture-description", "", "set the description on every picture added this run (--add-picture/--add-cover)")
	f.StringArrayVar(&e.removePicture, "remove-picture", nil, "remove pictures by role name or 1-based dump index, e.g. back-cover or 2 (repeatable; removals apply before adds)")
	f.BoolVar(&e.rmPics, "remove-pictures", false, "remove all embedded pictures")
	f.BoolVar(&e.force, "force", false, "embed --add-cover/--add-picture input even if it is not a recognized image (PNG/JPEG/GIF/WebP/BMP/TIFF); unrecognized bytes are stored as application/octet-stream. The check is header-only, not a full image decode")
	f.StringArrayVar(&e.addChapter, "add-chapter", nil, "add a chapter TIMESTAMP=Title (e.g. 1:30=Verse; repeatable); a file whose format cannot store chapters fails while capable files proceed. CLI-created chapters have no end time, so rewriting Matroska chapters this way drops explicit end times")
	f.BoolVar(&e.clearChapters, "clear-chapters", false, "remove all chapters (applied before --add-chapter, so combining them keeps only the added chapters)")
	f.BoolVar(&e.stripEncoder, "strip-encoder", false, "clear the ENCODER software stamp left behind by an encoder or transcoder")
	f.StringVar(&e.preset, "preset", "", "write policy preset: preserve|compatible|minimal")
	f.StringVar(&e.legacy, "legacy", "", "legacy-tag policy: preserve|strip")
	f.StringVar(&e.padding, "padding", "", "reserve at least N bytes of padding after the metadata (FLAC default 8192; MP3/AAC/MP4 reuse the existing region; 0 writes none, like --no-padding)")
	f.BoolVar(&e.noPadding, "no-padding", false, "write no padding after the metadata (no effect on Ogg/WAV/AIFF/Matroska, which have no padding region)")
	f.BoolVar(&e.numericGenre, "numeric-genre", false, "write a recognized genre as its numeric reference where the format supports one (ID3's TCON); by default the canonical genre name is written")
	f.BoolVar(&e.strict, "strict", false, "fail (exit 2) on an unknown key or a single-valued key given multiple values, instead of noting it")
}

// nonEditFlags are the set/output flags that do not by themselves constitute an
// edit; every other flag bound on set (the tag/picture/chapter/write-shaping ones)
// does. Listing the stable destination/output side rather than re-enumerating the
// editing flags means a newly-added editing flag counts as an edit automatically, so
// editFlagsEmpty cannot silently reject `set f --new-edit-flag` as "no edits given".
var nonEditFlags = map[string]bool{
	"output": true, "overwrite": true, "verify": true, "preserve-mtime": true,
	"recursive": true, "quiet": true, "json": true,
	"force": true, "picture-description": true, "strict": true,
}

// editFlagsEmpty reports whether the invocation requested no edit at all - only
// non-edit flags (or none) were set. set treats an in-place run in this state as a
// usage error (a no-op rewrite is almost always a forgotten flag), while with -o it is
// a deliberate verbatim copy. It reads the parsed flag set (Visit walks only the flags
// actually changed), so it tracks the bound flags rather than a hand-listed field set
// that could rot as edit flags are added.
func editFlagsEmpty(cmd *cobra.Command) bool {
	empty := true
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if !nonEditFlags[f.Name] {
			empty = false
		}
	})
	return empty
}

// quotingHint detects the unquoted-spaces symptom and returns the advisory hint text
// plus whether it applies: value-bearing flags (--set/--add) supplied alongside a
// stray bare-word positional that does not resolve yet sits beside a real input - the
// classic `--set TITLE=Two Words` that leaves the file plus a stray `Words` positional.
// Requiring a resolved sibling avoids a false positive on a lone, simply-missing
// extensionless filename, which on its own is indistinguishable from a split value
// fragment. It is the single detector shared by plan (which prints the hint as an
// advisory) and set (which refuses the whole run up front so no truncated tag is
// written), so the two cannot disagree on when the pattern holds.
func quotingHint(ef *editFlags, realOf func(string) string, args []string) (hint string, ok bool) {
	if len(ef.set) == 0 && len(ef.add) == 0 {
		return "", false
	}
	// Cheap string-only pre-pass: a hint is only possible if some positional is a stray
	// bare word. Most invocations name extension-bearing files, so this returns before
	// any stat - the per-file loop's own stats are not duplicated on the common path.
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
	// A bare word only signals an unquoted value when it does not itself resolve yet sits
	// beside a real input; stat to classify (only now that a candidate exists).
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
			hasRealInput = true // a present input (incl. an existing extensionless file)
		case looksLikeBareWord(a):
			hasStrayWord = true // a missing bare word: a likely split value fragment
		}
	}
	if hasRealInput && hasStrayWord {
		return "a value containing spaces must be quoted, e.g. --set 'TITLE=Two Words'", true
	}
	return "", false
}

// refuseUnquotedValue refuses a run whose value flag carries an unquoted spaced value
// (--set TITLE=Two Words) - detected by quotingHint as a stray bare-word positional
// beside a real input. Both set and plan refuse up front (exit 2) so a partial write
// or a misleading preview never happens; writes selects set's "; nothing was written"
// suffix, which would be false for plan (it never writes). Returns nil when there is
// no stray word. It is the single refusal both commands call, so their wording and
// exit code stay in lockstep.
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

// rejectEmptyScalarFlags rejects --preset, --legacy, or --padding given an
// explicitly empty value, which is otherwise indistinguishable from unset and
// silently ignored - matching how an unknown value (--preset bogus) is rejected,
// and keeping the three scalar write-shaping flags consistent. It reads Changed
// from the command, so set and plan (which both bind these via editFlags) share
// one check.
func rejectEmptyScalarFlags(cmd *cobra.Command) error {
	for _, name := range []string{"preset", "legacy", "padding"} {
		if cmd.Flags().Changed(name) {
			if v, _ := cmd.Flags().GetString(name); v == "" {
				return usagef("--%s cannot be empty", name)
			}
		}
	}
	return nil
}

// patch compiles -set/-add/-clear into a presence-aware patch. A malformed
// assignment or key is a usage error, as is the same key given to both a write
// (--set/--add) and a removal (--clear/--strip-encoder): the CLI compiles all sets,
// then adds, then clears, so --clear would silently win regardless of typed order -
// refuse the contradiction up front (exit 2, nothing written) rather than guess.
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
	// All --set/--add are recorded above, so p.Writes reads the "written by set/add"
	// set straight from the ops - a later removal of the same key is a conflict.
	// set+add on one key (--set ARTIST=A --add ARTIST=B) is legal and stays so; only
	// (set|add) vs clear contradicts.
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
		// Sugar for --clear ENCODER: the canonical software stamp on every format. Name
		// the flag the user actually typed in the conflict message, not a --clear they
		// never wrote.
		if p.Writes(tag.Encoder) {
			return p, usagef("%s is given to both --set/--add and --strip-encoder; remove one (they conflict)", tag.Encoder)
		}
		p.Clear(tag.Encoder)
	}
	return p, nil
}

// splitAssign parses a "KEY=VALUE" assignment. The key is normalized, alias-resolved,
// and validated; everything after the first '=' is the (possibly empty) value, so a
// value may itself contain '='.
func splitAssign(s string) (tag.Key, string, error) {
	i := strings.IndexByte(s, '=')
	if i < 0 {
		return "", "", usagef("missing '=' in %q (want KEY=VALUE; use --clear to remove a key)", s)
	}
	k, err := parseEditKey(strings.TrimSpace(s[:i]))
	if err != nil {
		return "", "", &usageError{msg: err.Error()}
	}
	return k, s[i+1:], nil
}

// parseEditKey validates a user-supplied tag key and resolves recognized aliases
// (DATE -> RECORDINGDATE, TOTALTRACKS -> TRACKTOTAL, ...) to their canonical keys.
// All CLI key entry points use it, so an alias replaces the real field instead of
// adding a duplicate custom field and bypassing the unknown-key guardrails.
func parseEditKey(s string) (tag.Key, error) {
	k, err := tag.ParseKey(s)
	if err != nil {
		return "", err
	}
	return wl.ResolveAlias(k), nil
}

// loadPictures reads and validates every --add-cover and --add-picture file once,
// returning the pictures to add to each edited file. --add-cover PATH is sugar for
// --add-picture front-cover=PATH (back-compat). Validating here - before any file is
// touched - means a bad input is reported once for the whole invocation rather than
// once per file in a bulk run. A --picture-description, if given, is applied to every
// picture added this run, and is a usage error with nothing to attach to.
func (e *editFlags) loadPictures() ([]wl.Picture, error) {
	var pics []wl.Picture
	for _, path := range e.addCover {
		p, err := e.loadPictureFile("cover image", wl.PicFrontCover, path)
		if err != nil {
			return nil, err
		}
		pics = append(pics, p)
	}
	for _, spec := range e.addPicture {
		role, path, ok := strings.Cut(spec, "=")
		if !ok {
			return nil, usagef("--add-picture wants ROLE=PATH, got %q", spec)
		}
		pt, ok := pictureRole(role) // pictureRole trims/lowercases the role itself
		if !ok {
			return nil, usagef("unknown picture role %q; valid roles: %s", strings.TrimSpace(role), pictureRoleList())
		}
		// The path is used verbatim (not trimmed), matching --add-cover and the
		// --set KEY=VALUE convention where the value after '=' is taken as-is - so a
		// path with a leading/trailing space stays selectable.
		p, err := e.loadPictureFile("picture image", pt, path)
		if err != nil {
			return nil, err
		}
		pics = append(pics, p)
	}
	if e.pictureDescription != "" {
		if len(pics) == 0 {
			return nil, usagef("--picture-description needs at least one --add-picture or --add-cover")
		}
		for i := range pics {
			pics[i].Description = e.pictureDescription
		}
	}
	return pics, nil
}

// loadPictureFile reads one image file and turns it into a picture of role pt. label
// ("cover image" / "picture image") prefixes its errors so the message names which
// flag failed. A directory or other non-regular source is rejected as a usage error
// (exit 2) before the read, so a mis-pointed flag fails like other bad inputs; a
// genuinely missing file falls through to os.ReadFile, classified as io (exit 6). A
// 0-byte file is refused even under --force (no legitimate image is empty - always a
// mistake), distinct from non-empty unsniffable bytes, which --force still embeds. The
// picture is sniffed on load so its MIME and dimensions are filled for the plan's
// added-picture detail (C4a); Editor.AddPicture re-sniffs idempotently.
func (e *editFlags) loadPictureFile(label string, pt wl.PictureType, path string) (wl.Picture, error) {
	// A picture source is read with os.ReadFile and has no "-" stdin path, so the
	// non-regular hint must not suggest one (acceptsStdin false).
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
		return wl.Picture{}, usagef("%s: %s: not a recognized image (PNG/JPEG/GIF/WebP/BMP/TIFF); use --force to embed anyway", label, path)
	}
	p := wl.Picture{Type: pt, Data: data}
	p.SniffInto()
	return p, nil
}

// pictureLoadError reports a failure to read an --add-cover/--add-picture image
// file. It renders "<label>: <path>: <reason>" - the flag context, the path, and the
// bare cause - dropping Go's "open" verb (and the path os.ReadFile's *fs.PathError
// repeats) that a plain fmt.Errorf would surface. It Unwraps to that *fs.PathError so
// the failure still classifies as a local I/O error (exit 6); a naive
// fmt.Errorf("%s: %s", ...) reformat would instead drop the error from the chain and
// downgrade a missing cover to the generic exit 1. It mirrors tempCreateError's
// shape (a wrapper that cleans the message while preserving the I/O classification).
type pictureLoadError struct {
	label string
	path  string
	err   error // the os.ReadFile failure, normally an *fs.PathError
}

func (e *pictureLoadError) Error() string {
	// perFileReason single-sources the "*fs.PathError -> bare cause" rule (dropping Go's
	// "open" verb and the path os.ReadFile's PathError repeats); this only frames it with
	// the flag label and the path.
	return fmt.Sprintf("%s: %s: %s", e.label, e.path, perFileReason(e.err))
}

func (e *pictureLoadError) Unwrap() error { return e.err }

// pictureRoles maps each cover-art role name to its PictureType. The name is
// PictureType.String() lowercased with spaces turned to hyphens, derived from the
// enum itself (the same derive-from-source pattern as the key vocabulary) so it
// tracks PictureType automatically and disambiguates lead-artist (PicLeadArtist)
// from artist (PicArtist), which a hand-list would blur.
var pictureRoles = func() map[string]wl.PictureType {
	m := map[string]wl.PictureType{}
	for i := 0; i < 256; i++ {
		p := wl.PictureType(i)
		name := p.String()
		if name == "reserved" {
			break // past the last defined role (String returns "reserved")
		}
		m[strings.ReplaceAll(strings.ToLower(name), " ", "-")] = p
	}
	return m
}()

// pictureRole resolves a role name (case-insensitive, whitespace-trimmed) to its
// PictureType.
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

// resolveRemovals turns the --remove-picture selectors into the set of picture
// indices (into pics, which is the file's pictures in dump order) to remove. A
// selector is a 1-based dump index or a cover-art role name; an out-of-range index
// or an unrecognized role is a usage error. A role that matches no picture is a
// no-op (so a bulk "remove every back cover" does not fail on a file without one),
// not an error. The returned map drives a shift-proof closure in prepare.
func resolveRemovals(selectors []string, pics []wl.Picture) (map[int]bool, error) {
	targets := map[int]bool{}
	for _, sel := range selectors {
		s := strings.TrimSpace(sel)
		if n, err := strconv.Atoi(s); err == nil {
			if n < 1 || n > len(pics) {
				return nil, usagef("--remove-picture index %d is out of range (file has %d picture(s))", n, len(pics))
			}
			targets[n-1] = true
			continue
		}
		pt, ok := pictureRole(s)
		if !ok {
			return nil, usagef("--remove-picture wants a role name or a 1-based index, got %q; valid roles: %s", sel, pictureRoleList())
		}
		for i, p := range pics {
			if p.Type == pt {
				targets[i] = true
			}
		}
	}
	return targets, nil
}

// chapterAdds parses the --add-chapter "TIMESTAMP=Title" assignments into chapters,
// validating every timestamp once - before any file is parsed - so a malformed entry
// is reported a single time for the whole invocation, not once per file in a bulk
// run. A bad assignment is a usage error.
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

// writeOptions resolves -preset, -legacy, and the padding flags into library
// write options, applied in that order so an explicit option overrides the
// preset's. An unknown name or a bad padding value is a usage error.
func (e *editFlags) writeOptions() ([]wl.WriteOption, bool, error) {
	opts, err := resolveWriteFlags(e.preset, e.legacy)
	if err != nil {
		return nil, false, err
	}
	// Append the padding option after the preset/legacy options so an explicit
	// --padding / --no-padding overrides the preset's padding policy (e.g.
	// "--preset minimal --padding 16384" reserves 16 KiB rather than the preset's
	// zero). Padding is editing-command-only, so it lives here rather than in the
	// resolveWriteFlags shared with copy.
	padOpt, padFlag, err := resolvePaddingFlag(e.padding, e.noPadding)
	if err != nil {
		return nil, false, err
	}
	if padOpt != nil {
		opts = append(opts, padOpt)
	}
	// --force embeds a picture the image sniff does not recognize, so opt the library's
	// added-picture validation out to match: without it, Prepare would reject the
	// exotic image loadPictureFile just waved through. loadPictureFile still pre-checks
	// the common mistake (a non-image file) for a friendly exit-2 message before any
	// file is touched, and refuses a 0-byte file even here; this only affects the
	// --force path.
	if e.force {
		opts = append(opts, wl.WithUnrecognizedPictures())
	}
	// --numeric-genre writes a recognized genre as its numeric reference (ID3's
	// TCON) instead of the name. Resolved here beside --force so both plan and set
	// pick it up through compile() without per-caller wiring.
	if e.numericGenre {
		opts = append(opts, wl.WithNumericGenre())
	}
	return opts, padFlag, nil
}

// maxPaddingBytes caps an explicit --padding value. Cover art runs KB-MB, and the
// padding floor makes a plain edit able to pre-reserve the requested region (the
// MP3/AAC reuse -> ClampTarget path is otherwise unbounded under Max 0), so an
// absurd value would allocate a multi-gigabyte metadata region. 64 MiB is a
// generous ceiling, far above any real cover.
const maxPaddingBytes = 64 << 20

// resolvePaddingFlag turns the --padding/--no-padding values into a write option
// (nil when neither is set, leaving the default 8 KiB policy in place) plus whether
// a padding flag was given at all (which the per-format applicability note reads -
// only the given-or-not distinction matters, since every format that honors padding
// honors it for any spelling). An explicit byte count must be a non-negative integer
// no larger than maxPaddingBytes; each violation is a usage error.
//
// The value is parsed once, so every spelling of zero ("0", "00", " 0 ") behaves
// identically: --padding 0 is a synonym for --no-padding, and the two conflict only
// when --padding asks for a *positive* amount. A naive string "!= 0" test would
// wrongly reject "00" or " 0 " alongside --no-padding.
//
// --no-padding / --padding 0 writes none (Target 0, Max 0). A positive --padding N
// is a floor: it reserves at least N bytes, so it sets Min=N as well as Target=N -
// a rewrite grows a too-small region up to N instead of reusing it (without Min,
// the reuse branch would keep the smaller existing region and silently ignore N).
// Max stays 0 (no policy ceiling); the maxPaddingBytes usage cap and each format's
// hard cap are the actual upper bounds.
func resolvePaddingFlag(padding string, noPadding bool) (opt wl.WriteOption, flagGiven bool, err error) {
	var value int64
	hasValue := false
	// Gate on the raw value, not the trimmed one: an empty "" means the flag was not
	// given (the default sentinel), but a whitespace-only "   " WAS given and is not a
	// valid byte count, so it must fail the parse below rather than silently default.
	if padding != "" {
		v, perr := strconv.ParseInt(strings.TrimSpace(padding), 10, 64)
		if perr != nil || v < 0 {
			return nil, false, usagef("--padding wants a non-negative byte count, got %q", padding)
		}
		if v > maxPaddingBytes {
			return nil, false, usagef("--padding %d is too large (max %d bytes, 64 MiB)", v, maxPaddingBytes)
		}
		value, hasValue = v, true
	}
	switch {
	case !noPadding && !hasValue:
		return nil, false, nil // neither flag: keep the default policy
	case noPadding && value > 0:
		// value > 0 implies --padding was given (value defaults to 0); the two contradict
		// only for a positive amount, so every spelling of zero falls through and agrees.
		return nil, false, usagef("--padding and --no-padding cannot be combined")
	case value > 0:
		return wl.WithPadding(wl.PaddingPolicy{Target: value, Min: value, Max: 0, ReuseInPlace: true}), true, nil
	default:
		// --no-padding, or --padding 0 in any spelling: write no padding. A floor policy
		// with Min 0 would instead reuse an existing region rather than dropping it, so
		// "0" would not shrink the file as a user reasonably expects.
		return wl.WithPadding(wl.PaddingPolicy{Target: 0, Max: 0}), true, nil
	}
}

// resolveWriteFlags turns the shared -preset/-legacy flag values into library
// write options, applied in that order so an explicit -legacy overrides the
// preset's legacy policy. It is shared by the edit commands (plan/set) and copy
// so they parse these flags identically. An unknown name is a usage error.
func resolveWriteFlags(preset, legacy string) ([]wl.WriteOption, error) {
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

// compiledEdit holds the invocation-level edit inputs resolved once: the tag
// patch, the write options (edit options plus any save-only extras), and the
// validated pictures to add. A bulk run compiles these a single time, then applies
// them to each file via prepare, so flag and picture validation happens once - not
// once per file.
type compiledEdit struct {
	patch         tag.TagPatch
	opts          []wl.WriteOption
	addPics       []wl.Picture // --add-cover/--add-picture pictures, validated and sniffed at compile time
	replaceFront  bool         // --add-cover was used: replace any existing front cover (not --add-picture front-cover, which appends)
	removePics    []string     // --remove-picture selectors (role name or 1-based index), resolved per file
	rmPics        bool         // --remove-pictures (all)
	chapters      []wl.Chapter // --add-chapter additions, validated at compile time
	clearChapters bool         // --clear-chapters
	unknownKeys   []tag.Key    // --set/--add keys outside the canonical vocabulary, first-seen order
	clearKeys     []tag.Key    // --clear keys outside the canonical vocabulary, first-seen order
	paddingFlag   bool         // whether --padding/--no-padding was given, for the per-format note
}

// compile resolves the edit flags into a compiledEdit, surfacing any usage error
// in the flags (bad --set, unknown preset/legacy, or a rejected cover) before any
// file is parsed. extra carries save-only options (verify, preserve-mtime).
func (e *editFlags) compile(extra ...wl.WriteOption) (*compiledEdit, error) {
	opts, padFlag, err := e.writeOptions()
	if err != nil {
		return nil, err
	}
	opts = append(opts, extra...)
	patch, err := e.patch()
	if err != nil {
		return nil, err
	}
	// When the edit touches ENCODER (--set/--clear/--add ENCODER, or --strip-encoder,
	// which all land as an op on the key), also strip a removable inherited encoder
	// stamp held in a native field no canonical edit reaches - the WAV ISFT. This
	// drops the transcoder leftover and prevents a split-brain (a fresh id3 ENCODER
	// beside a surviving ISFT=Lavf); codecs without such a stamp ignore the option.
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
	return &compiledEdit{
		patch:         patch,
		opts:          opts,
		addPics:       addPics,
		replaceFront:  len(e.addCover) > 0, // only --add-cover replaces; --add-picture front-cover appends
		removePics:    e.removePicture,
		rmPics:        e.rmPics,
		chapters:      chapters,
		clearChapters: e.clearChapters,
		unknownKeys:   e.unknownAssignKeys(),
		clearKeys:     e.unknownClearKeys(),
		paddingFlag:   padFlag,
	}, nil
}

// unknownAssignKeys returns the --set/--add keys outside the published canonical
// vocabulary, in first-seen order with no duplicates. These are written as custom
// fields, which a command surfaces as a note (or, under --strict, an error);
// --clear is exempt, since clearing a stray key is harmless. The raw assignments
// have already been validated by patch(), so a re-parse cannot fail here.
func (e *editFlags) unknownAssignKeys() []tag.Key {
	var keys []tag.Key
	for _, kv := range slices.Concat(e.set, e.add) {
		if k, _, err := splitAssign(kv); err == nil {
			keys = append(keys, k)
		}
	}
	return dedupUnknownKeys(keys)
}

// unknownClearKeys returns the --clear keys outside the published canonical
// vocabulary, in first-seen order with no duplicates. Clearing such a key affects
// only a custom field of that exact name, so a typo'd --clear (e.g. ARTIS) is a
// silent no-op on a file that has no such field - a command surfaces these as a
// text note. --strip-encoder is exempt (it clears the canonical ENCODER). The
// raw values were already validated by patch(), so a re-parse cannot fail here.
func (e *editFlags) unknownClearKeys() []tag.Key {
	var keys []tag.Key
	for _, ks := range e.clear {
		if k, err := parseEditKey(strings.TrimSpace(ks)); err == nil {
			keys = append(keys, k)
		}
	}
	return dedupUnknownKeys(keys)
}

// dedupUnknownKeys returns the keys outside the published canonical vocabulary, in
// first-seen order with no duplicates. It is the shared filter behind the
// unknown-assign and unknown-clear notes, so the "is it unknown / already seen" rule
// cannot drift between them.
func dedupUnknownKeys(keys []tag.Key) []tag.Key {
	var out []tag.Key
	seen := map[tag.Key]bool{}
	for _, k := range keys {
		if k.Known() || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

// anyInputExists reports whether at least one of paths names something this
// invocation can act on - the "-" stdin sentinel always counts (its bytes were
// buffered before the loop). set and plan use it to hold the cosmetic
// invocation-level notes (the non-strict unknown-key notes and the value notes)
// until there is a real file to act on, so a single missing file is reported as
// not-found rather than first lectured about its keys. It stats realOf(p): the
// buffered temp path for "-", the path itself otherwise. Any stat failure - not
// only not-exist, but e.g. an unsearchable parent dir - counts as "nothing to act
// on here": a path that cannot be stat'd cannot be parsed either, so the per-file
// loop will surface the real error, and a cosmetic note is better withheld than
// printed before that inevitable failure. The --strict guardrail is deliberately
// not gated on this - a strict-key misuse is a usage error checked upfront,
// independent of the file.
//
// A path expandPaths recorded as a pre-flight failure (a directory without
// --recursive, or a directly-named FIFO/device) is skipped: it is not an actionable
// input - the per-file loop turns it into a per-element error and writes nothing - so
// a directory-only run must not print a value note that claims the value was written.
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

// notifyInvocationNotes emits the invocation-level guardrails and notes shared by
// set and plan, once there is at least one path to act on. The --strict unknown-key
// guardrail fires regardless of whether any input exists, so its exit-2 misuse
// error stays independent of the file (set nope.flac --strict --set BOGUS=1 is exit
// 2, not 6); notifyUnknownKeys prints nothing under --strict, so the strict-but-
// absent path is note-free. The cosmetic notes (the non-strict unknown-key notes
// and the value notes) wait until an input actually exists, so a lone missing file
// is not lectured about its key before the not-found error. Centralizing this
// keeps the strict-vs-exists policy single-sourced across set and plan.
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

// guardrailKeys applies the policy shared by every key-based edit guardrail: no
// keys is a pass; under strict the keys are a usage error (built by strictErr);
// under --json the keys are suppressed (a note would corrupt the machine stream);
// otherwise they are returned for the caller to note on stderr (applying any
// per-key dedup itself). Centralizing the strict/JSON/empty branches keeps the
// guardrails - unknown-key and single-valued-multi - from drifting on policy.
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

// notifyUnknownKeys handles the invocation-level unknown-key guardrail once,
// before any file is processed: with strict it is a usage error (exit 2, nothing
// touched); otherwise each unknown --set/--add key is noted on stderr. Notes are
// text-only and suppressed under --json (where they would corrupt the machine
// stream); the strict error flows through the normal json-aware error path.
func notifyUnknownKeys(errOut io.Writer, ce *compiledEdit, strict, asJSON bool) error {
	note, err := guardrailKeys(ce.unknownKeys, strict, asJSON, func(ks []tag.Key) error {
		return usagef("unknown key(s) not in the canonical vocabulary: %s (omit --strict to write them as custom fields)", keyList(ks))
	})
	for _, k := range note {
		fmt.Fprintf(errOut, "note: %s is not a known key; written as a custom field%s\n", k, didYouMean(k))
	}
	// One trailing hint after the per-key lines (not per key, so five unknown keys do
	// not repeat it five times) points at the discovery command for the canonical
	// vocabulary. Only when at least one key was actually noted.
	if len(note) > 0 {
		fmt.Fprintln(errOut, "note: run 'waxlabel keys' to list the canonical vocabulary")
	}
	return err
}

// didYouMean returns a "; did you mean KEY?" suffix when an unknown key is a near
// miss for a canonical one ([tag.ClosestKey]), or "" when nothing is close enough.
// Shared by the unknown-assign-key note and the unknown-clear-key note so both
// phrase the suggestion identically.
func didYouMean(k tag.Key) string {
	if s, ok := tag.ClosestKey(string(k)); ok {
		return fmt.Sprintf("; did you mean %s?", s)
	}
	return ""
}

// notifyClearKeys notes each --clear key outside the canonical vocabulary:
// clearing it affects only a custom field of that exact name, so a typo'd --clear
// (e.g. ARTIS) is otherwise a silent no-op on a file that has no such field. The
// note is text-only and suppressed under --json, and - unlike the unknown-assign
// guardrail - is never escalated under --strict (clearing a stray key is harmless).
// It carries the same "did you mean?" suggestion as the assign note.
func notifyClearKeys(errOut io.Writer, ce *compiledEdit, asJSON bool) {
	if asJSON {
		return
	}
	for _, k := range ce.clearKeys {
		fmt.Fprintf(errOut, "note: %s is not a known key (clearing affects only a custom field of that exact name)%s\n", k, didYouMean(k))
	}
}

// notifyValueNotes emits the invocation-level, text-only advisory notes about the
// user's --set/--add values: a malformed numeric or date value and a
// present-but-empty --set value. Both are pure notes - the value is written
// faithfully either way - so they are suppressed under --json (where they would
// corrupt the machine stream) and, unlike the unknown-key and cardinality
// guardrails, are never escalated to an error under --strict. Notes are emitted in
// command-line order: the --set assignments, then the --add ones.
func notifyValueNotes(errOut io.Writer, e *editFlags, asJSON bool) {
	if asJSON {
		return
	}
	for _, kv := range e.set {
		k, v, err := splitAssign(kv)
		if err != nil {
			continue // a malformed assignment is already reported by patch()
		}
		// Match the writer's numeric trim so the advisory describes the stored value.
		// A padded number is checked in its trimmed form, and whitespace-only input uses
		// the empty-value note below instead of a misleading malformed-value note.
		v = tag.TrimNumericValue(k, v)
		if v == "" {
			// A present-but-empty --set value, distinct from --clear (which removes the
			// key). No file has been inspected yet, so the note cannot promise a specific
			// format outcome: some formats store the empty value and some drop an empty
			// field. The typed check skips empties, so a bare KEY= never double-notes.
			fmt.Fprintf(errOut, "note: %s= writes an empty value (some formats may drop an empty field rather than store it); use --clear %s to remove it\n", k, k)
			continue
		}
		noteMalformedValue(errOut, k, v)
	}
	for _, kv := range e.add {
		// An empty --add value is not the same as an empty --set value: --clear is a
		// replacement operation, not an append. Numeric trimming still mirrors the writer.
		if k, v, err := splitAssign(kv); err == nil {
			if v = tag.TrimNumericValue(k, v); v != "" {
				noteMalformedValue(errOut, k, v)
			}
		}
	}
}

// noteMalformedValue emits the advisory for values that do not match the typed shape of
// their key category. This is emitted before any file is parsed, so
// it cannot know the target codec: most formats keep the value as text, but a
// format-specific encoding may still drop it (e.g. an ID3v2.3 date with no numeric
// year, which has no TYER/TORY representation) - so the note no longer promises
// unconditional persistence, and the per-file value-dropped warning is the
// authoritative drop signal. It reads the same [tag.ValidatorFor] registry
// [Document.Lint] consumes, so the set-time note and the linter cannot disagree on what
// a malformed value is: numeric, date, boolean, MEDIATYPE, and ReplayGain. The
// note is a single line, so the key and value are run through [tag.SanitizeLine] - they
// are the user's own --set input, but a control byte must not reach the terminal raw
// and an embedded newline must not forge a line. (SanitizeLine, not SanitizeText, to
// match the single-line convention.)
func noteMalformedValue(errOut io.Writer, k tag.Key, v string) {
	ks, vs := tag.SanitizeLine(string(k)), tag.SanitizeLine(v)
	if val, ok := tag.ValidatorFor(k); ok && !val.Valid(k, v) {
		fmt.Fprintf(errOut, "note: %s=%s %s; kept as text where the format supports it\n", ks, vs, val.NoteDetail)
		return
	}
	// A numeric value that parses but is negative round-trips faithfully, so it is not
	// "malformed" - but a negative number/total or play count is semantically odd, so it
	// gets a separate advisory (the value is still written).
	if tag.IsNumericKey(k) && tag.NegativeNumericValue(k, v) {
		fmt.Fprintf(errOut, "note: %s=%s is negative; written as-is (numbering is normally non-negative)\n", ks, vs)
	}
}

// strictEscalatingCodes are the per-file plan warnings --strict promotes to a usage
// error (exit 2): a value the codec would drop as unrepresentable, and a known
// single-valued key left holding multiple values.
// Both are library warnings the plan report already carries (and the human/JSON output
// already renders), so the gate reads that one signal rather than re-deriving the rule
// from plan.Changes() - so the warning the user sees and the --strict decision cannot
// disagree. A result-based re-derivation went silent in the Matroska single-valued case,
// which is why this reads the plan warnings directly. The unknown-key strict path
// stays separate (notifyUnknownKeys): an unknown key is a CLI-vocabulary concept the
// library accepts by design, so it is a pre-flight usage error, not a plan warning.
var strictEscalatingCodes = map[wl.WarningCode]bool{
	wl.WarnValueDropped:      true,
	wl.WarnSingleValuedMulti: true,
}

// strictWarningGate applies the per-file --strict escalation for plan and set: when a
// plan carries an escalating warning, strict fails that file at exit 2, naming the
// offending key(s) and the reason. Off --strict it is a no-op - the plan report already
// carries the warning for the human and JSON output, so a non-strict run just shows it
// and proceeds. It is the per-file counterpart to the invocation-level unknown-key
// guardrail (notifyUnknownKeys), and it returns a per-file usage error (not an
// invocation abort) so a multi-file run's aggregate exit stays order-independent.
type strictWarningGate struct {
	strict bool
}

func newStrictWarningGate(strict bool) *strictWarningGate {
	return &strictWarningGate{strict: strict}
}

// check returns a usage error when strict is on and the plan carries an escalating
// warning, so the caller fails that file (exit 2); otherwise nil. The keys come from
// the structured [wl.Warning.Keys] the library populates, so the gate names them
// without parsing the prose message.
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
	return usagef("%s (omit --strict to write anyway)", strings.Join(reasons, "; "))
}

// strictWarningReason renders one escalating warning as "key(s): reason" for the
// --strict error, keyed off the warning code so the wording stays close to the
// plan-body warning the user also sees.
func strictWarningReason(w wl.Warning) string {
	keys := keyList(w.Keys)
	if keys == "" {
		// Every escalating warning is built with its key(s) (WarnKeyed), so this only
		// guards a malformed keyless warning: render the warning's own prose (which
		// should name the key) rather than a message with a leading bare colon.
		return w.Message
	}
	switch w.Code {
	case wl.WarnValueDropped:
		return fmt.Sprintf("%s: value cannot be represented in this format and would be dropped", keys)
	case wl.WarnSingleValuedMulti:
		return fmt.Sprintf("%s: single-valued but given multiple values", keys)
	default:
		return keys
	}
}

// paddingNoter emits the per-format note that a --padding/--no-padding flag does
// not apply to a file's format, deduped so a bulk run reports each distinct format
// once instead of once per file. It is text-only (suppressed under --json, where a
// note would corrupt the machine stream) and never an error - the flag is simply a
// no-op on that format. set and plan share it, keyed off the format's
// Capabilities.Padding level, so their guidance cannot drift. The caller gates
// note() on whether a padding flag was even given (ce.paddingFlag), so the format's
// Capabilities are not built when no flag is present (the common case).
type paddingNoter struct {
	asJSON bool
	errOut io.Writer
	seen   map[wl.Format]bool
}

func newPaddingNoter(asJSON bool, errOut io.Writer) *paddingNoter {
	return &paddingNoter{asJSON: asJSON, errOut: errOut, seen: map[wl.Format]bool{}}
}

// note emits the padding note for caps's format, at most once per format, only when
// the format has no padding concept at all (AccessNone: Ogg/WAV/AIFF/Matroska) so the
// flag genuinely does nothing. AccessFull (FLAC) and AccessPartial (the front-tag
// codecs MP3/AAC and MP4) both honor the flags - on Partial the effect depends on
// whether the edit reuses the existing tag region in place, which is too nuanced for a
// one-line note and was previously stated wrongly (MP3/AAC --no-padding does shrink),
// so those get no note; caps and the README document the per-format detail.
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

// keyList renders keys as a comma-separated string for a one-line message.
func keyList(keys []tag.Key) string {
	s := make([]string, len(keys))
	for i, k := range keys {
		s[i] = string(k)
	}
	return strings.Join(s, ", ")
}

// prepare parses the file at realPath (reported under origPath's display name, so
// a buffered-stdin temp path never leaks into a parse error), applies the
// compiled edit, and resolves the write plan. Prepare performs no I/O beyond the
// parse, so plan and set share this without writing anything. Picture removals
// happen before adds so "--remove-picture front-cover --add-cover x" replaces the
// cover (and "--remove-pictures --add-cover x" yields just the new cover); an
// --add-cover additionally clears any pre-existing front cover on its own, so
// adding a cover always replaces rather than duplicating.
func (ce *compiledEdit) prepare(ctx context.Context, realPath, origPath string) (*wl.Document, *wl.Plan, error) {
	doc, err := parseInput(ctx, realPath, origPath)
	if err != nil {
		return nil, nil, err
	}
	ed := doc.Edit().Apply(ce.patch)
	if ce.rmPics {
		ed.ClearPictures()
	}
	// Selective --remove-picture: resolve the selectors against the file's pictures in
	// dump order, then remove by index with a stateful closure. Editor.RemovePictures
	// evaluates the match once per picture in order, so the running counter stays
	// aligned with doc.Pictures() and a removal cannot shift the indices of later
	// pictures out from under the selectors.
	if len(ce.removePics) > 0 {
		targets, err := resolveRemovals(ce.removePics, doc.Pictures())
		if err != nil {
			return nil, nil, err
		}
		i := -1
		ed.RemovePictures(func(wl.Picture) bool {
			i++
			return targets[i]
		})
	}
	// An --add-cover replaces any existing front cover rather than appending a
	// duplicate. Clear pre-existing front covers first so the common case leaves
	// exactly one front cover and does not warn about multiple front covers.
	// This is CLI policy only: --add-picture front-cover=... and Editor.AddPicture
	// both remain append operations for callers that deliberately want more than
	// one front cover.
	if ce.replaceFront {
		ed.RemovePictures(func(p wl.Picture) bool { return p.Type == wl.PicFrontCover })
	}
	for _, p := range ce.addPics {
		ed.AddPicture(p)
	}
	// Chapters: --add-chapter appends to the file's existing chapters; --clear-chapters
	// drops them first, so "clear + add" keeps only the added ones (the
	// --remove-pictures --add-cover ordering). --clear-chapters alone empties the list.
	// The library sorts the final list by start time.
	if len(ce.chapters) > 0 {
		base := doc.Chapters()
		if ce.clearChapters {
			base = nil
		}
		// Dedup CLI additions against the accumulated list (existing chapters plus earlier
		// additions), skipping exact Start/End/Title matches. Additions have End == 0, so
		// an on-disk chapter with the same start/title and a real end time is kept as a
		// distinct chapter. The library API still permits callers to set duplicates.
		merged := slices.Clone(base)
		for _, add := range ce.chapters {
			// wl.Chapter is comparable, so slices.Contains is the whole exact-match check.
			if !slices.Contains(merged, add) {
				merged = append(merged, add)
			}
		}
		ed.SetChapters(merged...)
	} else if ce.clearChapters {
		ed.ClearChapters()
	}
	plan, err := ed.Prepare(ce.opts...)
	if err != nil {
		return nil, nil, err
	}
	return doc, plan, nil
}
