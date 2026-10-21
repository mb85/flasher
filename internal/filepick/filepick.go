// Package filepick shows the platform's native "open file" dialog.
package filepick

import "errors"

var (
	// ErrCancelled means the user closed the dialog without choosing.
	ErrCancelled = errors.New("no file chosen")
	// ErrUnavailable means no native dialog could be shown, so the caller
	// should fall back to its own file browser.
	ErrUnavailable = errors.New("native file dialog unavailable")
)

// Patterns are the file name globs offered by the "Disk images" filter.
var Patterns = []string{
	"*.img", "*.iso", "*.raw", "*.bin",
	"*.img.xz", "*.iso.xz", "*.xz", "*.gz", "*.zst", "*.bz2", "*.zip",
}

// Parent identifies the window a dialog belongs to, so the dialog opens
// over it and blocks it like any other modal dialog. The zero value opens
// an unparented dialog.
type Parent struct {
	// Portal is the window identifier xdg-desktop-portal takes:
	// "x11:<hex XID>" or "wayland:<exported handle>".
	Portal string
	// HWND is the owner window on Windows.
	HWND uintptr
}

// Open blocks until the user picks a file or closes the dialog. Call it
// from a goroutine, never from the UI frame function.
func Open(title string, parent Parent) (string, error) { return open(title, parent) }
