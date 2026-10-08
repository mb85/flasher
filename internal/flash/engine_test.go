package flash

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"flasher/internal/device"
	"flasher/internal/plan"
	"flasher/internal/source"
)

const testChunk = 64 << 10

func writeImage(t *testing.T, data []byte, gz bool) *source.Image {
	t.Helper()
	p := filepath.Join(t.TempDir(), "img")
	var buf bytes.Buffer
	if gz {
		w := gzip.NewWriter(&buf)
		w.Write(data)
		w.Close()
	} else {
		buf.Write(data)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	img, err := source.Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func newDevice(t *testing.T, size int64) *device.File {
	t.Helper()
	d, err := device.CreateFile(filepath.Join(t.TempDir(), "drive"), size)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func readDevice(t *testing.T, d device.Device, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := d.ReadAt(b, 0); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRawWriteAndVerify(t *testing.T) {
	for _, gz := range []bool{false, true} {
		// An odd length makes the last testChunk end mid-sector.
		data := randBytes(5*testChunk + 1234)
		img := writeImage(t, data, gz)
		if gz && img.Size != -1 {
			t.Fatalf("gzip size should be unknown, got %d", img.Size)
		}
		dev := newDevice(t, 8*testChunk)
		var last Progress
		var calls atomic.Int32
		err := Run(context.Background(), dev, plan.Raw(img), Options{
			Verify:    true,
			ChunkSize: testChunk,
			Progress:  func(p Progress) { last = p; calls.Add(1) },
		})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(readDevice(t, dev, len(data)), data) {
			t.Fatal("drive contents differ from the image")
		}
		if last.Stage != Done || last.Fraction != 1 || calls.Load() == 0 {
			t.Fatalf("final progress %+v after %d calls", last, calls.Load())
		}
	}
}

// corrupting flips one byte on read-back, as a failing stick would.
type corrupting struct {
	device.Device
	at int64
}

func (c *corrupting) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.Device.ReadAt(p, off)
	if c.at >= off && c.at < off+int64(n) {
		p[c.at-off] ^= 0xff
	}
	return n, err
}

func TestVerifyCatchesCorruption(t *testing.T) {
	data := randBytes(4 * testChunk)
	dev := &corrupting{Device: newDevice(t, 4*testChunk), at: 2*testChunk + 77}
	err := Run(context.Background(), dev, plan.Raw(writeImage(t, data, false)), Options{Verify: true, ChunkSize: testChunk})
	var ve *VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("want a VerifyError, got %v", err)
	}
	if ve.Offset != 2*testChunk {
		t.Fatalf("mismatch reported at %d, want %d", ve.Offset, 2*testChunk)
	}
}

func TestTooLarge(t *testing.T) {
	data := randBytes(4 * testChunk)
	for _, gz := range []bool{false, true} {
		dev := newDevice(t, 2*testChunk)
		err := Run(context.Background(), dev, plan.Raw(writeImage(t, data, gz)), Options{ChunkSize: testChunk})
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("gz=%v: want ErrTooLarge, got %v", gz, err)
		}
	}
}

// slowReader cancels the context after the first read.
type slowReader struct {
	cancel context.CancelFunc
	n      int
}

func (r *slowReader) Read(p []byte) (int, error) {
	r.n++
	if r.n == 2 {
		r.cancel()
	}
	return len(p), nil
}

func TestCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &plan.Plan{Total: -1, Segments: []plan.Segment{{
		Name: "endless", Length: -1,
		Open: func() (io.ReadCloser, error) { return io.NopCloser(&slowReader{cancel: cancel}), nil },
	}}}
	err := Run(ctx, newDevice(t, 1<<30), p, Options{ChunkSize: testChunk})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestSegments(t *testing.T) {
	a, b := randBytes(700), randBytes(3*testChunk+5)
	p := &plan.Plan{Total: int64(len(a) + len(b)), Segments: []plan.Segment{
		plan.Bytes("a", 0, a),
		plan.Bytes("b", 4096, b),
	}}
	dev := newDevice(t, 8*testChunk)
	if err := Run(context.Background(), dev, p, Options{Verify: true, ChunkSize: testChunk}); err != nil {
		t.Fatal(err)
	}
	got := readDevice(t, dev, 4096+len(b))
	if !bytes.Equal(got[:700], a) || !bytes.Equal(got[4096:], b) {
		t.Fatal("segments landed in the wrong place")
	}
	if !bytes.Equal(got[700:1024], make([]byte, 324)) {
		t.Fatal("padding after a short segment should be zeros")
	}

	bad := &plan.Plan{Total: 1, Segments: []plan.Segment{plan.Bytes("x", 100, []byte{1})}}
	if err := Run(context.Background(), dev, bad, Options{ChunkSize: testChunk}); err == nil {
		t.Fatal("expected an error for an unaligned segment")
	}
}
