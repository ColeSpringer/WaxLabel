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

// fileSource adapts an open file to ReaderAtSized. Not retained by Document.
type fileSource struct {
	f    *os.File
	size int64
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
	return &fileSource{f: f, size: fi.Size()}, nil
}

func (s *fileSource) ReadAt(p []byte, off int64) (int, error) { return s.f.ReadAt(p, off) }
func (s *fileSource) Size() int64                             { return s.size }
func (s *fileSource) Close() error                            { return s.f.Close() }

// fileIdentity captures size, mtime, and inode/device for save-back checks.
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

var (
	minRepresentableTime = time.Unix(0, math.MinInt64)
	maxRepresentableTime = time.Unix(0, math.MaxInt64)
)

// unixNanoOrZero returns Unix nanos, or 0 outside the representable range / as unknown.
func unixNanoOrZero(t time.Time) int64 {
	if t.Before(minRepresentableTime) || t.After(maxRepresentableTime) {
		return 0
	}
	return t.UnixNano()
}

// reopensFileSource is true for ParseFile documents (source may have changed).
func (d *Document) reopensFileSource() bool {
	return d.src == nil && d.path != ""
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
		fs, err := openFileSource(d.path)
		if err != nil {
			return nil, noop, err
		}
		return fs, func() { fs.Close() }, nil
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
