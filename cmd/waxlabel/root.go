package main

import (
	"fmt"
	"io"
	"runtime/debug"
	"strings"

	wl "github.com/colespringer/waxlabel"
	"github.com/spf13/cobra"
)

// newRootCmd wires the root command, --json, and subcommands. Cobra adds help/completion on its own.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "waxlabel",
		Short: "Read and write audio-file metadata",
		Long: "WaxLabel reads and writes audio-file tags and embedded cover art,\n" +
			"reimplemented from public specifications. The CLI dogfoods the library:\n" +
			"dump reads a file, plan previews a write, set applies edits, and verify\n" +
			"reports the audio-essence identity used for deduplication.\n\n" +
			"All data commands support --json for scriptable output.",
		// cobra.Version unset: built-in --version runs before RunE and ignores --json.
		// Custom --version handled in RunE; printVersion shared with "version" subcommand.
		//
		// Silence cobra errors/usage; dispatch renders failures once.
		SilenceErrors: true,
		SilenceUsage:  true,
		// Bare waxlabel hits RunE (subcommands, --help, unknown cmds handled earlier).
		// No subcommand → usage error (exit 2), not help+exit 0. --version honors --json.
		RunE: func(cmd *cobra.Command, _ []string) error {
			if v, _ := cmd.Flags().GetBool("version"); v {
				return printVersion(cmd)
			}
			return noCommand(cmd)
		},
	}
	root.PersistentFlags().Bool("json", false, "emit machine-readable JSON instead of human output")
	// Cap streamed "-"/stdin size (e.g. 2GiB, 500MB; 0 = unlimited). stdin commands only; named files not buffered.
	maxSize := byteSizeValue(wl.DefaultMaxSourceBytes)
	root.PersistentFlags().Var(&maxSize, "max-size", "maximum size of a streamed '-'/stdin input (e.g. 2GiB, 500MB; 0 = unlimited)")
	// Custom --version routes through RunE for --json support.
	root.Flags().Bool("version", false, "print the waxlabel version and exit")
	root.AddCommand(
		newDumpCmd(),
		newPlanCmd(),
		newSetCmd(),
		newVerifyCmd(),
		newCopyCmd(),
		newDiffCmd(),
		newExportPictureCmd(),
		newLintCmd(),
		newCleanCmd(),
		newCapsCmd(),
		newKeysCmd(),
		newVersionCmd(),
		newCompletionCmd(),
	)
	// Custom help: unknown topic exits 2 like unknown command. Register before wrapUsageErrors.
	root.SetHelpCommand(newHelpCmd())
	wrapUsageErrors(root)
	return root
}

// Bare waxlabel (no subcommand) is usage error (exit 2); cobra default is help+exit 0.
// Human: help on stderr + already-rendered error. --json: unrendered usageError for dispatch.
// --help/-h still exit 0 (resolved before RunE).
func noCommand(cmd *cobra.Command) error {
	if jsonMode(cmd) {
		return &usageError{msg: "no command given", cmd: "waxlabel", wantsHint: true}
	}
	cmd.SetOut(cmd.ErrOrStderr())
	if err := cmd.Help(); err != nil {
		return err
	}
	// Failure line after help so stderr logs show non-zero exit.
	fmt.Fprintln(cmd.ErrOrStderr(), "waxlabel: no command given")
	return alreadyRendered(usagef("no command given"))
}

// Custom help: unknown topic exits 2 (cobra default is 0). Valid topics and bare "help" exit 0.
// --help flag unchanged.
func newHelpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "help [command]",
		Short: "Help about any command",
		RunE: func(c *cobra.Command, args []string) error {
			// Reject unknown topics and trailing junk (e.g. "help set bogus").
			// Leftover tokens are topic words, not flags (FlagErrorFunc handles flags).
			target, remaining, err := c.Root().Find(args)
			if err != nil || target == nil || len(remaining) > 0 {
				return usagef("unknown help topic %q", strings.Join(args, " "))
			}
			target.InitDefaultHelpFlag()
			return target.Help()
		},
	}
}

// "version" subcommand for bare "waxlabel version". Same output as --version; resolveVersion is source.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the waxlabel version",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return printVersion(cmd) },
	}
}

// Replaces cobra completion: unknown shell/extra args exit 2. cobra's non-runnable parent skips
// NoArgs ("completion zzz" exits 0); runnable parent + per-shell NoArgs fixes that. Named
// "completion" prevents cobra's default. Generators use RunE-time OutOrStdout for redirects.
func newCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate a shell completion script",
		Long: "Generate a shell completion script for waxlabel. Run the subcommand for your\n" +
			"shell (bash, zsh, fish, or powershell) and source or install its output; see\n" +
			"each subcommand's --help for shell-specific instructions.",
		Args: cobra.NoArgs,
		// Runnable parent so NoArgs runs; bare "completion" prints help.
		RunE: func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	for _, sh := range []struct {
		use, short string
		gen        func(root *cobra.Command, w io.Writer) error
	}{
		{"bash", "Generate the bash completion script", func(r *cobra.Command, w io.Writer) error { return r.GenBashCompletionV2(w, true) }},
		{"zsh", "Generate the zsh completion script", func(r *cobra.Command, w io.Writer) error { return r.GenZshCompletion(w) }},
		{"fish", "Generate the fish completion script", func(r *cobra.Command, w io.Writer) error { return r.GenFishCompletion(w, true) }},
		{"powershell", "Generate the PowerShell completion script", func(r *cobra.Command, w io.Writer) error { return r.GenPowerShellCompletionWithDesc(w) }},
	} {
		cmd.AddCommand(&cobra.Command{
			Use:   sh.use,
			Short: sh.short,
			Args:  cobra.NoArgs,
			RunE:  func(c *cobra.Command, _ []string) error { return sh.gen(c.Root(), c.Root().OutOrStdout()) },
		})
	}
	return cmd
}

// printVersion for --version and "version" subcommand; honors --json.
func printVersion(cmd *cobra.Command) error {
	if jsonMode(cmd) {
		return writeJSON(cmd.OutOrStdout(), jsonVersion{SchemaVersion: schemaVersion, Version: resolveVersion()})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "waxlabel version %s\n", resolveVersion())
	return nil
}

// jsonVersion is JSON output for version.
type jsonVersion struct {
	SchemaVersion int    `json:"schemaVersion"`
	Version       string `json:"version"`
}

// wrapUsageErrors: flag/arg failures → usageError (exit 2); dispatch renders once.
func wrapUsageErrors(cmd *cobra.Command) {
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	// Silenced usage drops help pointer; set cmd path and wantsHint.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		ue := &usageError{msg: err.Error(), cmd: c.CommandPath(), wantsHint: true}
		// Path-like unknown flag (-track.flac) gets "--" hint, not --help.
		// dashPathHint overrides wantsHint in classifyError.
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

// Set via -ldflags "-X main.version=<tag>" for release builds; overrides VCS-derived build info.
var version string

// resolveVersion: ldflags version, else build info, else "dev".
func resolveVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}
