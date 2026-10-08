package plan

import (
	"errors"

	"flasher/internal/source"
)

// For picks how to write img to a drive of devSize bytes.
func For(img *source.Image, devSize int64) (*Plan, error) {
	if img.Kind == source.KindWindowsISO {
		return nil, errors.New("Windows installer media is not implemented yet")
	}
	return Raw(img), nil
}
