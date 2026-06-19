package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/spf13/cobra"
)

// editPrecedenceHelp documents how flags combine for one key. The edits apply in
// a fixed order (set, then add, then clear) regardless of command-line position,
// because each flag's occurrences are collected separately, so this is the rule
// users can rely on.
const editPrecedenceHelp = "For one key, --clear takes precedence over --add and --set (it is applied\n" +
	"last); otherwise --set replaces the key and --add appends to it. Order on the\n" +
	"command line does not change this."

// editFlags holds the tag- and picture-editing options shared by the plan and
// set commands, plus the write-shaping options (preset, legacy policy). It binds
// onto a command's flag set and compiles into a presence-aware [tag.TagPatch], a
// picture mutation, and a list of [wl.WriteOption].
type editFlags struct {
	set      []string // KEY=VALUE, replace
	add      []string // KEY=VALUE, append (multi-value)
	clear    []string // KEY, remove
	addCover []string // image file path, added as a front cover
	rmPics   bool
	force    bool // embed --add-cover input even when it is not a recognized image

	stripEncoder bool // clear the ENCODER software stamp

	preset string
	legacy string

	strict bool // promote the unknown-key and single-valued-multi notes to errors
}

// bind registers the edit and write-option flags on cmd.
func (e *editFlags) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringArrayVar(&e.set, "set", nil, "set KEY=VALUE, replacing the key (repeatable)")
	f.StringArrayVar(&e.add, "add", nil, "append KEY=VALUE to a key (repeatable, for multi-value fields)")
	f.StringArrayVar(&e.clear, "clear", nil, "remove KEY (repeatable)")
	f.StringArrayVar(&e.addCover, "add-cover", nil, "add a front-cover picture from an image file (repeatable)")
	f.BoolVar(&e.rmPics, "remove-pictures", false, "remove all embedded pictures")
	f.BoolVar(&e.force, "force", false, "embed --add-cover input even if it is not a recognized image (PNG/JPEG/GIF/WebP/BMP/TIFF)")
	f.BoolVar(&e.stripEncoder, "strip-encoder", false, "clear the ENCODER software stamp (the transcoder leftover)")
	f.StringVar(&e.preset, "preset", "", "write policy preset: preserve|compatible|canonical|minimal")
	f.StringVar(&e.legacy, "legacy", "", "legacy-tag policy: preserve|strip|reconcile|update-existing")
	f.BoolVar(&e.strict, "strict", false, "fail (exit 2) on an unknown key or a single-valued key given multiple values, instead of noting it")
}

// patch compiles -set/-add/-clear into a presence-aware patch. A malformed
// assignment or key is a usage error.
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
	for _, ks := range e.clear {
		k, err := tag.ParseKey(strings.TrimSpace(ks))
		if err != nil {
			return p, &usageError{msg: err.Error()}
		}
		p.Clear(k)
	}
	if e.stripEncoder {
		// Sugar for --clear ENCODER: the canonical software stamp on every format.
		p.Clear(tag.Encoder)
	}
	return p, nil
}

// splitAssign parses a "KEY=VALUE" assignment. The key is normalized and
// validated; everything after the first '=' is the (possibly empty) value, so a
// value may itself contain '='.
func splitAssign(s string) (tag.Key, string, error) {
	i := strings.IndexByte(s, '=')
	if i < 0 {
		return "", "", usagef("missing '=' in %q (want KEY=VALUE; use --clear to remove a key)", s)
	}
	k, err := tag.ParseKey(strings.TrimSpace(s[:i]))
	if err != nil {
		return "", "", &usageError{msg: err.Error()}
	}
	return k, s[i+1:], nil
}

