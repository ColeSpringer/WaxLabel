package waxlabel

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

type destKind uint8

const (
	destSaveBack destKind = iota
	destSaveAsFile
	destWriteTo
)

// Destination names where [Plan.Execute] writes. Construct one with [SaveBack],
// [SaveAsFile], or [WriteTo].
type Destination struct {
	kind   destKind
	path   string
	w      io.Writer
	source core.ReaderAtSized
}

// SaveBack rewrites the original file in place, atomically (temp file, fsync,
// rename, directory fsync where the platform has one). It requires the document to
// have come from [ParseFile], verifies the file has not changed since parse
// ([waxerr.ErrSourceChanged] otherwise), and writes nothing for a no-op plan.
//
// A handle the CALLER still holds on the path fails the rename on Windows; close it
// before saving. The library releases its own.
func SaveBack() Destination { return Destination{kind: destSaveBack} }

// SaveAsFile writes a complete file at path, atomically. Unlike SaveBack it is never a no-op:
// a fresh destination is always written whole.
//
// It OVERWRITES an existing file without refusing, so the "do not clobber" check is the
// caller's; the CLI's -o guard is CLI policy, not a library one. A path that resolves to the
// document's own source spends the plan as SaveBack does; other paths leave it reusable.
//
// For a [ParseFile] document it verifies the source has not changed since parse
// ([waxerr.ErrSourceChanged] otherwise), since the copied byte offsets came from the source
// as parsed. An in-place target gets the mtime-inclusive check, another path the
// inode+size+fingerprint one so a benign touch does not block it. An [OpenSource] document
// reads stable bytes and is not checked.
//
// A detached [Parse] document carries no source and fails with [waxerr.ErrInvalidData]; write
// it with [WriteTo] and an explicit source.
func SaveAsFile(path string) Destination { return Destination{kind: destSaveAsFile, path: path} }

// WriteTo streams the complete output to w. source is required for a detached [Parse]
// document; pass nil to use a [ParseFile] or [OpenSource] document's own.
//
// Reopening a [ParseFile] source verifies it is unchanged first, with the precise
// inode+size+fingerprint check since a streaming write never clobbers it. An explicit
// source or an [OpenSource] document is not checked.
func WriteTo(w io.Writer, source ReaderAtSized) Destination {
	return Destination{kind: destWriteTo, w: w, source: source}
}

// verifySourceUnchanged confirms the on-disk source has not changed since parse, wrapping
// [waxerr.ErrSourceChanged] on mismatch. It returns the current identity so callers can
// report SaveResult{Dest: current}, and reuses the open src for the fingerprint. samePath
// picks the strength:
//
//   - true (in-place): the mtime-inclusive [Identity.Matches], staying conservative about
//     clobbering the source.
//   - false (derived): [Identity.MatchesContent], so a benign mtime touch during a long
//     parse-to-write window does not block a still-valid write.
func (p *Plan) verifySourceUnchanged(src core.ReaderAtSized, samePath bool) (core.Identity, error) {
	current, err := fileIdentity(p.doc.path)
	if err != nil {
		return core.Identity{}, err
	}
	match := p.doc.media.Identity.Matches
	if !samePath {
		match = p.doc.media.Identity.MatchesContent
	}
	// Cheap stat first: current carries no fingerprint yet, so match skips that arm and a
	// moved, resized, or re-inoded source is rejected without paying for the hash.
	if ok, why := match(current); !ok {
		return current, fmt.Errorf("%w: %s", waxerr.ErrSourceChanged, why)
	}
	// Now fold in the metadata region's fingerprint, so a tamper that preserved size,
	// mtime, and inode is still caught.
	if p.doc.media.Identity.HasFinger {
		// The document's own PARSE limit. Anything smaller would make core.Fingerprint skip
		// silently for an elevated-limit document, degrading the guard to inode+size+mtime.
		if fp, ok := core.Fingerprint(src, p.doc.media, p.doc.fingerprintLimit()); ok {
			current.Fingerprint, current.HasFinger = fp, true
			if ok, why := match(current); !ok {
				return current, fmt.Errorf("%w: %s", waxerr.ErrSourceChanged, why)
			}
		}
	}
	return current, nil
}

