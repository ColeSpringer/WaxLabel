package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestParseByteSize: --max-size human-size parser (binary/decimal units, zero unlimited, rejects).
func TestParseByteSize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"2GiB", 2 << 30, false},
		{"500MB", 500 * 1000 * 1000, false},
		{"2147483648", 2147483648, false},
		{"0", 0, false},
		{"1MiB", 1 << 20, false},
		{"500 MB", 500 * 1000 * 1000, false}, // an optional space is allowed before the unit
		{"4k", 4096, false},                  // a bare unit letter is binary
		{"4kB", 4000, false},                 // a trailing B without an i is decimal
		{"1.5KiB", 1536, false},              // a fractional value is allowed
		{"7TiB", 7 << 40, false},             // large but well within int64
		{"+5MB", 5 * 1000 * 1000, false},     // a redundant leading '+' is accepted
		{"8.2MB", 8200000, false},            // whole, though 8.2 has no exact binary form
		{"16.1GB", 16100000000, false},
		{"2.01GB", 2010000000, false},
		{"0.27TB", 270000000000, false},
		{"1.001MB", 1001000, false},
		{".5KiB", 512, false},
		{"5.KiB", 5120, false},
		{"9223372036854775807", 9223372036854775807, false}, // the largest count there is
		{"0.5", 0, true},                                    // half a byte
		{"1.9KiB", 0, true},                                 // 1945.6 bytes
		{"0.0001KB", 0, true},                               // a tenth of a byte
		{"1.2.3MB", 0, true},
		{".", 0, true},
		{"", 0, true},
		{"abc", 0, true},
		{"12zz", 0, true},
		{"-5", 0, true},                  // a negative bare count is rejected
		{"-5MB", 0, true},                // a negative value with a unit is rejected
		{"9223372036854775808", 0, true}, // 2^63: int64(total) would wrap to a negative "unlimited"
		{"8388608TiB", 0, true},          // 8388608 * 2^40 = 2^63: same overflow via a unit
	}
	for _, tc := range cases {
		got, err := parseByteSize(tc.in)
		switch {
		case tc.wantErr && err == nil:
			t.Errorf("parseByteSize(%q) = %d, want error", tc.in, got)
		case !tc.wantErr && err != nil:
			t.Errorf("parseByteSize(%q) unexpected error: %v", tc.in, err)
		case !tc.wantErr && got != tc.want:
			t.Errorf("parseByteSize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestMaxSizeRejectsFractionalBytes: a size that is not a whole number of bytes is refused,
// not truncated: 0.5 would become 0, which means unlimited.
func TestMaxSizeRejectsFractionalBytes(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"0.5", "0.4", "1.5", "1.9KiB", "0.0001KB"} {
		_, errb, code := runCLIStdin(t, string(data), "dump", "--max-size", v, "-")
		if code != 2 || !strings.Contains(errb, "whole number of bytes") {
			t.Errorf("--max-size %s: exit %d, stderr %q; want the usage error", v, code, errb)
		}
	}
	for _, v := range []string{"1.5KiB", "0", "0.0", "0.5KiB"} {
		_, errb, code := runCLIStdin(t, string(data), "dump", "--max-size", v, "-")
		if want := map[bool]int{true: 0, false: 7}[strings.HasPrefix(v, "0") && !strings.Contains(v, "K")]; code != want {
			t.Errorf("--max-size %s: exit %d, want %d; stderr %q", v, code, want, errb)
		}
	}
}

// TestMaxSizeStdinBoundary: at cap dumps; one byte under exits 7 (input-too-large); 0 disables cap.
func TestMaxSizeStdinBoundary(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	size := strconv.Itoa(len(data))
	under := strconv.Itoa(len(data) - 1)

	if _, errb, code := runCLIStdin(t, string(data), "dump", "--max-size", size, "-"); code != 0 {
		t.Errorf("dump - at the limit exit = %d, want 0; stderr=%q", code, errb)
	}

	_, errb, code := runCLIStdin(t, string(data), "dump", "--max-size", under, "-")
	if code != 7 {
		t.Errorf("dump - over the limit exit = %d, want 7; stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "exceeds") {
		t.Errorf("over-limit stderr should explain the size cap, got %q", errb)
	}
	if strings.Contains(errb, "declared size") {
		t.Errorf("over-limit stderr must not use the corruption 'declared size' framing, got %q", errb)
	}

	if _, errb, code := runCLIStdin(t, string(data), "dump", "--max-size", "0", "-"); code != 0 {
		t.Errorf("dump - with --max-size 0 exit = %d, want 0; stderr=%q", code, errb)
	}
}

// endlessReader: unbounded stream with no EOF.
type endlessReader struct{}

func (endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'A'
	}
	return len(p), nil
}

// TestMaxSizeStopsEndlessStdin: bounded dump - on endless stdin exits 7 without hanging.
func TestMaxSizeStopsEndlessStdin(t *testing.T) {
	t.Parallel()
	type result struct {
		errb string
		code int
	}
	done := make(chan result, 1)
	go func() {
		var out, errb bytes.Buffer
		c := dispatch(context.Background(), []string{"dump", "--max-size", "1MB", "-"}, endlessReader{}, &out, &errb)
		done <- result{errb.String(), c}
	}()
	select {
	case r := <-done:
		if r.code != 7 {
			t.Errorf("endless stdin exit = %d, want 7 (bounded, input-too-large); stderr=%q", r.code, r.errb)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bounded dump - hung on an endless stream")
	}
}

// TestOversizedStdinFailsAlone: standard input over --max-size is that operand's failure. The
// other operands are still read, and a corrupt one outranks it.
func TestOversizedStdinFailsAlone(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(t.TempDir(), "corrupt.flac")
	if err := os.WriteFile(corrupt, data[:300], 0o644); err != nil {
		t.Fatal(err)
	}

	out, errb, code := runCLIStdin(t, string(data), "dump", "--max-size", "1KiB", corrupt, sampleFLAC, "-")
	if code != 4 {
		t.Errorf("exit %d, want 4 (invalid-data outranks input-too-large)", code)
	}
	if !strings.Contains(out, "Original Title") {
		t.Errorf("the good operand should still be dumped:\n%s", out)
	}
	if !strings.Contains(errb, "waxlabel: <stdin>: input too large: standard input exceeds 1.0 KiB\n") ||
		!strings.Contains(errb, "waxlabel: "+corrupt+": ") {
		t.Errorf("stderr should carry a line per failed operand:\n%s", errb)
	}

	out, _, code = runCLIStdin(t, string(data), "--json", "dump", "--max-size", "1KiB", corrupt, sampleFLAC, "-")
	docs := decodeJSONList[jsonDocument](t, out)
	if code != 4 || len(docs) != 3 {
		t.Fatalf("--json: exit %d, %d elements; want 4 and 3\n%s", code, len(docs), out)
	}
	if docs[1].Error != nil || docs[2].File != "<stdin>" || docs[2].Error == nil || docs[2].Error.Code != "input-too-large" {
		t.Errorf("--json: elements %+v, %+v; want the dump and the stdin failure", docs[1].Error, docs[2])
	}

	// Every command that reads standard input reports it that way.
	out2 := filepath.Join(t.TempDir(), "out")
	for _, args := range [][]string{
		{"dump", "-"}, {"lint", "-"}, {"verify", "-"}, {"plan", "-"}, {"caps", "-"},
		{"diff", "-", sampleFLAC}, {"export-picture", "-", "-o", out2},
		{"set", "-", "--set", "TITLE=x", "-o", out2},
	} {
		_, errb, code := runCLIStdin(t, string(data), append(args, "--max-size", "1KiB")...)
		if code != 7 || !strings.Contains(errb, "waxlabel: <stdin>: input too large") {
			t.Errorf("%v: exit %d, stderr %q; want 7 and the <stdin> line", args, code, errb)
		}
	}
	if _, err := os.Stat(out2); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused stream wrote %s (stat err %v)", out2, err)
	}
}

// TestStdinSpoolFailureNamesTheDirectory: a failure to buffer standard input is an I/O error
// that names the temp directory, never the temp file.
func TestStdinSpoolFailureNamesTheDirectory(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, gone)
	}
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	_, errb, code := runCLIStdin(t, string(data), "dump", "-")
	if code != 6 || !strings.Contains(errb, "waxlabel: <stdin>: create temp file in "+gone+": ") {
		t.Errorf("exit %d, stderr %q; want 6 and the directory named", code, errb)
	}
	if strings.Contains(errb, "waxlabel-stdin-") {
		t.Errorf("the temp file's name leaked: %q", errb)
	}
	out, _, _ := runCLIStdin(t, string(data), "--json", "dump", "-")
	if doc := decodeJSONOne[jsonDocument](t, out); doc.Error == nil || doc.Error.Code != "io" {
		t.Errorf("--json error = %+v, want code io", doc.Error)
	}

	// A write that fails later (a full disk) reads the same way.
	full := spoolError("/tmp", "/tmp/waxlabel-stdin-1", &fs.PathError{Op: "write", Path: "/tmp/waxlabel-stdin-1", Err: syscall.ENOSPC})
	if c := classifyError(full); c.code != "io" || strings.Contains(c.message, "waxlabel-stdin-") || !strings.Contains(c.message, "/tmp: ") {
		t.Errorf("a failed spool write classifies as %q, %q", c.code, c.message)
	}
	// A failed read of the stream itself is left as it is.
	if other := errors.New("read failed"); spoolError("/tmp", "/tmp/waxlabel-stdin-1", other) != other {
		t.Error("spoolError rewrote an error that is not the temp file's")
	}
}

