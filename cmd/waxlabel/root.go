package main

import (
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
)

// newRootCmd builds the root command, its persistent --json flag, and the
// subcommand set. Cobra adds the help and completion commands on its own.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "waxlabel",
		Short: "Read and write audio-file metadata",
		Long: "WaxLabel reads and writes audio-file tags and embedded cover art,\n" +
			"reimplemented from public specifications. The CLI dogfoods the library:\n" +
			"dump reads a file, plan previews a write, set applies edits, and verify\n" +
			"reports the audio-essence identity used for deduplication.\n\n" +
			"All data commands support --json for scriptable output.",
		Version: resolveVersion(),
		// Errors and usage are rendered once, centrally, in dispatch; silence
		// cobra's own printing so failures are not reported twice.
		SilenceErrors: true,
		SilenceUsage:  true,
		// A bare `waxlabel` reaches this RunE (cobra dispatches a subcommand, resolves
		// --help/-h, and rejects an unknown command before it), so treat "no command"
		// as a usage error - exit 2, not cobra's default help-and-exit-0 - letting a
		// script tell it apart from success.
		RunE: func(cmd *cobra.Command, _ []string) error { return noCommand(cmd) },
	}
	root.PersistentFlags().Bool("json", false, "emit machine-readable JSON instead of human output")
	root.AddCommand(
		newDumpCmd(),
		newPlanCmd(),
		newSetCmd(),
		newVerifyCmd(),
		newCopyCmd(),
		newDiffCmd(),
		newLintCmd(),
		newCapsCmd(),
		newKeysCmd(),
		newVersionCmd(),
	)
	// Replace cobra's help command so an unknown topic exits non-zero, matching an
	// unknown command. Register before wrapUsageErrors so it picks up the same
	// usage-error wrapping as every other command.
	root.SetHelpCommand(newHelpCmd())
	wrapUsageErrors(root)
	return root
}

// noCommand reports a bare `waxlabel` (no subcommand) as a usage error so a script
// can distinguish it from a successful run - cobra's default would print help and
// exit 0. For a human it prints the full help to stderr and returns the error
// already-rendered (exit 2, no second line); under --json it returns the usage error
// unrendered so dispatch emits the machine-readable error envelope instead of human
// help text. cobra resolves --help/-h before RunE, so those still print to stdout and
// exit 0.
func noCommand(cmd *cobra.Command) error {
	if jsonMode(cmd) {
		return &usageError{msg: "no command given", cmd: "waxlabel", wantsHint: true}
	}
	cmd.SetOut(cmd.ErrOrStderr())
	if err := cmd.Help(); err != nil {
		return err
	}
	// Print the failure line after the help text so the non-zero exit is obvious in a
	// log that captured stderr - the help alone reads like a successful invocation
	// (JSON mode already returns the error envelope above). (#8)
	fmt.Fprintln(cmd.ErrOrStderr(), "waxlabel: no command given")
	return alreadyRendered(usagef("no command given"))
}

// newHelpCmd replaces cobra's default help command so "help <bogus>" exits 2 (a
// usage error) instead of cobra's silent exit 0 - an unknown help topic is as much
// a mistake as an unknown command. A bare "help" and a valid "help <command>" still
// print help and exit 0. The "--help" flag uses cobra's help func, not this
// command, so it is unaffected.
func newHelpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "help [command]",
		Short: "Help about any command",
		RunE: func(c *cobra.Command, args []string) error {
			// Find resolves args against the command tree, returning the deepest
			// matching command and any unresolved trailing tokens. An unknown top-level
			// topic yields a non-nil error (via legacyArgs); a valid command followed by
			// a token that names no subcommand (e.g. "help set bogus") resolves to that
			// command with the stray token left over - reject that too, so it does not
			// silently print the command's help and exit 0. Cobra strips the help
			// command's own flags before RunE, so leftover tokens here are unresolved
			// topic words, not flags (a bad flag is already caught by FlagErrorFunc).
			target, remaining, err := c.Root().Find(args)
			if err != nil || target == nil || len(remaining) > 0 {
				return usagef("unknown help topic %q", strings.Join(args, " "))
			}
			target.InitDefaultHelpFlag()
			return target.Help()
		},
	}
}

// newVersionCmd registers an explicit "version" subcommand so the conventional
// "waxlabel version" spelling works, not just the --version flag (which a bare
// "version" word would otherwise hit as an unknown command). It prints the same
// line cobra's --version template produces - "waxlabel version <v>" - so the two
// cannot disagree (U3); resolveVersion is the single source for the value.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the waxlabel version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// With --json, emit the documented object so a script reads the version like
			// every other data command's machine output, rather than parsing the text line
			// (F6). resolveVersion stays the single source for the value.
			if jsonMode(cmd) {
				return writeJSON(cmd.OutOrStdout(), jsonVersion{SchemaVersion: schemaVersion, Version: resolveVersion()})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "waxlabel version %s\n", resolveVersion())
			return nil
		},
	}
}

// jsonVersion is the machine-readable form of the version command's output.
type jsonVersion struct {
	SchemaVersion int    `json:"schemaVersion"`
	Version       string `json:"version"`
}

// wrapUsageErrors maps every command's flag- and argument-parsing failures to a
// usageError (exit code 2) and silences cobra's own error/usage printing, so the
// central renderer in dispatch reports each failure exactly once.
func wrapUsageErrors(cmd *cobra.Command) {
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	// A flag-parse or arg-count failure dead-ends with no pointer to help (cobra's
	// usage is silenced), so capture the resolved command path and request the help
	// hint (M5). c already holds the resolved command at both sites.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		ue := &usageError{msg: err.Error(), cmd: c.CommandPath(), wantsHint: true}
		// A leading-dash file path (-track.flac / --track.flac) reaches cobra as an
		// unknown flag; when the offending token looks like a path, point at the "--"
		// end-of-flags marker instead of the generic --help pointer (U5). A genuine flag
		// typo (--bogus) is not path-like and keeps the help hint. dashPathHint overrides
		// wantsHint in classifyError.
		if msg := err.Error(); (strings.HasPrefix(msg, "unknown flag") || strings.HasPrefix(msg, "unknown shorthand")) && looksLikePathFlag(msg) {
			ue.hint = dashPathHint
		}
		return ue
	})
	if inner := cmd.Args; inner != nil {
		cmd.Args = func(c *cobra.Command, args []string) error {
			if err := inner(c, args); err != nil {
				return &usageError{msg: err.Error(), cmd: c.CommandPath(), wantsHint: true}
			}
			return nil
		}
	}
	for _, sub := range cmd.Commands() {
		wrapUsageErrors(sub)
	}
}

// resolveVersion reports the build version from the embedded module info, or
// "dev" for an untagged build (the common case during development).
func resolveVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}
