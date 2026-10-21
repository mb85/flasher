package ui

import (
	"fmt"

	. "go.hasen.dev/shirei"
	"go.hasen.dev/shirei/waylandbackend"
	"go.hasen.dev/shirei/x11backend"

	"flasher/internal/filepick"
)

// dialogParent names the app window for the file dialog, so the portal
// opens it attached to the window. On Wayland the window is exported with
// xdg-foreign; an X11 parent only takes effect in an X11 session.
func dialogParent() filepick.Parent {
	switch c := GetHost().EscapeHatchBackendContext.(type) {
	case waylandbackend.Context:
		if h := c.ExportedHandle(); h != "" {
			return filepick.Parent{Portal: "wayland:" + h}
		}
	case x11backend.Context:
		if c.Connected() {
			return filepick.Parent{Portal: fmt.Sprintf("x11:%x", uint32(c.Window()))}
		}
	}
	return filepick.Parent{}
}
