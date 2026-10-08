//go:build !linux && !windows

package filepick

func open(title string) (string, error) { return "", ErrUnavailable }
