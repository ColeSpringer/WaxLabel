package waxlabel

import (
	"context"
	"fmt"
	"io"
	"os"
	"syscall"

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
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &fileSource{f: f, size: info.Size()}, nil
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
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		id.INode = uint64(st.Ino)
		id.Device = uint64(st.Dev)
	}
	return id, nil
}

// resolveSource selects the bytes to read for a write or a hash: an explicit
// source, else the document's in-memory source (from OpenSource), else its file
// reopened (from ParseFile). The returned closer must always be called. Both
// the write path and the hashing path share this.
func (d *Document) resolveSource(explicit core.ReaderAtSized) (core.ReaderAtSized, func(), error) {
	noop := func() {}
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
	return nil, noop, fmt.Errorf("%w: no source available; supply one via WriteTo or WithHashSource", waxerr.ErrInvalidData)
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
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	doc, err := parseSource(ctx, core.BytesSource(data), "", resolveParseOptions(opts))
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
