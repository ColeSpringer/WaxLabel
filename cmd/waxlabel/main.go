// Command waxlabel is the command-line interface to the WaxLabel audio-metadata
// library. It reads and writes audio-file tags and embedded cover art for the
// formats the library supports (FLAC, Ogg Vorbis/Opus, MP3, WAV, MP4/M4A/M4B,
// AAC/ADTS, Matroska/WebM, and AIFF/AIFF-C) and exists to dogfood the library
// end to end - every command maps directly onto the public API.
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
	// Two-stage interrupt: the first signal cancels in-flight work (parse, hash,
	// and the atomic write all honor ctx); a second forces an immediate exit - the
	// escape hatch for an operation that cannot observe cancellation (e.g. blocked
	// in an fsync syscall). The handler runs on its own goroutine so the second
	// signal fires even while the main goroutine is stuck. os.Exit skips deferred
	// calls, so the signal registration is released explicitly before it, and the
	// forced-exit path drains the cleanup registry so a buffered-stdin temp file is
	// still removed.
	// WithCancelCause lets the pipe goroutine cancel with a distinct sentinel (errBrokenPipe)
	// while the interrupt path cancels with the default context.Canceled: a canceled op returns
	// context.Canceled either way, so the cause is the only thing that tells a benign closed pipe
	// (exit 0) from a real Ctrl-C (exit 130). dispatch reads context.Cause(ctx) to make that call.
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

	// SIGPIPE (a closed output pipe, e.g. `dump - | head`) is caught on its OWN channel: without
	// catching it, the next stdout write default-kills the process before any cleanup runs.
	// Catching it turns the write into an EPIPE the command unwinds through its deferred cleanup,
	// and cancelling stops in-flight work promptly - but keeping it OUT of the interrupt two-stage
	// machine means a broken pipe does not consume a later Ctrl-C's graceful-cancel stage. The
	// distinct errBrokenPipe cause is what lets dispatch classify this as broken-pipe (exit 0,
	// silent) rather than a cancel. The constant compiles on every target; Windows defines it but
	// never delivers it.
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
// rendering any terminal error exactly once - as a JSON envelope on stdout under
// --json, or a human-readable line on stderr otherwise. It takes its streams and
// arguments as parameters so tests can drive it without spawning a process; stdin
// is the source for the "-" path sentinel.
func dispatch(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Wrap both human-output streams in the sanitizing boundary so renderers cannot
	// leak a terminal-control sequence from untrusted file bytes (a tag value, a
	// native block label, a hostile filename, an error line) to the terminal. Every
	// subcommand inherits these via cobra's OutOrStdout/ErrOrStderr, and the --json
	// paths unwrap to the raw stream inside writeJSON, so the machine contract still
	// carries exact bytes. Flush each before
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
	// A broken output pipe (`waxlabel dump --recursive DIR | head`) is benign: re-tag it as
	// broken-pipe (exit 0, silent). It surfaces two ways:
	//   - a synchronous EPIPE returned from the terminal write itself (dump --json's single
	//     emitJSONList, or a single-result command like diff/copy). The write is definitive proof
	//     the pipe closed, so do NOT gate this on the SIGPIPE cancel cause - that goroutine is
	//     async and may not have run yet, which would otherwise misclassify it as exit-6 "io".
	//     WaxLabel only writes to stdout/stderr, so an EPIPE is always a closed output pipe.
	//   - a context.Canceled whose cancel cause is errBrokenPipe: text multi-file mode, where the
	//     pipe broke mid-render and a later parse observed the cancel. Gated on the cause to keep a
	//     real Ctrl-C (also context.Canceled, but cause context.Canceled) exiting 130.
	if errors.Is(err, syscall.EPIPE) ||
		(errors.Is(err, context.Canceled) && errors.Is(context.Cause(ctx), errBrokenPipe)) {
		err = errBrokenPipe
	}
	// A command that already wrote its own output (e.g. dump emitting per-file
	// error objects) carries an alreadyRenderedError: keep its exit class but do
	// not render a second time.
	if _, rendered := errors.AsType[*alreadyRenderedError](err); rendered {
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
	// A list command's --json output is an array, so wrap its pre-flight error in the
	// same single-element array shape rather than a bare object - resolved from the same
	// root via cobra's Find, so there is no separate command-name list to drift.
	renderError(out, asJSON, emitsJSONList(root, args), err)
	return exitCodeFor(err)
}
