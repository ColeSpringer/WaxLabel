// Command waxlabel is the CLI for the WaxLabel audio-metadata library. It reads
// and writes tags and cover art for the formats the library supports (FLAC, Ogg
// Vorbis/Opus/FLAC, MP3, WAV, MP4/M4A/M4B, AAC/ADTS, Matroska/WebM, AIFF/AIFF-C,
// WavPack, Monkey's Audio, Musepack; WMA/ASF read-only).
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
// Run "waxlabel <command> --help" for flags; see README.md for exit codes.
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
	// First signal cancels in-flight work; a second forces exit for ops that cannot
	// observe cancellation (e.g. blocked in fsync). Own goroutine so the second
	// signal still lands if main is stuck. os.Exit skips defers, so the forced path
	// drains the cleanup registry itself.
	//
	// A canceled op always returns context.Canceled; only the cancel cause separates
	// a benign closed pipe (exit 0) from Ctrl-C (exit 130).
	ctx, cancel := context.WithCancelCause(context.Background())
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		cancel(nil) // nil cause -> context.Canceled: real interrupt, exit 130
		<-sig
		runCleanups()
		os.Exit(130)
	}()

	// SIGPIPE on its own channel: uncaught it kills before cleanup, and folding it
	// into the two-stage machine would let a broken pipe consume a later Ctrl-C's
	// graceful stage. Windows defines the constant but never delivers it.
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

// dispatch builds and runs the root command and returns the process exit code.
// Terminal errors render once: JSON envelope on stdout under --json, else a human
// line on stderr. Streams are parameters so tests can drive it without spawning.
func dispatch(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Sanitizing boundary so renderers cannot leak terminal-control sequences from
	// untrusted file bytes. Subcommands inherit via cobra; --json unwraps to the raw
	// stream in writeJSON. Closed on return to flush a held-back partial rune.
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
	// Closed output pipe is benign (exit 0, silent). Synchronous errno from the write
	// is definitive and is not gated on cancel cause (SIGPIPE goroutine may lag).
	// context.Canceled is gated on the cause so real Ctrl-C stays exit 130.
	if isBrokenPipe(err) ||
		(errors.Is(err, context.Canceled) && errors.Is(context.Cause(ctx), errBrokenPipe)) {
		err = errBrokenPipe
	}
	// Already-written output keeps its exit class but is not rendered twice.
	if _, rendered := errors.AsType[*alreadyRenderedError](err); rendered {
		return exitCodeFor(err)
	}
	// Scan raw args: cobra may have aborted before binding --json.
	asJSON := wantsJSON(args)
	// Cobra does not type unknown-command/flag errors; classify first.
	err = normalizeExecuteError(err)
	out := io.Writer(serr)
	if asJSON {
		out = sout
	}
	// List-command --json is an array; wrap its pre-flight error in a one-element array.
	renderError(out, asJSON, emitsJSONList(root, args), err)
	return exitCodeFor(err)
}
