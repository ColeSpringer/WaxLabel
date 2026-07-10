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

// newExportPictureCmd builds the "export-picture" command, which writes one embedded
// picture's image bytes to a file. It is read-only on the input: the audio file is never
// modified, only the selected cover is copied out verbatim.
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
			// export-picture writes a named image file; standard output is not a target
			// (checkOutputTarget also rejects "-", but its message names set, so pre-empt it).
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
				// Match the per-file "waxlabel: <path>: <reason>" human line the other commands
				// print; JSON returns the raw error to dispatch, which emits the machine envelope.
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
			// Refuse an output that IS the input file. Unlike set, export-picture has no in-place
			// mode: writing the picture bytes over the audio would destroy it. checkOutputTarget is
			// shared with set, whose -o legitimately targets the input for an atomic in-place rewrite,
			// so it waves output==input through the overwrite gate - which here means os.WriteFile
			// clobbering the audio with the cover. Guard it here, up front and unconditionally (even
			// --overwrite cannot make in-place extraction meaningful). os.SameFile keys on inode
			// identity, so it also catches a symlink to the input and a hardlink sharing its inode
			// that a canonical-path compare would miss; it is false when the output does not yet exist.
			if oi, oerr := os.Stat(output); oerr == nil {
				if ii, ierr := os.Stat(realOf(inPath)); ierr == nil && os.SameFile(oi, ii) {
					return usagef("-o %q is the input file; export-picture cannot write the picture over the audio it reads", output)
				}
			}
			// The output image is a distinct file from the audio input, so the -o overwrite gate
			// refuses replacing an existing target unless --overwrite is passed (consistent with
			// set). realOf(inPath) is the input's real path, the operand the collision check keys on.
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

// resolveExportPicture resolves the --picture selector to exactly one picture in pics (the
// file's pictures in dump order, the same list dump numbers). An explicit selector is a
// 1-based dump index or a cover-art role name and must resolve to exactly one picture: a role
// matching none, or several, is an error - unlike resolveRemovals, where a role matching
// nothing is a harmless no-op (removal is a bulk operation; extract must pick a single file to
// write). With no selector it defaults to the sole front cover, else the sole picture of any
// type, else a usage error asking for --picture rather than silently picking among several.
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
	// A 1-based dump index picks exactly one picture.
	if n, err := strconv.Atoi(sel); err == nil {
		if n < 1 || n > len(pics) {
			return wl.Picture{}, usagef("--picture index %d is out of range (file has %d picture(s))", n, len(pics))
		}
		return pics[n-1], nil
	}
	// A role name must resolve to exactly one picture.
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

// picturesOfType returns the indices (into pics) of every picture of type pt, in order.
func picturesOfType(pics []wl.Picture, pt wl.PictureType) []int {
	var idx []int
	for i, p := range pics {
		if p.Type == pt {
			idx = append(idx, i)
		}
	}
	return idx
}

// jsonExportPicture is the machine-readable result of an export-picture: the input file, the
// output path written, and the exported picture's metadata (the same jsonPicture dump emits).
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
