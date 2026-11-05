package waxlabel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// fileSource adapts an open file to ReaderAtSized. Not retained by Document. info is the
// fstat taken at open, so it describes the bytes read through f.
type fileSource struct {
	f    *os.File
	size int64
	info os.FileInfo
}

func openFileSource(path string) (*fileSource, error) {
	// Stat before Open: FIFO open blocks until a writer appears.
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
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &fileSource{f: f, size: fi.Size(), info: fi}, nil
}

func (s *fileSource) ReadAt(p []byte, off int64) (int, error) { return s.f.ReadAt(p, off) }
func (s *fileSource) Size() int64                             { return s.size }
func (s *fileSource) Close() error                            { return s.f.Close() }

// identity is the open file's identity now, recorded under path.
func (s *fileSource) identity(path string) (core.Identity, error) {
	info, err := s.f.Stat()
	if err != nil {
		return core.Identity{}, err
	}
	return core.FileIdentity(path, info), nil
}

// checkStable refuses a source that changed while a read of it ran: an append, a truncation, or an
// in-place write since before was taken. A write that replaces the source also refuses a change
// time move (a chmod or attribute edit the rename would undo); other reads only need the content
// to have held still, which a moved mtime casts doubt on.
func (s *fileSource) checkStable(path string, before core.Identity, inPlace bool) error {
	after, err := s.identity(path)
	if err != nil {
		return err
	}
	ok, why := before.Matches(after)
	if !inPlace {
		if ok, why = before.MatchesContent(after); ok && before.ModTimeUnixNano != after.ModTimeUnixNano {
			ok, why = false, "modification time changed"
		}
	}
	if !ok {
		return fmt.Errorf("%w: %s during the read", waxerr.ErrSourceChanged, why)
	}
	return nil
}

// fileIdentity captures size, times, and file identity of path for save-back checks.
func fileIdentity(path string) (core.Identity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return core.Identity{}, err
	}
	return core.FileIdentity(path, info), nil
}

// reopenFileSource reopens a parsed document's file. A path that no longer names a regular file,
// or names nothing, is a source that changed since parse.
func reopenFileSource(path string) (*fileSource, error) {
	info, err := os.Stat(path)
	if err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is no longer a regular file", waxerr.ErrSourceChanged, path)
	}
	src, err := openFileSource(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s no longer exists (%w)", waxerr.ErrSourceChanged, path, err)
	}
	return src, err
}

// resolveSource picks explicit, then in-memory, then reopened file. Closer is idempotent.
func (d *Document) resolveSource(explicit core.ReaderAtSized, remedy string) (core.ReaderAtSized, func(), error) {
	noop := func() {}
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
		src, err := reopenFileSource(d.path)
		if err != nil {
			return nil, noop, err
		}
		return src, func() { src.Close() }, nil
	}
	return nil, noop, fmt.Errorf("%w: no source available; %s", waxerr.ErrInvalidData, remedy)
}

// Source owns teed bytes of a non-seekable stream. Edit [Source.Document]; Source supplies copy bytes.
type Source struct {
	doc  *Document
	data []byte
}

// OpenSource parses a non-seekable stream into memory. Close any caller-held path
// before save on Windows.
func OpenSource(ctx context.Context, r io.Reader, opts ...ParseOption) (*Source, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("%w: nil reader", waxerr.ErrInvalidData)
	}
	po := resolveParseOptions(opts)
	limit := po.MaxSourceBytes
	if limit == math.MaxInt64 {
		limit = 0
	}
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

// Document returns the parsed document (valid after Close).
func (s *Source) Document() *Document { return s.doc }

// Close releases the buffer; saving afterward needs an explicit source.
func (s *Source) Close() error {
	s.data = nil
	if s.doc != nil {
		s.doc.src = nil
	}
	return nil
}
