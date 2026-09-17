package waxlabel

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// TempFilePrefix and TempFileSuffix name atomic-write temps beside the target.
const (
	TempFilePrefix = ".waxlabel-"
	TempFileSuffix = ".tmp"
)

// IsTempFileName reports a ".waxlabel-*.tmp" (or CLI writecheck) leftover name.
func IsTempFileName(name string) bool {
	return strings.HasPrefix(name, TempFilePrefix) && strings.HasSuffix(name, TempFileSuffix)
}

type destKind uint8

const (
	destSaveBack destKind = iota
	destSaveAsFile
	destWriteTo
)

// Destination is where [Plan.Execute] writes. Build with [SaveBack], [SaveAsFile], or [WriteTo].
type Destination struct {
	kind   destKind
	path   string
	w      io.Writer
	source core.ReaderAtSized
}

// SaveBack rewrites the original file in place (temp, fsync, rename). Requires
// [ParseFile]; checks [waxerr.ErrSourceChanged]; no-op writes nothing.
// Close any caller-held handle on the path before saving (Windows).
func SaveBack() Destination { return Destination{kind: destSaveBack} }

// SaveAsFile writes a complete file at path atomically. Always writes whole.
// Overwrites without refusing (caller/CLI guard clobber). Path resolving to the
// source spends the plan like SaveBack. [ParseFile] checks source unchanged;
// [Parse] needs [WriteTo] with an explicit source.
func SaveAsFile(path string) Destination { return Destination{kind: destSaveAsFile, path: path} }

// WriteTo streams complete output to w. source required for detached [Parse];
// nil uses ParseFile/OpenSource's own. Reopened ParseFile source is verified unchanged.
func WriteTo(w io.Writer, source ReaderAtSized) Destination {
	return Destination{kind: destWriteTo, w: w, source: source}
}

// verifySourceUnchanged checks the on-disk source since parse.
// samePath true: mtime-inclusive Matches; false: MatchesContent (ignore benign touch).
func (p *Plan) verifySourceUnchanged(src core.ReaderAtSized, samePath bool) (core.Identity, error) {
	current, err := fileIdentity(p.doc.path)
	if err != nil {
		return core.Identity{}, err
	}
	match := p.doc.media.Identity.Matches
	if !samePath {
		match = p.doc.media.Identity.MatchesContent
	}
	if ok, why := match(current); !ok {
		return current, fmt.Errorf("%w: %s", waxerr.ErrSourceChanged, why)
	}
	if p.doc.media.Identity.HasFinger {
		if fp, ok := core.Fingerprint(src, p.doc.media, p.doc.fingerprintLimit()); ok {
			current.Fingerprint, current.HasFinger = fp, true
			if ok, why := match(current); !ok {
				return current, fmt.Errorf("%w: %s", waxerr.ErrSourceChanged, why)
			}
		}
	}
	return current, nil
}

// fingerprintLimit is the save-time fingerprint alloc ceiling (parse limit, or default).
func (d *Document) fingerprintLimit() int64 {
	if d.limits.MaxAllocBytes > 0 {
		return d.limits.MaxAllocBytes
	}
	return bits.DefaultLimits.MaxAllocBytes
}

func (p *Plan) saveBack(ctx context.Context) (*Document, SaveResult, error) {
	if p.doc.path == "" {
		return nil, SaveResult{}, fmt.Errorf("%w: SaveBack needs a file; use SaveAsFile or WriteTo", waxerr.ErrNeedsFile)
	}
	// Execute holds the already-committed guard, so this plan has not yet written.
	src, err := openFileSource(p.doc.path)
	if err != nil {
		return nil, SaveResult{}, err
	}
	defer src.Close()

	// An in-place save uses the full mtime-inclusive check.
	if current, err := p.verifySourceUnchanged(src, true); err != nil {
		return nil, SaveResult{Dest: current}, err
	}

	// Contract: a no-op SaveBack writes nothing.
	if p.plan.NoOp {
		return p.doc, SaveResult{Committed: false, Dest: p.doc.media.Identity, Doc: p.doc}, nil
	}

	// The hook closes src before the rename replaces the path it was opened from; the
	// defer above stays as the backstop, and a double Close is harmless.
	committed, werr := p.writeFile(ctx, p.doc.path, src, func() { src.Close() })
	destID, _ := fileIdentity(p.doc.path)
	if !committed {
		// No post-write file to describe, so no Document.
		return nil, SaveResult{Committed: false, Dest: destID}, werr
	}
	// Bytes are in place (the rename succeeded), even if a later step like the
	// directory fsync errored; mark the plan so a second SaveBack is refused.
	p.committed = true
	resDoc := p.resultDocument(p.doc.path, nil, destID)
	return resDoc, SaveResult{Committed: true, Dest: destID, Doc: resDoc}, werr
}

