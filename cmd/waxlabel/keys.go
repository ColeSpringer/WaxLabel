package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/colespringer/waxlabel/tag"
	"github.com/spf13/cobra"
)

// newKeysCmd builds the "keys" command, which lists the canonical, format-neutral
// tag vocabulary with each key's cardinality and meaning - the discovery query
// that needs no file and no format. It is the format-independent counterpart to
// caps (which reports a single format's editable subset and storage fidelity);
// every key here is writable on some format, and which formats store it is the
// caps question. It dogfoods tag.KnownKeys, tag.Key.Multivalued, and
// tag.Key.Description.
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

// jsonKeys is the machine-readable key catalog: the whole canonical vocabulary,
// each key with its cardinality and description.
type jsonKeys struct {
	SchemaVersion int       `json:"schemaVersion"`
	Keys          []jsonKey `json:"keys"`
}

// jsonKey is one canonical key's catalog entry. Cardinality is the strict enum
// "single" or "multi" (matching jsonCapKey), here the key's inherent cardinality
// with no format restriction applied.
type jsonKey struct {
	Key         string `json:"key"`
	Cardinality string `json:"cardinality"`
	Description string `json:"description,omitempty"`
	// Aliases are the alternative spellings --set/--add accept for this key (e.g. DATE, YEAR
	// for RECORDINGDATE), so the common Vorbis spellings are discoverable rather than silently
	// normalized. Omitted for a key with no aliases. Only the keys command uses this; caps
	// shares renderKeyTable but not this field.
	Aliases []string `json:"aliases,omitempty"`
}

// buildKeys projects the canonical vocabulary into its JSON form, in KnownKeys'
// stable sorted order.
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

// keyCardinality reports a key's inherent cardinality as the "single"/"multi"
// enum. Unlike cardinalityOf (caps), there is no format here to restrict a
// multi-valued key to one, so this reads tag.Key.Multivalued alone.
func keyCardinality(k tag.Key) string {
	if k.Multivalued() {
		return "multi"
	}
	return "single"
}

// renderKeys writes the human-readable key catalog: aligned key, cardinality, and
// description columns at the top level (2-space indent).
func renderKeys(w io.Writer, jk jsonKeys) {
	fmt.Fprintf(w, "canonical keys (%d):\n", len(jk.Keys))
	rows := make([]keyRow, len(jk.Keys))
	for i, k := range jk.Keys {
		// Append the aliases to the description column (keys only); renderKeyTable is shared
		// with caps, so aliases must not become a new column there. TrimSpace drops the leading
		// gap when a key has no description of its own.
		desc := k.Description
		if len(k.Aliases) > 0 {
			desc = strings.TrimSpace(desc + "  (aliases: " + strings.Join(k.Aliases, ", ") + ")")
		}
		rows[i] = keyRow{key: k.Key, cardinality: k.Cardinality, description: desc}
	}
	renderKeyTable(w, "  ", rows)
}

// keyRow is one line of a key listing: the canonical key, its cardinality, and its
// description. It is the shared shape rendered by both the caps editable-keys block
// and this keys catalog (see renderKeyTable).
type keyRow struct {
	key, cardinality, description string
}

// renderKeyTable writes aligned key/cardinality/description columns, one row per
// line, each prefixed by indent. The key and cardinality columns are padded to
// their widest value so the descriptions line up. Shared by caps (its editable-keys
// block, 4-space indent) and keys (the full catalog, 2-space indent), so the two
// listings cannot drift in column layout.
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
