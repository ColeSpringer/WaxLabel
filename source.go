package waxlabel

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// fileSource adapts an open file to ReaderAtSized for the duration of a parse
// or a write. It is never retained by a Document.
type fileSource struct {
	f    *os.File
	size int64
}

func openFileSource(path string) (*fileSource, error) {
	// Stat before Open: opening the read end of a FIFO (or certain device files)
	// blocks until a writer appears, so a non-regular path must be rejected before
	// os.Open or the parse hangs before any guard can run. This stat-first check is
	// the library backstop that stops the hang for every caller (the CLI adds a
	// friendlier exit-2 layer on top). os.Stat follows symlinks, so a symlink to a
	// regular file is still accepted; the check also folds in the former directory
	// guard, keeping its specific message.
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		if info.IsDir() {
			return nil, fmt.Errorf("%w: %s is a directory, not a file", waxerr.ErrInvalidData, path)
		}
		return nil, fmt.Errorf("%w: %s is not a regular file", waxerr.ErrInvalidData, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	// Take the size from the open descriptor, not the pre-open stat: that stat only
	// gated the file kind, and a concurrent truncate/extend in the stat-then-open
	// window could have made its size stale. A regular file's f.Stat never blocks
	// (the FIFO hazard is os.Open, already past), so this is safe and authoritative.
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &fileSource{f: f, size: fi.Size()}, nil
}

func (s *fileSource) ReadAt(p []byte, off int64) (int, error) { return s.f.ReadAt(p, off) }
func (s *fileSource) Size() int64                             { return s.size }
func (s *fileSource) Close() error                            { return s.f.Close() }

// fileIdentity captures a file's strong identity: size, mtime, and (where the
// OS exposes them) inode and device. These detect a same-source save against a
// file that changed underneath us.
func fileIdentity(path string) (core.Identity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return core.Identity{}, err
	}
	id := core.Identity{
		Path:            path,
		Size:            info.Size(),
		ModTimeUnixNano: info.ModTime().UnixNano(),
	}
	id.INode, id.Device = sysInodeDevice(info)
	return id, nil
}

// reopensFileSource reports whether resolving this document's own source (no explicit
// override) reopens its file from disk rather than reusing in-memory bytes. It is true only for
// a ParseFile document (a path, no retained src); an OpenSource document (src set) or a detached
// Parse document (no path) is false. It gates the source-unchanged guard on the write paths:
// only a reopened file can have changed under the parsed document since parse.
func (d *Document) reopensFileSource() bool {
	return d.src == nil && d.path != ""
}

// resolveSource selects the bytes to read for a write or a hash: an explicit
// source, else the document's in-memory source (from OpenSource), else its file
// reopened (from ParseFile). The returned closer must always be called. Both
// the write path and the hashing path share this. remedy is the caller-specific
// "here is how to supply a source" hint appended to the no-source error, since a
// generic one is half-wrong for each caller (a hash path cannot use WriteTo, a
// write path cannot use WithHashSource).
func (d *Document) resolveSource(explicit core.ReaderAtSized, remedy string) (core.ReaderAtSized, func(), error) {
	noop := func() {}
	// A zero-value Document has no media to write or hash; report the uninitialized
	// state with the same message Prepare uses, rather than the generic
	// "no source available" below. This is the shared chokepoint for the write and
	// hash paths, so it fixes a zeroDoc.HashAudioEssence/HashFile at once; the generic
	// message then fires only for an initialized-but-detached Parse doc, where
	// supplying a source genuinely is the remedy.
	if d.zero() {
		return nil, noop, fmt.Errorf("%w: document is not initialized; use ParseFile/Parse", waxerr.ErrInvalidData)
	}
	if explicit != nil {
		return explicit, noop, nil
	}
	if d.src != nil {
		return d.src, noop, nil
	}
	if d.path != "" {
		fs, err := openFileSource(d.path)
		if err != nil {
			return nil, noop, err
		}
		return fs, func() { fs.Close() }, nil
	}
	return nil, noop, fmt.Errorf("%w: no source available; %s", waxerr.ErrInvalidData, remedy)
}

// Source retains the complete bytes of a non-seekable stream that was parsed
// for editing. Unlike a Document (which is detached and holds nothing), a
// Source is closable: it owns the teed buffer. Edit the [Source.Document] and
// save it; the Source supplies the original bytes the rewrite copies.
type Source struct {
	doc  *Document
	data []byte
}

// OpenSource parses a non-seekable stream, teeing the complete stream into
// memory as it reads (you cannot spool bytes after they have passed). The
// returned Source is closable and its Document can be edited and saved.
func OpenSource(ctx context.Context, r io.Reader, opts ...ParseOption) (*Source, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	// io.ReadAll(nil) panics; reject a nil reader with a clean error first, mirroring
	// the context guard above and Parse's nil-source guard.
	if r == nil {
		return nil, fmt.Errorf("%w: nil reader", waxerr.ErrInvalidData)
	}
	// Resolve the options once, before the read, and reuse the resolved struct for the
	// parse below: the ingest cap must come from the same options the parse sees, and a
	// second resolveParseOptions pass would re-run every option needlessly.
	po := resolveParseOptions(opts)
	limit := po.MaxSourceBytes
	// A bound at the int64 ceiling can never be exceeded by a real stream and would overflow
	// the limit+1 probe below to a negative that io.LimitReader reads as "nothing", so treat
	// it as unbounded.
	if limit >= math.MaxInt64 {
		limit = 0
	}
	// Bound the buffering so an endless stream cannot exhaust memory. Read limit+1 bytes so a
	// stream of exactly limit still parses while the first byte past it trips the guard; a
	// plain io.LimitReader would instead truncate at the limit and misparse the shortened
	// bytes. A non-positive limit keeps the read unbounded.
	reader := r
	if limit > 0 {
		reader = io.LimitReader(r, limit+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if limit > 0 && int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: stream exceeds %s", waxerr.ErrInputTooLarge, HumanBytes(limit))
	}
	doc, err := parseSource(ctx, core.BytesSource(data), "", po)
	if err != nil {
		return nil, err
	}
	doc.src = core.BytesSource(data)
	return &Source{doc: doc, data: data}, nil
}

// Document returns the parsed document. It remains valid after Close (it is
// detached); only the Source's role as a write source ends at Close.
func (s *Source) Document() *Document { return s.doc }

// Close releases the retained buffer. After Close the Document can still be
// read, but saving it requires supplying a source explicitly.
func (s *Source) Close() error {
	s.data = nil
	if s.doc != nil {
		s.doc.src = nil
	}
	return nil
}
