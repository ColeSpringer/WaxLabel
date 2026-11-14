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
	ctx, interrupt, pipeClosed := newRunContext(context.Background())

	// First signal cancels in-flight work; a second forces exit for ops that cannot
	// observe cancellation (e.g. blocked in fsync). Own goroutine so the second
	// signal is still handled if main is stuck. os.Exit skips defers, so the forced path
	// drains the cleanup registry itself.
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		interrupt()
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
		pipeClosed()
	}()

	code := dispatch(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	signal.Stop(sig)
	signal.Stop(pipe)
	interrupt()
	os.Exit(code)
}

// interruptKey holds the context only an interrupt cancels.
type interruptKey struct{}

// newRunContext returns the context a command runs under and its two cancels. interrupt
// (SIGINT, SIGTERM) cancels everything. pipeClosed (SIGPIPE) cancels ctx alone, so reads and
// listings stop while a write goes on under [mutationContext].
func newRunContext(parent context.Context) (ctx context.Context, interrupt, pipeClosed func()) {
	sigCtx, interrupt := context.WithCancel(parent)
	pipeCtx, cancelPipe := context.WithCancelCause(context.WithValue(sigCtx, interruptKey{}, sigCtx))
	return pipeCtx, interrupt, func() { cancelPipe(errBrokenPipe) }
}

// mutationContext is the context a write runs under: an interrupt cancels it, a closed
// output pipe does not. A context newRunContext did not make is its own.
func mutationContext(ctx context.Context) context.Context {
	if sigCtx, ok := ctx.Value(interruptKey{}).(context.Context); ok {
		return sigCtx
	}
	return ctx
}

// outputClosed reports that ctx ended because the output pipe closed and no interrupt followed.
func outputClosed(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), errBrokenPipe) && mutationContext(ctx).Err() == nil
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
	// is definitive and is not gated on the cancel (SIGPIPE goroutine may lag).
	// context.Canceled is gated on it so Ctrl-C stays exit 130.
	if isBrokenPipe(err) || (errors.Is(err, context.Canceled) && outputClosed(ctx)) {
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
