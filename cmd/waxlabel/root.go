package main

import (
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
	)
	// Replace cobra's help command so an unknown topic exits non-zero, matching an
	// unknown command. Register before wrapUsageErrors so it picks up the same
	// usage-error wrapping as every other command.
	root.SetHelpCommand(newHelpCmd())
	wrapUsageErrors(root)
	return root
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
		return &usageError{msg: err.Error(), cmd: c.CommandPath(), wantsHint: true}
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
