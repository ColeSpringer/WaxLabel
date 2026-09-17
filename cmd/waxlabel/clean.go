package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/spf13/cobra"
)

// cleanAgeGate: temps newer than this are assumed in-flight unless --all.
const cleanAgeGate = time.Hour

// newCleanCmd builds "clean": list (or --remove) .waxlabel-*.tmp leftovers. SIGINT/
// SIGTERM already drain temps in main; this covers SIGKILL and power loss. Age-gated
// to one hour; --all lifts the gate.
func newCleanCmd() *cobra.Command {
	var recursive, remove, all bool
	cmd := &cobra.Command{
		Use:     "clean <dir>...",
		Short:   "List or remove temp files left by an interrupted write",
		Example: "  waxlabel clean ~/Music\n  waxlabel clean --recursive --remove ~/Music",
		Long: "Find the .waxlabel-*.tmp files a killed write left beside its target. Without\n" +
			"--remove nothing is deleted. Files modified within the last hour are assumed to\n" +
			"belong to a write in progress and are skipped unless --all is given.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClean(cmd, args, recursive, remove, all)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&recursive, "recursive", false, "descend into subdirectories (hidden directories are skipped, as the audio walk does)")
	f.BoolVar(&remove, "remove", false, "delete the files listed")
	f.BoolVar(&all, "all", false, "include files modified within the last hour")
	return markListCommand(cmd)
}

// jsonClean is one leftover in the JSON report. Removed is false on dry run or failed remove.
type jsonClean struct {
	SchemaVersion int          `json:"schemaVersion"`
	File          string       `json:"file"`
	SizeBytes     int64        `json:"sizeBytes"`
	Modified      string       `json:"modified"`
	Removed       bool         `json:"removed"`
	Error         *jsonErrBody `json:"error,omitempty"`
}

// errorPath returns the path on the error when present, else fallback. Walk failures
// name the unreadable subdirectory.
func errorPath(err error, fallback string) string {
	var pe *fs.PathError
	if errors.As(err, &pe) && pe.Path != "" {
		return pe.Path
	}
	return fallback
}

// humanAge formats leftover age. humanDuration is playback time ("72:00:00" for 3 days).
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d minute(s)", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hour(s)", int(d.Hours()))
	default:
		return fmt.Sprintf("%d day(s)", int(d.Hours()/24))
	}
}

// leftoverTemp is one scan candidate before removal.
type leftoverTemp struct {
	path string
	size int64
	mod  time.Time
}

// runClean scans each directory, then lists or removes. Unreadable dirs are per-path
// errors (exit 6); other args still run.
func runClean(cmd *cobra.Command, dirs []string, recursive, remove, all bool) error {
	if err := checkEmptyOperands(dirs...); err != nil {
		return err
	}
	// Stat every operand before any delete so a mid-run usage error cannot leave
	// earlier removals unreported.
	stats := make([]error, len(dirs))
	for i, dir := range dirs {
		info, err := os.Stat(dir)
		if err == nil && !info.IsDir() {
			return usagef("%s is not a directory; clean takes directories", dir)
		}
		stats[i] = err
	}
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	asJSON := jsonMode(cmd)
	var items []any
	var worstErr error
	found, removed := 0, 0
	// report records a per-path failure without stopping. path is what failed (subdir for walks).
	report := func(path string, err error) {
		if worseError(worstErr, err) {
			worstErr = err
		}
		if asJSON {
			items = append(items, errorEntry(path, err))
		} else {
			perFileError(errOut, path, err)
		}
	}
	for i, dir := range dirs {
		if err := stats[i]; err != nil {
			report(dir, err)
			continue
		}
		temps, unreadable, err := scanLeftovers(cmd.Context(), dir, recursive, all)
		for _, uerr := range unreadable {
			report(errorPath(uerr, dir), uerr)
		}
		if err != nil {
			report(dir, err)
			continue
		}
		for _, t := range temps {
			found++
			jc := jsonClean{SchemaVersion: schemaVersion, File: jsonFileName(t.path), SizeBytes: t.size, Modified: t.mod.UTC().Format(time.RFC3339)}
			if remove {
				if rerr := os.Remove(t.path); rerr != nil {
					if worseError(worstErr, rerr) {
						worstErr = rerr
					}
					if asJSON {
						jc.Error = &jsonErrBody{Code: classifyError(rerr).code, Message: perFileReason(rerr)}
					} else {
						perFileError(errOut, t.path, rerr)
					}
				} else {
					jc.Removed = true
					removed++
				}
			}
			if asJSON {
				items = append(items, jc)
				continue
			}
			fmt.Fprintf(out, "  %s  %s  %s old\n", displayName(t.path), wl.HumanBytes(t.size), humanAge(time.Since(t.mod)))
		}
	}
	if asJSON {
		if werr := emitJSONList(out, items); werr != nil && worstErr == nil {
			return werr
		}
		return alreadyRendered(worstErr)
	}
	switch {
	// Unreadable dirs: don't claim "none found" for a tree we couldn't fully scan.
	case found == 0 && worstErr != nil:
		fmt.Fprintln(out, "no leftover temp files where the scan could look")
	case found == 0:
		fmt.Fprintln(out, "no leftover temp files")
	case remove:
		fmt.Fprintf(out, "removed %d leftover temp file(s)\n", removed)
	default:
		fmt.Fprintf(out, "%d leftover temp file(s) found; pass --remove to delete them\n", found)
	}
	return alreadyRendered(worstErr)
}

// scanLeftovers collects temps under dir, sorted. Non-recursive: dir entries only.
// Recursive: walk like walkAudioFiles (prune hidden dirs). Without --all, skip files
// newer than the age gate (likely in-flight writes).
//
// Unreadable subtrees are reported alongside finds, not swallowed: one bad album must
// not stop --remove elsewhere, and silence must not imply a clean tree.
func scanLeftovers(ctx context.Context, dir string, recursive, all bool) ([]leftoverTemp, []error, error) {
	var out []leftoverTemp
	var unreadable []error
	cutoff := time.Now().Add(-cleanAgeGate)
	consider := func(path string, d fs.DirEntry) {
		if !wl.IsTempFileName(d.Name()) {
			return
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return
		}
		if !all && info.ModTime().After(cutoff) {
			return
		}
		out = append(out, leftoverTemp{path: path, size: info.Size(), mod: info.ModTime()})
	}
	if !recursive {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, nil, err
		}
		for _, d := range entries {
			if d.IsDir() {
				continue
			}
			consider(filepath.Join(dir, d.Name()), d)
		}
	} else {
		// WalkDir never follows links; resolve symlink roots like the audio walk.
		walkRoot, linked := resolvedWalkRoot(dir)
		err := filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if err != nil {
				// Record and continue; WalkDir already skipped this node.
				unreadable = append(unreadable, err)
				return nil
			}
			if d.IsDir() {
				if path != walkRoot && strings.HasPrefix(d.Name(), ".") {
					return fs.SkipDir
				}
				return nil
			}
			consider(rebaseWalkPath(dir, walkRoot, linked, path), d)
			return nil
		})
		if err != nil {
			return nil, unreadable, err // cancel only
		}
	}
	slices.SortFunc(out, func(a, b leftoverTemp) int { return strings.Compare(a.path, b.path) })
	return out, unreadable, nil
}
