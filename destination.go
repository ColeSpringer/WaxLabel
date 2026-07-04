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
// rename, directory fsync). It requires the document to have come from
// [ParseFile], verifies the file has not changed since parse
// ([waxerr.ErrSourceChanged] otherwise), and writes nothing for a no-op plan.
func SaveBack() Destination { return Destination{kind: destSaveBack} }

// SaveAsFile writes a complete file at path (atomically). Unlike SaveBack it is
// never a no-op: a fresh destination is always written whole. Writing to a path that
// resolves to the document's own source file spends the plan just as SaveBack does
// (see [Plan.Execute]); writing to other paths leaves the plan reusable.
//
// For a [ParseFile] document it verifies the source file has not changed since parse
// ([waxerr.ErrSourceChanged] otherwise), as SaveBack does: the copied byte offsets come
// from the source as parsed, so a changed source would produce a corrupt file. Writing
// in place (a target that resolves to the source) uses the full check; writing to another
// path uses the precise inode+size+fingerprint check, so a benign mtime-only touch does
// not block it. An [OpenSource] document reads stable in-memory bytes and is not checked.
//
// It needs a document that can resolve its own source bytes - one from [ParseFile]
// or [OpenSource]. A detached document from [Parse] carries no source, so SaveAsFile
// fails with [waxerr.ErrInvalidData]; write it with [WriteTo] and an explicit source
// instead.
func SaveAsFile(path string) Destination { return Destination{kind: destSaveAsFile, path: path} }

// WriteTo streams the complete output to w. The source bytes to copy come from
// source (required when the document is detached, i.e. from [Parse]); for a
// [ParseFile] or [OpenSource] document, pass nil to use its own source.
//
// When it reopens a [ParseFile] document's own source (source is nil), it first verifies
// that file has not changed since parse ([waxerr.ErrSourceChanged] otherwise), as SaveBack
// does - a streaming write never clobbers the source, so it uses the precise
// inode+size+fingerprint check. An explicit source or an [OpenSource] document supplies
// stable bytes and is not checked.
func WriteTo(w io.Writer, source ReaderAtSized) Destination {
	return Destination{kind: destWriteTo, w: w, source: source}
}

// verifySourceUnchanged confirms the on-disk source has not changed under the parsed
// document since parse. It returns the source's current identity - so every caller can
// report SaveResult{Dest: current} - and wraps [waxerr.ErrSourceChanged] on mismatch. It
// recomputes the same stat plus the structural fingerprint of the metadata region as the
// original save-back check, reusing the already-open src (the same handle the write copies
// from) for the fingerprint, so no third open is needed. samePath selects the strength:
//
//   - samePath == true (SaveBack, or a SaveAsFile whose target resolves to the source):
//     the full mtime-inclusive [Identity.Matches], staying conservative about clobbering
//     the source.
//   - samePath == false (a derived write - SaveAsFile to another path, WriteTo): the
//     content-only [Identity.MatchesContent] (inode+size+fingerprint), so a benign
//     mtime-only touch during a long parse->write window does not spuriously block a write
//     whose planned byte offsets are still valid.
func (p *Plan) verifySourceUnchanged(src core.ReaderAtSized, samePath bool) (core.Identity, error) {
	current, err := fileIdentity(p.doc.path)
	if err != nil {
		return core.Identity{}, err
	}
	match := p.doc.media.Identity.Matches
	if !samePath {
		match = p.doc.media.Identity.MatchesContent
	}
	// Cheap stat comparison first (inode/size, plus mtime for a same-path write). current
	// carries no fingerprint yet, so match skips its fingerprint arm here: a moved, resized, or
	// re-inoded source is rejected without the read + SHA-256 the fingerprint would cost -
	// potentially many MB for a large-cover file.
	if ok, why := match(current); !ok {
		return current, fmt.Errorf("%w: %s", waxerr.ErrSourceChanged, why)
	}
	// Stat matched; now fold in the structural fingerprint of the metadata region and re-check,
	// so a tamper that preserved size, mtime, and inode is still caught.
	if p.doc.media.Identity.HasFinger {
		// Fingerprint under the document's own PARSE limit, not p.opts.Limits (a WriteOptions
		// field no WriteOption ever sets, so always DefaultLimits). A document parsed with an
		// elevated WithLimits and a >256 MiB metadata region would otherwise silently skip its
		// save-time fingerprint (core.Fingerprint returns ok=false), degrading the guard to
		// inode+size+mtime. Using the same limit the parse-time fingerprint used keeps the two
		// symmetric (see fingerprintLimit).
		if fp, ok := core.Fingerprint(src, p.doc.media, p.doc.fingerprintLimit()); ok {
			current.Fingerprint, current.HasFinger = fp, true
			if ok, why := match(current); !ok {
				return current, fmt.Errorf("%w: %s", waxerr.ErrSourceChanged, why)
			}
		}
	}
	return current, nil
}

