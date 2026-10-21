//go:build !linux && !windows

package device

import (
	"errors"

	"flasher/internal/drives"
)

// Open is replaced per platform.
func Open(d drives.Drive) (Device, error) {
	return nil, errors.New("opening drives is not implemented on this platform yet")
}
