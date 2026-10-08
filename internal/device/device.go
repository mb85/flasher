// Package device opens drives (or files standing in for them) for raw,
// exclusive reading and writing.
package device

import (
	"fmt"
	"os"
	"unsafe"
)

// Device is a block device opened for raw I/O. Offsets and lengths passed to
// ReadAt and WriteAt must be multiples of SectorSize, and buffers must come
// from AlignedBuffer, because unbuffered I/O on both Linux and Windows
// rejects anything else.
type Device interface {
	ReadAt(p []byte, off int64) (int, error)
	WriteAt(p []byte, off int64) (int, error)
	Size() int64
	SectorSize() int
	// Sync flushes everything written so far to the medium.
	Sync() error
	// Close releases the device. Implementations also ask the OS to
	// re-read the partition table, so new partitions show up.
	Close() error
}

// Alignment suits every sector size in practical use (512 and 4096).
const Alignment = 4096

// AlignedBuffer returns an n-byte slice whose start is Alignment-aligned.
func AlignedBuffer(n int) []byte {
	b := make([]byte, n+Alignment)
	off := int(uintptr(unsafe.Pointer(&b[0])) & (Alignment - 1))
	if off != 0 {
		off = Alignment - off
	}
	return b[off : off+n : off+n]
}

// File is a regular file standing in for a drive, for tests and for
// writing an image file instead of a device.
type File struct {
	f    *os.File
	size int64
}

// CreateFile creates (or truncates) a file of the given size to act as a
// drive.
func CreateFile(path string, size int64) (*File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		return nil, err
	}
	return &File{f: f, size: size}, nil
}

// OpenFile opens an existing file as a drive, keeping its size.
func OpenFile(path string) (*File, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return &File{f: f, size: fi.Size()}, nil
}

func (d *File) ReadAt(p []byte, off int64) (int, error)  { return d.f.ReadAt(p, off) }
func (d *File) WriteAt(p []byte, off int64) (int, error) { return d.f.WriteAt(p, off) }
func (d *File) Size() int64                              { return d.size }
func (d *File) SectorSize() int                          { return 512 }
func (d *File) Sync() error                              { return d.f.Sync() }
func (d *File) Close() error                             { return d.f.Close() }
