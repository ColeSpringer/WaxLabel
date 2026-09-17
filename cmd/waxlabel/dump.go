package main

import (
	"context"
	"io"

	wl "github.com/colespringer/waxlabel"
	"github.com/spf13/cobra"
)

// newDumpCmd builds dump: read each file and print metadata. One file failing does not stop the rest.
func newDumpCmd() *cobra.Command {
	var native bool
	var recursive bool
	cmd := &cobra.Command{
		Use:   "dump <file>...",
		Short: "Show a file's tags, properties, pictures, and warnings",
		Example: "  waxlabel dump song.flac\n" +
			"  waxlabel dump --native --json album/*.flac",
		Long: "Parse each file and print its canonical tags, audio properties, embedded\n" +
			"pictures, and any parse warnings. With --native, also show the native\n" +
			"metadata blocks and the per-family view that records which container\n" +
			"supplied each value. dump reports the warnings noticed at parse; lint adds\n" +
			"the computed checks (malformed dates and numbers, single-valued cardinality,\n" +
			"custom keys, duplicate pictures), so run lint for the full issue set. With\n" +
			"--recursive, directory arguments are walked for audio files. A single \"-\"\n" +
			"reads from standard input.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			realOf, cleanup, err := readInputs(cmd.InOrStdin(), maxSizeFlag(cmd), args)
			if err != nil {
				return err
			}
			defer cleanup()
			paths, skipped, leftovers, pathErrors, err := expandPaths(args, recursive)
			if err != nil {
				return err
			}
			noteNoFiles(cmd.ErrOrStderr(), paths, jsonMode(cmd))
			noteSkipped(cmd.ErrOrStderr(), skipped, jsonMode(cmd))
			noteLeftovers(cmd.ErrOrStderr(), leftovers, jsonMode(cmd))
			// dump treats metadata parse as success; no-audio exits 0 with a warning. Hash/write/lint return invalid-data for the same case.
			return perFile(cmd, paths,
				guardPathErrors(pathErrors, func(ctx context.Context, path string) (*wl.Document, error) {
					return parseInput(ctx, realOf(path), path)
				}),
				func(path string, doc *wl.Document) any { return toJSONDocument(path, doc, native) },
				func(w io.Writer, path string, doc *wl.Document) { renderDocument(w, path, doc, native) },
				false,
			)
		},
	}
	cmd.Flags().BoolVar(&native, "native", false, "include native blocks and the per-family view")
	cmd.Flags().BoolVar(&recursive, "recursive", false, "recurse into directory arguments, dumping every audio file found (selected by file extension)")
	return markListCommand(cmd)
}

// jsonDocument is one dumped file in JSON. Error matches jsonErrorEntry so every array element decodes the same way.
type jsonDocument struct {
	SchemaVersion int          `json:"schemaVersion"`
	File          string       `json:"file"`
	Error         *jsonErrBody `json:"error,omitempty"`
	Format        string       `json:"format,omitempty"`
	// Subformat is the exact container (e.g. "WebM", "AIFC"); Format is the family. Mirrors properties.container.
	Subformat    string             `json:"subformat,omitempty"`
	Properties   *jsonProperties    `json:"properties,omitempty"`
	Tags         []jsonTag          `json:"tags"`
	Pictures     []jsonPicture      `json:"pictures"`
	Chapters     []jsonChapter      `json:"chapters"`
	SyncedLyrics []jsonSyncedLyrics `json:"syncedLyrics"`
	Warnings     []jsonWarning      `json:"warnings"`
	// LegacyOnly lists canonical keys only in legacy containers (Document.LegacyOnlyKeys). Not gated on --native. Omitted when empty.
	LegacyOnly []string     `json:"legacyOnly,omitempty"`
	Native     []jsonNative `json:"native,omitempty"`
	Sources    []jsonSource `json:"sources,omitempty"`
}

type jsonProperties struct {
	Container     string `json:"container,omitempty"`
	Codec         string `json:"codec,omitempty"`
	CodecProfile  string `json:"codecProfile,omitempty"` // raw container spelling when it differs (e.g. "mp4a")
	SampleRate    int    `json:"sampleRate,omitempty"`
	Channels      int    `json:"channels,omitempty"`
	BitsPerSample int    `json:"bitsPerSample,omitempty"`
	DurationMs    int64  `json:"durationMs,omitempty"`
	BitrateBps    int    `json:"bitrateBps,omitempty"`   // average bps, not nominal PCM header rate; omitted when durationMs is 0
	PaddingBytes  int64  `json:"paddingBytes,omitempty"` // metadata padding (same as plan); omitted when 0
	// OutputGainDb is stream header output gain in dB. Ogg Opus only; omitted when 0.
	OutputGainDb float64 `json:"outputGainDb,omitempty"`
}

type jsonTag struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
	// Cardinality: "duplicate", "conflict", or empty (matches text dump single-valued marker).
	Cardinality string `json:"cardinality,omitempty"`
}

