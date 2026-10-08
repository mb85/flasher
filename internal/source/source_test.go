package source

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// fixture builds 1 MiB of image data with the requested signatures.
func fixture(t *testing.T, mbr, iso bool) []byte {
	t.Helper()
	b := make([]byte, 1<<20)
	rand.Read(b[sniffLen:]) // keep the header region deterministic
	if mbr {
		b[446+4] = 0x0c // partition 1 type
		b[510], b[511] = 0x55, 0xaa
	}
	if iso {
		copy(b[0x8001:], "CD001")
	}
	return b
}

func compress(t *testing.T, c Compression, data []byte, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "img."+c.String())
	var buf bytes.Buffer
	switch c {
	case None:
		buf.Write(data)
	case Gzip:
		w := gzip.NewWriter(&buf)
		w.Write(data)
		w.Close()
	case Xz:
		w, err := xz.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
		w.Close()
	case Zstd:
		w, _ := zstd.NewWriter(&buf)
		w.Write(data)
		w.Close()
	case Zip:
		zw := zip.NewWriter(&buf)
		zw.Create("__MACOSX/")
		w, _ := zw.Create("disk.img")
		w.Write(data)
		zw.Close()
	case Bzip2:
		if _, err := exec.LookPath("bzip2"); err != nil {
			t.Skip("bzip2 not installed")
		}
		cmd := exec.Command("bzip2", "-c")
		cmd.Stdin = bytes.NewReader(data)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(out)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInspectAndStream(t *testing.T) {
	kinds := []struct {
		name     string
		mbr, iso bool
		want     Kind
	}{
		{"raw", true, false, KindRaw},
		{"hybrid", true, true, KindHybridISO},
		{"plain-iso", false, true, KindPlainISO},
	}
	for _, k := range kinds {
		data := fixture(t, k.mbr, k.iso)
		for _, c := range []Compression{None, Gzip, Xz, Zstd, Bzip2, Zip} {
			t.Run(k.name+"/"+c.String(), func(t *testing.T) {
				p := compress(t, c, data, t.TempDir())
				img, err := Inspect(p)
				if err != nil {
					t.Fatal(err)
				}
				if img.Compression != c || img.Kind != k.want {
					t.Fatalf("got %v/%v, want %v/%v", img.Compression, img.Kind, c, k.want)
				}
				if (c == None || c == Zip) && img.Size != int64(len(data)) {
					t.Fatalf("size %d, want %d", img.Size, len(data))
				}
				s, err := img.Open()
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				got, err := io.ReadAll(s)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, data) {
					t.Fatal("streamed data differs from the original")
				}
				if f := s.Fraction(); f < 0.99 {
					t.Fatalf("fraction %.3f after reading everything", f)
				}
			})
		}
	}
}

func TestZipWithTwoFiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "two.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	zw.Create("a.img")
	zw.Create("b.img")
	zw.Close()
	os.WriteFile(p, buf.Bytes(), 0o644)
	if _, err := Inspect(p); err == nil {
		t.Fatal("expected an error for a zip holding two files")
	}
}

// TestRealISOs classifies the ISOs on the development machine, if present.
func TestRealISOs(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := []struct {
		glob string
		want Kind
	}{
		{"Desktop/Win11_*.iso", KindWindowsISO},
		{"Desktop/SERVER2025_*.iso", KindWindowsISO},
		{"Downloads/archlinux-*.iso", KindHybridISO},
		{"Downloads/kali-linux-*.iso", KindHybridISO},
		{".config/VirtualBox/VBoxGuestAdditions_*.iso", KindPlainISO},
	}
	for _, c := range cases {
		matches, _ := filepath.Glob(filepath.Join(home, c.glob))
		if len(matches) == 0 {
			continue
		}
		p := matches[0]
		t.Run(filepath.Base(p), func(t *testing.T) {
			img, err := Inspect(p)
			if err != nil {
				t.Fatal(err)
			}
			if img.Kind != c.want {
				t.Fatalf("kind %v, want %v", img.Kind, c.want)
			}
			t.Logf("%s (label %q)", img.Description(), img.Label)
		})
	}
}
