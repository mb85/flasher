// Package drives lists the drives an image could be written to, and marks
// the ones that must never be offered.
package drives

import (
	"errors"
	"fmt"
	"strings"
)

// Drive is a whole disk, not a partition.
type Drive struct {
	Path      string // /dev/sdb, or \\.\PhysicalDrive2 on Windows
	ID        string // platform handle: a UDisks object path, or a disk number
	Vendor    string
	Model     string
	Size      int64
	Bus       string // "usb", "sdio", "ata", "nvme", "loop", ...
	Removable bool
	// IsSystem is set when the drive holds the running system, so writing
	// to it would destroy the machine's own installation.
	IsSystem bool
	Mounts   []string // mount points or drive letters of its volumes
}

// Name is the vendor and model, or the path when neither is known.
func (d Drive) Name() string {
	n := strings.TrimSpace(strings.TrimSpace(d.Vendor) + " " + strings.TrimSpace(d.Model))
	if n == "" {
		return d.Path
	}
	return n
}

// Describe is a one-line summary such as "SanDisk Ultra (USB), mounted at /run/media/x".
func (d Drive) Describe() string {
	s := d.Name()
	if d.Bus != "" {
		s += " (" + strings.ToUpper(d.Bus) + ")"
	}
	if d.IsSystem {
		s += ", SYSTEM DRIVE"
	}
	if len(d.Mounts) > 0 {
		s += ", mounted at " + strings.Join(d.Mounts, ", ")
	}
	return s
}

// Options widens the default list.
type Options struct {
	// All includes fixed and system drives as well as removable ones.
	All bool
	// IncludeLoop lists loop devices, which are handy for testing.
	IncludeLoop bool
}

// eligible decides whether a drive appears in the default list.
func (d Drive) eligible(opts Options) bool {
	if d.Bus == "loop" {
		return opts.IncludeLoop
	}
	if opts.All {
		return true
	}
	if d.IsSystem || d.Size == 0 {
		return false
	}
	switch d.Bus {
	case "usb", "sdio", "mmc", "sd":
		return true
	}
	return d.Removable
}

// ErrNotFound means no drive has the requested path.
var ErrNotFound = errors.New("drive not found")

// List returns the drives that match opts.
func List(opts Options) ([]Drive, error) {
	all, err := list()
	if err != nil {
		return nil, err
	}
	var out []Drive
	for _, d := range all {
		if d.eligible(opts) {
			out = append(out, d)
		}
	}
	return out, nil
}

// Find looks a drive up by its path.
func Find(path string, opts Options) (Drive, error) {
	ds, err := List(opts)
	if err != nil {
		return Drive{}, err
	}
	for _, d := range ds {
		if strings.EqualFold(d.Path, path) {
			return d, nil
		}
	}
	return Drive{}, fmt.Errorf("%w: %s", ErrNotFound, path)
}