type jsonPicture struct {
	Type        string `json:"type"`
	MIME        string `json:"mime"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	Depth       int    `json:"depth,omitempty"`  // color depth in bits per pixel; omitted when unknown (0)
	Colors      int    `json:"colors,omitempty"` // palette size for indexed images; omitted when 0 (non-indexed)
	Bytes       int    `json:"bytes"`
	Description string `json:"description,omitempty"`
}

type jsonChapter struct {
	StartMs      int64  `json:"startMs"`
	EndMs        int64  `json:"endMs,omitempty"`
	Title        string `json:"title,omitempty"`
	Language     string `json:"language,omitempty"`
	LanguageIETF string `json:"languageIetf,omitempty"`
	Hidden       bool   `json:"hidden,omitempty"`
	Disabled     bool   `json:"disabled,omitempty"`
}

// jsonSyncedLyrics is one timed-lyrics set. Lines is always present; Text may be empty, so no omitempty.
type jsonSyncedLyrics struct {
	Language    string           `json:"language,omitempty"`
	Description string           `json:"description,omitempty"`
	Lines       []jsonSyncedLine `json:"lines"`
}

type jsonSyncedLine struct {
	TimeMs int64  `json:"timeMs"` // matches jsonChapter.StartMs (integer milliseconds)
	Text   string `json:"text"`
}

type jsonNative struct {
	Kind string `json:"kind"`
	Size int    `json:"size"`
	Note string `json:"note,omitempty"`
}

type jsonSource struct {
	Key      string   `json:"key"`
	Family   string   `json:"family"`
	Scope    string   `json:"scope"`
	Values   []string `json:"values"`
	Selected bool     `json:"selected"`
}

// toJSONDocument maps a parsed document to JSON.
func toJSONDocument(path string, doc *wl.Document, native bool) jsonDocument {
	props := doc.Properties()
	t := props.First()
	// Zero bitsPerSample for lossy codecs (container depth is noise). Matches text view bitDepthMeaningful gate.
	bitsPerSample := t.BitsPerSample
	if !bitDepthMeaningful(t.Codec) {
		bitsPerSample = 0
	}
	// Zero bitrateBps when durationMs is 0 (no average to report). Same gate as durationMs; text kbps rounding is display-only.
	bitrateBps := t.Bitrate
	if roundMs(props.Duration()) == 0 {
		bitrateBps = 0
	}
	format := doc.Format().String()
	jd := jsonDocument{
		SchemaVersion: schemaVersion,
		File:          jsonFileName(path),
		Format:        format,
		Subformat:     subformatOf(props.Container, format),
		Properties: &jsonProperties{
			Container:     props.Container,
			Codec:         t.Codec,
			CodecProfile:  t.CodecProfile,
			SampleRate:    t.SampleRate,
			Channels:      t.Channels,
			BitsPerSample: bitsPerSample,
			DurationMs:    roundMs(props.Duration()),
			BitrateBps:    bitrateBps,
			PaddingBytes:  doc.Padding(),
			OutputGainDb:  wl.OutputGainDecibels(t.OutputGain),
		},
		// Init all four collections non-nil so empty files emit []. jq '.[].tags[]' never breaks. native/sources stay omitempty (--native).
		Tags:         []jsonTag{},
		Pictures:     []jsonPicture{},
		Chapters:     []jsonChapter{},
		SyncedLyrics: []jsonSyncedLyrics{},
		Warnings:     []jsonWarning{},
	}
	for k, vals := range doc.Tags().All() {
		jd.Tags = append(jd.Tags, jsonTag{Key: string(k), Values: vals, Cardinality: cardinalityState(k, vals)})
	}
	for _, p := range doc.Pictures() {
		jd.Pictures = append(jd.Pictures, jsonPicture{
			Type:        p.Type.String(),
			MIME:        p.MIME,
			Width:       p.Width,
			Height:      p.Height,
			Depth:       p.Depth,
			Colors:      p.Colors,
			Bytes:       len(p.Data),
			Description: p.Description,
		})
	}
	for _, c := range doc.Chapters() {
		jd.Chapters = append(jd.Chapters, jsonChapter{
			StartMs:      roundMs(c.Start),
			EndMs:        roundMs(c.End),
			Title:        c.Title,
			Language:     c.Language,
			LanguageIETF: c.LanguageIETF,
			Hidden:       c.Hidden,
			Disabled:     c.Disabled,
		})
	}
	for _, sl := range doc.SyncedLyrics() {
		js := jsonSyncedLyrics{Language: sl.Language, Description: sl.Description, Lines: []jsonSyncedLine{}}
		for _, ln := range sl.Lines {
			js.Lines = append(js.Lines, jsonSyncedLine{TimeMs: roundMs(ln.Time), Text: ln.Text})
		}
		jd.SyncedLyrics = append(jd.SyncedLyrics, js)
	}
	for _, x := range doc.Warnings() {
		jd.Warnings = append(jd.Warnings, jsonWarning{Code: x.Code.String(), Message: x.Message})
	}
	for _, k := range doc.LegacyOnlyKeys() {
		jd.LegacyOnly = append(jd.LegacyOnly, string(k))
	}
	if native {
		if nd := doc.Native(); nd != nil {
			for _, e := range nd.Describe() {
				jd.Native = append(jd.Native, jsonNative{Kind: e.Kind, Size: e.Size, Note: e.Note})
			}
		}
		for _, f := range doc.Families() {
			jd.Sources = append(jd.Sources, jsonSource{
				Key:      string(f.Key),
				Family:   f.Family.String(),
				Scope:    f.Scope.String(),
				Values:   f.Values,
				Selected: f.Selected,
			})
		}
	}
	return jd
}
