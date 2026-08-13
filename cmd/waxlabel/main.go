// Command waxlabel is the command-line interface to the WaxLabel audio-metadata
// library. It reads and writes audio-file tags and embedded cover art for the
// formats the library supports (FLAC, Ogg Vorbis/Opus, MP3, WAV, MP4/M4A/M4B,
// AAC/ADTS, Matroska/WebM, and AIFF/AIFF-C). Every command maps directly onto the
// public API, so it dogfoods the library end to end.
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
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// Two-stage interrupt: the first signal cancels in-flight work, a second forces an
	// exit for an operation that cannot observe cancellation (blocked in fsync, say).
	// Its own goroutine, so the second signal lands even with main stuck. os.Exit skips
	// defers, so the forced path drains the cleanup registry itself.
	//
	// A canceled op returns context.Canceled either way, so the cancel CAUSE is the only
	// thing telling a benign closed pipe (exit 0) from a real Ctrl-C (exit 130).
	ctx, cancel := context.WithCancelCause(context.Background())
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		cancel(nil) // nil cause -> context.Canceled: a real interrupt, exit 130
		<-sig
		runCleanups()
		os.Exit(130)
	}()

	// SIGPIPE gets its OWN channel: uncaught it kills the process before any cleanup,
	// and folding it into the two-stage machine above would let a broken pipe consume a
	// later Ctrl-C's graceful stage. Windows defines the constant but never delivers it.
	pipe := make(chan os.Signal, 1)
	signal.Notify(pipe, syscall.SIGPIPE)
	go func() {
		<-pipe
		cancel(errBrokenPipe)
	}()

	code := dispatch(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	signal.Stop(sig)
	signal.Stop(pipe)
	cancel(nil)
	os.Exit(code)
}

// dispatch builds and runs the root command and returns the process exit code,
// rendering any terminal error exactly once: a JSON envelope on stdout under --json, a
// human line on stderr otherwise. Streams are parameters so tests can drive it without
// spawning a process.
func dispatch(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Sanitizing boundary, so no renderer can leak a terminal-control sequence from
	// untrusted file bytes. Subcommands inherit these via cobra; the --json paths unwrap
	// to the raw stream in writeJSON so the machine contract keeps exact bytes. Closed on
	// return to flush a held-back partial rune.
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
	// A closed output pipe is benign: re-tag as broken-pipe (exit 0, silent). Two shapes.
	// A synchronous errno from the terminal write is definitive proof, so it is NOT gated
	// on the cancel cause, which the async SIGPIPE goroutine may not have set yet.
	// A context.Canceled IS gated on the cause, to keep a real Ctrl-C exiting 130.
	if isBrokenPipe(err) ||
		(errors.Is(err, context.Canceled) && errors.Is(context.Cause(ctx), errBrokenPipe)) {
		err = errBrokenPipe
	}
	// A command that already wrote its own output keeps its exit class but is not
	// rendered twice.
	if _, rendered := errors.AsType[*alreadyRenderedError](err); rendered {
		return exitCodeFor(err)
	}
	// Scan raw args rather than the parsed flag: cobra may have aborted before binding
	// it, and a --json caller still expects the error as JSON on stdout.
	asJSON := wantsJSON(args)
	// Cobra does not type unknown-command/flag errors, so classify them first.
	err = normalizeExecuteError(err)
	out := io.Writer(serr)
	if asJSON {
		out = sout
	}
	// A list command's --json output is an array, so its pre-flight error is wrapped in a
	// single-element array. Resolved via cobra's Find, so no command-name list can drift.
	renderError(out, asJSON, emitsJSONList(root, args), err)
	return exitCodeFor(err)
}
