package waxlabel

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"time"

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
	// Stat before Open: opening the read end of a FIFO blocks until a writer appears, so a
	// non-regular path must be rejected before os.Open or the parse hangs before any guard
	// runs. Stat follows symlinks, so a symlink to a regular file still works.
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
	// Size from the open descriptor, not the pre-open stat: a concurrent truncate in the
	// stat-then-open window could have made that one stale.
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
		ModTimeUnixNano: unixNanoOrZero(info.ModTime()),
	}
	id.INode, id.Device = sysInodeDevice(info)
	return id, nil
}

// The window time.Time.UnixNano is defined over; outside it the result wraps.
var (
	minRepresentableTime = time.Unix(0, math.MinInt64)
	maxRepresentableTime = time.Unix(0, math.MaxInt64)
)

// unixNanoOrZero returns t as Unix nanoseconds, or 0 where UnixNano is undefined (outside
// roughly 1678-2262). 0 already means "mtime unknown" to Identity.Matches, so such a file
// is treated the way a stream is: never compared, never preserved. So is a file dated
// exactly at the epoch, a real time this sentinel cannot distinguish.
func unixNanoOrZero(t time.Time) int64 {
	if t.Before(minRepresentableTime) || t.After(maxRepresentableTime) {
		return 0
	}
	return t.UnixNano()
}

// reopensFileSource reports whether resolving this document's own source reopens its file
// rather than reusing in-memory bytes, true only for a ParseFile document. It gates the
// source-unchanged guard: only a reopened file can have changed since parse.
func (d *Document) reopensFileSource() bool {
	return d.src == nil && d.path != ""
}

// resolveSource selects the bytes to read for a write or a hash: an explicit source, else the
// document's in-memory source (OpenSource), else its file reopened (ParseFile). remedy is the
// caller-specific "how to supply a source" hint, since a generic one is half-wrong for each.
//
// The closer must always be called and is idempotent: the write path calls it as soon as the
// copy is done, so the rename is not blocked by a handle on the file it replaces, and keeps
// its defer as the backstop.
func (d *Document) resolveSource(explicit core.ReaderAtSized, remedy string) (core.ReaderAtSized, func(), error) {
	noop := func() {}
	// A zero Document has nothing to write or hash. Prepare's wording, so the generic
	// message below fires only for an initialized-but-detached Parse doc, where supplying a
	// source really is the remedy.
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

// Source retains the complete bytes of a non-seekable stream parsed for editing. Unlike a
// Document, which is detached and holds nothing, a Source owns the teed buffer and is
// closable. Edit the [Source.Document] and save it; the Source supplies the copied bytes.
type Source struct {
	doc  *Document
	data []byte
}

// OpenSource parses a non-seekable stream, teeing it into memory as it reads (you cannot
// spool bytes after they have passed). The returned Source is closable and its Document
// can be edited and saved.
//
// Saving over a path the CALLER still holds open fails on Windows. The document reads
// in-memory bytes, so the library has no handle to release; close r's file first.
func OpenSource(ctx context.Context, r io.Reader, opts ...ParseOption) (*Source, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	// io.ReadAll(nil) panics.
	if r == nil {
		return nil, fmt.Errorf("%w: nil reader", waxerr.ErrInvalidData)
	}
	// Resolved once and reused below: the ingest cap must come from the same options the
	// parse sees.
	po := resolveParseOptions(opts)
	limit := po.MaxSourceBytes
	// A bound at the int64 ceiling would overflow the limit+1 probe below to a negative
	// that io.LimitReader reads as "nothing", and nothing exceeds it anyway.
	if limit == math.MaxInt64 {
		limit = 0
	}
	// Bounded so an endless stream cannot exhaust memory. limit+1 so a stream of exactly
	// limit still parses while the first byte past it trips the guard; a plain LimitReader
	// would truncate and misparse instead.
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

// Document returns the parsed document. It is detached, so it stays valid after Close;
// only the Source's role as a write source ends there.
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
