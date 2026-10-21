package plan

import (
	"encoding/binary"
	"io"
	"os"
	"strings"
	"testing"

	"flasher/internal/source"
)

func TestConfigPatch(t *testing.T) {
	for _, c := range []struct {
		name string
		p    configPatch
		in   string
		want string
	}{
		{"ubuntu", configPatch{persistence: source.Casper},
			"menuentry \"Try\" {\n    linux  /casper/vmlinuz  --- quiet splash\n    initrd /casper/initrd\n}\n",
			"menuentry \"Try\" {\n    linux  /casper/vmlinuz persistent  --- quiet splash\n    initrd /casper/initrd\n}\n"},
		{"zorin", configPatch{persistence: source.Casper},
			"\tlinux\t/casper/vmlinuz maybe-ubiquity quiet splash ---\n",
			"\tlinux\t/casper/vmlinuz persistent quiet splash ---\n"},
		{"already persistent", configPatch{persistence: source.Casper},
			"linux /casper/vmlinuz persistent quiet\n", "linux /casper/vmlinuz persistent quiet\n"},
		{"memtest untouched", configPatch{persistence: source.Casper},
			"linux /boot/memtest86+x64.bin\n", "linux /boot/memtest86+x64.bin\n"},
		{"debian", configPatch{persistence: source.LiveBoot},
			"linux /live/vmlinuz boot=live components quiet\n", "linux /live/vmlinuz boot=live persistence components quiet\n"},
		{"rocky label", configPatch{isoLabel: "Rocky-10-2-x86_64-dvd", fatLabel: "ROCKY-10-2-"},
			"search --no-floppy --set=root -l 'Rocky-10-2-x86_64-dvd'\n\tlinuxefi /images/pxeboot/vmlinuz inst.stage2=hd:LABEL=Rocky-10-2-x86_64-dvd quiet\n# Rocky-10-2-x86_64-dvd\n",
			"search --no-floppy --set=root -l 'ROCKY-10-2-'\n\tlinuxefi /images/pxeboot/vmlinuz inst.stage2=hd:LABEL=ROCKY-10-2- quiet\n# Rocky-10-2-x86_64-dvd\n"},
		{"spaced label", configPatch{isoLabel: "Fedora WS 41", fatLabel: "FEDORA WS 4"},
			"linux /vmlinuz root=live:CDLABEL=Fedora\\x20WS\\x2041 rd.live.image\n",
			"linux /vmlinuz root=live:CDLABEL=FEDORA\\x20WS\\x204 rd.live.image\n"},
		{"red hat repo", configPatch{isoLabel: "Rocky-9-4-x86_64-dvd", fatLabel: "ROCKY-9-4-X", redHat: true},
			"linuxefi /images/pxeboot/vmlinuz inst.stage2=hd:LABEL=Rocky-9-4-x86_64-dvd quiet\n",
			"linuxefi /images/pxeboot/vmlinuz inst.repo=hd:LABEL=ROCKY-9-4-X quiet\n"},
	} {
		if got := c.p.apply(c.in); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestRedHatRepo(t *testing.T) {
	for label, want := range map[string]bool{
		"Rocky-9-4-x86_64-dvd": true, "Rocky-10-2-x86_64-dvd": false, "AlmaLinux-9-4-x86_64-dvd": true,
		"Fedora-WS-Live-41": false, "Fedora-S-dvd-x86_64-38": true, "ARCH_202610": false,
	} {
		if got := redHatRepo(label, "/isos/x.iso"); got != want {
			t.Errorf("redHatRepo(%q) = %v", label, got)
		}
	}
	if redHatRepo("Rocky-9-4-x86_64-dvd", `C:\isos\Rocky-9.4-x86_64-netinst.iso`) {
		t.Error("network install media should keep inst.stage2")
	}
}

// TestLinuxLayout plans a real Linux ISO onto drives of several sizes, with
// and without persistence, and checks the partitions, for example:
//
//	FLASHER_TEST_LINUX_ISO=~/ubuntu.iso go test -run LinuxLayout ./internal/plan
func TestLinuxLayout(t *testing.T) {
	iso := os.Getenv("FLASHER_TEST_LINUX_ISO")
	if iso == "" {
		t.Skip("set FLASHER_TEST_LINUX_ISO to run")
	}
	img, err := source.Inspect(iso)
	if err != nil {
		t.Fatal(err)
	}
	if img.Linux == nil {
		t.Fatalf("%s cannot be copied file by file", iso)
	}
	for _, c := range []struct {
		size, persistence int64
		ss                int
	}{
		{16 << 30, 0, 512},
		{16<<30 + 12345*512, 4e9, 512},
		{250059350016, 100e9, 512},
		{3 << 40, 1e12, 512}, // past what MBR can address
		{64 << 30, 8e9, 4096},
	} {
		if c.persistence > 0 && img.Linux.Persistence == source.NoPersistence {
			continue
		}
		p, err := Linux(img, c.size, c.ss, c.persistence)
		if err != nil {
			t.Fatalf("%d bytes: %v", c.size, err)
		}
		var end, total int64
		for _, s := range p.Segments {
			if s.Offset < end || s.Offset%int64(c.ss) != 0 {
				t.Fatalf("%d bytes: %s at %d overlaps or is not sector aligned (previous end %d)", c.size, s.Name, s.Offset, end)
			}
			end = s.Offset + s.Length
			total += s.Length
		}
		if end != c.size || total != p.Total {
			t.Errorf("%d bytes: ends at %d, total %d of %d", c.size, end, total, p.Total)
		}
		rc, _ := p.Segments[0].Open()
		mbr := make([]byte, 512)
		io.ReadFull(rc, mbr)
		rc.Close()
		part := func(i int) (start, length int64, typ byte) {
			e := mbr[446+16*i:]
			return int64(binary.LittleEndian.Uint32(e[8:])) * int64(c.ss), int64(binary.LittleEndian.Uint32(e[12:])) * int64(c.ss), e[4]
		}
		s1, l1, t1 := part(0)
		s2, l2, t2 := part(1)
		if s1 != 1<<20 || t1 != 0x0C {
			t.Errorf("%d bytes: FAT32 partition at %d, type %#x", c.size, s1, t1)
		}
		switch {
		case c.persistence == 0 && t2 != 0:
			t.Errorf("%d bytes: a second partition without persistence", c.size)
		case c.persistence > 0 && (t2 != 0x83 || s2 != s1+l1 || l2 < c.persistence || s2+l2 > c.size || s2%(1<<20) != 0):
			t.Errorf("%d bytes: persistence partition at %d, %d bytes, type %#x", c.size, s2, l2, t2)
		}
		t.Logf("%d bytes, %d-byte sectors: %d segments, FAT32 %d bytes, persistence %d bytes", c.size, c.ss, len(p.Segments), l1, l2)
		p.Close()
	}
	if _, err := Linux(img, 1<<30, 512, 0); err == nil || !strings.Contains(err.Error(), "too small") {
		t.Errorf("a 1 GiB drive should be too small, got %v", err)
	}
}
