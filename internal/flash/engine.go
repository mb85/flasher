// Package flash runs a write plan against a device, reporting progress and
// verifying the result by reading it back.
package flash

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"flasher/internal/device"
	"flasher/internal/plan"
)

// Stage is the phase a flash is in.
type Stage int

const (
	Writing Stage = iota
	Syncing
	Verifying
	Done
)

func (s Stage) String() string {
	return [...]string{"Writing", "Syncing", "Verifying", "Done"}[s]
}

// Progress is a snapshot reported while a flash runs.
type Progress struct {
	Stage Stage
	Bytes int64 // written (or verified) so far in this stage
	// Fraction of the stage complete, from 0 to 1.
	Fraction    float64
	BytesPerSec float64
	ETA         time.Duration // zero when unknown
}

// Options adjusts a flash.
type Options struct {
	Verify bool
	// Progress is called from a background goroutine a few times a second.
	Progress func(Progress)
	// ChunkSize is the write size; it defaults to 4 MiB.
	ChunkSize int
}

// ErrTooLarge means the plan does not fit on the device.
var ErrTooLarge = errors.New("image is larger than the drive")

// VerifyError reports the first chunk that read back differently.
type VerifyError struct {
	Segment string
	Offset  int64
}

func (e *VerifyError) Error() string {
	return fmt.Sprintf("verification failed: data read back from offset %d (%s) differs from what was written", e.Offset, e.Segment)
}

type record struct {
	seg    int
	offset int64
	length int // real bytes, before sector padding
	sum    [sha256.Size]byte
}

type chunk struct {
	buf    []byte
	n      int // real bytes
	padded int // bytes to write, a multiple of the sector size
	offset int64
	seg    int
}

// Run writes p to dev, then reads it back if opts.Verify is set.
func Run(ctx context.Context, dev device.Device, p *plan.Plan, opts Options) error {
	if opts.ChunkSize == 0 {
		opts.ChunkSize = 4 << 20
	}
	sector := dev.SectorSize()
	if opts.ChunkSize%sector != 0 {
		return fmt.Errorf("chunk size %d is not a multiple of the sector size %d", opts.ChunkSize, sector)
	}
	if p.DriveSize != 0 && p.DriveSize != dev.Size() || p.SectorSize != 0 && p.SectorSize != sector {
		return fmt.Errorf("the drive changed since it was chosen (now %d bytes with %d-byte sectors, planned for %d with %d)",
			dev.Size(), sector, p.DriveSize, p.SectorSize)
	}
	if p.Total > dev.Size() {
		return fmt.Errorf("%w (%d bytes, drive holds %d)", ErrTooLarge, p.Total, dev.Size())
	}
	for _, s := range p.Segments {
		if s.Offset%int64(sector) != 0 {
			return fmt.Errorf("segment %s starts at %d, which is not sector aligned", s.Name, s.Offset)
		}
	}

	rep := newReporter(opts.Progress, p)
	defer rep.stop()

	records, err := write(ctx, dev, p, opts.ChunkSize, sector, rep)
	if err != nil {
		return err
	}
	rep.setStage(Syncing, 0)
	if err := dev.Sync(); err != nil {
		return &DriveError{Op: "flush", Offset: -1, Err: err}
	}
	if opts.Verify {
		var total int64
		for _, r := range records {
			total += int64(r.length)
		}
		rep.setStage(Verifying, total)
		if err := verify(ctx, dev, p, records, opts.ChunkSize, sector, rep); err != nil {
			return err
		}
	}
	rep.setStage(Done, 0)
	return nil
}

