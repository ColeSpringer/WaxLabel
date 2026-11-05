package waxlabel

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/colespringer/waxlabel/internal/atomicfile"
	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// TempFilePrefix and TempFileSuffix name atomic-write temps beside the target.
const (
	TempFilePrefix = atomicfile.TempPrefix
	TempFileSuffix = atomicfile.TempSuffix
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
// [ParseFile]; checks [waxerr.ErrSourceChanged] before and after the copy and before the
// rename; no-op writes nothing. A save that writes takes an advisory flock on the file where
// available and waits, until ctx is done, while another descriptor holds one, so release your
// own first; [WithLockWait] reports the wait. Close any caller-held handle on the path before
// saving (Windows).
func SaveBack() Destination { return Destination{kind: destSaveBack} }

// SaveAsFile writes a complete file at path atomically, even for a no-op plan.
// Overwrites an existing file unless [WithNoReplace]. A path resolving
// to the source commits the plan like SaveBack. [ParseFile] checks the source is
// unchanged; [Parse] needs [WriteTo] with an explicit source.
func SaveAsFile(path string) Destination { return Destination{kind: destSaveAsFile, path: path} }

// WriteTo streams complete output to w. source required for detached [Parse];
// nil uses ParseFile/OpenSource's own. Reopened ParseFile source is verified unchanged,
// before and after the copy. On error w may hold part of the output: discard it.
func WriteTo(w io.Writer, source ReaderAtSized) Destination {
	return Destination{kind: destWriteTo, w: w, source: source}
}

// verifySource compares current, the source as it is now, and the fingerprint read through src
// with the parse identity. samePath true: Matches (times included); false: MatchesContent
// (ignore a benign touch). File and size are compared before the fingerprint is read, so a
// refusal names the most specific change.
func (d *Document) verifySource(src core.ReaderAtSized, current core.Identity, samePath bool) error {
	parsed := d.media.Identity
	if ok, why := parsed.MatchesContent(current); !ok {
		return fmt.Errorf("%w: %s", waxerr.ErrSourceChanged, why)
	}
	if parsed.HasFinger {
		fp, ok, err := core.ReadFingerprint(src, d.media)
		if err != nil {
			return fmt.Errorf("%w: the metadata fingerprint could not be re-read (%w)", waxerr.ErrSourceChanged, err)
		}
		if !ok {
			return fmt.Errorf("%w: the metadata fingerprint could not be re-read", waxerr.ErrSourceChanged)
		}
		current.Fingerprint, current.HasFinger = fp, true
	}
	match := parsed.Matches
	if !samePath {
		match = parsed.MatchesContent
	}
	if ok, why := match(current); !ok {
		return fmt.Errorf("%w: %s", waxerr.ErrSourceChanged, why)
	}
	return nil
}

// checkHeldSource verifies a reopened source before a read of it and returns its identity, the
// baseline [fileSource.checkStable] compares afterwards.
func (d *Document) checkHeldSource(src *fileSource, samePath bool) (core.Identity, error) {
	before, err := src.identity(d.path)
	if err != nil {
		return core.Identity{}, err
	}
	return before, d.verifySource(src, before, samePath)
}

// recheckPath re-checks the source through its path just before a rename replaces it. The held
// descriptor reads the parsed file even after a competitor renames its own file over the path.
func (p *Plan) recheckPath() error {
	fresh, err := reopenFileSource(p.doc.path)
	if err != nil {
		return err
	}
	defer fresh.Close()
	return p.doc.verifySource(fresh, core.FileIdentity(p.doc.path, fresh.info), true)
}

func (p *Plan) saveBack(ctx context.Context) (*Document, SaveResult, error) {
	if p.doc.path == "" {
		return nil, SaveResult{}, fmt.Errorf("%w: SaveBack needs a file; use SaveAsFile or WriteTo", waxerr.ErrNeedsFile)
	}
	// Execute holds the already-committed guard, so this plan has not yet written.
	src, err := reopenFileSource(p.doc.path)
	if err != nil {
		return nil, SaveResult{}, err
	}
	defer src.Close()
	// Held from before the check until after the rename, so concurrent saves of this file run
	// one at a time and the later one sees the earlier one's rename. A no-op writes nothing to
	// guard.
	if !p.plan.NoOp {
		unlock, err := atomicfile.Lock(ctx, src.f, p.opts.LockWait)
		if err != nil {
			return nil, SaveResult{}, err
		}
		defer unlock()
	}

	before, err := p.doc.checkHeldSource(src, true)
	if err != nil {
		return nil, SaveResult{Dest: before}, err
	}
	// Contract: a no-op SaveBack writes nothing.
	if p.plan.NoOp {
		return p.doc, SaveResult{Committed: false, Dest: p.doc.media.Identity, Doc: p.doc}, nil
	}

	committed, destID, resDoc, werr := p.writeFile(ctx, p.doc.path, src, before, fileWrite{inPlace: true})
	if !committed {
		// No post-write file to describe, so no Document.
		return nil, SaveResult{Committed: false, Dest: destID}, werr
	}
	// The rename succeeded, so the bytes are in place even if the directory fsync
	// failed. Mark the plan so a second SaveBack is refused.
	p.committed = true
	return resDoc, SaveResult{Committed: true, Dest: destID, Doc: resDoc}, werr
}

func (p *Plan) saveAsFile(ctx context.Context, path string) (*Document, SaveResult, error) {
	src, closer, err := p.doc.resolveSource(nil, "this document was parsed with Parse; use WriteTo(w, source) to write it")
	if err != nil {
		return nil, SaveResult{}, err
	}
	defer closer()

	// A ParseFile document reopens its source, so a change since parse would copy the
	// wrong bytes. An OpenSource document reads stable bytes; a detached Parse
	// document failed above.
	inPlace := sameFileTarget(path, p.doc.path)
	var before core.Identity
	if held, ok := src.(*fileSource); ok {
		if inPlace {
			unlock, err := atomicfile.Lock(ctx, held.f, p.opts.LockWait)
			if err != nil {
				return nil, SaveResult{}, err
			}
			defer unlock()
		}
		if before, err = p.doc.checkHeldSource(held, inPlace); err != nil {
			return nil, SaveResult{Dest: before}, err
		}
	}

	committed, destID, resDoc, werr := p.writeFile(ctx, path, src, before, fileWrite{inPlace: inPlace, noReplace: p.opts.NoReplace})
	if !committed {
		return nil, SaveResult{Committed: false, Dest: destID}, werr
	}
	if inPlace {
		// This replaced the plan's source, so mark it committed as SaveBack does. Matched
		// by resolved path, not inode: a rename onto a hardlink alias leaves the source.
		p.committed = true
	}
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

	held, reopened := src.(*fileSource)
	var before core.Identity
	if reopened {
		if before, err = p.doc.checkHeldSource(held, false); err != nil {
			return nil, SaveResult{Dest: before}, err
		}
	}

	written, err := p.verifyStream(ctx, src)
	if err == nil {
		atSaveStage("copy")
		_, err = bits.Write(ctx, dst.w, src, p.plan.Segments, nil)
		atSaveStage("copied")
	}
	if reopened {
		if serr := held.checkStable(p.doc.path, before, false); serr != nil {
			return nil, SaveResult{}, serr
		}
	}
	if err != nil {
		return nil, SaveResult{}, err
	}
	id := core.Identity{Size: bits.OutputLen(p.plan.Segments)}
	resDoc := resultDocument(written, "", id, nil)
	return resDoc, SaveResult{Committed: true, Dest: id, Doc: resDoc}, nil
}

// verifyStream reads back the output WriteTo would stream, through the plan's segments, before a
// byte of it is sent. Its source digest is hashed from the source's own essence.
func (p *Plan) verifyStream(ctx context.Context, src core.ReaderAtSized) (*Document, error) {
	var srcEssence []byte
	if p.opts.VerifyEssence {
		cfg, ranges, err := p.sourceEssence()
		if err != nil {
			return nil, err
		}
		if srcEssence, err = hashRanges(ctx, src, cfg, ranges); err != nil {
			return nil, err
		}
	}
	return p.verifyOutput(ctx, bits.NewSegmentsReader(src, p.plan.Segments), srcEssence)
}

// saveHook, when set, runs at each named stage of a write: "copy" before the source copy,
// "copied" after it, "commit" before the final checks and the rename. Test-only.
var saveHook func(stage string)

func atSaveStage(stage string) {
	if saveHook != nil {
		saveHook(stage)
	}
}

// fileWrite says how a file destination relates to the source.
type fileWrite struct {
	inPlace   bool // the write replaces the parsed source: re-check its path before the rename
	noReplace bool // refuse a file already at the destination
}

// writeFile atomically writes the plan to path and returns the written file's identity and the
// Document read back from the temp. A reopened source is checked again after the copy.
func (p *Plan) writeFile(ctx context.Context, path string, src core.ReaderAtSized, before core.Identity, mode fileWrite) (bool, core.Identity, *Document, error) {
	held, reopened := src.(*fileSource)
	var srcEssence []byte
	var written *Document
	write := func(f *os.File) error {
		atSaveStage("copy")
		sum, err := p.streamCopy(ctx, f, src)
		atSaveStage("copied")
		if reopened {
			if serr := held.checkStable(p.doc.path, before, mode.inPlace); serr != nil {
				return serr
			}
			if atomicfile.OpenFilesBlockRename {
				held.Close()
			}
		}
		srcEssence = sum
		return err
	}
	verify := func(f *os.File) (err error) {
		written, err = p.verifyOutput(ctx, sizedReaderAt{ReaderAt: f, size: bits.OutputLen(p.plan.Segments)}, srcEssence)
		return err
	}
	preCommit := func() error {
		atSaveStage("commit")
		if err := ctx.Err(); err != nil {
			return err
		}
		if mode.inPlace {
			return p.recheckPath()
		}
		return nil
	}
	opts := atomicfile.Options{Verify: verify, PreCommit: preCommit, ModTime: p.preserveModTime(), NoReplace: mode.noReplace, KeepOpen: true}
	committed, info, err := atomicfile.Write(path, write, opts)
	if !committed {
		id, _ := fileIdentity(path)
		return false, id, nil, err
	}
	// The temp's parse fingerprinted the written bytes; the stat is the file's after the rename.
	var id core.Identity
	if info != nil {
		id = core.FileIdentity(path, info)
		id.Fingerprint, id.HasFinger = written.media.Identity.Fingerprint, written.media.Identity.HasFinger
	}
	return true, id, resultDocument(written, path, id, info), err
}

// preserveModTime is the mtime to stamp, or nil for write time.
func (p *Plan) preserveModTime() *time.Time {
	if !p.opts.PreserveModTime || !p.doc.hasModTime {
		return nil
	}
	return &p.doc.modTime
}

// streamCopy writes plan segments to dst, optionally tapping essence for verify.
func (p *Plan) streamCopy(ctx context.Context, dst io.Writer, source core.ReaderAtSized) ([]byte, error) {
	var tap bits.Tap
	var hasher *bits.Hasher
	if p.opts.VerifyEssence {
		cfg, ranges, err := p.sourceEssence()
		if err != nil {
			return nil, err
		}
		hasher = bits.NewHasher(ranges)
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

// sourceEssence is what the source digest hashes, the same for file saves and WriteTo: the decoder
// config first, then the essence ranges. A no-audio source has none.
func (p *Plan) sourceEssence() (cfg []byte, ranges [][2]int64, err error) {
	if hasNoAudioWarning(p.doc.media) {
		return nil, nil, fmt.Errorf("%w: cannot verify audio essence of a no-audio file", waxerr.ErrInvalidData)
	}
	_, cfg = p.doc.essenceExtent()
	return cfg, p.doc.media.EssenceRanges(), nil
}

// verifyOutput parses written bytes as ParseFile would, refusing output that does not read back as
// the source's format with audio. Under VerifyEssence the essence that parse finds must also be
// where the plan put it and hash to srcEssence. A failed read refuses with its own cause.
func (p *Plan) verifyOutput(ctx context.Context, out core.ReaderAtSized, srcEssence []byte) (*Document, error) {
	rb := &readErrors{ReaderAtSized: out}
	written, err := p.checkOutput(ctx, rb, srcEssence)
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if rb.err != nil {
		// A parse can shrug a failed read off, so its result is not trusted either.
		return nil, rb.err
	}
	return written, err
}

// readErrors records the first read error other than the end of the data.
type readErrors struct {
	core.ReaderAtSized
	err error
}

func (r *readErrors) ReadAt(p []byte, off int64) (int, error) {
	n, err := r.ReaderAtSized.ReadAt(p, off)
	if err != nil && r.err == nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		r.err = err
	}
	return n, err
}

func (p *Plan) checkOutput(ctx context.Context, out core.ReaderAtSized, srcEssence []byte) (*Document, error) {
	format := p.doc.media.Format
	codec, ok := detectCodec(out)
	if !ok {
		return nil, fmt.Errorf("%w: the output would no longer be recognizable as %s", waxerr.ErrInvalidData, format)
	}
	// Floored at the defaults, so an edit that outgrows a tight caller limit still reads back. The
	// returned Document keeps these limits.
	def, lim := bits.DefaultLimits, p.doc.limits
	limits := bits.Limits{
		MaxAllocBytes: max(lim.MaxAllocBytes, def.MaxAllocBytes),
		MaxDepth:      max(lim.MaxDepth, def.MaxDepth),
		MaxElements:   max(lim.MaxElements, def.MaxElements),
	}
	written, err := parseWith(ctx, codec, out, core.ParseOptions{Limits: limits})
	if err != nil {
		return nil, fmt.Errorf("%w: the output would not parse back cleanly (%v)", waxerr.ErrInvalidData, err)
	}
	if got := written.media.Format; got != format {
		return nil, fmt.Errorf("%w: the output would read as %s, not %s", waxerr.ErrInvalidData, got, format)
	}
	if hasNoAudioWarning(written.media) {
		return nil, fmt.Errorf("%w: the output would have no audio", waxerr.ErrInvalidData)
	}
	// The plan's result already reflects every drop the edit meant. A source parsed under tighter
	// limits can miss items past a cap that this read-back sees, so the counts compare only when no
	// limit was raised.
	if items, got, want, differ := core.ItemCountDiff(written.media, p.resultMedia()); differ && limits == lim {
		return nil, fmt.Errorf("%w: the output would not hold the planned %s (%d read back, %d planned)",
			waxerr.ErrInvalidData, items, got, want)
	}
	if p.opts.VerifyEssence {
		if err := p.verifyEssence(ctx, out, written, srcEssence); err != nil {
			return nil, err
		}
	}
	return written, nil
}

// verifyEssence hashes the written essence once, over the ranges the read-back found, after checking
// that the plan's own ranges cover the same bytes.
func (p *Plan) verifyEssence(ctx context.Context, out core.ReaderAtSized, written *Document, srcEssence []byte) error {
	ranges := written.media.EssenceRanges()
	if !slices.Equal(coveredRanges(ranges), coveredRanges(p.resultMedia().EssenceRanges())) {
		return fmt.Errorf("%w: the output's audio is not where the plan placed it", waxerr.ErrInvalidData)
	}
	_, cfg := written.essenceExtent()
	sum, err := hashRanges(ctx, out, cfg, ranges)
	if err != nil {
		return err
	}
	if !bytes.Equal(sum, srcEssence) {
		return fmt.Errorf("%w: written audio essence does not match the source", waxerr.ErrInvalidData)
	}
	return nil
}

// coveredRanges drops empty ranges and merges adjacent ones, so two lists covering the same bytes
// compare equal however they are split.
func coveredRanges(ranges [][2]int64) [][2]int64 {
	var out [][2]int64
	for _, r := range ranges {
		if r[0] == r[1] {
			continue
		}
		if n := len(out); n > 0 && out[n-1][1] == r[0] {
			out[n-1][1] = r[1]
			continue
		}
		out = append(out, r)
	}
	return out
}

// sizedReaderAt is ReaderAt + Size, for reading back a temp file.
type sizedReaderAt struct {
	io.ReaderAt
	size int64
}

func (s sizedReaderAt) Size() int64 { return s.size }

// NewTempCreateError matches the error a save returns when it cannot create its temp (CLI -o
// probe).
func NewTempCreateError(dir string, err error) error { return atomicfile.NewTempCreateError(dir, err) }

// ResolveWriteTarget is the path an atomic write renames over (symlink-resolved).
func ResolveWriteTarget(path string) string { return atomicfile.ResolveTarget(path) }
