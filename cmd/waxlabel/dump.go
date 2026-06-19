package main

import (
	"context"
	"io"

	wl "github.com/colespringer/waxlabel"
	"github.com/spf13/cobra"
)

// newDumpCmd builds the "dump" command, which reads each file and prints its
// metadata. Multiple files are processed independently: a parse failure on one
// is reported (and reflected in the exit code) without aborting the rest.
func newDumpCmd() *cobra.Command {
	var native bool
	var recursive bool
	cmd := &cobra.Command{
		Use:   "dump <file>...",
		Short: "Show a file's tags, properties, pictures, and warnings",
		Long: "Parse each file and print its canonical tags, audio properties, embedded\n" +
			"pictures, and any parse warnings. With --native, also show the native\n" +
			"metadata blocks and the per-source (family) view that records which\n" +
			"container supplied each value. With --recursive, directory arguments are\n" +
			"walked for audio files. A single \"-\" reads from standard input.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			realOf, cleanup, err := readInputs(cmd.InOrStdin(), args)
			if err != nil {
				return err
			}
			defer cleanup()
			paths := expandPaths(args, recursive)
			noteNoFiles(cmd.ErrOrStderr(), paths)
			return perFile(cmd, paths,
				func(ctx context.Context, path string) (*wl.Document, error) {
					return wl.ParseFile(ctx, realOf(path))
				},
				func(path string, doc *wl.Document) any { return toJSONDocument(path, doc, native) },
				func(path string, c classifiedError) any {
					return jsonDocument{SchemaVersion: schemaVersion, File: path, Error: &jsonErrBody{c.code, c.message}}
				},
				func(w io.Writer, path string, doc *wl.Document) { renderDocument(w, path, doc, native) },
			)
		},
	}
	cmd.Flags().BoolVar(&native, "native", false, "include native blocks and the per-source (family) view")
	cmd.Flags().BoolVar(&recursive, "recursive", false, "recurse into directory arguments, dumping every audio file found")
	return cmd
}

// jsonDocument is the machine-readable view of one dumped file. On a parse
// failure only SchemaVersion, File, and Error are set; otherwise Error is nil
// and the metadata fields are populated.
type jsonDocument struct {
	SchemaVersion int             `json:"schemaVersion"`
	File          string          `json:"file"`
	Error         *jsonErrBody    `json:"error,omitempty"`
	Format        string          `json:"format,omitempty"`
	Properties    *jsonProperties `json:"properties,omitempty"`
	Tags          []jsonTag       `json:"tags,omitempty"`
	Pictures      []jsonPicture   `json:"pictures,omitempty"`
	Chapters      []jsonChapter   `json:"chapters,omitempty"`
	Warnings      []jsonWarning   `json:"warnings,omitempty"`
	Native        []jsonNative    `json:"native,omitempty"`
	Sources       []jsonSource    `json:"sources,omitempty"`
}

type jsonProperties struct {
	Container     string `json:"container,omitempty"`
	Codec         string `json:"codec,omitempty"`
	CodecProfile  string `json:"codecProfile,omitempty"` // container's raw spelling when it differs (e.g. "mp4a")
	SampleRate    int    `json:"sampleRate,omitempty"`
	Channels      int    `json:"channels,omitempty"`
	BitsPerSample int    `json:"bitsPerSample,omitempty"`
	DurationMs    int64  `json:"durationMs,omitempty"`
	BitrateBps    int    `json:"bitrateBps,omitempty"` // average bits per second (text dump shows kbps)
}

type jsonTag struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

type jsonPicture struct {
	Type        string `json:"type"`
	MIME        string `json:"mime"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	Bytes       int    `json:"bytes"`
	Description string `json:"description,omitempty"`
}

type jsonChapter struct {
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs,omitempty"`
	Title   string `json:"title,omitempty"`
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

// toJSONDocument projects a parsed document into its JSON form.
func toJSONDocument(path string, doc *wl.Document, native bool) jsonDocument {
	props := doc.Properties()
	t := props.First()
	jd := jsonDocument{
		SchemaVersion: schemaVersion,
		File:          path,
		Format:        doc.Format().String(),
		Properties: &jsonProperties{
			Container:     props.Container,
			Codec:         t.Codec,
			CodecProfile:  t.CodecProfile,
			SampleRate:    t.SampleRate,
			Channels:      t.Channels,
			BitsPerSample: t.BitsPerSample,
			DurationMs:    props.Duration().Milliseconds(),
			BitrateBps:    t.Bitrate,
		},
		Tags:     []jsonTag{},
		Pictures: []jsonPicture{},
	}
	for k, vals := range doc.Tags().All() {
		jd.Tags = append(jd.Tags, jsonTag{Key: string(k), Values: vals})
	}
	for _, p := range doc.Pictures() {
		jd.Pictures = append(jd.Pictures, jsonPicture{
			Type:        p.Type.String(),
			MIME:        p.MIME,
			Width:       p.Width,
			Height:      p.Height,
			Bytes:       len(p.Data),
			Description: p.Description,
		})
	}
	for _, c := range doc.Chapters() {
		jd.Chapters = append(jd.Chapters, jsonChapter{
			StartMs: c.Start.Milliseconds(),
			EndMs:   c.End.Milliseconds(),
			Title:   c.Title,
		})
	}
	for _, x := range doc.Warnings() {
		jd.Warnings = append(jd.Warnings, jsonWarning{Code: x.Code.String(), Message: x.Message})
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
