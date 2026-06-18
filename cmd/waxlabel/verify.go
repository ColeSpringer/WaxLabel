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
	cmd := &cobra.Command{
		Use:   "verify <file>...",
		Short: "Compute audio-essence (and optionally whole-file) identity",
		Long: "Compute each file's audio-essence digest - a hash of the encoded audio\n" +
			"plus its decoder-critical configuration, independent of tags - which\n" +
			"answers \"is this the same audio?\" for deduplication. The digest carries\n" +
			"a versioned extent name, so it stays interpretable across library-wide\n" +
			"refinements. With --whole-file, also compute the whole-file identity.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return perFile(cmd, args,
				func(ctx context.Context, path string) (jsonVerify, error) {
					return computeVerify(ctx, path, whole)
				},
				func(path string, v jsonVerify) any { return v },
				func(path string, c classifiedError) any {
					return jsonVerify{File: path, Error: &jsonErrBody{c.code, c.message}}
				},
				func(w io.Writer, path string, v jsonVerify) { renderVerify(w, v, whole) },
			)
		},
	}
	cmd.Flags().BoolVar(&whole, "whole-file", false, "also compute the whole-file identity")
	return cmd
}

// computeVerify parses path and computes its essence digest (and the whole-file
// digest when whole is set).
func computeVerify(ctx context.Context, path string, whole bool) (jsonVerify, error) {
	doc, err := wl.ParseFile(ctx, path)
	if err != nil {
		return jsonVerify{File: path}, err
	}
	essence, err := doc.HashAudioEssence(ctx)
	if err != nil {
		return jsonVerify{File: path}, err
	}
	v := jsonVerify{File: path, Essence: essence.String()}
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
	fmt.Fprintf(w, "%s\n", v.File)
	fmt.Fprintf(w, "  essence:    %s\n", v.Essence)
	if whole {
		fmt.Fprintf(w, "  whole-file: %s\n", v.WholeFile)
	}
}

// jsonVerify is the machine-readable identity for one file. On failure only File
// and Error are set.
type jsonVerify struct {
	File      string       `json:"file"`
	Essence   string       `json:"essence,omitempty"`
	WholeFile string       `json:"wholeFile,omitempty"`
	Error     *jsonErrBody `json:"error,omitempty"`
}