// fingerprintLimit is the alloc ceiling for a save-time fingerprint: the document's parse
// limit verbatim, so it never allocates past a caller's WithLimits cap. The default covers
// a Document built without a resolved limit, where a zero would make Fingerprint skip.
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

// sameFileTarget reports whether an atomic write to dst would replace the path the
// document was parsed from, comparing absolute paths after symlink resolution. A symlink
// to the source is guarded; a hardlink alias is not, since the rename replaces only the
// alias entry. An unreliable comparison (Abs failed) reads as a match, failing closed.
func sameFileTarget(dst, src string) bool {
	if src == "" {
		return false // a detached document (from Parse) has no source file to clobber
	}
	a, aok := absResolved(dst)
	b, bok := absResolved(src)
	if a == b {
		return true
	}
	return !aok || !bok
}

// absResolved returns the path form sameFileTarget compares: the write target after
// symlink resolution, made absolute and cleaned. reliable is false when filepath.Abs
// failed, leaving a cleaned path that may still be relative.
func absResolved(path string) (resolved string, reliable bool) {
	r := ResolveWriteTarget(path)
	if abs, err := filepath.Abs(r); err == nil {
		return abs, true
	}
	return filepath.Clean(r), false
}

func (p *Plan) writeTo(ctx context.Context, dst Destination) (*Document, SaveResult, error) {
	// Would panic on the first bits.Write deref.
	if dst.w == nil {
		return nil, SaveResult{}, fmt.Errorf("%w: nil writer", waxerr.ErrInvalidData)
	}
	src, closer, err := p.doc.resolveSource(dst.source, "pass the source bytes as the second argument to WriteTo(w, source)")
	if err != nil {
		return nil, SaveResult{}, err
	}
	defer closer()

	// A reopened ParseFile source may be stale. Always a derived write (a stream never
	// clobbers the source), so the mtime-skipping check. Supplied bytes need no check.
	if dst.source == nil && p.doc.reopensFileSource() {
		if current, err := p.verifySourceUnchanged(src, false); err != nil {
			return nil, SaveResult{Dest: current}, err
		}
	}

	// A streaming destination cannot be re-read, so VerifyEssence does not apply.
	if _, err := bits.Write(ctx, dst.w, src, p.plan.Segments, nil); err != nil {
		return nil, SaveResult{}, err
	}
	id := core.Identity{Size: bits.OutputLen(p.plan.Segments)}
	resDoc := p.resultDocument("", nil, id)
	return resDoc, SaveResult{Committed: true, Dest: id, Doc: resDoc}, nil
}

// writeFile performs an atomic write of the plan to path, copying from src. When
// VerifyEssence is set it hashes the source audio as it copies, then re-reads the written
// extent and compares before committing.
//
// release closes src. writeFile owns that call rather than the caller's defer because the
// rename may replace the very path src was opened from.
func (p *Plan) writeFile(ctx context.Context, path string, src core.ReaderAtSized, release func()) (bool, error) {
	var srcEssence []byte
	write := func(f *os.File) error {
		sum, err := p.streamCopy(ctx, f, src)
		// The copy is the source's last read, so release here: on Windows a handle open on
		// the file writeAtomic is about to rename over fails MoveFileEx, and every in-place
		// save with it. Safe only because writeAtomic runs write to completion before
		// verify, since the closures share srcEssence rather than snapshot it.
		//
		// FILE_SHARE_DELETE does not help: MoveFileEx still refuses, and the alternative
		// (SetFileInformationByHandle + FileRenameInfoEx) is unreachable from stdlib and
		// still fails on a read-only target. Do not retry this.
		release()
		srcEssence = sum
		return err
	}
	verify := func(f *os.File) error {
		return p.verifyOutput(ctx, f, srcEssence)
	}
	return writeAtomic(path, write, verify, p.preserveModTimeUnixNano())
}

