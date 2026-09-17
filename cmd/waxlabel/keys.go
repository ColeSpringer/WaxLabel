package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/colespringer/waxlabel/tag"
	"github.com/spf13/cobra"
)

// newKeysCmd builds "keys": the canonical, format-neutral tag vocabulary with
// cardinality and meaning. No file or format needed. Counterpart to caps (which
// reports one format's editable subset). Dogfoods tag.KnownKeys / Multivalued /
// Description.
func newKeysCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keys",
		Short: "List the canonical tag keys and their meanings",
		Example: "  waxlabel keys\n" +
			"  waxlabel keys --json",
		Long: "List WaxLabel's canonical, format-neutral tag vocabulary: every known key\n" +
			"with its cardinality (single- or multi-valued) and meaning. These are the\n" +
			"keys --set/--add/--clear accept; the mapping layer translates each to the\n" +
			"native scheme of whatever format is being written. Use caps to see which of\n" +
			"these a particular format can store and how faithfully.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			jk := buildKeys()
			if jsonMode(cmd) {
				return writeJSON(cmd.OutOrStdout(), jk)
			}
			renderKeys(cmd.OutOrStdout(), jk)
			return nil
		},
	}
	return cmd
}

// jsonKeys is the machine-readable key catalog.
type jsonKeys struct {
	SchemaVersion int       `json:"schemaVersion"`
	Keys          []jsonKey `json:"keys"`
}

// jsonKey is one catalog entry. Cardinality is "single" or "multi" (inherent;
// no format restriction, unlike caps).
type jsonKey struct {
	Key         string `json:"key"`
	Cardinality string `json:"cardinality"`
	Description string `json:"description,omitempty"`
	// Aliases accepted by --set/--add (e.g. DATE for RECORDINGDATE). Omitted when
	// empty. Keys-only; caps shares renderKeyTable but not this field.
	Aliases []string `json:"aliases,omitempty"`
}

// buildKeys projects KnownKeys into JSON, stable sorted order.
func buildKeys() jsonKeys {
	jk := jsonKeys{SchemaVersion: schemaVersion}
	for _, k := range tag.KnownKeys() {
		jk.Keys = append(jk.Keys, jsonKey{
			Key:         string(k),
			Cardinality: keyCardinality(k),
			Description: k.Description(),
			Aliases:     tag.KeyAliases(k),
		})
	}
	return jk
}

// keyCardinality returns "single"/"multi" from Multivalued alone (no format cap).
func keyCardinality(k tag.Key) string {
	if k.Multivalued() {
		return "multi"
	}
	return "single"
}

// renderKeys writes the human catalog: key, cardinality, description (2-space indent).
func renderKeys(w io.Writer, jk jsonKeys) {
	fmt.Fprintf(w, "canonical keys (%d):\n", len(jk.Keys))
	rows := make([]keyRow, len(jk.Keys))
	for i, k := range jk.Keys {
		// Aliases go in the description column (keys only; caps must not get a new
		// column). TrimSpace drops the leading gap when Description is empty.
		desc := k.Description
		if len(k.Aliases) > 0 {
			desc = strings.TrimSpace(desc + "  (aliases: " + strings.Join(k.Aliases, ", ") + ")")
		}
		rows[i] = keyRow{key: k.Key, cardinality: k.Cardinality, description: desc}
	}
	renderKeyTable(w, "  ", rows)
}

// keyRow is one listing line. Shared by caps editable-keys and this catalog.
type keyRow struct {
	key, cardinality, description string
}

// renderKeyTable writes aligned key/cardinality/description columns under indent.
// Shared by caps (4-space) and keys (2-space) so layouts cannot drift.
func renderKeyTable(w io.Writer, indent string, rows []keyRow) {
	keyWidth, cardWidth := 0, 0
	for _, r := range rows {
		if n := len(r.key); n > keyWidth {
			keyWidth = n
		}
		if n := len(r.cardinality); n > cardWidth {
			cardWidth = n
		}
	}
	for _, r := range rows {
		fmt.Fprintf(w, "%s%-*s  %-*s  %s\n", indent, keyWidth, r.key, cardWidth, r.cardinality, r.description)
	}
}
