package main

import (
	"runtime/debug"

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
			"Every command supports --json for scriptable output.",
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
	wrapUsageErrors(root)
	return root
}

// wrapUsageErrors maps every command's flag- and argument-parsing failures to a
// usageError (exit code 2) and silences cobra's own error/usage printing, so the
// central renderer in dispatch reports each failure exactly once.
func wrapUsageErrors(cmd *cobra.Command) {
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &usageError{msg: err.Error()}
	})
	if inner := cmd.Args; inner != nil {
		cmd.Args = func(c *cobra.Command, args []string) error {
			if err := inner(c, args); err != nil {
				return &usageError{msg: err.Error()}
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
