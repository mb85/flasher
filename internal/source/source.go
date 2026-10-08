// Package source opens disk images, works out what they are, and streams
// their contents with decompression handled transparently.
package source

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync/atomic"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// Compression is the container format wrapped around the image data.
type Compression int

const (
	None Compression = iota
	Gzip
	Xz
	Zstd
	Bzip2
	Zip
)

func (c Compression) String() string {
	return [...]string{"none", "gzip", "xz", "zstd", "bzip2", "zip"}[c]
}

// Kind is what the decompressed image is, which decides how it gets written.
type Kind int

const (
	// KindRaw is a disk image with a partition table, or anything else
	// that is not an ISO. It is written byte for byte.
	KindRaw Kind = iota
	// KindHybridISO is an ISO that also carries an MBR or GPT partition
	// table, so it boots when written byte for byte.
	KindHybridISO
	// KindPlainISO is an ISO with no partition table. Written byte for
	// byte it will usually not boot from USB.
	KindPlainISO
	// KindWindowsISO is a Windows installer ISO. It is rebuilt as FAT32
	// media rather than written byte for byte.
	KindWindowsISO
)

func (k Kind) String() string {
	return [...]string{"disk image", "hybrid ISO", "ISO without a partition table", "Windows installer ISO"}[k]
}

// Image is an inspected source image.
type Image struct {
	Path        string
	FileSize    int64 // bytes on disk, compressed or not
	Compression Compression
	Kind        Kind
	// Size is the decompressed size, or -1 when it cannot be known without
	// decompressing everything.
	Size int64
	// Label is the ISO volume label, when there is one.
	Label string

	zipEntry string // the image inside a zip archive
}

// Description is a short human-readable summary, such as
// "hybrid ISO, xz compressed".
func (img *Image) Description() string {
	d := img.Kind.String()
	if img.Compression != None {
		d += ", " + img.Compression.String() + " compressed"
	}
	return d
}

// sniffLen covers the ISO9660 primary volume descriptor at 32 KiB.
const sniffLen = 64 << 10

// Inspect opens the image at path and works out its compression and kind.
func Inspect(p string) (*Image, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	img := &Image{Path: p, FileSize: fi.Size(), Size: -1}

	magic := make([]byte, 8)
	n, _ := io.ReadFull(f, magic)
	img.Compression = sniffCompression(magic[:n])

	switch img.Compression {
	case None:
		img.Size = fi.Size()
	case Zip:
		name, size, err := zipImage(f, fi.Size())
		if err != nil {
			return nil, err
		}
		img.zipEntry, img.Size = name, size
	}

	r, err := img.open(f, nil)
	if err != nil {
		return nil, err
	}
	head := make([]byte, sniffLen)
	n, err = io.ReadFull(r, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	head = head[:n]
	if c, ok := r.(io.Closer); ok {
		c.Close()
	}

	img.Kind = classify(head)
	if img.Kind == KindPlainISO && img.Compression == None {
		label, ok, err := windowsISO(p)
		if err != nil {
			return nil, err
		}
		if ok {
			img.Kind, img.Label = KindWindowsISO, label
		}
	}
	return img, nil
}

func sniffCompression(b []byte) Compression {
	switch {
	case bytes.HasPrefix(b, []byte{0x1f, 0x8b}):
		return Gzip
	case bytes.HasPrefix(b, []byte{0xfd, '7', 'z', 'X', 'Z', 0}):
		return Xz
	case bytes.HasPrefix(b, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		return Zstd
	case bytes.HasPrefix(b, []byte("BZh")):
		return Bzip2
	case bytes.HasPrefix(b, []byte("PK\x03\x04")):
		return Zip
	}
	return None
}

// classify looks at the first 64 KiB of decompressed data.
func classify(head []byte) Kind {
	isISO := len(head) >= 0x8006 && string(head[0x8001:0x8006]) == "CD001"
	if !isISO {
		return KindRaw
	}
	if hasPartitionTable(head) {
		return KindHybridISO
	}
	return KindPlainISO
}

func hasPartitionTable(head []byte) bool {
	if len(head) >= 520 && string(head[512:520]) == "EFI PART" {
		return true
	}
	if len(head) < 512 || head[510] != 0x55 || head[511] != 0xaa {
		return false
	}
	for i := range 4 {
		e := head[446+16*i:]
		if e[4] != 0 { // partition type
			return true
		}
	}
	return false
}

// zipImage finds the single disk image inside a zip archive, ignoring
// directories and macOS metadata.
func zipImage(f *os.File, size int64) (string, int64, error) {
	zr, err := zip.NewReader(f, size)
	if err != nil {
		return "", 0, err
	}
	var found *zip.File
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() || strings.HasPrefix(zf.Name, "__MACOSX/") || strings.HasPrefix(path.Base(zf.Name), "._") {
			continue
		}
		if found != nil {
			return "", 0, fmt.Errorf("zip archive holds more than one file (%s and %s)", found.Name, zf.Name)
		}
		found = zf
	}
	if found == nil {
		return "", 0, errors.New("zip archive is empty")
	}
	return found.Name, int64(found.UncompressedSize64), nil
}

// Stream is an open, decompressing reader over an image.
type Stream struct {
	io.Reader
	file     *os.File
	closer   io.Closer
	consumed atomic.Int64 // compressed bytes read from the file
	fileSize int64
}

// Fraction is how far through the source file the stream has read, which
// tracks progress even when the decompressed size is unknown.
func (s *Stream) Fraction() float64 {
	if s.fileSize == 0 {
		return 0
	}
	return min(float64(s.consumed.Load())/float64(s.fileSize), 1)
}

func (s *Stream) Close() error {
	if s.closer != nil {
		s.closer.Close()
	}
	return s.file.Close()
}

// Open starts streaming the decompressed image.
func (img *Image) Open() (*Stream, error) {
	f, err := os.Open(img.Path)
	if err != nil {
		return nil, err
	}
	s := &Stream{file: f, fileSize: img.FileSize}
	r, err := img.open(f, &s.consumed)
	if err != nil {
		f.Close()
		return nil, err
	}
	s.Reader = r
	if c, ok := r.(io.Closer); ok {
		s.closer = c
	}
	return s, nil
}

func (img *Image) open(f *os.File, counter *atomic.Int64) (io.Reader, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var raw io.Reader = f
	if counter != nil {
		raw = &countingReader{r: f, n: counter}
	}
	switch img.Compression {
	case None:
		return raw, nil
	case Gzip:
		zr, err := gzip.NewReader(bufio.NewReaderSize(raw, 1<<20))
		if err != nil {
			return nil, err
		}
		return zr, nil
	case Xz:
		return xz.NewReader(bufio.NewReaderSize(raw, 1<<20))
	case Zstd:
		zr, err := zstd.NewReader(raw)
		if err != nil {
			return nil, err
		}
		return zr.IOReadCloser(), nil
	case Bzip2:
		return bzip2.NewReader(bufio.NewReaderSize(raw, 1<<20)), nil
	case Zip:
		var ra io.ReaderAt = f
		if counter != nil {
			ra = &countingReaderAt{r: f, n: counter}
		}
		zr, err := zip.NewReader(ra, img.FileSize)
		if err != nil {
			return nil, err
		}
		return zr.Open(img.zipEntry)
	}
	return nil, fmt.Errorf("unknown compression %d", img.Compression)
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

type countingReaderAt struct {
	r io.ReaderAt
	n *atomic.Int64
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.n.Add(int64(n))
	return n, err
}