// fingerprintLimit is the alloc ceiling for a save-time structural fingerprint: the
// document's parse limit used verbatim, so save/result fingerprinting stays symmetric with the
// parse-time fingerprint (the codecs pass the raw opts.Limits.MaxAllocBytes) and never allocates
// past a caller's explicit WithLimits cap - a deliberately tight sub-default limit is honored,
// not floored. The default is used only when the recorded limit is non-positive, which happens
// solely for a Document built without a resolved limit (a hand-constructed one in a test); a
// zero limit would otherwise make core.Fingerprint skip silently (bits.ReadSlice rejects a
// non-positive limit), degrading save-back change detection to inode+size+mtime.
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
	// The already-committed guard lives in Execute (it covers every destination, not
	// just a second SaveBack), so by here this plan has not yet written.
	src, err := openFileSource(p.doc.path)
	if err != nil {
		return nil, SaveResult{}, err
	}
	defer src.Close()

	// Strong change detection: an in-place save uses the full check (mtime included),
	// staying conservative about clobbering the source.
	if current, err := p.verifySourceUnchanged(src, true); err != nil {
		return nil, SaveResult{Dest: current}, err
	}

	// Contract: a no-op SaveBack writes nothing.
	if p.plan.NoOp {
		return p.doc, SaveResult{Committed: false, Dest: p.doc.media.Identity, Doc: p.doc}, nil
	}

	committed, werr := p.writeFile(ctx, p.doc.path, src)
	if committed {
		// Bytes are in place (the rename succeeded), even if a later step like the
		// directory fsync errored; mark the plan so a second SaveBack is refused (M2).
		p.committed = true
	}
	newID, _ := fileIdentity(p.doc.path)
	resDoc := p.resultDocument(p.doc.path, nil, newID)
	return resDoc, SaveResult{Committed: committed, Dest: newID, Doc: resDoc}, werr
}

func (p *Plan) saveAsFile(ctx context.Context, path string) (*Document, SaveResult, error) {
	src, closer, err := p.doc.resolveSource(nil, "this document was parsed with Parse; use WriteTo(w, source) to write it")
	if err != nil {
		return nil, SaveResult{}, err
	}
	defer closer()

	// A ParseFile document resolves its source by reopening the current on-disk file, so a
	// change since parse would make the planned byte offsets copy the wrong bytes - and for an
	// in-place target, silently replace the source with the corruption. Verify the source is
	// unchanged first, as SaveBack does. An in-place target gets the full mtime-inclusive check;
	// another path gets the precise inode+size+fingerprint check. An OpenSource document reads
	// stable in-memory bytes (no reopen), and a detached Parse doc fails resolveSource above, so
	// neither reaches here.
	if p.doc.reopensFileSource() {
		if current, err := p.verifySourceUnchanged(src, sameFileTarget(path, p.doc.path)); err != nil {
			return nil, SaveResult{Dest: current}, err
		}
	}

	committed, werr := p.writeFile(ctx, path, src)
	if committed && sameFileTarget(path, p.doc.path) {
		// This write replaced the plan's source file, so later executions would read bytes
		// that no longer match the planned segments. Treat it like SaveBack and spend the
		// plan. The match is by resolved path rather than inode because an atomic rename to
		// a hardlink alias leaves the original source bytes intact.
		p.committed = true
	}
	newID, _ := fileIdentity(path)
	resDoc := p.resultDocument(path, nil, newID)
	return resDoc, SaveResult{Committed: committed, Dest: newID, Doc: resDoc}, werr
}

