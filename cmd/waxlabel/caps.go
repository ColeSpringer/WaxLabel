package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/spf13/cobra"
)

// newCapsCmd builds the "caps" command, which reports what metadata a format can
// edit and how faithfully it stores each field. It has two modes: caps <file>
// answers the question for a file already in hand (file-aware, via
// Document.Capabilities), and caps --format <name> answers it for a format with
// no file (via wl.CapabilitiesFor) - the query an edit form for a not-yet-created
// file needs. It dogfoods tag.KnownKeys, tag.Key.Multivalued, and the capability
// model.
func newCapsCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "caps (<file>... | --format <name>)",
		Short: "Show which metadata a format can edit, and how",
		Example: "  waxlabel caps song.flac\n" +
			"  waxlabel caps --format mp3",
		Long: "Report what metadata each format can store and edit: the read/write level,\n" +
			"native representation, and fidelity for fields, pictures, and chapters, plus\n" +
			"every editable key with its cardinality (single- or multi-valued) and meaning.\n" +
			"For the format-independent key vocabulary on its own, see the keys command.\n\n" +
			"Pass files to describe them (a single \"-\" reads from standard input), or\n" +
			"--format <name> (e.g. flac, mp3, m4a) to describe a format with no file.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "" {
				if len(args) > 0 {
					return usagef("caps --format takes no file arguments")
				}
				f, opts, ok := parseFormat(format)
				if !ok {
					return usagef("unknown format %q; try one of: %s", format, formatHint())
				}
				return runCapsFormat(cmd, f, opts...)
			}
			if len(args) == 0 {
				// Carry the resolved command path (not a literal, which goes stale on a
				// rename) and request the --help pointer, so this dead-end prints the same
				// hint line the other commands' usage errors do (U5).
				return &usageError{msg: "caps requires a file argument or --format", cmd: cmd.CommandPath(), wantsHint: true}
			}
			return runCapsFiles(cmd, args)
		},
	}
	cmd.Flags().StringVar(&format, "format", "", "describe a format with no file (e.g. flac, mp3, m4a)")
	return cmd
}

// runCapsFormat renders a single format's capabilities (no file). opts carry any
// variant narrowing the format name implied (e.g. WithWebMSubset for "webm").
func runCapsFormat(cmd *cobra.Command, f wl.Format, opts ...wl.WriteOption) error {
	jc := buildCaps("", wl.CapabilitiesFor(f, opts...))
	if jsonMode(cmd) {
		return writeJSON(cmd.OutOrStdout(), jc)
	}
	renderCaps(cmd.OutOrStdout(), jc)
	return nil
}

// runCapsFiles parses each file and reports its (file-aware) capabilities,
// reusing the per-file harness so a parse failure on one file is reported without
// aborting the rest.
func runCapsFiles(cmd *cobra.Command, args []string) error {
	realOf, cleanup, err := readInputs(cmd.InOrStdin(), args)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := checkRegularInputs(realOf, args...); err != nil {
		return err
	}
	return perFile(cmd, args,
		func(ctx context.Context, path string) (jsonCaps, error) {
			doc, err := parseInput(ctx, realOf(path), path)
			if err != nil {
				return jsonCaps{}, err
			}
			return buildCaps(path, doc.Capabilities()), nil
		},
		func(_ string, jc jsonCaps) any { return jc },
		func(w io.Writer, _ string, jc jsonCaps) { renderCaps(w, jc) },
		false,
	)
}

// jsonCaps is the machine-readable capability report for one file or format. A
// failed per-file element is emitted as the shared jsonErrorEntry; this struct keeps
// a matching Error field so a consumer can decode every array element into it (see
// jsonErrorEntry). Fields holds the default (generic) field capability shared by
// every key; Keys lists each key with its per-key cardinality, so the common case
// (a uniform field capability) is reported once rather than repeated per key.
type jsonCaps struct {
	SchemaVersion int          `json:"schemaVersion"`
	File          string       `json:"file,omitempty"`
	Error         *jsonErrBody `json:"error,omitempty"`
	Format        string       `json:"format,omitempty"`
	ReadOnly      bool         `json:"readOnly,omitempty"`
	Fields        *jsonCapDim  `json:"fields,omitempty"`
	Pictures      *jsonCapDim  `json:"pictures,omitempty"`
	Chapters      *jsonCapDim  `json:"chapters,omitempty"`
	// Padding grades how completely the format honors the --padding/--no-padding
	// controls: "none", "partial" (grow-only), or "full". Always present on a
	// successful report.
	Padding string       `json:"padding,omitempty"`
	Keys    []jsonCapKey `json:"keys,omitempty"`
}

// jsonCapDim is one dimension's (fields/pictures/chapters) support.
type jsonCapDim struct {
	Read           string   `json:"read"`
	Write          string   `json:"write"`
	Representation string   `json:"representation,omitempty"`
	Fidelity       string   `json:"fidelity,omitempty"`
	Constraints    []string `json:"constraints,omitempty"`
	MaxItems       int      `json:"maxItems,omitempty"`
}

// jsonCapKey is one canonical key's editable detail.
type jsonCapKey struct {
	Key         string `json:"key"`
	Description string `json:"description,omitempty"`
	Cardinality string `json:"cardinality"` // "single" or "multi"
}

