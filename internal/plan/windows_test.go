package plan

import (
	"encoding/binary"
	"io"
	"os"
	"strings"
	"testing"

	"flasher/internal/source"
)

// TestWindowsLayout plans a real Windows ISO onto drives of several sizes
// and checks the layout, for example:
//
//	FLASHER_TEST_ISO=~/Win11.iso go test -run WindowsLayout ./internal/plan
func TestWindowsLayout(t *testing.T) {
	iso := os.Getenv("FLASHER_TEST_ISO")
	if iso == "" {
		t.Skip("set FLASHER_TEST_ISO to run")
	}
	img, err := source.Inspect(iso)
	if err != nil {
		t.Fatal(err)
	}
	if img.Kind != source.KindWindowsISO {
		t.Fatalf("%s is a %s, not a Windows installer ISO", iso, img.Kind)
	}
	for _, c := range []struct {
		size int64
		ss   int
	}{
		{16 << 30, 512},
		{16<<30 + 12345*512, 512}, // not a whole number of MiB
		{250059350016, 512},       // a real 250 GB drive
		{3 << 40, 512},            // past what MBR can address
		{64 << 30, 4096},
	} {
		p, err := Windows(img, c.size, c.ss)
		if err != nil {
			t.Fatalf("%d bytes: %v", c.size, err)
		}
		var end, total int64
		for _, s := range p.Segments {
			if s.Offset < end {
				t.Fatalf("%d bytes: %s at %d overlaps the previous segment ending at %d", c.size, s.Name, s.Offset, end)
			}
			if s.Offset%int64(c.ss) != 0 {
				t.Fatalf("%d bytes: %s at %d is not sector aligned", c.size, s.Name, s.Offset)
			}
			end = s.Offset + s.Length
			total += s.Length
		}
		if end != c.size {
			t.Errorf("%d bytes: the last segment ends at %d", c.size, end)
		}
		if total != p.Total {
			t.Errorf("%d bytes: Total %d, segments add up to %d", c.size, p.Total, total)
		}

		rc, _ := p.Segments[0].Open()
		mbr := make([]byte, 512)
		io.ReadFull(rc, mbr)
		rc.Close()
		start := int64(binary.LittleEndian.Uint32(mbr[454:])) * int64(c.ss)
		length := int64(binary.LittleEndian.Uint32(mbr[458:])) * int64(c.ss)
		if start != 1<<20 || start+length > c.size || length <= 0 {
			t.Errorf("%d bytes: partition at %d, %d bytes", c.size, start, length)
		}

		var swm int
		for _, s := range p.Segments {
			if strings.HasSuffix(s.Name, ".swm") {
				swm++
			}
			if strings.EqualFold(s.Name, "install.wim") {
				t.Errorf("%d bytes: install.wim was not split", c.size)
			}
		}
		t.Logf("%d bytes, %d-byte sectors: %d segments, %d swm parts, partition %d bytes", c.size, c.ss, len(p.Segments), swm, length)
		p.Close()
	}
	if _, err := Windows(img, 4<<30, 512); err == nil || !strings.Contains(err.Error(), "too small") {
		t.Errorf("a 4 GiB drive should be too small, got %v", err)
	}
}
