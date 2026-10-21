package filepick

import (
	"fmt"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	comdlg32             = windows.NewLazySystemDLL("comdlg32.dll")
	procGetOpenFileName  = comdlg32.NewProc("GetOpenFileNameW")
	procCommDlgExtendErr = comdlg32.NewProc("CommDlgExtendedError")
)

// openFileName mirrors OPENFILENAMEW on 64-bit Windows.
type openFileName struct {
	structSize    uint32
	owner         uintptr
	instance      uintptr
	filter        *uint16
	customFilter  *uint16
	maxCustFilter uint32
	filterIndex   uint32
	file          *uint16
	maxFile       uint32
	fileTitle     *uint16
	maxFileTitle  uint32
	initialDir    *uint16
	title         *uint16
	flags         uint32
	fileOffset    uint16
	fileExtension uint16
	defExt        *uint16
	custData      uintptr
	hook          uintptr
	templateName  *uint16
	reserved      uintptr
	reserved2     uint32
	flagsEx       uint32
}

const (
	ofnNoChangeDir     = 0x00000008
	ofnPathMustExist   = 0x00000800
	ofnFileMustExist   = 0x00001000
	ofnExplorer        = 0x00080000
	ofnDontAddToRecent = 0x02000000
)

// open shows the standard Windows "Open" dialog, owned by parent.HWND so
// it stays over the app window and blocks it while open.
func open(title string, parent Parent) (string, error) {
	// The dialog runs its own message loop and expects a single-threaded
	// COM apartment on the calling thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err == nil {
		defer windows.CoUninitialize()
	}

	globs := strings.Join(Patterns, ";")
	filter := utf16Multi("Disk images ("+globs+")", globs, "All files (*.*)", "*.*")
	file := make([]uint16, 32768)
	titlePtr, _ := windows.UTF16PtrFromString(title)
	ofn := openFileName{
		owner:       parent.HWND,
		filter:      &filter[0],
		filterIndex: 1,
		file:        &file[0],
		maxFile:     uint32(len(file)),
		title:       titlePtr,
		flags:       ofnExplorer | ofnFileMustExist | ofnPathMustExist | ofnNoChangeDir | ofnDontAddToRecent,
	}
	ofn.structSize = uint32(unsafe.Sizeof(ofn))

	if r, _, _ := procGetOpenFileName.Call(uintptr(unsafe.Pointer(&ofn))); r != 0 {
		return windows.UTF16ToString(file), nil
	}
	if code, _, _ := procCommDlgExtendErr.Call(); code != 0 {
		return "", fmt.Errorf("%w: dialog error 0x%x", ErrUnavailable, code)
	}
	return "", ErrCancelled
}

// utf16Multi builds a NUL-separated, double-NUL-terminated UTF-16 list.
func utf16Multi(parts ...string) []uint16 {
	var out []uint16
	for _, p := range parts {
		out = append(out, windows.StringToUTF16(p)...) // includes its NUL
	}
	return append(out, 0)
}
