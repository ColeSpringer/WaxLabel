// Command waxlabel is the command-line interface to the WaxLabel audio-metadata
// library. It reads and writes audio-file tags and embedded cover art for the
// formats the library supports (FLAC today; more as codecs land) and exists to
// dogfood the library end to end - every command maps directly onto the public
// API.
//
// Usage:
//
//	waxlabel [--json] <command> [flags] <file>...
//
// Commands:
//
//	dump     show a file's tags, properties, pictures, and warnings
//	plan     show what an edit would write, without writing it
//	set      apply tag edits and save the file
//	lint     report metadata issues (and optionally fix the safe ones)
//	verify   compute audio-essence (and optionally whole-file) identity
//	caps     show which metadata a format can edit, and how
//	copy     copy metadata from one file onto another (cross-format)
//	diff     compare two files' canonical metadata
//
// Run "waxlabel <command> --help" for a command's flags, and see README.md for
// the exit-code table.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/colespringer/waxlabel/waxerr"
)

func main() {
	// Two-stage interrupt: the first signal cancels in-flight work (parse, hash,
	// and the atomic write all honor ctx); a second forces an immediate exit - the
	// escape hatch for an operation that cannot observe cancellation (e.g. blocked
	// in an fsync syscall). The handler runs on its own goroutine so the second
	// signal fires even while the main goroutine is stuck. os.Exit skips deferred
	// calls, so the signal registration is released explicitly before it.
	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		cancel()
		<-sig
		os.Exit(130)
	}()
	code := dispatch(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	signal.Stop(sig)
	cancel()
	os.Exit(code)
}

// dispatch builds and runs the root command and returns the process exit code,
// rendering any terminal error exactly once - as a JSON envelope on stdout under
// --json, or a human-readable line on stderr otherwise. It takes its streams and
// arguments as parameters so tests can drive it without spawning a process; stdin
// is the source for the "-" path sentinel.
func dispatch(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Wrap both human-output streams in the sanitizing boundary so no renderer -
	// current or future - can leak a terminal-control sequence from untrusted file
	// bytes (a tag value, a native block label, a hostile filename, an error line)
	// to the terminal. Every subcommand inherits these via cobra's OutOrStdout/
	// ErrOrStderr, and the --json paths unwrap to the raw stream inside writeJSON,
	// so the machine contract still carries exact bytes. Flush each before
	// returning so a held-back trailing partial rune is not dropped.
	sout := newSanitizingWriter(stdout)
	serr := newSanitizingWriter(stderr)
	defer sout.Close()
	defer serr.Close()

	root := newRootCmd()
	root.SetIn(stdin)
	root.SetOut(sout)
	root.SetErr(serr)
	root.SetArgs(args)

	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	// A command that already wrote its own output (e.g. dump emitting per-file
	// error objects) carries an alreadyRenderedError: keep its exit class but do
	// not render a second time.
	if _, rendered := waxerr.AsType[*alreadyRenderedError](err); rendered {
		return exitCodeFor(err)
	}
	// Route the terminal error to the right stream. Scan the raw args for --json
	// rather than reading the parsed flag: cobra may have aborted (unknown command
	// or bad flag) before binding the persistent flag, and a --json caller still
	// expects the error as JSON on stdout.
	asJSON := wantsJSON(args)
	// Cobra does not type unknown-command/flag errors, so classify them first.
	err = normalizeExecuteError(err)
	out := io.Writer(serr)
	if asJSON {
		out = sout
	}
	renderError(out, asJSON, err)
	return exitCodeFor(err)
}