func (p *Plan) saveAsFile(ctx context.Context, path string) (*Document, SaveResult, error) {
	src, closer, err := p.doc.resolveSource(nil, "this document was parsed with Parse; use WriteTo(w, source) to write it")
	if err != nil {
		return nil, SaveResult{}, err
	}
	defer closer()

	// A ParseFile document reopens its source, so a change since parse would copy the wrong
	// bytes, and for an in-place target write that corruption over the source. An
	// OpenSource document reads stable bytes and a detached Parse doc failed above.
	if p.doc.reopensFileSource() {
		if current, err := p.verifySourceUnchanged(src, sameFileTarget(path, p.doc.path)); err != nil {
			return nil, SaveResult{Dest: current}, err
		}
	}

	// The closer is the release hook: the target may resolve to the source, which the
	// rename then replaces. It is idempotent, so the defer above still backstops.
	committed, werr := p.writeFile(ctx, path, src, closer)
	destID, _ := fileIdentity(path)
	if !committed {
		return nil, SaveResult{Committed: false, Dest: destID}, werr
	}
	if sameFileTarget(path, p.doc.path) {
		// This replaced the plan's source, so spend the plan as SaveBack does. Matched by
		// resolved path, not inode: a rename to a hardlink alias leaves the source intact.
		p.committed = true
	}
	resDoc := p.resultDocument(path, nil, destID)
	return resDoc, SaveResult{Committed: true, Dest: destID, Doc: resDoc}, werr
}

// sameFileTarget reports whether dst replaces the parsed path (symlink-resolved Abs).
// Hardlink aliases are not guarded. Abs failure fails closed.
func sameFileTarget(dst, src string) bool {
	if src == "" {
		return false // detached Parse has no source file
	}
	a, aok := absResolved(dst)
	b, bok := absResolved(src)
	if a == b {
		return true
	}
	return !aok || !bok
}

// absResolved returns symlink-resolved absolute path; reliable false if Abs failed.
func absResolved(path string) (resolved string, reliable bool) {
	r := ResolveWriteTarget(path)
	if abs, err := filepath.Abs(r); err == nil {
		return abs, true
	}
	return filepath.Clean(r), false
}

func (p *Plan) writeTo(ctx context.Context, dst Destination) (*Document, SaveResult, error) {
	if dst.w == nil {
		return nil, SaveResult{}, fmt.Errorf("%w: nil writer", waxerr.ErrInvalidData)
	}
	src, closer, err := p.doc.resolveSource(dst.source, "pass the source bytes as the second argument to WriteTo(w, source)")
	if err != nil {
		return nil, SaveResult{}, err
	}
	defer closer()

	if dst.source == nil && p.doc.reopensFileSource() {
		if current, err := p.verifySourceUnchanged(src, false); err != nil {
			return nil, SaveResult{Dest: current}, err
		}
	}

	if _, err := bits.Write(ctx, dst.w, src, p.plan.Segments, nil); err != nil {
		return nil, SaveResult{}, err
	}
	id := core.Identity{Size: bits.OutputLen(p.plan.Segments)}
	resDoc := p.resultDocument("", nil, id)
	return resDoc, SaveResult{Committed: true, Dest: id, Doc: resDoc}, nil
}

// writeFile atomically writes the plan to path. Owns release (close before Windows rename).
func (p *Plan) writeFile(ctx context.Context, path string, src core.ReaderAtSized, release func()) (bool, error) {
	var srcEssence []byte
	write := func(f *os.File) error {
		sum, err := p.streamCopy(ctx, f, src)
		// Release before rename: Windows MoveFileEx fails if this process still holds the path.
		release()
		srcEssence = sum
		return err
	}
	verify := func(f *os.File) error {
		return p.verifyOutput(ctx, f, srcEssence)
	}
	return writeAtomic(path, write, verify, p.preserveModTimeUnixNano())
}

// preserveModTimeUnixNano is the mtime to stamp, or 0 for write time.
func (p *Plan) preserveModTimeUnixNano() int64 {
	if !p.opts.PreserveModTime {
		return 0
	}
	return p.doc.media.Identity.ModTimeUnixNano
}

// streamCopy writes plan segments to dst, optionally tapping essence for verify.
func (p *Plan) streamCopy(ctx context.Context, dst io.Writer, source core.ReaderAtSized) ([]byte, error) {
	var tap bits.Tap
	var hasher *bits.Hasher
	if p.opts.VerifyEssence {
		if hasNoAudioWarning(p.doc.media) {
			return nil, fmt.Errorf("%w: cannot verify audio essence of a no-audio file", waxerr.ErrInvalidData)
		}
		_, cfg := p.essenceExtent()
		hasher = bits.NewHasher(p.doc.media.EssenceRanges())
		hasher.Mix(cfg)
		tap = hasher
	}
	bw := bufio.NewWriterSize(dst, 1<<16)
	if _, err := bits.Write(ctx, bw, source, p.plan.Segments, tap); err != nil {
		return nil, err
	}
	if err := bw.Flush(); err != nil {
		return nil, err
	}
	if hasher != nil {
		return hasher.Sum(), nil
	}
	return nil, nil
}