// write streams every segment through a small pool of buffers so reading
// (and decompressing) the source overlaps with writing the drive.
func write(ctx context.Context, dev device.Device, p *plan.Plan, chunkSize, sector int, rep *reporter) ([]record, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	const nbuf = 3
	free := make(chan []byte, nbuf)
	for range nbuf {
		free <- device.AlignedBuffer(chunkSize)
	}
	full := make(chan chunk, nbuf)

	var readErr error
	var wg sync.WaitGroup
	wg.Go(func() {
		defer close(full)
		readErr = readSegments(ctx, p, free, full, chunkSize, sector)
	})

	var records []record
	var writeErr error
	for c := range full {
		if writeErr == nil {
			writeErr = writeChunk(dev, p, c)
			if writeErr != nil {
				cancel()
			} else {
				records = append(records, record{seg: c.seg, offset: c.offset, length: c.n, sum: sha256.Sum256(c.buf[:c.n])})
				rep.add(int64(c.n))
			}
		}
		free <- c.buf
	}
	wg.Wait()
	if writeErr != nil {
		return nil, writeErr
	}
	if readErr != nil {
		return nil, readErr
	}
	return records, ctx.Err()
}

func writeChunk(dev device.Device, p *plan.Plan, c chunk) error {
	if c.offset+int64(c.padded) > dev.Size() {
		return fmt.Errorf("%w (%s does not fit)", ErrTooLarge, p.Segments[c.seg].Name)
	}
	if _, err := dev.WriteAt(c.buf[:c.padded], c.offset); err != nil {
		return &DriveError{Op: "write", Offset: c.offset, Err: err}
	}
	return nil
}

func readSegments(ctx context.Context, p *plan.Plan, free chan []byte, full chan<- chunk, chunkSize, sector int) error {
	for i, s := range p.Segments {
		r, err := s.Open()
		if err != nil {
			return &SourceError{Segment: s.Name, Err: err}
		}
		err = readSegment(ctx, i, s, r, free, full, chunkSize, sector)
		r.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func readSegment(ctx context.Context, idx int, s plan.Segment, r io.Reader, free chan []byte, full chan<- chunk, chunkSize, sector int) error {
	off := s.Offset
	remaining := s.Length
	for remaining != 0 {
		var buf []byte
		select {
		case buf = <-free:
		case <-ctx.Done():
			return ctx.Err()
		}
		want := chunkSize
		if remaining > 0 && remaining < int64(want) {
			want = int(remaining)
		}
		n, err := io.ReadFull(r, buf[:want])
		eof := err == io.EOF || err == io.ErrUnexpectedEOF
		if err != nil && !eof {
			free <- buf
			return &SourceError{Segment: s.Name, Err: err}
		}
		if eof && remaining > 0 && n < want {
			free <- buf
			return &SourceError{Segment: s.Name, Err: fmt.Errorf("%w: %d bytes missing", io.ErrUnexpectedEOF, remaining-int64(n))}
		}
		if n == 0 {
			free <- buf
			break
		}
		padded := (n + sector - 1) / sector * sector
		clear(buf[n:padded])
		select {
		case full <- chunk{buf: buf, n: n, padded: padded, offset: off, seg: idx}:
		case <-ctx.Done():
			return ctx.Err()
		}
		off += int64(n)
		if remaining > 0 {
			remaining -= int64(n)
		}
		if eof || remaining == 0 {
			break
		}
		if n%sector != 0 {
			// Only the final chunk may end mid-sector.
			return &SourceError{Segment: s.Name, Err: fmt.Errorf("short read of %d bytes", n)}
		}
	}
	return nil
}

func verify(ctx context.Context, dev device.Device, p *plan.Plan, records []record, chunkSize, sector int, rep *reporter) error {
	buf := device.AlignedBuffer(chunkSize)
	for _, r := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		padded := (r.length + sector - 1) / sector * sector
		if _, err := dev.ReadAt(buf[:padded], r.offset); err != nil {
			return &DriveError{Op: "read back", Offset: r.offset, Err: err}
		}
		if sha256.Sum256(buf[:r.length]) != r.sum {
			return &VerifyError{Segment: p.Segments[r.seg].Name, Offset: r.offset}
		}
		rep.add(int64(r.length))
	}
	return nil
}
