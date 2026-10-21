package media

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/carbon-os/diskiso"
)

// TestSplitFromISO splits install.wim straight out of a Windows ISO and writes
// the parts to FLASHER_SWM_OUT for checking with wimlib, for example:
//
//	FLASHER_TEST_ISO=~/Win11.iso FLASHER_SWM_OUT=~/.cache/flasher-test/swm go test -run SplitFromISO ./internal/media
//	wimlib-imagex verify install.swm --ref='install*.swm'
func TestSplitFromISO(t *testing.T) {
	iso, out := os.Getenv("FLASHER_TEST_ISO"), os.Getenv("FLASHER_SWM_OUT")
	if iso == "" || out == "" {
		t.Skip("set FLASHER_TEST_ISO and FLASHER_SWM_OUT to run")
	}
	disc, err := diskiso.Attach(iso)
	if err != nil {
		t.Fatal(err)
	}
	defer disc.Detach()
	vol, err := disc.Mount()
	if err != nil {
		t.Fatal(err)
	}
	f, err := vol.Open("/sources/install.wim")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	src := f.(io.ReaderAt)
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	w, err := ReadWIM(src, fi.Size())
	if err != nil {
		t.Fatal(err)
	}
	parts, err := w.Split("install", DefaultPartSize)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range parts {
		o, err := os.Create(filepath.Join(out, p.Name))
		if err != nil {
			t.Fatal(err)
		}
		n, err := io.CopyBuffer(o, p.Open(src), make([]byte, 4<<20))
		if cerr := o.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			t.Fatal(err)
		}
		if n != p.Size {
			t.Fatalf("%s: wrote %d bytes, planned %d", p.Name, n, p.Size)
		}
		t.Logf("%s: %d bytes", p.Name, n)
	}
}