// sameFileTarget reports whether an atomic write to dst would replace the path the
// document was parsed from. It compares absolute paths after write-target symlink
// resolution. A symlink to the source resolves to the source and is guarded; a hardlink
// alias is not, because the atomic rename replaces only the alias directory entry and
// leaves the source path's bytes intact.
//
// If either path cannot be made absolute (filepath.Abs needs the working directory, which a
// removed or inaccessible cwd denies), the comparison is unreliable. Treat that case as a
// match so the guard fails closed.
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
	// A nil destination writer would panic on the first bits.Write deref; reject it
	// up front with a clean error, mirroring the nil-source/nil-reader guards on the
	// parse entry points (parse.go, source.go) (B2).
	if dst.w == nil {
		return nil, SaveResult{}, fmt.Errorf("%w: nil writer", waxerr.ErrInvalidData)
	}
	src, closer, err := p.doc.resolveSource(dst.source, "pass the source bytes as the second argument to WriteTo(w, source)")
	if err != nil {
		return nil, SaveResult{}, err
	}
	defer closer()

	// Like SaveAsFile, a ParseFile document with no explicit source reopens the on-disk file,
	// so verify it is unchanged before copying its (possibly stale) byte offsets. A streaming
	// writer never clobbers the source, so this is always a derived write - the precise
	// inode+size+fingerprint check (mtime skipped). An explicit WriteTo(w, source) or an
	// OpenSource document reads caller-supplied / in-memory bytes and needs no check.
	if dst.source == nil && p.doc.reopensFileSource() {
		if current, err := p.verifySourceUnchanged(src, false); err != nil {
			return nil, SaveResult{Dest: current}, err
		}
	}

	// A streaming destination cannot be re-read, so VerifyEssence (which checks
	// the written bytes) does not apply here.
	if _, err := bits.Write(ctx, dst.w, src, p.plan.Segments, nil); err != nil {
		return nil, SaveResult{}, err
	}
	id := core.Identity{Size: bits.OutputLen(p.plan.Segments)}
	resDoc := p.resultDocument("", nil, id)
	return resDoc, SaveResult{Committed: true, Dest: id, Doc: resDoc}, nil
}

// writeFile performs an atomic write of the plan to path, copying from src.
// When VerifyEssence is set it hashes the source audio once as it is copied,
// then re-reads the written output's audio extent and compares - confirming the
// rewrite preserved the essence before the file is committed. (The output read
// hits the page cache, so it guards the copy logic rather than disk media.)
func (p *Plan) writeFile(ctx context.Context, path string, src core.ReaderAtSized) (bool, error) {
	var srcEssence []byte
	write := func(f *os.File) error {
		sum, err := p.streamCopy(ctx, f, src)
		srcEssence = sum
		return err
	}
	verify := func(f *os.File) error {
		return p.verifyOutput(ctx, f, srcEssence)
	}
	return writeAtomic(path, write, verify, p.opts.PreserveModTime, p.doc.media.Identity.ModTimeUnixNano)
}

