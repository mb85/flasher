package plan

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"flasher/internal/media"
	"flasher/internal/source"
	"flasher/internal/units"
)

// MinPersistence is the smallest persistence partition worth making.
const MinPersistence = 1 << 30

// Linux copies a Linux ISO's files to a FAT32 partition, which boots on
// UEFI and stays an ordinary drive other systems can read. With persistence
// bytes set, a second partition of about that size follows it, formatted
// ext4 and labelled so the live system keeps its changes there, and the
// boot menus gain the option that turns persistence on.
//
// Boot menus that find the system by the ISO's volume label are pointed at
// the FAT32 label instead, which is shorter, as Rufus does.
func Linux(img *source.Image, driveSize int64, sectorSize int, persistence int64) (_ *Plan, err error) {
	if img.Linux == nil {
		return nil, errors.New("this ISO cannot be copied file by file")
	}
	if persistence > 0 && img.Linux.Persistence == source.NoPersistence {
		return nil, errors.New("this ISO does not support persistence")
	}
	if sectorSize <= 0 {
		sectorSize = 512
	}
	ss := int64(sectorSize)
	if driveSize%ss != 0 || driveSize < 64*mib {
		return nil, fmt.Errorf("a %d-byte drive is too small", driveSize)
	}

	iso, err := source.OpenISO(img.Path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", img.Path, err)
	}
	p := &Plan{DriveSize: driveSize, SectorSize: sectorSize, closers: []io.Closer{iso}}
	defer func() {
		if err != nil {
			p.Close()
		}
	}()

	root, err := isoTree(iso, "/")
	if err != nil {
		return nil, err
	}
	isoLabel := iso.Vol.Label()
	patch := configPatch{isoLabel: isoLabel, fatLabel: media.FATLabel(isoLabel), redHat: redHatRepo(isoLabel, img.Path)}
	if persistence > 0 {
		patch.persistence = img.Linux.Persistence
	}
	patched := map[string][]byte{}
	if err := patchConfigs(root, "", patch, patched); err != nil {
		return nil, err
	}
	if err := updateChecksums(root, patched); err != nil {
		return nil, err
	}

	tail := driveSize/mib*mib - mib // last whole MiB, cleared
	partStart := int64(mib)
	// MBR sector numbers are 32-bit, so the partitions end by 2 TiB with
	// 512-byte sectors; anything past that is left unused.
	end := min(tail, ((1<<32)-partStart/ss)*ss/mib*mib)
	fatBytes := end - partStart
	if persistence > 0 {
		persistence = (persistence + mib - 1) / mib * mib
		fatBytes -= persistence
		if fatBytes < 64*mib {
			return nil, errors.New("the drive is too small for this much persistence")
		}
	}

	vol, err := media.PlanFAT32(root, fatBytes, sectorSize, isoLabel)
	if err != nil {
		if nr, ok := errors.AsType[*media.NoRoomError](err); ok {
			if persistence > 0 {
				return nil, fmt.Errorf("the ISO's files need %s and the drive has %s left beside the persistence partition",
					units.Bytes(nr.Need), units.Bytes(nr.Have))
			}
			return nil, fmt.Errorf("the drive is too small for this ISO: its files need %s and the drive holds %s",
				units.Bytes(nr.Need), units.Bytes(nr.Have))
		}
		if errors.Is(err, media.ErrNoRoom) {
			return nil, errors.New("the drive is too small for this ISO")
		}
		return nil, err
	}
	parts := []media.Partition{{Start: partStart / ss, Count: fatBytes / ss, Type: 0x0C, Active: true}}

	var ext *media.Ext4
	extStart := partStart + fatBytes
	if persistence > 0 {
		label, files := "casper-rw", map[string][]byte(nil)
		if img.Linux.Persistence == source.LiveBoot {
			label, files = "persistence", map[string][]byte{"persistence.conf": []byte("/ union\n")}
		}
		if ext, err = media.PlanExt4(persistence, label, files); err != nil {
			return nil, err
		}
		parts = append(parts, media.Partition{Start: extStart / ss, Count: persistence / ss, Type: 0x83})
	}

	mbr, err := media.MBR(parts...)
	if err != nil {
		return nil, err
	}
	head := make([]byte, partStart)
	copy(head, mbr)
	p.Segments = append(p.Segments, Bytes("partition table", 0, head))
	p.addRegions(partStart, vol.Regions)
	if ext != nil {
		p.addRegions(extStart, ext.Regions)
	}
	p.finish(tail, driveSize)
	return p, nil
}

type configPatch struct {
	isoLabel, fatLabel string
	persistence        source.Persistence
	redHat             bool // use inst.repo rather than inst.stage2
}

// patchConfigs rewrites the boot menus under dir: GRUB and syslinux .cfg
// files anywhere, and systemd-boot entries in loader/entries. It records
// the new contents of each file it changes in patched, by path.
func patchConfigs(dir *media.Node, at string, c configPatch, patched map[string][]byte) error {
	for _, n := range dir.Children {
		p := path.Join(at, n.Name)
		if n.Dir {
			if err := patchConfigs(n, p, c, patched); err != nil {
				return err
			}
			continue
		}
		lower := strings.ToLower(p)
		isCfg := strings.HasSuffix(lower, ".cfg")
		isEntry := strings.HasPrefix(lower, "loader/entries/") && strings.HasSuffix(lower, ".conf")
		if !isCfg && !isEntry || n.Size > 1<<20 {
			continue
		}
		b, err := readNode(n, p)
		if err != nil {
			return err
		}
		if out := c.apply(string(b)); out != string(b) {
			setContents(n, []byte(out))
			patched[p] = []byte(out)
		}
	}
	return nil
}

