package ui

import (
	. "go.hasen.dev/shirei"
	"go.hasen.dev/shirei/win32backend"

	"flasher/internal/filepick"
)

// dialogParent names the app window as the file dialog's owner.
func dialogParent() filepick.Parent {
	if c, ok := GetHost().EscapeHatchBackendContext.(win32backend.Context); ok {
		return filepick.Parent{HWND: uintptr(c.HWND())}
	}
	return filepick.Parent{}
}
