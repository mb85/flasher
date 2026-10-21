//go:build !linux && !windows

package ui

import "flasher/internal/filepick"

func dialogParent() filepick.Parent { return filepick.Parent{} }
