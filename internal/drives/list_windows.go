package drives

import "errors"

func list() ([]Drive, error) {
	return nil, errors.New("listing drives is not implemented on Windows yet")
}
