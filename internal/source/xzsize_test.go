package source

import (
	"bytes"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ulikunitz/xz"
)

func xzBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(data)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestXZSize(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	a := make([]byte, 300_000)
	for i := range a {
		a[i] = byte(rng.IntN(16)) // compressible but not trivial
	}
	b := []byte("second stream")

	one := xzBytes(t, a)
	if got := xzSize(bytes.NewReader(one), int64(len(one))); got != int64(len(a)) {
		t.Errorf("one stream: %d, want %d", got, len(a))
	}

	// Two concatenated streams with padding between and after them.
	two := append(append(append(append([]byte{}, one...), 0, 0, 0, 0), xzBytes(t, b)...), make([]byte, 8)...)
	if got := xzSize(bytes.NewReader(two), int64(len(two))); got != int64(len(a)+len(b)) {
		t.Errorf("two streams: %d, want %d", got, len(a)+len(b))
	}

	// Damage anywhere in the index or footer gives "unknown", never a
	// wrong size.
	for _, off := range []int{1, 5, 8, 16} {
		bad := bytes.Clone(one)
		bad[len(bad)-off] ^= 0x40
		if got := xzSize(bytes.NewReader(bad), int64(len(bad))); got != -1 {
			t.Errorf("corrupt byte %d from the end: %d, want -1", off, got)
		}
	}
	if got := xzSize(bytes.NewReader([]byte("not xz at all")), 13); got != -1 {
		t.Errorf("not xz: %d", got)
	}

	// The xz tool writes multi-block files with -T; check one of those too.
	if _, err := exec.LookPath("xz"); err == nil {
		dir := t.TempDir()
		p := filepath.Join(dir, "a")
		big := bytes.Repeat(a, 20) // 6 MB, several blocks at --block-size=1MiB
		os.WriteFile(p, big, 0o644)
		if out, err := exec.Command("xz", "-T2", "--block-size=1MiB", p).CombinedOutput(); err != nil {
			t.Fatalf("xz: %v %s", err, out)
		}
		c, _ := os.ReadFile(p + ".xz")
		if got := xzSize(bytes.NewReader(c), int64(len(c))); got != int64(len(big)) {
			t.Errorf("xz -T2: %d, want %d", got, len(big))
		}
	}
}
