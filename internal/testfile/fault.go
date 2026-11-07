package testfile

import (
	"io"
	"io/fs"
	"syscall"
)

// Sized is a source that knows its length.
type Sized interface {
	io.ReaderAt
	Size() int64
}

// Faulty is a source whose reads that Fails picks fail as a device error does, with a
// *fs.PathError that holds syscall.EIO; every other read is Src's. Failed counts the failures.
type Faulty struct {
	Src    Sized
	Fails  func(off, n int64) bool
	Failed int
}

func (f *Faulty) Size() int64 { return f.Src.Size() }

func (f *Faulty) ReadAt(p []byte, off int64) (int, error) {
	if f.Fails(off, int64(len(p))) {
		f.Failed++
		return 0, &fs.PathError{Op: "read", Path: "media", Err: syscall.EIO}
	}
	return f.Src.ReadAt(p, off)
}

// From picks the reads that reach byte at: a fault from there to the end of the file.
func From(at int64) func(off, n int64) bool {
	return func(off, n int64) bool { return off+n > at }
}

// Covering picks the reads that cover byte at.
func Covering(at int64) func(off, n int64) bool {
	return func(off, n int64) bool { return off <= at && at < off+n }
}

// Nth picks only the nth read, counted from 1, of those pick picks, as a transient fault does: the
// reads before and after it succeed.
func Nth(nth int, pick func(off, n int64) bool) func(off, n int64) bool {
	seen := 0
	return func(off, n int64) bool {
		if !pick(off, n) {
			return false
		}
		seen++
		return seen == nth
	}
}
