package ui

import (
	"fmt"

	. "go.hasen.dev/shirei"
	"go.hasen.dev/shirei/x11backend"

	"flasher/internal/filepick"
)

// dialogParent names the app window for the file dialog. On Wayland the
// window would have to be exported with xdg-foreign, which Shirei does not
// offer yet, so the dialog opens unparented there.
func dialogParent() filepick.Parent {
	if c, ok := GetHost().EscapeHatchBackendContext.(x11backend.Context); ok && c.Connected() {
		return filepick.Parent{Portal: fmt.Sprintf("x11:%x", uint32(c.Window()))}
	}
	return filepick.Parent{}
}
