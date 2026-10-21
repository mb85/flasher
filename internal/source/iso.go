package source

import (
	"errors"
	"io/fs"
	"path"
	"strings"

	"github.com/carbon-os/diskiso"
)

// ISO is an ISO image opened for reading files, used to rebuild Windows
// installer media and to copy Linux ISOs file by file.
type ISO struct {
	disc *diskiso.Disc
	Vol  diskiso.Volume
}

// OpenISO opens the ISO at p, preferring its UDF layer and then Rock
// Ridge, which keeps Linux file names whole where Joliet cuts them at 64
// characters.
func OpenISO(p string) (*ISO, error) {
	disc, err := diskiso.Attach(p)
	if err != nil {
		return nil, err
	}
	vol, err := disc.Mount()
	if err == nil && vol.Type() == diskiso.Joliet.String() {
		if rr, err := disc.Mount(diskiso.RockRidge); err == nil {
			vol = rr
		}
	}
	if err != nil {
		disc.Detach()
		return nil, err
	}
	return &ISO{disc: disc, Vol: vol}, nil
}

func (iso *ISO) Close() error { return iso.disc.Detach() }

// Find resolves a slash-separated path case-insensitively and returns the
// path as stored on the disc.
func (iso *ISO) Find(p string) (string, bool) {
	cur := "/"
	for part := range strings.SplitSeq(strings.Trim(p, "/"), "/") {
		ents, err := iso.Vol.ReadDir(cur)
		if err != nil {
			return "", false
		}
		found := ""
		for _, e := range ents {
			if strings.EqualFold(e.Name(), part) {
				found = e.Name()
				break
			}
		}
		if found == "" {
			return "", false
		}
		cur = path.Join(cur, found)
	}
	return cur, true
}

// InstallImage returns the on-disc path of sources/install.wim,
// install.esd or install.swm (media that ships already split).
func (iso *ISO) InstallImage() (string, bool) {
	for _, name := range []string{"install.wim", "install.esd", "install.swm"} {
		if p, ok := iso.Find("sources/" + name); ok {
			return p, true
		}
	}
	return "", false
}

// efiBootNames are the removable-media boot loaders UEFI looks for, one
// per architecture, so ARM64 Windows media is recognised too.
var efiBootNames = []string{"bootx64.efi", "bootaa64.efi", "bootia32.efi", "bootarm.efi"}

// IsWindowsInstaller reports whether the disc looks like Windows Setup
// media that can boot from FAT32 on UEFI.
func (iso *ISO) IsWindowsInstaller() bool {
	if _, ok := iso.InstallImage(); !ok {
		return false
	}
	for _, name := range efiBootNames {
		if _, ok := iso.Find("efi/boot/" + name); ok {
			return true
		}
	}
	return false
}

// hasEFILoader reports whether the disc has a removable-media UEFI boot
// loader in its file tree, rather than only inside an El Torito image.
func (iso *ISO) hasEFILoader() bool {
	for _, name := range efiBootNames {
		if _, ok := iso.Find("efi/boot/" + name); ok {
			return true
		}
	}
	return false
}

// inspectISO looks inside an ISO for Windows Setup media, or for a Linux
// ISO that can be copied file by file.
func inspectISO(img *Image) error {
	iso, err := OpenISO(img.Path)
	if err != nil {
		// An ISO that diskiso cannot read is treated as an ordinary ISO.
		if _, ok := errors.AsType[*fs.PathError](err); ok {
			return err
		}
		return nil
	}
	defer iso.Close()
	img.Label = iso.Vol.Label()
	if iso.IsWindowsInstaller() {
		img.Kind = KindWindowsISO
		return nil
	}
	img.Linux = iso.linuxISO()
	return nil
}

// Persistence is how a live system finds a partition to keep changes on.
type Persistence int

const (
	NoPersistence Persistence = iota
	// Casper is Ubuntu's live boot: a partition labelled casper-rw and
	// the "persistent" boot option.
	Casper
	// LiveBoot is Debian's: a partition labelled persistence holding a
	// persistence.conf, and the "persistence" boot option.
	LiveBoot
)

// LinuxISO describes a Linux ISO whose files can be copied to a FAT32
// partition, where it boots on UEFI and can keep changes in a second
// partition.
type LinuxISO struct {
	Files       int   // files to copy
	Bytes       int64 // their total size
	Persistence Persistence
}

// linuxISO checks that every file can go on FAT32 as it is: no file of
// 4 GiB or more, no two names that differ only in case, and no names FAT
// cannot hold. Symbolic links are left out, as Rufus does; on these discs
// they are conveniences such as dists/stable pointing at a release.
func (iso *ISO) linuxISO() *LinuxISO {
	if !iso.hasEFILoader() {
		return nil
	}
	if _, ok := iso.Find(".rr_moved"); ok {
		return nil // deep directories that diskiso does not put back
	}
	l := &LinuxISO{}
	var walk func(dir string) bool
	walk = func(dir string) bool {
		ents, err := iso.Vol.ReadDir(dir)
		if err != nil {
			return false
		}
		seen := map[string]bool{}
		for _, e := range ents {
			if e.Type()&fs.ModeSymlink != 0 {
				continue
			}
			name := e.Name()
			if seen[strings.ToLower(name)] || !fatName(name) {
				return false
			}
			seen[strings.ToLower(name)] = true
			p := path.Join(dir, name)
			if e.IsDir() {
				if !walk(p) {
					return false
				}
				continue
			}
			info, err := e.Info()
			if err != nil || info.Size() >= 1<<32 {
				return false
			}
			l.Files++
			l.Bytes += info.Size()
		}
		return true
	}
	if !walk("/") {
		return nil
	}
	switch {
	case iso.globExists("casper", ".squashfs"):
		l.Persistence = Casper
	case iso.globExists("live", ".squashfs"):
		l.Persistence = LiveBoot
	}
	return l
}

// globExists reports whether dir holds a file ending in suffix.
func (iso *ISO) globExists(dir, suffix string) bool {
	p, ok := iso.Find(dir)
	if !ok {
		return false
	}
	ents, _ := iso.Vol.ReadDir(p)
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), suffix) {
			return true
		}
	}
	return false
}

// fatName reports whether a long FAT name can hold name unchanged.
func fatName(name string) bool {
	return name != "" && len(name) <= 255 && !strings.ContainsAny(name, `"*/:<>?\|`) &&
		!strings.HasSuffix(name, ".") && !strings.HasSuffix(name, " ") &&
		!strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 })
}
