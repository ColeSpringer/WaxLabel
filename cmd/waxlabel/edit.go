package main

import (
	"context"
	"fmt"
	"os"
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

	preset string
	legacy string
}

// bind registers the edit and write-option flags on cmd.
func (e *editFlags) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringArrayVar(&e.set, "set", nil, "set KEY=VALUE, replacing the key (repeatable)")
	f.StringArrayVar(&e.add, "add", nil, "append KEY=VALUE to a key (repeatable, for multi-value fields)")
	f.StringArrayVar(&e.clear, "clear", nil, "remove KEY (repeatable)")
	f.StringArrayVar(&e.addCover, "add-cover", nil, "add a front-cover picture from an image file (repeatable)")
	f.BoolVar(&e.rmPics, "remove-pictures", false, "remove all embedded pictures")
	f.StringVar(&e.preset, "preset", "", "write policy preset: preserve|compatible|canonical|minimal")
	f.StringVar(&e.legacy, "legacy", "", "legacy-tag policy: preserve|strip|reconcile|update-existing")
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

// applyPictures records the picture edits on ed. Removal happens before adds so
// "--remove-pictures --add-cover x.jpg" yields just the new cover. Reading a
// cover file is a runtime (not usage) failure.
func (e *editFlags) applyPictures(ed *wl.Editor) error {
	if e.rmPics {
		ed.ClearPictures()
	}
	for _, path := range e.addCover {
		data, err := os.ReadFile(path)
		if err != nil {
			// os.ReadFile's *fs.PathError already names the path, so do not repeat
			// it; just mark that the failure is about a cover image.
			return fmt.Errorf("cover image: %w", err)
		}
		ed.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: data})
	}
	return nil
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

// preparePlan parses path, applies the edits, and resolves the write plan under
// the edit's options plus any extra (save-only) options. Prepare performs no
// I/O beyond the parse, so plan and set share this without writing anything.
func preparePlan(ctx context.Context, path string, e *editFlags, extra ...wl.WriteOption) (*wl.Document, *wl.Plan, error) {
	opts, err := e.writeOptions()
	if err != nil {
		return nil, nil, err
	}
	opts = append(opts, extra...)
	patch, err := e.patch()
	if err != nil {
		return nil, nil, err
	}
	doc, err := wl.ParseFile(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	ed := doc.Edit().Apply(patch)
	if err := e.applyPictures(ed); err != nil {
		return nil, nil, err
	}
	plan, err := ed.Prepare(opts...)
	if err != nil {
		return nil, nil, err
	}
	return doc, plan, nil
}
