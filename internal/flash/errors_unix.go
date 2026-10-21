//go:build !windows

package flash

import (
	"errors"
	"syscall"
)

func writeProtected(err error) bool {
	return errors.Is(err, syscall.EROFS) || errors.Is(err, syscall.EPERM)
}

func driveGone(err error) bool {
	return errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.ENXIO) || errors.Is(err, syscall.ENOMEDIUM)
}

func ioError(err error) bool { return errors.Is(err, syscall.EIO) }