// TestInterruptEndsABlockedStdinRead: the first interrupt ends a read of a stream that never
// sends anything.
func TestInterruptEndsABlockedStdinRead(t *testing.T) {
	t.Parallel()
	out := filepath.Join(t.TempDir(), "out.flac")
	for _, args := range [][]string{{"dump", "-"}, {"set", "-", "--set", "TITLE=x", "-o", out}} {
		r, w := io.Pipe()
		t.Cleanup(func() { w.Close() })
		ctx, interrupt, _ := newRunContext(context.Background())
		time.AfterFunc(100*time.Millisecond, interrupt)
		type result struct {
			errb string
			code int
		}
		done := make(chan result, 1)
		go func() {
			var stdout, errb bytes.Buffer
			code := dispatch(ctx, args, r, &stdout, &errb)
			done <- result{errb.String(), code}
		}()
		select {
		case res := <-done:
			if res.code != 130 || !strings.Contains(res.errb, "<stdin>: canceled") {
				t.Errorf("%v: exit %d, stderr %q; want 130 and the canceled line", args, res.code, res.errb)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%v: the read did not end with the interrupt", args)
		}
	}
}

// scriptedReader hands out its reads in order and counts the calls.
type scriptedReader struct {
	reads []string
	err   error // returned with the last read
	calls int
}

func (s *scriptedReader) Read(p []byte) (int, error) {
	s.calls++
	if len(s.reads) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s.reads[0])
	if s.reads[0] = s.reads[0][n:]; s.reads[0] != "" {
		return n, nil
	}
	if s.reads = s.reads[1:]; len(s.reads) == 0 {
		return n, s.err
	}
	return n, nil
}

// TestCopyUntil: the copy passes every byte and the stream's own error through, reads nothing
// once its context is done, and leaves a read that blocks when the context ends.
func TestCopyUntil(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	var got bytes.Buffer
	n, err := copyUntil(context.Background(), &got, &scriptedReader{reads: []string{"hello ", "wor", "ld"}, err: boom})
	if got.String() != "hello world" || n != 11 || err != boom {
		t.Errorf("copied %q (%d), %v; want every byte, then the stream's error", got.String(), n, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	untouched := &scriptedReader{reads: []string{"data"}}
	if n, err := copyUntil(ctx, io.Discard, untouched); n != 0 || !errors.Is(err, context.Canceled) || untouched.calls != 0 {
		t.Errorf("after cancel: %d bytes, err %v, %d reads of the stream; want the context's error and none", n, err, untouched.calls)
	}

	blocked, w := io.Pipe()
	t.Cleanup(func() { w.Close() })
	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	done := make(chan error, 1)
	go func() {
		_, err := copyUntil(ctx, io.Discard, blocked)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("a blocked read: %v, want the context's error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the copy did not end with its context")
	}
}
