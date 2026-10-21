//go:build !linux && !windows

package filepick

func open(title string, parent Parent) (string, error) { return "", ErrUnavailable }
