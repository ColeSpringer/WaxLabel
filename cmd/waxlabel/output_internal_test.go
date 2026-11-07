package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/atomicfile"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestClassifyContentTooLarge: an oversize non-picture write is an exit-3 refusal ranked
// with picture-too-large, below unsupported-fragmentation and above io.
func TestClassifyContentTooLarge(t *testing.T) {
	tooLarge := fmt.Errorf("%w: x", waxerr.ErrContentTooLarge)
	if c := classifyError(tooLarge); c.exitCode != 3 || c.code != "content-too-large" {
		t.Fatalf("classify = (%d,%q), want (3,%q)", c.exitCode, c.code, "content-too-large")
	}
	worst := func(errs ...error) string {
		var w error
		for _, e := range errs {
			if worseError(w, e) {
				w = e
			}
		}
		return classifyError(w).code
	}
	fragmented := fmt.Errorf("w: %w", waxerr.ErrFragmented)
	ioErr := &fs.PathError{Op: "write", Path: "x", Err: errors.New("disk failure")}
	for _, errs := range [][]error{{tooLarge, fragmented}, {fragmented, tooLarge}} {
		if got := worst(errs...); got != "unsupported-fragmentation" {
			t.Errorf("worst of %v = %q, want unsupported-fragmentation", errs, got)
		}
	}
	for _, errs := range [][]error{{tooLarge, ioErr}, {ioErr, tooLarge}} {
		if got := worst(errs...); got != "content-too-large" {
			t.Errorf("worst of %v = %q, want content-too-large", errs, got)
		}
	}
}

// TestClassifySourceVanished: a source deleted since parse is source-changed (exit 5), even
// though the refusal wraps the open's not-found error.
func TestClassifySourceVanished(t *testing.T) {
	gone := &fs.PathError{Op: "open", Path: "/x.flac", Err: fs.ErrNotExist}
	err := fmt.Errorf("%w: /x.flac no longer exists (%w)", waxerr.ErrSourceChanged, gone)
	if c := classifyError(err); c.exitCode != 5 || c.code != "source-changed" {
		t.Errorf("classify = (%d,%q), want (5,%q)", c.exitCode, c.code, "source-changed")
	}
}

// TestOutputCommitError: a no-replace commit that found the -o target reads like the up-front
// existing-target refusal (usage, exit 2). Other failures keep their class, including a temp file
// that could not be created and a collision when no-replace was not asked for.
func TestOutputCommitError(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.flac")
	if err := os.WriteFile(out, []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}
	write := func(f *os.File) error { _, err := f.WriteString("ours"); return err }
	_, _, collided := atomicfile.Write(out, write, atomicfile.Options{NoReplace: true})
	if collided == nil {
		t.Fatal("setup: a no-replace write onto an existing file succeeded")
	}
	err := outputCommitError(out, true, collided)
	if c := classifyError(err); c.exitCode != 2 || !strings.Contains(c.message, "already exists") || !strings.Contains(c.message, "--overwrite") {
		t.Errorf("collision = (%d, %q), want exit 2 naming the existing target and --overwrite", c.exitCode, c.message)
	}
	if got := outputCommitError(out, false, collided); got != collided {
		t.Errorf("a collision without no-replace became %v", got)
	}
	exhausted := atomicfile.NewTempCreateError(filepath.Dir(out), &fs.PathError{Op: "createtemp", Path: out, Err: fs.ErrExist})
	if got := outputCommitError(out, true, exhausted); got != exhausted {
		t.Errorf("a temp-create failure became %v", got)
	}
	ioErr := &fs.PathError{Op: "write", Path: "out.flac", Err: errors.New("disk failure")}
	if got := outputCommitError(out, true, ioErr); got != error(ioErr) {
		t.Errorf("an I/O failure became %v", got)
	}
}

