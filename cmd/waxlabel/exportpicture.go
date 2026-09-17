package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/spf13/cobra"
)

// newExportPictureCmd builds "export-picture": write one embedded picture's bytes
// to a file. Read-only on the audio input.
func newExportPictureCmd() *cobra.Command {
	var (
		output    string
		selector  string
		overwrite bool
	)
	cmd := &cobra.Command{
		Use:   "export-picture <file>",
		Short: "Write an embedded picture's image to a file",
		Example: "  waxlabel export-picture album.flac -o cover.jpg\n" +
			"  waxlabel export-picture album.flac --picture back-cover -o back.png",
		Long: "Read <file> and write one embedded picture's image bytes verbatim to the -o\n" +
			"path. --picture selects the picture by role name (front-cover, back-cover, ...)\n" +
			"or 1-based dump index; with no --picture it exports the sole front cover, or the\n" +
			"sole picture when the file has exactly one. An explicit selector matching no\n" +
			"picture, or an ambiguous one matching several, is a usage error. The input file\n" +
			"is never modified. A single \"-\" reads the audio from standard input.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inPath := args[0]
			if err := checkEmptyOperands(inPath); err != nil {
				return err
			}
			if strings.TrimSpace(output) == "" {
				return usagef("export-picture requires an output path (-o FILE)")
			}
			// Named image file only; pre-empt checkOutputTarget's set-oriented "-" message.
			if output == stdinArg {
				return usagef("-o - is not supported; export-picture writes a named file")
			}

			ctx := cmd.Context()
			realOf, cleanup, err := readInputs(cmd.InOrStdin(), maxSizeFlag(cmd), args)
			if err != nil {
				return err
			}
			defer cleanup()
			if err := checkRegularInputs(realOf, true, inPath); err != nil {
				return err
			}

			asJSON := jsonMode(cmd)
			errOut := cmd.ErrOrStderr()
			doc, err := parseInput(ctx, realOf(inPath), inPath)
			if err != nil {
				// Human: per-file error line; JSON: raw error to dispatch.
				if asJSON {
					return err
				}
				perFileError(errOut, inPath, err)
				return alreadyRendered(err)
			}

			pic, err := resolveExportPicture(selector, doc.Pictures())
			if err != nil {
				return err
			}
			// Refuse output == input. Unlike set, there is no in-place mode; checkOutputTarget
			// allows that for set's atomic rewrite. os.SameFile catches symlink/hardlink aliases.
			if oi, oerr := os.Stat(output); oerr == nil {
				if ii, ierr := os.Stat(realOf(inPath)); ierr == nil && os.SameFile(oi, ii) {
					return usagef("-o %q is the input file; export-picture cannot write the picture over the audio it reads", output)
				}
			}
			// Overwrite gate like set; collision check keys on realOf(inPath).
			if err := checkOutputTarget(output, realOf(inPath), overwrite); err != nil {
				return err
			}
			if err := os.WriteFile(output, pic.Data, 0o644); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if asJSON {
				return writeJSON(out, toJSONExportPicture(inPath, output, pic))
			}
			fmt.Fprintf(out, "%s: exported %s (%s, %s) to %s\n",
				displayName(inPath), tag.SanitizeLine(pic.Type.String()), tag.SanitizeLine(pic.MIME),
				wl.HumanBytes(int64(len(pic.Data))), displayName(output))
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "write the picture to this path (required); an existing target is refused unless --overwrite")
	cmd.Flags().StringVar(&selector, "picture", "", "which picture to export: a role name (front-cover, back-cover, ...) or a 1-based dump index; defaults to the sole front cover, else the sole picture")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "allow -o to replace an existing output file (by default an existing target is refused)")
	return cmd
}

// resolveExportPicture picks exactly one picture from pics (dump order). Selector is a
// 1-based index or role; zero or many matches are errors (unlike resolveRemovals, where a
// missing role is a no-op). No selector: sole front cover, else sole picture, else usage error.
func resolveExportPicture(selector string, pics []wl.Picture) (wl.Picture, error) {
	if len(pics) == 0 {
		return wl.Picture{}, usagef("file has no embedded pictures to export")
	}
	sel := strings.TrimSpace(selector)
	if sel == "" {
		if fronts := picturesOfType(pics, wl.PicFrontCover); len(fronts) == 1 {
			return pics[fronts[0]], nil
		}
		if len(pics) == 1 {
			return pics[0], nil
		}
		return wl.Picture{}, usagef("file has %d pictures and no single front cover; pass --picture with a role name or a 1-based index (roles: %s)", len(pics), pictureRoleList())
	}
	// 1-based dump index.
	if n, err := strconv.Atoi(sel); err == nil {
		if n < 1 || n > len(pics) {
			return wl.Picture{}, usagef("--picture index %d is out of range (file has %d picture(s))", n, len(pics))
		}
		return pics[n-1], nil
	}
	// Role must match exactly one.
	pt, ok := pictureRole(sel)
	if !ok {
		return wl.Picture{}, usagef("--picture wants a role name or a 1-based index, got %q; valid roles: %s", selector, pictureRoleList())
	}
	matches := picturesOfType(pics, pt)
	switch len(matches) {
	case 0:
		return wl.Picture{}, usagef("--picture %q matched no picture in this file", selector)
	case 1:
		return pics[matches[0]], nil
	default:
		return wl.Picture{}, usagef("--picture %q matched %d pictures; pass a 1-based index (1..%d) to pick one", selector, len(matches), len(pics))
	}
}

// picturesOfType returns indices of pictures of type pt, in order.
func picturesOfType(pics []wl.Picture, pt wl.PictureType) []int {
	var idx []int
	for i, p := range pics {
		if p.Type == pt {
			idx = append(idx, i)
		}
	}
	return idx
}

// jsonExportPicture is the machine-readable export result.
type jsonExportPicture struct {
	SchemaVersion int         `json:"schemaVersion"`
	File          string      `json:"file"`
	Output        string      `json:"output"`
	Picture       jsonPicture `json:"picture"`
}

func toJSONExportPicture(inPath, output string, p wl.Picture) jsonExportPicture {
	return jsonExportPicture{
		SchemaVersion: schemaVersion,
		File:          jsonFileName(inPath),
		Output:        output,
		Picture: jsonPicture{
			Type:        p.Type.String(),
			MIME:        p.MIME,
			Width:       p.Width,
			Height:      p.Height,
			Depth:       p.Depth,
			Colors:      p.Colors,
			Bytes:       len(p.Data),
			Description: p.Description,
		},
	}
}