// loadCovers reads and validates the --add-cover files once, returning the
// front-cover pictures to add to each edited file. Reading a cover file is a
// runtime (not usage) failure; a file that is not a recognized image is rejected
// as a usage error (the common mistake of pointing --add-cover at the wrong
// file), overridable with --force for a deliberate exotic format. Validating here
// - before any file is touched - means a bad cover is reported once for the whole
// invocation rather than once per file in a bulk run.
func (e *editFlags) loadCovers() ([]wl.Picture, error) {
	var pics []wl.Picture
	for _, path := range e.addCover {
		data, err := os.ReadFile(path)
		if err != nil {
			// os.ReadFile's *fs.PathError already names the path, so do not repeat
			// it; just mark that the failure is about a cover image.
			return nil, fmt.Errorf("cover image: %w", err)
		}
		if !e.force && !wl.IsRecognizedImage(data) {
			return nil, usagef("cover image: %s: not a recognized image (PNG/JPEG/GIF/WebP/BMP/TIFF); use --force to embed anyway", path)
		}
		pics = append(pics, wl.Picture{Type: wl.PicFrontCover, Data: data})
	}
	return pics, nil
}

// writeOptions resolves -preset and -legacy into library write options, applied
// in that order so an explicit -legacy overrides the preset's legacy policy. An
// unknown name is a usage error.
func (e *editFlags) writeOptions() ([]wl.WriteOption, error) {
	return resolveWriteFlags(e.preset, e.legacy)
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
			return nil, usagef("unknown preset %q (want preserve|compatible|canonical|minimal)", preset)
		}
		opts = append(opts, opt)
	}
	if legacy != "" {
		pol, ok := legacyOptions[strings.ToLower(legacy)]
		if !ok {
			return nil, usagef("unknown legacy policy %q (want preserve|strip|reconcile|update-existing)", legacy)
		}
		opts = append(opts, wl.WithLegacyPolicy(pol))
	}
	return opts, nil
}

var presetOptions = map[string]wl.WriteOption{
	"preserve":   wl.Preserve,
	"compatible": wl.Compatible,
	"canonical":  wl.Canonical,
	"minimal":    wl.Minimal,
}

var legacyOptions = map[string]wl.LegacyPolicy{
	"preserve":        wl.LegacyPreserve,
	"strip":           wl.LegacyStrip,
	"reconcile":       wl.LegacyReconcile,
	"update-existing": wl.LegacyUpdateExisting,
}

// compiledEdit holds the invocation-level edit inputs resolved once: the tag
// patch, the write options (edit options plus any save-only extras), and the
// validated cover pictures. A bulk run compiles these a single time, then applies
// them to each file via prepare, so flag and cover validation happens once - not
// once per file.
type compiledEdit struct {
	patch       tag.TagPatch
	opts        []wl.WriteOption
	covers      []wl.Picture
	rmPics      bool
	unknownKeys []tag.Key // --set/--add keys outside the canonical vocabulary, first-seen order
}

// compile resolves the edit flags into a compiledEdit, surfacing any usage error
// in the flags (bad --set, unknown preset/legacy, or a rejected cover) before any
// file is parsed. extra carries save-only options (verify, preserve-mtime).
func (e *editFlags) compile(extra ...wl.WriteOption) (*compiledEdit, error) {
	opts, err := e.writeOptions()
	if err != nil {
		return nil, err
	}
	opts = append(opts, extra...)
	patch, err := e.patch()
	if err != nil {
		return nil, err
	}
	covers, err := e.loadCovers()
	if err != nil {
		return nil, err
	}
	return &compiledEdit{patch: patch, opts: opts, covers: covers, rmPics: e.rmPics, unknownKeys: e.unknownAssignKeys()}, nil
}

