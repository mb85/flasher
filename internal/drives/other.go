//go:build !linux && !windows

package drives

import "errors"

func list() ([]Drive, error) {
	return nil, errors.New("listing drives is only supported on Linux and Windows")
}
