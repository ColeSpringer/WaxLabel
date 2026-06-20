package main

import (
	"context"
	"fmt"
	"io"

	wl "github.com/colespringer/waxlabel"
	"github.com/spf13/cobra"
)

// newVerifyCmd builds the "verify" command, which computes each file's
// audio-essence identity (and, with --file, its whole-file identity). Like dump,
// it processes files independently and reflects any failure in the exit code.
func newVerifyCmd() *cobra.Command {
	var whole bool
	var recursive bool
	var quiet bool
	cmd := &cobra.Command{
		Use:   "verify <file>...",
		Short: "Compute audio-essence (and optionally whole-file) identity",
		Long: "Compute each file's audio-essence digest - a hash of the encoded audio\n" +
			"plus its decoder-critical configuration, independent of tags - which\n" +
			"answers \"is this the same audio?\" for deduplication. The digest carries\n" +
			"a versioned extent name, so it stays interpretable across library-wide\n" +
			"refinements. With --whole-file, also compute the whole-file identity. With\n" +
			"--recursive, directory arguments are walked for audio files. With --quiet,\n" +
			"print one tab-separated \"essence<TAB>path\" line per file (essence, whole-file,\n" +
			"then path under --whole-file) for piping into sort/uniq to find duplicates. A\n" +
			"single \"-\" reads from standard input.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			realOf, cleanup, err := readInputs(cmd.InOrStdin(), args)
			if err != nil {
				return err
			}
			defer cleanup()
			paths, err := expandPaths(args, recursive)
			if err != nil {
				return err
			}
			noteNoFiles(cmd.ErrOrStderr(), paths)
			// quiet is a text-mode presentation choice; --json has a fixed shape. In
			// quiet mode each file is one TSV line, so the inter-record blank line is
			// dropped (noSeparator) to keep a sort/uniq pipe clean.
			quiet = quiet && !jsonMode(cmd)
			return perFile(cmd, paths,
				func(ctx context.Context, path string) (jsonVerify, error) {
					return computeVerify(ctx, realOf(path), path, whole)
				},
				func(path string, v jsonVerify) any { return v },
				func(path string, c classifiedError) any {
					return jsonVerify{SchemaVersion: schemaVersion, File: path, Error: &jsonErrBody{c.code, c.message}}
				},
				func(w io.Writer, path string, v jsonVerify) {
					if quiet {
						renderVerifyQuiet(w, v, whole)
						return
					}
					renderVerify(w, v, whole)
				},
				quiet,
			)
		},
	}
	cmd.Flags().BoolVar(&whole, "whole-file", false, "also compute the whole-file identity")
	cmd.Flags().BoolVar(&recursive, "recursive", false, "recurse into directory arguments, verifying every audio file found")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "print one tab-separated digest+path line per file, for piping into sort/uniq to dedup")
	return cmd
}

// computeVerify parses the file at realPath and computes its essence digest (and
// the whole-file digest when whole is set). displayPath is the name recorded in
// the result and shown to the user; it differs from realPath only for standard
// input ("-"), whose bytes are parsed from a temp file.
func computeVerify(ctx context.Context, realPath, displayPath string, whole bool) (jsonVerify, error) {
	doc, err := wl.ParseFile(ctx, realPath)
	if err != nil {
		return jsonVerify{File: displayPath}, err
	}
	essence, err := doc.HashAudioEssence(ctx)
	if err != nil {
		return jsonVerify{File: displayPath}, err
	}
	v := jsonVerify{SchemaVersion: schemaVersion, File: displayPath, Essence: essence.String()}
	if whole {
		fileSum, err := doc.HashFile(ctx)
		if err != nil {
			return v, err
		}
		v.WholeFile = fileSum.String()
	}
	return v, nil
}

func renderVerify(w io.Writer, v jsonVerify, whole bool) {
	fmt.Fprintf(w, "%s\n", displayName(v.File))
	fmt.Fprintf(w, "  essence:    %s\n", v.Essence)
	if whole {
		fmt.Fprintf(w, "  whole-file: %s\n", v.WholeFile)
	}
}

// renderVerifyQuiet writes one tab-separated line per file - "essence<TAB>path", or
// "essence<TAB>whole-file<TAB>path" under whole - so a run pipes straight into
// `sort | uniq` to find duplicate audio. The path goes last and through displayName
// (which routes via tag.SanitizeLine), so a tab/newline/CR in a filename is escaped
// to \xNN: the digest columns stay intact and a hostile name cannot forge a TSV line
// when the output is fed to awk/sort/uniq.
func renderVerifyQuiet(w io.Writer, v jsonVerify, whole bool) {
	if whole {
		fmt.Fprintf(w, "%s\t%s\t%s\n", v.Essence, v.WholeFile, displayName(v.File))
		return
	}
	fmt.Fprintf(w, "%s\t%s\n", v.Essence, displayName(v.File))
}

// jsonVerify is the machine-readable identity for one file. On failure only
// SchemaVersion, File, and Error are set.
type jsonVerify struct {
	SchemaVersion int          `json:"schemaVersion"`
	File          string       `json:"file"`
	Essence       string       `json:"essence,omitempty"`
	WholeFile     string       `json:"wholeFile,omitempty"`
	Error         *jsonErrBody `json:"error,omitempty"`
}