// unknownAssignKeys returns the --set/--add keys outside the published canonical
// vocabulary, in first-seen order with no duplicates. These are written as custom
// fields, which a command surfaces as a note (or, under --strict, an error);
// --clear is exempt, since clearing a stray key is harmless. The raw assignments
// have already been validated by patch(), so a re-parse cannot fail here.
func (e *editFlags) unknownAssignKeys() []tag.Key {
	var out []tag.Key
	seen := map[tag.Key]bool{}
	for _, kv := range slices.Concat(e.set, e.add) {
		k, _, err := splitAssign(kv)
		if err != nil || k.Known() || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

// guardrailKeys applies the policy shared by every key-based edit guardrail: no
// keys is a pass; under strict the keys are a usage error (built by strictErr);
// under --json the keys are suppressed (a note would corrupt the machine stream);
// otherwise they are returned for the caller to note on stderr (applying any
// per-key dedup itself). Centralizing the strict/JSON/empty branches keeps the
// guardrails - unknown-key and single-valued-multi - from drifting on policy, so a
// future change (e.g. emitting notes as JSON warnings) lands in one place.
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
		fmt.Fprintf(errOut, "note: %s is not a known key; written as a custom field\n", k)
	}
	return err
}

// singleValuedViolations returns the known single-valued keys a plan would store
// with more than one value - the cardinality the writer faithfully preserves but a
// typed reader would collapse to the first value. It reads the plan's field
// changes, so it reflects exactly what saving would write. A custom (unknown) key
// is exempt: it has no typed accessor and no canonical cardinality, so explicitly
// giving it several values is legitimate - it is surfaced by the unknown-key note
// instead, and double-flagging it as single-valued would contradict that.
func singleValuedViolations(plan *wl.Plan) []tag.Key {
	var out []tag.Key
	for _, c := range plan.Changes() {
		if c.Key.SingleValuedMulti(len(c.New)) {
			out = append(out, c.Key)
		}
	}
	return out
}

// singleValuedNotifier applies the per-file single-valued-multi guardrail for plan
// and set, holding the run-wide dedup set so each offending key is reported once
// across every file - a --recursive walk of 500 files notes ENCODER once, not 500
// times. It is the per-file counterpart to notifyUnknownKeys.
type singleValuedNotifier struct {
	strict bool
	asJSON bool
	errOut io.Writer
	noted  map[tag.Key]bool
}

func newSingleValuedNotifier(strict, asJSON bool, errOut io.Writer) *singleValuedNotifier {
	return &singleValuedNotifier{strict: strict, asJSON: asJSON, errOut: errOut, noted: map[tag.Key]bool{}}
}

// check inspects one file's plan: under strict a violation is a usage error (so
// the caller fails that file, exit 2); otherwise each newly-seen offending key is
// noted on stderr (text-only, suppressed in JSON). It returns nil when the plan is
// within cardinality, or after only printing notes.
func (n *singleValuedNotifier) check(plan *wl.Plan) error {
	note, err := guardrailKeys(singleValuedViolations(plan), n.strict, n.asJSON, func(ks []tag.Key) error {
		return usagef("%s is single-valued but given multiple values (omit --strict to write them anyway)", keyList(ks))
	})
	for _, k := range note {
		if !n.noted[k] {
			n.noted[k] = true
			fmt.Fprintf(n.errOut, "note: %s is single-valued but is being given multiple values\n", k)
		}
	}
	return err
}

// keyList renders keys as a comma-separated string for a one-line message.
func keyList(keys []tag.Key) string {
	s := make([]string, len(keys))
	for i, k := range keys {
		s[i] = string(k)
	}
	return strings.Join(s, ", ")
}

// prepare parses path, applies the compiled edit, and resolves the write plan.
// Prepare performs no I/O beyond the parse, so plan and set share this without
// writing anything. Picture removal happens before adds so
// "--remove-pictures --add-cover x" yields just the new cover.
func (ce *compiledEdit) prepare(ctx context.Context, path string) (*wl.Document, *wl.Plan, error) {
	doc, err := wl.ParseFile(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	ed := doc.Edit().Apply(ce.patch)
	if ce.rmPics {
		ed.ClearPictures()
	}
	for _, p := range ce.covers {
		ed.AddPicture(p)
	}
	plan, err := ed.Prepare(ce.opts...)
	if err != nil {
		return nil, nil, err
	}
	return doc, plan, nil
}
