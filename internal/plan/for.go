package plan

import "flasher/internal/source"

// For picks how to write img to a drive of driveSize bytes with the given
// logical sector size. The caller closes the plan when done with it.
func For(img *source.Image, driveSize int64, sectorSize int) (*Plan, error) {
	if img.Kind == source.KindWindowsISO {
		return Windows(img, driveSize, sectorSize)
	}
	return Raw(img), nil
}
