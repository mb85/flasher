package flash

import (
	"errors"

	"golang.org/x/sys/windows"
)

func writeProtected(err error) bool { return errors.Is(err, windows.ERROR_WRITE_PROTECT) }

func driveGone(err error) bool {
	for _, e := range []error{windows.ERROR_DEVICE_NOT_CONNECTED, windows.ERROR_NO_SUCH_DEVICE, windows.ERROR_NOT_READY,
		windows.ERROR_DEVICE_REMOVED, windows.ERROR_FILE_NOT_FOUND, windows.ERROR_INVALID_HANDLE} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func ioError(err error) bool {
	return errors.Is(err, windows.ERROR_CRC) || errors.Is(err, windows.ERROR_GEN_FAILURE) || errors.Is(err, windows.ERROR_IO_DEVICE)
}