// buildCaps projects a Capabilities into its JSON form. Only keys the format can
// write are listed (the editable set); the format-independent vocabulary in full
// is the keys command's job.
func buildCaps(file string, caps wl.Capabilities) jsonCaps {
	jc := jsonCaps{
		SchemaVersion: schemaVersion,
		File:          file,
		Format:        caps.Format.String(),
		ReadOnly:      caps.ReadOnly,
		Fields:        capDim(caps.GenericField),
		Pictures:      capDim(caps.Pictures),
		Chapters:      capDim(caps.Chapters),
		Padding:       caps.Padding.String(),
	}
	for _, k := range tag.KnownKeys() {
		fc := caps.Field(k)
		if fc.Write < wl.AccessPartial {
			continue // editable-only: skip a key the format cannot write
		}
		jc.Keys = append(jc.Keys, jsonCapKey{
			Key:         string(k),
			Description: k.Description(),
			Cardinality: cardinalityOf(k, fc),
		})
	}
	return jc
}

// capDim projects one capability dimension into its JSON form.
func capDim(c wl.Capability) *jsonCapDim {
	return &jsonCapDim{
		Read:           c.Read.String(),
		Write:          c.Write.String(),
		Representation: c.Representation,
		Fidelity:       c.Fidelity,
		Constraints:    c.Constraints,
		MaxItems:       c.MaxItems,
	}
}

// cardinalityOf reports whether key holds a single value or many under capability
// c, as the strict enum "single" or "multi" (the jsonCapKey.Cardinality contract).
// Cardinality is the key's inherent property (tag.Key.Multivalued) unless the
// format restricts it to one (Capability.MaxValues == 1 forces single even on a
// multi-valued key); the two are combined here so the signal a caller sees already
// accounts for both.
func cardinalityOf(key tag.Key, c wl.Capability) string {
	if !key.Multivalued() || c.MaxValues == 1 {
		return "single"
	}
	return "multi"
}

// renderCaps writes the human-readable capability report.
func renderCaps(w io.Writer, jc jsonCaps) {
	if jc.File != "" {
		fmt.Fprintln(w, displayName(jc.File))
	}
	fmt.Fprintf(w, "  %-9s %s\n", "format:", jc.Format)
	if jc.ReadOnly {
		fmt.Fprintln(w, "  (read-only: this format cannot be written)")
	}
	renderCapDim(w, "fields", jc.Fields)
	renderCapDim(w, "pictures", jc.Pictures)
	renderCapDim(w, "chapters", jc.Chapters)
	if jc.Padding != "" {
		// Padding is a single level (none/partial/full), not a read/write dimension, so
		// it gets its own one-word line rather than a renderCapDim row.
		fmt.Fprintf(w, "  %-9s %s\n", "padding:", jc.Padding)
	}

	fmt.Fprintf(w, "  editable keys (%d):\n", len(jc.Keys))
	rows := make([]keyRow, len(jc.Keys))
	for i, k := range jc.Keys {
		rows[i] = keyRow{key: k.Key, cardinality: k.Cardinality, description: k.Description}
	}
	renderKeyTable(w, "    ", rows)
}

// renderCapDim writes one dimension line: its read/write levels, then the native
// representation and fidelity, with any constraints on a following indented line.
func renderCapDim(w io.Writer, label string, d *jsonCapDim) {
	if d == nil {
		return
	}
	parts := []string{fmt.Sprintf("read %s, write %s", d.Read, d.Write)}
	if d.Representation != "" {
		parts = append(parts, d.Representation)
	}
	if d.Fidelity != "" {
		parts = append(parts, d.Fidelity)
	}
	line := strings.Join(parts, " · ")
	if d.MaxItems > 0 {
		line += fmt.Sprintf(" [max %d]", d.MaxItems)
	}
	fmt.Fprintf(w, "  %-9s %s\n", label+":", line)
	if len(d.Constraints) > 0 {
		// 12 spaces aligns "constraints:" under the dimension value above (the value
		// starts at column 12: 2 leading + the 9-wide label + 1 space).
		fmt.Fprintf(w, "            constraints: %s\n", strings.Join(d.Constraints, "; "))
	}
}

// parseFormat resolves a user-supplied format name to a Format and any write
// options needed to describe it. It accepts any file extension a codec claims (with
// or without a leading dot) and a few friendly aliases for the formats whose name is
// not an extension (the two Ogg codecs and Matroska/WebM). Matching is
// case-insensitive.
func parseFormat(s string) (wl.Format, []wl.WriteOption, bool) {
	norm := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), ".")
	switch norm {
	case "vorbis", "oggvorbis":
		return wl.FormatOggVorbis, nil, true
	case "opus", "oggopus":
		return wl.FormatOggOpus, nil, true
	case "matroska":
		return wl.FormatMatroska, nil, true
	case "webm":
		// WebM is not a distinct Format - it is a subset of Matroska whose defining
		// restriction is that cover attachments are outside the subset. Describe it via
		// the Matroska codec under WithWebMSubset, which applies that one restriction
		// (the codec's own, reused - not a parallel copy), so the format-level "webm"
		// answer matches what a real .webm file reports.
		return wl.FormatMatroska, []wl.WriteOption{wl.WithWebMSubset()}, true
	}
	for _, f := range wl.Formats() {
		for _, ext := range wl.ExtensionsFor(f) {
			if strings.TrimPrefix(ext, ".") == norm {
				return f, nil, true
			}
		}
	}
	return wl.FormatUnknown, nil, false
}

// formatHint lists representative format names for the unknown-format error.
func formatHint() string {
	return "flac, mp3, mp4 (m4a), wav, aiff, aac, ogg (vorbis), opus, matroska (mka), webm"
}
