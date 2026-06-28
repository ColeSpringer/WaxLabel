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
			realOf, cleanup, err := readInputs(cmd.InOrStdin(), args)
			if err != nil {
				return err
			}
			defer cleanup()
			paths, skipped, pathErrors, err := expandPaths(args, recursive)
			if err != nil {
				return err
			}
			noteNoFiles(cmd.ErrOrStderr(), paths)
			noteSkipped(cmd.ErrOrStderr(), skipped, jsonMode(cmd))
			return perFile(cmd, paths,
				guardPathErrors(pathErrors, func(ctx context.Context, path string) (*wl.Document, error) {
					return parseInput(ctx, realOf(path), path)
				}),
				func(path string, doc *wl.Document) any { return toJSONDocument(path, doc, native) },
				func(w io.Writer, path string, doc *wl.Document) { renderDocument(w, path, doc, native) },
				// A document with no decodable audio essence renders fully, then exits 4 - the
				// same verdict the write/lint commands reach - so a read command's exit code on a
				// tag-only or truncated file is no longer the exit-0 outlier.
				func(_ string, doc *wl.Document) error {
					if docHasNoAudio(doc) {
						return errNoAudioEssence()
					}
					return nil
				},
				false,
			)
		},
	}
	cmd.Flags().BoolVar(&native, "native", false, "include native blocks and the per-family view")
	cmd.Flags().BoolVar(&recursive, "recursive", false, "recurse into directory arguments, dumping every audio file found (selected by file extension)")
	return markListCommand(cmd)
}

// jsonDocument is the machine-readable view of one dumped file. A failed element is
// emitted as the shared jsonErrorEntry; this struct keeps a matching Error field so
// a consumer can decode every array element into it (Error set, metadata absent on
// failure; Error nil and metadata populated on success). See jsonErrorEntry.
// docHasNoAudio reports whether a parsed document carries the no-audio-frames warning -
// a tag-only or truncated file with no decodable essence. It is the shared signal behind
// the dump and caps no-audio severity hooks (caps records it on jsonCaps, which has no
// warnings field of its own).
func docHasNoAudio(doc *wl.Document) bool {
	for _, w := range doc.Warnings() {
		if w.Code == wl.WarnNoAudioFrames {
			return true
		}
	}
	return false
}

type jsonDocument struct {
	SchemaVersion int          `json:"schemaVersion"`
	File          string       `json:"file"`
	Error         *jsonErrBody `json:"error,omitempty"`
	Format        string       `json:"format,omitempty"`
	// Subformat is the exact container subtype, such as "WebM" or "AIFC".
	// Format stays at the codec family level. This mirrors properties.container at the
	// top level so machine consumers do not have to inspect properties to distinguish
	// WebM from Matroska.
	Subformat  string          `json:"subformat,omitempty"`
	Properties *jsonProperties `json:"properties,omitempty"`
	Tags       []jsonTag       `json:"tags"`
	Pictures   []jsonPicture   `json:"pictures"`
	Chapters   []jsonChapter   `json:"chapters"`
	Warnings   []jsonWarning   `json:"warnings"`
	Native     []jsonNative    `json:"native,omitempty"`
	Sources    []jsonSource    `json:"sources,omitempty"`
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
	// Cardinality matches the human dump's single-valued key marker: "duplicate" when
	// values fold to one, "conflict" when they differ, or empty for ordinary keys.
	Cardinality string `json:"cardinality,omitempty"`
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
	StartMs      int64  `json:"startMs"`
	EndMs        int64  `json:"endMs,omitempty"`
	Title        string `json:"title,omitempty"`
	Language     string `json:"language,omitempty"`
	LanguageIETF string `json:"languageIetf,omitempty"`
	Hidden       bool   `json:"hidden,omitempty"`
	Disabled     bool   `json:"disabled,omitempty"`
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
	// Bit depth describes the stored samples only for a fixed-width codec; for a
	// lossy codec a container-stored depth (e.g. the legacy 16 MP4 writes for AAC)
	// is noise. Zero it so omitempty drops the field, mirroring the text view's
	// bitDepthMeaningful gate (audioLine) so human and JSON output agree.
	bitsPerSample := t.BitsPerSample
	if !bitDepthMeaningful(t.Codec) {
		bitsPerSample = 0
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
			DurationMs:    props.Duration().Milliseconds(),
			BitrateBps:    t.Bitrate,
		},
		// All four iterable collections are inited non-nil (not just tags/pictures) so a
		// no-tags / no-chapters / no-warnings file emits "[]" rather than null or an
		// omitted field - `jq '.[].tags[]'` (and the others) never breaks. native/sources
		// stay omitempty: they are feature-gated (--native), not always-present collections.
		Tags:     []jsonTag{},
		Pictures: []jsonPicture{},
		Chapters: []jsonChapter{},
		Warnings: []jsonWarning{},
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
			Bytes:       len(p.Data),
			Description: p.Description,
		})
	}
	for _, c := range doc.Chapters() {
		jd.Chapters = append(jd.Chapters, jsonChapter{
			StartMs:      c.Start.Milliseconds(),
			EndMs:        c.End.Milliseconds(),
			Title:        c.Title,
			Language:     c.Language,
			LanguageIETF: c.LanguageIETF,
			Hidden:       c.Hidden,
			Disabled:     c.Disabled,
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
