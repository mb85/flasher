package media

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeExt4(t *testing.T, v *Ext4, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	// Fill where the regions leave old data, so the test sees what a
	// reused drive would hold: the first MiB and the inode tables.
	junk := []byte(strings.Repeat("\xA5", 1<<20))
	f.WriteAt(junk, 0)
	end := int64(0)
	for _, r := range v.Regions {
		if r.Offset < end {
			t.Fatalf("region %s at %d overlaps the previous one ending at %d", r.Name, r.Offset, end)
		}
		if r.Offset%extBlock != 0 || r.Length%512 != 0 {
			t.Fatalf("region %s at %d+%d is not aligned", r.Name, r.Offset, r.Length)
		}
		end = r.Offset + r.Length
		if end > size {
			t.Fatalf("region %s ends at %d, past the volume's %d bytes", r.Name, end, size)
		}
		b := r.Data
		if b == nil {
			b = make([]byte, r.Length)
		}
		if _, err := f.WriteAt(b, r.Offset); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExt4Volume(t *testing.T) {
	if _, err := exec.LookPath("e2fsck"); err != nil {
		t.Skip("e2fsprogs not installed")
	}
	sizes := []int64{
		20 << 20, 128 << 20, 128<<20 + 100<<10, 129 << 20, 1 << 30, 3<<30 + 4<<10 + 512,
		50 << 30, 300 << 30, 2<<40 - 1<<20,
	}
	for _, size := range sizes {
		t.Run(fmt.Sprint(size>>20, "MiB"), func(t *testing.T) {
			files := map[string][]byte{"persistence.conf": []byte("/ union\n")}
			v, err := PlanExt4(size, "persistence", files)
			if err != nil {
				t.Fatal(err)
			}
			img := filepath.Join(t.TempDir(), "vol.img")
			writeExt4(t, v, img, size)

			out, err := exec.Command("e2fsck", "-fn", img).CombinedOutput()
			if err != nil {
				t.Fatalf("e2fsck: %v\n%s", err, out)
			}
			t.Logf("%d groups: %s", v.Groups, lastLine(out))

			out, err = exec.Command("debugfs", "-R", "cat /persistence.conf", img).Output()
			if err != nil || string(out) != "/ union\n" {
				t.Errorf("persistence.conf reads %q: %v", out, err)
			}
			out, err = exec.Command("blkid", "-p", "-o", "export", img).Output()
			if err != nil || !strings.Contains(string(out), "TYPE=ext4") || !strings.Contains(string(out), "LABEL=persistence") {
				t.Errorf("blkid: %v\n%s", err, out)
			}
		})
	}
}

func TestExt4TooSmall(t *testing.T) {
	if _, err := PlanExt4(4<<20, "x", nil); err == nil {
		t.Error("a 4 MiB volume should be refused")
	}
}

func TestCRC16(t *testing.T) {
	// The kernel's crc16 is CRC-16/ARC when started from zero.
	if got := crc16(0, []byte("123456789")); got != 0xBB3D {
		t.Errorf("crc16 = %#x, want 0xbb3d", got)
	}
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}
