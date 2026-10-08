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

// Open blocks until the user picks a file or closes the dialog. Call it
// from a goroutine, never from the UI frame function.
func Open(title string) (string, error) { return open(title) }