// streamCopy writes the plan's segments to dst, copying ranges from source. If
// VerifyEssence is set, it taps the copied audio (one read, no extra pass) and
// returns its hash for verifyOutput to check against the written output.
func (p *Plan) streamCopy(ctx context.Context, dst io.Writer, source core.ReaderAtSized) ([]byte, error) {
	var tap bits.Tap
	var hasher *bits.Hasher
	if p.opts.VerifyEssence {
		// Defense-in-depth behind Editor.Prepare, which already refuses a no-audio file
		// (so a no-audio document never reaches Execute): never verify the "essence" of a
		// file the parser flagged WarnNoAudioFrames, which would hash non-audio bytes as
		// if they were audio (H1). Not load-bearing, but it keeps the verify path honest
		// on its own terms.
		if hasNoAudioWarning(p.doc.media) {
			return nil, fmt.Errorf("%w: cannot verify audio essence of a no-audio file", waxerr.ErrInvalidData)
		}
		_, cfg := p.essenceExtent()
		hasher = bits.NewHasher(p.doc.media.EssenceRanges())
		hasher.Mix(cfg)
		tap = hasher
	}
	// Buffer the destination file. A renumbering Ogg rewrite emits three small
	// segments per audio page (an 18-byte header copy, an 8-byte patch, the body
	// copy); without buffering those become thousands of tiny writes to the temp
	// file. The tap still sees the raw source bytes, so verification is unaffected.
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
	// Structural re-parse: the essence hash re-reads the same verbatim media bytes, so it alone
	// cannot notice a corrupt container wrapped around them (the MP4 truncated-moov write that
	// motivated this passed --verify while producing a self-unreadable file). Parsing the output
	// back - the large mdat/audio essence is never read, only its range recorded - catches any
	// codec's structurally invalid rewrite before it is committed, and the result is discarded.
	if codec, ok := core.ForFormat(p.doc.media.Format); ok {
		size := bits.OutputLen(p.plan.Segments)
		// This verifies our own just-written output, so it must not reject a valid rewrite for a
		// resource-limit reason it should clear. Take, per field, the more permissive of the
		// document's parse-time limits and the library defaults: the doc's limits cover a structure
		// an elevated WithLimits accepted (a deep tree or an oversized cover the defaults would
		// reject), while the defaults cover a rewrite that grew past a caller's tight parse cap, since
		// an edit can add elements the input lacked. The writers' own size checks (id3.CheckSize and
		// the like) gate against DefaultLimits rather than the document's, so the output already fits
		// the defaults. Finally floor the alloc cap at the output size: no single element can exceed
		// the whole file, and a hostile declared size still cannot overrun it.
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

// sizedReaderAt pairs an io.ReaderAt with a known size to satisfy core.ReaderAtSized, so the
// --verify structural re-parse can read the still-open temp file in place, without loading it
// into memory the way core.BytesSource would.
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

// tempCreateError reports a failure to create the atomic-write temp file. It
// names the destination directory (which the user chose) but not the internal
// temp pattern (which they did not), while still unwrapping to the underlying
// *os.PathError so the failure classifies as a local I/O error. It deliberately
// does not satisfy os.IsNotExist (it is not a *PathError itself), so a missing
// destination directory stays in the I/O class rather than being reported as a
// "no such file" on a temp name the user never named.
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

// NewTempCreateError builds the same temp-create failure [writeAtomic] returns when a
// destination directory rejects a write: it names dir (not the random temp file) and unwraps
// to err so the failure classifies as local I/O. It is exported so a caller that probes a
// directory's writability up front - the CLI's -o pre-check - surfaces the identical error
// (same message and exit class) the late atomic write would, instead of re-implementing it.
func NewTempCreateError(dir string, err error) error {
	return &tempCreateError{dir: dir, err: err}
}

// ResolveWriteTarget returns the path an atomic write will rename over: the symlink-resolved
// target when path resolves (so the rewrite updates the file a link points at, leaving the
// link in place), else path verbatim (a fresh target or a dangling link). It is the single
// resolution rule [writeAtomic] uses; a caller pre-checking an -o destination resolves the
// same way so its probe inspects the directory the write actually lands in.
func ResolveWriteTarget(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// writeAtomic writes via a temp file in the destination directory, fsyncs it,
// optionally verifies it (before commit), renames it over path, then fsyncs the
// directory. It returns committed=true once the rename succeeds (even if the
// later directory fsync errors, since the data is already in place).
func writeAtomic(path string, write, verify func(*os.File) error, preserveMtime bool, origMtimeUnixNano int64) (bool, error) {
	// Resolve a symlink to its target so the rewrite updates the file the link
	// points at and leaves the link in place; otherwise the atomic rename would
	// replace the symlink with a regular file (silent data divergence from the
	// real target). A path that does not resolve - a brand-new SaveAsFile target,
	// a dangling link - falls back to the literal path. A hard link is still
	// broken by the rename (an unavoidable consequence of atomic replace); that is
	// documented behavior, not worked around here.
	target := ResolveWriteTarget(path)
	dir := filepath.Dir(target)
	// The temp file must live in the target's directory so the rename is on one
	// filesystem (os.Rename cannot cross devices) and lands beside the real file.
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

	// Mode and mtime are best-effort and deliberately not fatal: many media
	// libraries live on FAT/exFAT drives that do not support per-file chmod, and
	// failing the whole save over a cosmetic attribute would be worse than the
	// data write succeeding. Carry over an existing file's mode; widen a brand-
	// new file from os.CreateTemp's 0600 to a conventional 0644.
	if info, err := os.Stat(target); err == nil {
		_ = os.Chmod(tmpName, info.Mode())
	} else {
		_ = os.Chmod(tmpName, 0o644)
	}
	if preserveMtime && origMtimeUnixNano > 0 {
		mt := time.Unix(0, origMtimeUnixNano)
		_ = os.Chtimes(tmpName, mt, mt)
	}

	if err := os.Rename(tmpName, target); err != nil {
		return false, err
	}
	committed = true
	return true, fsyncDir(dir)
}

// fsyncDir flushes a directory entry so the rename is durable. Best-effort:
// platforms that cannot open a directory for sync return nil.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("directory fsync: %w", err)
	}
	return nil
}
