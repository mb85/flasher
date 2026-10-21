package media

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestGeometry(t *testing.T) {
	for _, size := range []int64{40 << 20, 200 << 20, 8 << 30, 32<<30 + 1<<20, 256 << 30, 2 << 40} {
		for _, ss := range []int{512, 4096} {
			if ss == 4096 && size < 1<<30 {
				continue // too few 4 KiB clusters for FAT32
			}
			v, err := PlanFAT32(&Node{Dir: true}, size, ss, "TEST")
			if err != nil {
				t.Fatalf("%d bytes, %d-byte sectors: %v", size, ss, err)
			}
			entries := v.FATSectors * int64(ss) / 4
			if entries < v.Clusters+2 {
				t.Errorf("%d bytes: FAT holds %d entries for %d clusters", size, entries, v.Clusters)
			}
			if data := (v.Reserved + 2*v.FATSectors) * int64(ss); data%dataAlign != 0 {
				t.Errorf("%d bytes: data region starts at %d, not 1 MiB aligned", size, data)
			}
			if v.Clusters < minFAT32Clusters || v.Clusters > maxFAT32Clusters {
				t.Errorf("%d bytes: %d clusters is not FAT32", size, v.Clusters)
			}
			if v.Reserved > 0xFFFF {
				t.Errorf("%d bytes: %d reserved sectors", size, v.Reserved)
			}
			// Waste: the FAT may be a little larger than needed, but no
			// more than a sector or so beyond what the clusters need.
			if need := ceilDiv((v.Clusters+2)*4, int64(ss)); v.FATSectors > need+1 {
				t.Errorf("%d bytes: FAT has %d sectors, %d needed", size, v.FATSectors, need)
			}
		}
	}
}

func TestShortNames(t *testing.T) {
	taken := map[[11]byte]bool{}
	for _, c := range []struct{ name, want string }{
		{"setup.exe", "SETUP~1 EXE"},
		{"setup.exe.bak", "SETUPE~1BAK"},
		{"A long file name.txt", "ALONGF~1TXT"},
		{"A long file name 2.txt", "ALONGF~2TXT"},
		{".hidden", "HIDDEN~1   "},
		{"Ünï cödé.txt", "_N_C_D~1TXT"},
	} {
		s, err := aliasFor(c.name, taken)
		if err != nil {
			t.Fatal(err)
		}
		taken[s] = true
		if string(s[:]) != c.want {
			t.Errorf("aliasFor(%q) = %q, want %q", c.name, s[:], c.want)
		}
	}
	for name, ok := range map[string]bool{"BOOTMGR": true, "SETUP.EXE": true, "setup.exe": false, "A.B.C": false, "TOOLONGNAME": false, "X.ABCD": false, "EFI": true} {
		if _, got := exactShortName(name); got != ok {
			t.Errorf("exactShortName(%q) = %v, want %v", name, got, ok)
		}
	}
}

// testTree builds a tree covering the naming and allocation cases, and
// returns the expected contents by path.
func testTree(clusterSize int) (*Node, map[string][]byte) {
	rng := rand.New(rand.NewPCG(1, 2))
	want := map[string][]byte{}
	mod := time.Date(2025, 9, 14, 13, 37, 42, 0, time.UTC)
	file := func(dir, name string, size int) *Node {
		b := make([]byte, size)
		for i := range b {
			b[i] = byte(rng.Uint32())
		}
		want[strings.TrimPrefix(dir+"/"+name, "/")] = b
		return &Node{Name: name, Size: int64(size), ModTime: mod,
			Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }}
	}
	dir := func(name string, children ...*Node) *Node {
		return &Node{Name: name, Dir: true, ModTime: mod, Children: children}
	}
	var many []*Node
	for i := range 300 {
		many = append(many, file("boot/fonts", fmt.Sprintf("font file %03d.ttf", i), 100+i))
	}
	root := dir("",
		file("", "README.TXT", 50),
		file("", "setup.exe", 70_000),
		file("", "autorun.inf", 128),
		file("", "empty.txt", 0),
		file("", "exact.bin", clusterSize),
		file("", "A long file name with spaces.txt", 3),
		file("", "longname1.txt", 1),
		file("", "longname2.txt", 2),
		file("", "Ünïcödé ✓ naïve.txt", 9),
		file("", "thirteen_char", 13), // long name that exactly fills one entry
		dir("boot",
			file("boot", "bcd", 16_384),
			dir("fonts", many...),
		),
		dir("Program Files", dir("Deep", dir("Deeper", file("Program Files/Deep/Deeper", "x.dat", 3<<20)))),
		dir("EMPTYDIR"),
	)
	want["EMPTYDIR/"] = nil
	return root, want
}