func readNode(n *media.Node, p string) ([]byte, error) {
	rc, err := n.Open()
	if err != nil {
		return nil, fmt.Errorf("read %s on the ISO: %w", p, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read %s on the ISO: %w", p, err)
	}
	return b, nil
}

func setContents(n *media.Node, b []byte) {
	n.Size = int64(len(b))
	n.Open = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
}

// checksumFiles are the lists live systems check their media against,
// such as Ubuntu's boot-time integrity check.
var checksumFiles = map[string]func([]byte) string{
	"md5sum.txt":    func(b []byte) string { return fmt.Sprintf("%x", md5.Sum(b)) },
	"MD5SUMS":       func(b []byte) string { return fmt.Sprintf("%x", md5.Sum(b)) },
	"sha256sum.txt": func(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) },
	"SHA256SUMS":    func(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) },
}

// updateChecksums corrects the entries for patched files in the checksum
// lists at the root, as Rufus does, so the media still passes its check.
func updateChecksums(root *media.Node, patched map[string][]byte) error {
	if len(patched) == 0 {
		return nil
	}
	for _, n := range root.Children {
		sum, ok := checksumFiles[n.Name]
		if !ok || n.Dir || n.Size > 16<<20 {
			continue
		}
		b, err := readNode(n, n.Name)
		if err != nil {
			return err
		}
		lines := strings.SplitAfter(string(b), "\n")
		changed := false
		for i, line := range lines {
			hash, file, ok := strings.Cut(line, " ")
			if !ok {
				continue
			}
			name := strings.TrimSpace(file)
			name = strings.TrimPrefix(strings.TrimPrefix(name, "*"), "./")
			if data, ok := patched[strings.TrimPrefix(name, "/")]; ok && len(hash) == len(sum(nil)) {
				lines[i] = sum(data) + " " + file
				changed = true
			}
		}
		if changed {
			setContents(n, []byte(strings.Join(lines, "")))
		}
	}
	return nil
}

// apply patches one config file. Only lines that load a kernel, pass it
// options or search for the boot volume are touched.
func (c configPatch) apply(cfg string) string {
	lines := strings.SplitAfter(cfg, "\n")
	for i, line := range lines {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch strings.ToLower(f[0]) {
		case "linux", "linuxefi", "$linux", "options", "append", "kernel", "search", "for":
		default:
			continue
		}
		if c.isoLabel != c.fatLabel && c.isoLabel != "" {
			esc := strings.ReplaceAll(c.isoLabel, " ", `\x20`)
			line = strings.ReplaceAll(line, esc, strings.ReplaceAll(c.fatLabel, " ", `\x20`))
			line = strings.ReplaceAll(line, c.isoLabel, c.fatLabel)
		}
		if c.redHat {
			line = strings.ReplaceAll(line, "inst.stage2=", "inst.repo=")
		}
		switch c.persistence {
		case source.Casper:
			// After the kernel's path on lines that load a casper kernel.
			if isKernelLine(f) && strings.Contains(f[1], "casper/") && !hasWord(f, "persistent") {
				line = insertAfter(line, f[1], " persistent")
				// Otherwise the installer's welcome window opens on
				// every boot, as Rufus also avoids.
				line = strings.Replace(line, " maybe-ubiquity", "", 1)
			}
		case source.LiveBoot:
			if !hasWord(f, "persistence") {
				line = insertAfter(line, "boot=live", " persistence")
			}
		}
		lines[i] = line
	}
	return strings.Join(lines, "")
}

func isKernelLine(f []string) bool {
	switch strings.ToLower(f[0]) {
	case "linux", "linuxefi", "$linux", "kernel":
		return len(f) > 1
	}
	return false
}

func hasWord(f []string, w string) bool {
	for _, s := range f {
		if s == w {
			return true
		}
	}
	return false
}

// insertAfter inserts s after the first whole-word occurrence of word.
func insertAfter(line, word, s string) string {
	for i := 0; ; {
		j := strings.Index(line[i:], word)
		if j < 0 {
			return line
		}
		end := i + j + len(word)
		if end == len(line) || strings.ContainsRune(" \t\r\n", rune(line[end])) {
			return line[:end] + s + line[end:]
		}
		i = end
	}
}

// redHatISOs are the Red Hat derivatives whose installers, since 8.2, only
// look for packages on the boot volume when told with inst.repo (from
// Rufus's list).
var redHatISOs = regexp.MustCompile(`^(AlmaLinux-[89]|Fedora.*-3[3-9]|CentOS.*-[89]|Circle.*-[89]|OL-[89]|RHEL-[89]|Rocky-[89]|MIRACLE-LINUX-[89])`)

// redHatRepo reports whether to swap inst.stage2 for inst.repo. Network
// install media has no packages on it, and Rufus can only tell it apart
// by its file name.
func redHatRepo(label, file string) bool {
	return redHatISOs.MatchString(label) && !strings.Contains(strings.ToLower(filepath.Base(file)), "netinst")
}
