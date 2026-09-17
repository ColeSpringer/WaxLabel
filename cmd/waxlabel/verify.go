package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// newVerifyCmd builds "verify": each file's audio-essence identity (and with
// --file, whole-file identity). Like dump, files are independent; failures
// affect the exit code.
func newVerifyCmd() *cobra.Command {
	var whole bool
	var recursive bool
	var quiet bool
	cmd := &cobra.Command{
		Use:   "verify <file>...",
		Short: "Compute audio-essence (and optionally whole-file) identity",
		Example: "  waxlabel verify song.flac\n" +
			"  waxlabel verify --whole-file --quiet *.flac | sort",
		Long: "Compute each file's audio-essence digest - a hash of the encoded audio\n" +
			"plus its decoder-critical configuration, independent of tags - which\n" +
			"answers \"is this the same audio?\" for deduplication. The digest is\n" +
			"container-scoped: the same audio remuxed into another container (FLAC in\n" +
			".flac vs .oga) hashes differently. It carries a versioned extent name, so it\n" +
			"stays interpretable across library-wide refinements. With --whole-file, also\n" +
			"compute the whole-file identity. With --recursive, directory arguments are\n" +
			"walked for audio files. With --quiet, print one tab-separated\n" +
			"\"essence<TAB>path\" line per file (essence, whole-file, then path under\n" +
			"--whole-file) for piping into sort/uniq to find duplicates. A single \"-\"\n" +
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
			// quiet is text-only; --json has a fixed shape. Quiet is one TSV line per
			// file, so drop the inter-record blank (noSeparator) for sort/uniq pipes.
			quiet = quiet && !jsonMode(cmd)
			return perFile(cmd, paths,
				guardPathErrors(pathErrors, func(ctx context.Context, path string) (jsonVerify, error) {
					return computeVerify(ctx, realOf(path), path, whole)
				}),
				func(path string, v jsonVerify) any { return v },
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
	cmd.Flags().BoolVar(&recursive, "recursive", false, "recurse into directory arguments, verifying every audio file found (selected by file extension)")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "print one tab-separated digest+path line per file, for piping into sort/uniq to dedup")
	return markListCommand(cmd)
}

// computeVerify parses realPath and computes its essence digest (and whole-file
// when whole is set). displayPath is the user-facing name; it differs from
// realPath only for stdin ("-"), whose bytes come from a temp file.
func computeVerify(ctx context.Context, realPath, displayPath string, whole bool) (jsonVerify, error) {
	doc, err := parseInput(ctx, realPath, displayPath)
	if err != nil {
		return jsonVerify{File: jsonFileName(displayPath)}, err
	}
	essence, err := doc.HashAudioEssence(ctx)
	if err != nil {
		return jsonVerify{File: jsonFileName(displayPath)}, err
	}
	v := jsonVerify{SchemaVersion: schemaVersion, File: jsonFileName(displayPath), Essence: essence.String()}
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

// renderVerifyQuiet writes one TSV line: "essence\tpath", or
// "essence\twhole-file\tpath" under whole, for `sort | uniq`. Path is last and
// through displayName (SanitizeLine) so a tab/newline in a filename cannot forge
// a TSV column when fed to awk/sort/uniq.
func renderVerifyQuiet(w io.Writer, v jsonVerify, whole bool) {
	if whole {
		fmt.Fprintf(w, "%s\t%s\t%s\n", v.Essence, v.WholeFile, displayName(v.File))
		return
	}
	fmt.Fprintf(w, "%s\t%s\n", v.Essence, displayName(v.File))
}

// jsonVerify is one file's machine-readable identity. Failures use jsonErrorEntry;
// Error is kept so a mixed array decodes into this type.
type jsonVerify struct {
	SchemaVersion int          `json:"schemaVersion"`
	File          string       `json:"file"`
	Essence       string       `json:"essence,omitempty"`
	WholeFile     string       `json:"wholeFile,omitempty"`
	Error         *jsonErrBody `json:"error,omitempty"`
}
