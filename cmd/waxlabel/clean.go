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

// cleanAgeGate is how recently a temp file must have been written to be assumed part of a
// write still in flight. Nothing newer is listed or removed unless --all is given.
const cleanAgeGate = time.Hour

// newCleanCmd builds "clean": list (and with --remove, delete) the .waxlabel-*.tmp files a
// killed write left beside its target. A write interrupted by SIGINT or SIGTERM removes its
// own temp (the signal path in main.go drains them); this is for SIGKILL and power loss.
// Age-gated to an hour so a write in flight is never touched; --all lifts the gate.
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

// jsonClean is one leftover temp file in the machine-readable report. Removed is false on a
// dry run and on a removal that failed, which records its own error entry.
type jsonClean struct {
	SchemaVersion int          `json:"schemaVersion"`
	File          string       `json:"file"`
	SizeBytes     int64        `json:"sizeBytes"`
	Modified      string       `json:"modified"`
	Removed       bool         `json:"removed"`
	Error         *jsonErrBody `json:"error,omitempty"`
}

// errorPath names the path an error happened on, for an error that carries one, falling
// back to the argument that reached it. A walk failure inside a tree names the subdirectory
// nobody could open, which is the path a user has to act on.
func errorPath(err error, fallback string) string {
	var pe *fs.PathError
	if errors.As(err, &pe) && pe.Path != "" {
		return pe.Path
	}
	return fallback
}

// humanAge renders how long ago a leftover was written. humanDuration formats a playback
// position, so it would print three days as "72:00:00"; a leftover's age is measured in
// days once a library has sat untouched for a while.
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

// leftoverTemp is one candidate found by the scan, before any removal.
type leftoverTemp struct {
	path string
	size int64
	mod  time.Time
}

// runClean scans each directory argument, then lists or removes what it found. A directory
// that cannot be scanned is a per-path error (exit 6) and the other arguments still run.
func runClean(cmd *cobra.Command, dirs []string, recursive, remove, all bool) error {
	if err := checkEmptyOperands(dirs...); err != nil {
		return err
	}
	// Every operand is stat'd once, before anything is deleted: a usage error raised part
	// way through would leave files already removed for earlier arguments unreported.
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
	// report records a per-path failure without stopping the run. path names what actually
	// failed, which for a walk error is the subdirectory, not the argument that reached it.
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
	// A directory the scan could not read makes "none found" a claim nobody can stand
	// behind, so say what was seen rather than that the tree is clean.
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

// scanLeftovers collects the temp files under dir, sorted by path. Without recursive it
// reads the directory's own entries; with it, a walk that prunes hidden directories the way
// walkAudioFiles does, so a .git or .cache tree is not searched. Without all, a file written
// within the last hour is left alone: it most likely belongs to a write still running.
//
// A subtree the walk cannot read is reported, not swallowed: saying nothing over a directory
// nobody could look inside would tell a user their library is clean when the one place a
// leftover might sit was never opened. The audio walk reports the same condition. It is
// reported alongside what the scan did find, never instead of it, so one unreadable album
// does not stop --remove from deleting the leftovers everywhere else.
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
		// WalkDir lstats its root and never follows links, so a symlinked music directory
		// would yield one node it refuses to descend. Resolve the root the way the audio
		// walk does and map matches back under the name the user passed.
		walkRoot, linked := resolvedWalkRoot(dir)
		err := filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if err != nil {
				// Record it and keep walking: WalkDir has already skipped this node, and
				// the rest of the tree is still worth scanning. The error carries the path
				// it happened on, which is the subdirectory, not the argument.
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
			return nil, unreadable, err // only a cancelled context reaches here
		}
	}
	slices.SortFunc(out, func(a, b leftoverTemp) int { return strings.Compare(a.path, b.path) })
	return out, unreadable, nil
}
