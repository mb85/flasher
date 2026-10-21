package plan

import "flasher/internal/source"

// Options are the choices for images that can be written more than one way.
type Options struct {
	// CopyFiles copies a Linux ISO's files to FAT32 (see Linux) rather
	// than writing it byte for byte.
	CopyFiles bool
	// Persistence is the size of the partition for a live system's
	// changes when CopyFiles is set, or 0 for none.
	Persistence int64
}

// For picks how to write img to a drive of driveSize bytes with the given
// logical sector size. The caller closes the plan when done with it.
func For(img *source.Image, driveSize int64, sectorSize int, opts Options) (*Plan, error) {
	switch {
	case img.Kind == source.KindWindowsISO:
		return Windows(img, driveSize, sectorSize)
	case opts.CopyFiles && img.Linux != nil:
		return Linux(img, driveSize, sectorSize, opts.Persistence)
	}
	return Raw(img), nil
}