// verifyOutput compares written essence to the copy tap and re-parses before commit.
func (p *Plan) verifyOutput(ctx context.Context, out io.ReaderAt, srcEssence []byte) error {
	if !p.opts.VerifyEssence {
		return nil
	}
	_, cfg := p.essenceExtent()
	res := p.plan.Result
	outSum, err := hashRanges(ctx, out, cfg, res.EssenceRanges())
	if err != nil {
		return err
	}
	if !bytes.Equal(outSum, srcEssence) {
		return fmt.Errorf("%w: written audio essence does not match the source", waxerr.ErrInvalidData)
	}
	if codec, ok := core.ForFormat(p.doc.media.Format); ok {
		size := bits.OutputLen(p.plan.Segments)
		def := bits.DefaultLimits
		limits := bits.Limits{
			MaxAllocBytes: max(p.doc.limits.MaxAllocBytes, def.MaxAllocBytes, size),
			MaxDepth:      max(p.doc.limits.MaxDepth, def.MaxDepth),
			MaxElements:   max(p.doc.limits.MaxElements, def.MaxElements),
		}
		sized := sizedReaderAt{ReaderAt: out, size: size}
		if _, err := codec.Parse(ctx, sized, core.ParseOptions{Limits: limits}); err != nil {
			return fmt.Errorf("%w: the written file did not parse back cleanly (%v)", waxerr.ErrInvalidData, err)
		}
	}
	return nil
}

// sizedReaderAt is ReaderAt + Size for in-place verify re-parse.
type sizedReaderAt struct {
	io.ReaderAt
	size int64
}

func (s sizedReaderAt) Size() int64 { return s.size }

func (p *Plan) essenceExtent() (string, []byte) {
	if codec, ok := core.ForFormat(p.doc.media.Format); ok {
		return codec.EssenceExtent(p.doc.media)
	}
	return "audio-extent-v1", nil
}

// tempCreateError names the destination directory, not the internal temp pattern.
type tempCreateError struct {
	dir string
	err error // usually *os.PathError
}

func (e *tempCreateError) Error() string {
	reason := e.err.Error()
	if pe, ok := e.err.(*os.PathError); ok {
		reason = pe.Err.Error() // the bare cause, without the random temp name
	}
	return fmt.Sprintf("create temp file in %s: %s", e.dir, reason)
}

func (e *tempCreateError) Unwrap() error { return e.err }

// renameError names the target; unwraps LinkError for I/O classification.
type renameError struct {
	target string
	err    error // usually *os.LinkError
}

func (e *renameError) Error() string {
	reason := e.err.Error()
	if le, ok := e.err.(*os.LinkError); ok {
		reason = le.Err.Error() // the bare cause, without the random temp name
	}
	return fmt.Sprintf("replace %s: %s", e.target, reason)
}

func (e *renameError) Unwrap() error { return e.err }

// NewTempCreateError matches writeAtomic's temp-create failure (CLI -o probe).
func NewTempCreateError(dir string, err error) error {
	return &tempCreateError{dir: dir, err: err}
}

// ResolveWriteTarget is the path an atomic write renames over (symlink-resolved).
func ResolveWriteTarget(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// writeAtomic: temp in dest dir, fsync, optional verify, rename, dir fsync.
// committed=true once rename succeeds.
func writeAtomic(path string, write, verify func(*os.File) error, preserveModTimeUnixNano int64) (bool, error) {
	target := ResolveWriteTarget(path)
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, TempFilePrefix+"*"+TempFileSuffix)
	if err != nil {
		return false, &tempCreateError{dir: dir, err: err}
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if err := write(tmp); err != nil {
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		return false, err
	}
	if verify != nil {
		if err := verify(tmp); err != nil { // runs before commit; temp discarded on failure
			return false, err
		}
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}

	// Mode/mtime best-effort (FAT has no chmod). Existing mode carried; new file 0644.
	info, statErr := os.Stat(target)
	if statErr == nil {
		_ = os.Chmod(tmpName, info.Mode())
	} else {
		_ = os.Chmod(tmpName, 0o644)
	}
	if preserveModTimeUnixNano != 0 {
		mt := time.Unix(0, preserveModTimeUnixNano)
		_ = os.Chtimes(tmpName, mt, mt)
	}

	restoreReadOnly := clearTargetReadOnly(target, info)
	defer restoreReadOnly()

	if err := renameReplace(tmpName, target); err != nil {
		return false, &renameError{target: target, err: err}
	}
	committed = true
	return true, fsyncDir(dir)
}
