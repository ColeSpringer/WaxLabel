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

// newCapsCmd builds the "caps" command: report editable metadata and fidelity.
// File mode uses Document.Capabilities; --format uses wl.CapabilitiesFor.
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
				f, opts, container, err := parseFormat(format)
				if err != nil {
					return usagef("%s", err)
				}
				return runCapsFormat(cmd, f, container, opts...)
			}
			if len(args) == 0 {
				// CommandPath + wantsHint: same usage hint as other commands.
				return &usageError{msg: "caps requires a file argument or --format", cmd: cmd.CommandPath(), wantsHint: true}
			}
			return runCapsFiles(cmd, args)
		},
	}
	cmd.Flags().StringVar(&format, "format", "", "describe a format with no file (e.g. flac, mp3, m4a)")
	return markListCommand(cmd)
}

// runCapsFormat renders format capabilities (no file). opts narrow variant (e.g. WebM).
func runCapsFormat(cmd *cobra.Command, f wl.Format, container string, opts ...wl.WriteOption) error {
	jc := buildCaps("", container, wl.CapabilitiesFor(f, opts...))
	if jsonMode(cmd) {
		return writeJSON(cmd.OutOrStdout(), jc)
	}
	renderCaps(cmd.OutOrStdout(), jc)
	return nil
}

// runCapsFiles: per-file capabilities via perFile harness.
func runCapsFiles(cmd *cobra.Command, args []string) error {
	// Empty operand: usage error before parse (avoids exit 4 outranking not-found).
	// caps has no expandPaths, so check here.
	if err := checkEmptyOperands(args...); err != nil {
		return err
	}
	realOf, cleanup, err := readInputs(cmd.InOrStdin(), maxSizeFlag(cmd), args)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := checkRegularInputs(realOf, true, args...); err != nil {
		return err
	}
	// No-audio still has readable capabilities; caps exits 0 (unlike hash/write/lint).
	return perFile(cmd, args,
		func(ctx context.Context, path string) (jsonCaps, error) {
			doc, err := parseInput(ctx, realOf(path), path)
			if err != nil {
				return jsonCaps{}, err
			}
			// WebM/Matroska distinction is container subtype only (same as copy.go).
			return buildCaps(path, doc.Properties().Container, doc.Capabilities()), nil
		},
		func(_ string, jc jsonCaps) any { return jc },
		func(w io.Writer, _ string, jc jsonCaps) { renderCaps(w, jc) },
		false,
	)
}