// TestHasFlagHonorsDoubleDash: tokens after "--" are positionals, not flags (file named --format).
func TestHasFlagHonorsDoubleDash(t *testing.T) {
	if !hasFlag([]string{"caps", "--format", "flac"}, "format") {
		t.Error("--format before -- should be detected as the flag")
	}
	if !hasFlag([]string{"caps", "--format=flac"}, "format") {
		t.Error("--format=value should be detected")
	}
	if hasFlag([]string{"caps", "--", "--format"}, "format") {
		t.Error("--format after -- is a positional, not the flag")
	}
	if hasFlag([]string{"caps", "song.flac"}, "format") {
		t.Error("no --format present, should be false")
	}
}

// TestWantsJSONParsesBoolForms: wantsJSON follows pflag bool forms; invalid leaves prior state.
func TestWantsJSONParsesBoolForms(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"caps", "--json"}, true},
		{[]string{"caps", "--json=true"}, true},
		{[]string{"caps", "--json=1"}, true},
		{[]string{"caps", "--json=t"}, true},
		{[]string{"caps", "--json=T"}, true},
		{[]string{"caps", "--json=TRUE"}, true},
		{[]string{"caps", "--json=True"}, true},
		{[]string{"caps", "--json=false"}, false},
		{[]string{"caps", "--json=0"}, false},
		{[]string{"caps", "--json=f"}, false},
		{[]string{"caps", "--json=bogus"}, false},         // unparseable: prior state (false) stands
		{[]string{"caps", "--json=yes"}, false},           // ParseBool rejects "yes" (not a bool spelling)
		{[]string{"caps", "--json=1", "--json=0"}, false}, // last wins
		{[]string{"caps", "--", "--json=1"}, false},       // after -- is a positional
		{[]string{"caps"}, false},
	} {
		if got := wantsJSON(c.args); got != c.want {
			t.Errorf("wantsJSON(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}

// TestStrictWarningReasonKeyless: keyed warnings become "KEY: reason"; keyless use message as-is.
func TestStrictWarningReasonKeyless(t *testing.T) {
	keyed := wl.Warning{Code: wl.WarnValueCoerced, Message: "TITLE coerced", Keys: []tag.Key{tag.Title}}
	if got := strictWarningReason(keyed); !strings.HasPrefix(got, "TITLE:") {
		t.Errorf("keyed reason = %q, want it to name TITLE with a colon", got)
	}
	keyless := wl.Warning{Code: wl.WarnValueCoerced, Message: "something was coerced"}
	if got := strictWarningReason(keyless); got != "something was coerced" {
		t.Errorf("keyless reason = %q, want the warning's own message (no leading colon)", got)
	}
}

// TestStrictWarningReasonValueDroppedEchoesMessage: value-dropped strict reason mirrors plan
// warning verbatim (e.g. MP4 zero slot wording).
func TestStrictWarningReasonValueDroppedEchoesMessage(t *testing.T) {
	zero := wl.Warning{
		Code:    wl.WarnValueDropped,
		Message: `TRACKNUMBER value "0" is treated as unset in this format and reads back as absent`,
		Keys:    []tag.Key{tag.TrackNumber},
	}
	if got := strictWarningReason(zero); got != zero.Message {
		t.Errorf("value-dropped strict reason = %q, want the warning's own message verbatim", got)
	}
	if got := strictWarningReason(zero); strings.Contains(got, "cannot be represented") {
		t.Errorf("zero-slot strict reason must not use the 'cannot be represented' wording: %q", got)
	}
}

// TestStrictEscalatesTagStructureDropped: tag-structure-dropped escalates under --strict with key.
func TestStrictEscalatesTagStructureDropped(t *testing.T) {
	if !strictEscalatingCodes[wl.WarnTagStructureDropped] {
		t.Error("--strict must escalate tag-structure-dropped (a lossy keyed edit)")
	}
	w := wl.Warning{Code: wl.WarnTagStructureDropped, Message: "an edited album tag dropped its structure", Keys: []tag.Key{tag.Artist}}
	if got := strictWarningReason(w); !strings.HasPrefix(got, "ARTIST:") {
		t.Errorf("tag-structure-dropped strict reason = %q, want it to name ARTIST", got)
	}
}

// TestPerFileReasonAndEntryAgree: perFileReason and errorEntry message match for edge errors.
func TestPerFileReasonAndEntryAgree(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want string
	}{{
		name: "bare PathError drops the path its caller already printed",
		err:  &fs.PathError{Op: "open", Path: "f.flac", Err: fs.ErrPermission},
		want: "permission denied",
	}, {
		name: "not-found takes the canonical wording",
		err:  &fs.PathError{Op: "open", Path: "f.flac", Err: fs.ErrNotExist},
		want: notFoundReason,
	}, {
		name: "bare cancellation",
		err:  context.Canceled,
		want: canceledReason,
	}, {
		name: "wrapped deadline",
		err:  fmt.Errorf("computing: %w", context.DeadlineExceeded),
		want: timeoutReason,
	}, {
		// LinkError is not a PathError, so perFileReason leaves it whole.
		name: "LinkError is not reduced",
		err:  &os.LinkError{Op: "rename", Old: "a.tmp", New: "f.flac", Err: errors.New("cross-device link")},
		want: "rename a.tmp f.flac: cross-device link",
	}, {
		// Wrapped PathError keeps caller framing (pictureLoadError shape).
		name: "wrapped PathError keeps the caller's framing and the inner path",
		err:  fmt.Errorf("reading cover art: %w", &fs.PathError{Op: "open", Path: "cover.jpg", Err: fs.ErrPermission}),
		want: "reading cover art: open cover.jpg: permission denied",
	}} {
		t.Run(c.name, func(t *testing.T) {
			if got := perFileReason(c.err); got != c.want {
				t.Errorf("perFileReason = %q, want %q", got, c.want)
			}
			if got := errorEntry("f.flac", c.err).Error.Message; got != c.want {
				t.Errorf("errorEntry message = %q, want %q", got, c.want)
			}
		})
	}
}

// TestClassifyWalkReadFault: a device error while a parse walks the stream to find where its audio
// ends keeps its cause, so it is an I/O failure (exit 6), not corrupt data (exit 4).
func TestClassifyWalkReadFault(t *testing.T) {
	flac, err := os.ReadFile(td("sample.flac"))
	if err != nil {
		t.Fatal(err)
	}
	wv, err := os.ReadFile(td("sample.wv"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		data []byte
		at   int // a byte only the walk reads
	}{
		{"the FLAC frame walk", flac, bytes.LastIndex(flac, []byte{0xFF, 0xF8, 0x79, 0x88, 0x09})}, // the last frame's header
		{"the WavPack block walk", wv, 14744},                                                      // the second block's header
	} {
		_, err := wl.Parse(context.Background(), &testfile.Faulty{Src: wl.BytesSource(c.data), Fails: testfile.Covering(int64(c.at))})
		if cl := classifyError(err); cl.exitCode != 6 || cl.code != "io" {
			t.Errorf("%s: %v classifies (%d,%q), want (6,%q)", c.name, err, cl.exitCode, cl.code, "io")
		}
	}
}

// TestClassifyTolerantReadFault: a device error in bytes whose reader tolerates a short read or an
// absent structure is an I/O failure (exit 6), as is one in bytes only the metadata fingerprint
// reads.
func TestClassifyTolerantReadFault(t *testing.T) {
	skipped := testfile.WAVChunk("junk", bytes.Repeat([]byte{0x5A}, 4096)) // past the 1 KiB detection sniffs
	wav := testfile.WAV(testfile.WAVFmtPCM(), skipped, testfile.WAVData(64))
	for _, c := range []struct {
		name string
		data []byte
		at   int64
	}{
		{"the Musepack stream header", fixtureBytes(t, "chapters.mpc"), 1500},
		{"the Monkey's Audio header", fixtureBytes(t, "sample.ape"), 2000},
		{"an Ogg header page body", fixtureBytes(t, "sample.ogg"), 1500},
		{"an Ogg segment table", fixtureBytes(t, "sample.opus"), 14874},
		{"the MP4 mvhd", fixtureBytes(t, "sample.m4a"), 9065},
		{"the MP4 chapter track", fixtureBytes(t, "sample_chapters.m4b"), 19795},
		{"the Matroska Cues", fixtureBytes(t, "sample.mka"), 13431},
		{"a WAV chunk only the fingerprint reads", wav, int64(bytes.Index(wav, skipped) + 2048)},
	} {
		_, err := wl.Parse(context.Background(), &testfile.Faulty{Src: wl.BytesSource(c.data), Fails: testfile.Covering(c.at)})
		if cl := classifyError(err); cl.exitCode != 6 || cl.code != "io" {
			t.Errorf("%s: %v classifies (%d,%q), want (6,%q)", c.name, err, cl.exitCode, cl.code, "io")
		}
	}
}

// fixtureBytes reads the testdata fixture name.
func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(td(name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// shortSource is a source that declares size bytes but holds only data, as a file cut short under a
// parse does: its reads past data end early with io.EOF.
type shortSource struct {
	data []byte
	size int64
}

func (s shortSource) Size() int64 { return s.size }

func (s shortSource) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(s.data)) {
		return 0, io.EOF
	}
	n := copy(p, s.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// TestClassifyHeaderReadFault: an ordinary header read fails by the walks' rule. A device error keeps
// its cause, so it is an I/O failure (exit 6); a source that ends before the bytes the header declares
// is corrupt data (exit 4).
func TestClassifyHeaderReadFault(t *testing.T) {
	flac, err := os.ReadFile(td("sample.flac"))
	if err != nil {
		t.Fatal(err)
	}
	const at = 4000 // inside the PADDING block the metadata walk reads, past the 1 KiB detection sniffs
	for _, c := range []struct {
		name string
		src  wl.ReaderAtSized
		exit int
		code string
	}{
		{"a device error", &testfile.Faulty{Src: wl.BytesSource(flac), Fails: testfile.Covering(at)}, 6, "io"},
		{"a short read", shortSource{flac[:at], int64(len(flac))}, 4, "invalid-data"},
	} {
		_, err := wl.Parse(context.Background(), c.src)
		if cl := classifyError(err); cl.exitCode != c.exit || cl.code != c.code {
			t.Errorf("%s: %v classifies (%d,%q), want (%d,%q)", c.name, err, cl.exitCode, cl.code, c.exit, c.code)
		}
	}
}

// TestClassifyDetectionReadFault: a device error while format detection reads the file is an I/O
// failure (exit 6), in the first KiB and in the window after a leading tag, while a file whose first
// bytes match no format is still unsupported (exit 3).
func TestClassifyDetectionReadFault(t *testing.T) {
	flac, err := os.ReadFile(td("sample.flac"))
	if err != nil {
		t.Fatal(err)
	}
	tag := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", strings.Repeat("x", 2000)))
	for _, c := range []struct {
		name string
		src  wl.ReaderAtSized
		exit int
		code string
	}{
		{"a device error in the first KiB", &testfile.Faulty{Src: wl.BytesSource(flac), Fails: testfile.Covering(100)}, 6, "io"},
		{"a device error in the window after a leading tag", &testfile.Faulty{Src: wl.BytesSource(slices.Concat(tag, flac)), Fails: testfile.Covering(int64(len(tag) + 100))}, 6, "io"},
		{"bytes that match no format", wl.BytesSource([]byte("matches no format at all")), 3, "unsupported-format"},
	} {
		_, err := wl.Parse(context.Background(), c.src)
		if cl := classifyError(err); cl.exitCode != c.exit || cl.code != c.code {
			t.Errorf("%s: %v classifies (%d,%q), want (%d,%q)", c.name, err, cl.exitCode, cl.code, c.exit, c.code)
		}
	}
}