// writeVolume writes v's regions into a fresh file at path.
func writeVolume(t *testing.T, v *FAT32, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	end := int64(0)
	for _, r := range v.Regions {
		if r.Offset < end {
			t.Fatalf("region %s at %d overlaps the previous one ending at %d", r.Name, r.Offset, end)
		}
		if r.Offset%int64(v.SectorSize) != 0 {
			t.Fatalf("region %s at %d is not sector aligned", r.Name, r.Offset)
		}
		end = r.Offset + r.Length
		if end > size {
			t.Fatalf("region %s ends at %d, past the volume's %d bytes", r.Name, end, size)
		}
		switch {
		case r.Data != nil:
			if _, err := f.WriteAt(r.Data, r.Offset); err != nil {
				t.Fatal(err)
			}
		case r.Open != nil:
			rc, err := r.Open()
			if err != nil {
				t.Fatal(err)
			}
			n, err := io.Copy(io.NewOffsetWriter(f, r.Offset), rc)
			rc.Close()
			if err != nil || n != r.Length {
				t.Fatalf("region %s: wrote %d of %d bytes: %v", r.Name, n, r.Length, err)
			}
		}
	}
}

func TestFAT32Volume(t *testing.T) {
	if _, err := exec.LookPath("fsck.fat"); err != nil {
		t.Skip("fsck.fat not installed")
	}
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7z not installed")
	}
	for _, size := range []int64{40 << 20, 300 << 20} {
		t.Run(fmt.Sprint(size>>20, "MiB"), func(t *testing.T) {
			cs := ClusterSizeFor(size)
			root, want := testTree(cs)
			v, err := PlanFAT32(root, size, 512, "Test Label 2025")
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			img := filepath.Join(dir, "vol.img")
			writeVolume(t, v, img, size)

			out, err := exec.Command("fsck.fat", "-n", "-v", img).CombinedOutput()
			if err != nil {
				t.Fatalf("fsck.fat: %v\n%s", err, out)
			}
			for _, bad := range []string{"differ", "Free cluster summary wrong", "Dirty bit", "will be truncated", "Contains a free cluster"} {
				if strings.Contains(string(out), bad) {
					t.Errorf("fsck.fat reports %q:\n%s", bad, out)
				}
			}

			x := filepath.Join(dir, "x")
			if out, err := exec.Command("7z", "x", "-tfat", "-o"+x, img).CombinedOutput(); err != nil {
				t.Fatalf("7z: %v\n%s", err, out)
			}
			got := map[string][]byte{}
			filepath.WalkDir(x, func(p string, d fs.DirEntry, err error) error {
				if err != nil || p == x {
					return err
				}
				rel, _ := filepath.Rel(x, p)
				if d.IsDir() {
					if ents, _ := os.ReadDir(p); len(ents) == 0 {
						got[rel+"/"] = nil
					}
					return nil
				}
				b, err := os.ReadFile(p)
				got[rel] = b
				return err
			})
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			t.Logf("fsck.fat: %s; 7z extracted %d entries", lines[len(lines)-1], len(got))
			for name, b := range want {
				g, ok := got[name]
				switch {
				case !ok:
					t.Errorf("%s missing", name)
				case !bytes.Equal(g, b):
					t.Errorf("%s differs (%d bytes, want %d)", name, len(g), len(b))
				}
			}
			for name := range got {
				if _, ok := want[name]; !ok {
					t.Errorf("unexpected %s", name)
				}
			}
			if t.Failed() {
				names := slices.Sorted(func(yield func(string) bool) {
					for n := range got {
						if !yield(n) {
							return
						}
					}
				})
				t.Logf("extracted: %v", names[:min(len(names), 20)])
			}

			label, err := exec.Command("fatlabel", img).Output()
			if err == nil && strings.TrimSpace(string(label)) != "TEST LABEL " && strings.TrimSpace(string(label)) != "TEST LABEL" {
				t.Errorf("label %q", label)
			}
		})
	}
}

func TestMBR(t *testing.T) {
	b, err := MBR(Partition{Start: 2048, Count: 1_000_000, Type: 0x0C, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if b[510] != 0x55 || b[511] != 0xAA {
		t.Error("no boot signature")
	}
	if binary.LittleEndian.Uint32(b[440:]) == 0 {
		t.Error("zero disk signature")
	}
	e := b[446:]
	if e[0] != 0x80 || e[4] != 0x0C || binary.LittleEndian.Uint32(e[8:]) != 2048 || binary.LittleEndian.Uint32(e[12:]) != 1_000_000 {
		t.Errorf("partition entry % x", e[:16])
	}
	if !bytes.Equal(e[1:4], []byte{32, 33, 0}) { // LBA 2048 is C0 H32 S33
		t.Errorf("start CHS % x", e[1:4])
	}
	if _, err := MBR(Partition{Start: 2048, Count: 1 << 32, Type: 0x0C}); err == nil {
		t.Error("a partition past 2 TiB should be refused")
	}
}