// jsonCaps: machine capability report. Error matches jsonErrorEntry.
// Fields: generic field capability; Keys: per-key cardinality (avoids repeating uniform caps).
type jsonCaps struct {
	SchemaVersion int          `json:"schemaVersion"`
	File          string       `json:"file,omitempty"`
	Error         *jsonErrBody `json:"error,omitempty"`
	Format        string       `json:"format,omitempty"`
	// Subformat: container subtype ("WebM", "AIFC"). Format: codec family.
	// Same subtype signal dump exposes via properties.container.
	Subformat    string      `json:"subformat,omitempty"`
	ReadOnly     bool        `json:"readOnly,omitempty"`
	Fields       *jsonCapDim `json:"fields,omitempty"`
	Pictures     *jsonCapDim `json:"pictures,omitempty"`
	Chapters     *jsonCapDim `json:"chapters,omitempty"`
	SyncedLyrics *jsonCapDim `json:"syncedLyrics,omitempty"`
	// Padding: "none", "partial" (grow-only), or "full".
	Padding string `json:"padding,omitempty"`
	// OutputGain: "full" for Ogg Opus, "none" elsewhere.
	OutputGain string       `json:"outputGain,omitempty"`
	Keys       []jsonCapKey `json:"keys"`

	// humanFormat: human "format:" label (WebM/Matroska or bare Format). Unexported;
	// JSON format stays codec family. Use Subformat for exact subtype.
	humanFormat string
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

// buildCaps projects Capabilities to JSON. Lists writable keys only (see keys command).
// container: subtype for human format label.
func buildCaps(file, container string, caps wl.Capabilities) jsonCaps {
	format := caps.Format.String()
	jc := jsonCaps{
		SchemaVersion: schemaVersion,
		File:          jsonFileName(file),
		Format:        format,
		Subformat:     subformatOf(container, format),
		humanFormat:   transferFormatLabel(caps.Format, container),
		ReadOnly:      caps.ReadOnly,
		Fields:        capDim(caps.GenericField),
		Pictures:      capDim(caps.Pictures),
		Chapters:      capDim(caps.Chapters),
		SyncedLyrics:  capDim(caps.SyncedLyrics),
		Padding:       caps.Padding.String(),
		OutputGain:    caps.OutputGain.String(),
		// Non-nil keys so read-only JSON emits "keys": [].
		Keys: []jsonCapKey{},
	}
	// Read-only: skip key loop (per-field levels still describe format; empty keys[]).
	if jc.ReadOnly {
		return jc
	}
	for _, k := range tag.KnownKeys() {
		fc := caps.Field(k)
		if fc.Write < wl.AccessPartial {
			continue // skip non-writable keys
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

// cardinalityOf: "single" or "multi". Key.Multivalued unless MaxValues == 1.
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
	// humanFormat distinguishes WebM/Matroska; fall back to jc.Format.
	format := jc.humanFormat
	if format == "" {
		format = jc.Format
	}
	fmt.Fprintf(w, "  %-*s %s\n", capLabelWidth, "format:", format)
	if jc.ReadOnly {
		// Per-file read-only (e.g. fragmented MP4): clarify dimension rows describe format.
		if jc.File != "" {
			fmt.Fprintln(w, "  (read-only: this file cannot be written; the levels below describe the format)")
		} else {
			fmt.Fprintln(w, "  (read-only: this format cannot be written)")
		}
	}
	renderCapDim(w, "fields", jc.Fields)
	renderCapDim(w, "pictures", jc.Pictures)
	renderCapDim(w, "chapters", jc.Chapters)
	renderCapDim(w, "synced lyrics", jc.SyncedLyrics)
	// Padding/output gain are scalar levels, not read/write dimensions.
	if jc.Padding != "" {
		fmt.Fprintf(w, "  %-*s %s\n", capLabelWidth, "padding:", jc.Padding)
	}
	if jc.OutputGain != "" {
		fmt.Fprintf(w, "  %-*s %s\n", capLabelWidth, "output gain:", jc.OutputGain)
	}

	fmt.Fprintf(w, "  editable keys (%d):\n", len(jc.Keys))
	rows := make([]keyRow, len(jc.Keys))
	for i, k := range jc.Keys {
		rows[i] = keyRow{key: k.Key, cardinality: k.Cardinality, description: k.Description}
	}
	renderKeyTable(w, "    ", rows)
}

// renderCapDim: one dimension line plus optional constraints.
// capLabelWidth fits longest label ("synced lyrics:").
const capLabelWidth = 14

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
	fmt.Fprintf(w, "  %-*s %s\n", capLabelWidth, label+":", line)
	if len(d.Constraints) > 0 {
		// 12 spaces: align constraints under value column.
		fmt.Fprintf(w, "            constraints: %s\n", strings.Join(d.Constraints, "; "))
	}
}

// parseFormat resolves a format name to Format, write opts, and container label.
// Accepts extensions (optional dot) and aliases (Ogg codecs, Matroska/WebM).
// Case-insensitive. Ambiguous extensions (e.g. .oga) error with alternatives.
func parseFormat(s string) (f wl.Format, opts []wl.WriteOption, container string, err error) {
	norm := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), ".")
	switch norm {
	case "ogg", "vorbis", "oggvorbis":
		// "ogg" means Vorbis despite shared .ogg extension.
		return wl.FormatOggVorbis, nil, "", nil
	case "opus", "oggopus":
		return wl.FormatOggOpus, nil, "", nil
	case "oggflac":
		// .oga is ambiguous; use oggflac alias.
		return wl.FormatOggFLAC, nil, "", nil
	case "matroska":
		return wl.FormatMatroska, nil, "", nil
	case "webm":
		// WebM is Matroska subset (WithWebMSubset). Container label "WebM"; JSON format stays Matroska.
		return wl.FormatMatroska, []wl.WriteOption{wl.WithWebMSubset()}, "WebM", nil
	}
	var hits []wl.Format
	for _, cand := range wl.Formats() {
		for _, ext := range wl.ExtensionsFor(cand) {
			if strings.TrimPrefix(ext, ".") == norm {
				hits = append(hits, cand)
				break
			}
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil, "", nil
	case 0:
		return wl.FormatUnknown, nil, "", fmt.Errorf("unknown format %q; try one of: %s", s, formatHint())
	}
	names := make([]string, len(hits))
	for i, h := range hits {
		names[i] = h.String()
	}
	return wl.FormatUnknown, nil, "", fmt.Errorf("%q names more than one format (%s); name the one you mean",
		s, strings.Join(names, ", "))
}

// formatHint lists representative format names for the unknown-format error.
func formatHint() string {
	return "flac, mp3, mp4 (m4a), wav, aiff, aac, ogg (vorbis), opus, oggflac, matroska (mka), webm, wv, ape, mpc, wma"
}
