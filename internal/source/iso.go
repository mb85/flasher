package source

import (
	"errors"
	"io/fs"
	"path"
	"strings"

	"github.com/carbon-os/diskiso"
)

// ISO is an ISO image opened for reading files, used to rebuild Windows
// installer media.
type ISO struct {
	disc *diskiso.Disc
	Vol  diskiso.Volume
}

// OpenISO opens the ISO at p, preferring its UDF layer.
func OpenISO(p string) (*ISO, error) {
	disc, err := diskiso.Attach(p)
	if err != nil {
		return nil, err
	}
	vol, err := disc.Mount()
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

func windowsISO(p string) (label string, ok bool, err error) {
	iso, err := OpenISO(p)
	if err != nil {
		// An ISO that diskiso cannot read is treated as an ordinary ISO.
		if _, ok := errors.AsType[*fs.PathError](err); ok {
			return "", false, err
		}
		return "", false, nil
	}
	defer iso.Close()
	if !iso.IsWindowsInstaller() {
		return "", false, nil
	}
	return iso.Vol.Label(), true, nil
}