// preserveModTimeUnixNano is the source mtime the rewrite should carry, or 0 to let it
// take the write's own time. Read here so writeAtomic only has to apply a timestamp or not.
func (p *Plan) preserveModTimeUnixNano() int64 {
	if !p.opts.PreserveModTime {
		return 0
	}
	return p.doc.media.Identity.ModTimeUnixNano
}

// streamCopy writes the plan's segments to dst, copying ranges from source. If
// VerifyEssence is set, it taps the copied audio (one read, no extra pass) and
// returns its hash for verifyOutput to check against the written output.
func (p *Plan) streamCopy(ctx context.Context, dst io.Writer, source core.ReaderAtSized) ([]byte, error) {
	var tap bits.Tap
	var hasher *bits.Hasher
	if p.opts.VerifyEssence {
		// Behind Prepare's own no-audio refusal, so not load-bearing. Verifying the
		// "essence" of a no-audio file would hash non-audio bytes as if they were audio.
		if hasNoAudioWarning(p.doc.media) {
			return nil, fmt.Errorf("%w: cannot verify audio essence of a no-audio file", waxerr.ErrInvalidData)
		}
		_, cfg := p.essenceExtent()
		hasher = bits.NewHasher(p.doc.media.EssenceRanges())
		hasher.Mix(cfg)
		tap = hasher
	}
	// A renumbering Ogg rewrite emits three small segments per audio page, which without
	// buffering become thousands of tiny writes. The tap still sees raw source bytes.
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

// verifyOutput re-hashes the written file's audio extent and compares it to the
// source essence captured during the copy, then re-parses the file structurally.
// Both run before the atomic commit (writeAtomic's verify hook), so a mismatch or
// an unreadable rewrite discards the temp file rather than shipping it.
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
	// The essence hash re-reads the same verbatim media bytes, so it cannot notice a
	// corrupt container wrapped around them: an MP4 truncated-moov write passed --verify
	// while producing a self-unreadable file. Re-parsing catches that; the result is
	// discarded.
	if codec, ok := core.ForFormat(p.doc.media.Format); ok {
		size := bits.OutputLen(p.plan.Segments)
		// Per field, the more permissive of the document's parse limits and the defaults, so
		// verifying our own output cannot reject a rewrite it should clear: the former covers
		// an elevated WithLimits, the latter a rewrite that grew past a tight parse cap.
		// Floored at the output size, since no element can exceed the whole file.
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

// sizedReaderAt pairs an io.ReaderAt with a known size, so the --verify re-parse can read
// the still-open temp file in place rather than loading it into memory.
type sizedReaderAt struct {
	io.ReaderAt
	size int64
}

func (s sizedReaderAt) Size() int64 { return s.size }

// essenceExtent returns the codec's essence-digest inputs for this plan's
// document (version, config), or a neutral fallback if the format is unknown.
func (p *Plan) essenceExtent() (string, []byte) {
	if codec, ok := core.ForFormat(p.doc.media.Format); ok {
		return codec.EssenceExtent(p.doc.media)
	}
	return "audio-extent-v1", nil
}

// tempCreateError reports a failed atomic-write temp create. It names the destination
// directory the user chose, not the internal temp pattern, and unwraps to the *os.PathError
// so it still classifies as local I/O. It deliberately does not satisfy os.IsNotExist,
// which would report "no such file" on a temp name the user never named.
type tempCreateError struct {
	dir string
	err error // the os.CreateTemp failure, normally an *os.PathError
}

func (e *tempCreateError) Error() string {
	reason := e.err.Error()
	if pe, ok := e.err.(*os.PathError); ok {
		reason = pe.Err.Error() // the bare cause, without the random temp name
	}
	return fmt.Sprintf("create temp file in %s: %s", e.dir, reason)
}

func (e *tempCreateError) Unwrap() error { return e.err }

// renameError reports a failed atomic-write commit. It names the target, drops the internal
// temp name os.Rename's *os.LinkError carries, and unwraps to that LinkError so it keeps
// its local-I/O class. Naming the target is not redundant with the CLI's per-file prefix,
// which is the input and under -o not the file being replaced.
type renameError struct {
	target string
	err    error // the os.Rename failure, normally an *os.LinkError
}

func (e *renameError) Error() string {
	reason := e.err.Error()
	if le, ok := e.err.(*os.LinkError); ok {
		reason = le.Err.Error() // the bare cause, without the random temp name
	}
	return fmt.Sprintf("replace %s: %s", e.target, reason)
}

func (e *renameError) Unwrap() error { return e.err }

// NewTempCreateError builds the same failure [writeAtomic] returns when a destination
// directory rejects a write. Exported so a caller probing writability up front, like the
// CLI's -o pre-check, reports the identical message and exit class the late write would.
func NewTempCreateError(dir string, err error) error {
	return &tempCreateError{dir: dir, err: err}
}

// ResolveWriteTarget returns the path an atomic write will rename over: the
// symlink-resolved target, else path verbatim for a fresh target or dangling link. The
// single rule [writeAtomic] uses, so a caller pre-checking an -o destination inspects the
// directory the write really lands in.
func ResolveWriteTarget(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// writeAtomic writes via a temp file in the destination directory, fsyncs it,
// optionally verifies it (before commit), renames it over path, then fsyncs the
// directory where the platform supports it (see fsyncDir). It returns
// committed=true once the rename succeeds (even if the later directory fsync
// errors, since the data is already in place). preserveModTimeUnixNano is the mtime to
// stamp on the result, 0 to let it take the write's own time.
func writeAtomic(path string, write, verify func(*os.File) error, preserveModTimeUnixNano int64) (bool, error) {
	// Follow a symlink so the rewrite updates the file it points at and leaves the link in
	// place; the rename would otherwise replace the link with a regular file. A hard link
	// is still broken by the rename, which is documented behavior.
	target := ResolveWriteTarget(path)
	dir := filepath.Dir(target)
	// The temp must share the target's directory: os.Rename cannot cross devices.
	tmp, err := os.CreateTemp(dir, ".waxlabel-*.tmp")
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

	// Mode and mtime are best-effort: FAT/exFAT has no per-file chmod, and failing a save
	// over a cosmetic attribute is worse than not carrying it. An existing file's mode
	// carries over; a new one widens from os.CreateTemp's 0600 to 0644. One stat, reused
	// below, so a chmod racing the write cannot leave the two readings disagreeing.
	info, statErr := os.Stat(target)
	if statErr == nil {
		_ = os.Chmod(tmpName, info.Mode())
	} else {
		_ = os.Chmod(tmpName, 0o644)
	}
	// != 0, not > 0: core.Identity spells "timestamp known" the same way, and a pre-1970
	// file carries a negative value that > 0 would discard, updating the mtime
	// --preserve-mtime promised to keep.
	if preserveModTimeUnixNano != 0 {
		mt := time.Unix(0, preserveModTimeUnixNano)
		_ = os.Chtimes(tmpName, mt, mt)
	}

	// After the chmod above: earlier, the temp would inherit an already-cleared mode and the
	// save would strip the read-only attribute. The chmod likewise stays before the rename,
	// or a 0600 source's replacement is briefly world-readable.
	restoreReadOnly := clearTargetReadOnly(target, info)
	// Unconditional: it restores the untouched original on failure and re-applies to the
	// renamed-in file on success, without resting on the discarded chmod error.
	defer restoreReadOnly()

	// Precondition: no handle this process holds on target may still be open, which is
	// why writeFile releases the source right after the copy.
	if err := renameReplace(tmpName, target); err != nil {
		return false, &renameError{target: target, err: err}
	}
	committed = true
	return true, fsyncDir(dir)
}
