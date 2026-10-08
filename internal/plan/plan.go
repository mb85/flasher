// Package plan describes what gets written where on a drive, so one engine
// can write raw images and rebuilt Windows media alike.
package plan

import (
	"io"
	"sync/atomic"

	"flasher/internal/source"
)

// Segment is a run of bytes written at a fixed offset on the drive.
type Segment struct {
	Name   string // shown in errors, e.g. "image" or "sources/boot.wim"
	Offset int64  // must be a multiple of the drive's sector size
	// Length is the exact byte count, or -1 to write until the reader
	// is exhausted (compressed images of unknown size).
	Length int64
	Open   func() (io.ReadCloser, error)
}

// Plan is an ordered list of segments, sorted by offset.
type Plan struct {
	Segments []Segment
	// Total is the number of bytes the segments will write, or -1 when a
	// segment's length is unknown.
	Total int64
	// Fraction reports progress when Total is unknown. It may be nil.
	Fraction func() float64
}

// Raw writes an image byte for byte from offset 0.
func Raw(img *source.Image) *Plan {
	var stream atomic.Pointer[source.Stream]
	p := &Plan{Total: img.Size}
	p.Segments = []Segment{{
		Name:   "image",
		Offset: 0,
		Length: img.Size,
		Open: func() (io.ReadCloser, error) {
			s, err := img.Open()
			if err != nil {
				return nil, err
			}
			stream.Store(s)
			return s, nil
		},
	}}
	p.Fraction = func() float64 {
		if s := stream.Load(); s != nil {
			return s.Fraction()
		}
		return 0
	}
	return p
}

// Bytes is a segment holding in-memory data, such as a partition table.
func Bytes(name string, offset int64, b []byte) Segment {
	return Segment{
		Name:   name,
		Offset: offset,
		Length: int64(len(b)),
		Open: func() (io.ReadCloser, error) {
			return io.NopCloser(&byteReader{b: b}), nil
		},
	}
}

type byteReader struct{ b []byte }

func (r *byteReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}
