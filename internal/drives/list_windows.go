package drives

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"golang.org/x/sys/windows"
)

// IOCTL codes from winioctl.h.
const (
	ioctlStorageQueryProperty       = 0x002D1400
	ioctlDiskGetDriveGeometryEx     = 0x000700A0
	ioctlVolumeGetVolumeDiskExtents = 0x00560000
)

// Bus types from STORAGE_BUS_TYPE.
var busNames = map[uint32]string{
	0x01: "scsi", 0x02: "atapi", 0x03: "ata", 0x04: "1394", 0x05: "ssa", 0x06: "fibre",
	0x07: "usb", 0x08: "raid", 0x09: "iscsi", 0x0A: "sas", 0x0B: "sata", 0x0C: "sd",
	0x0D: "mmc", 0x0E: "virtual", 0x0F: "vhd", 0x10: "spaces", 0x11: "nvme", 0x12: "scm", 0x13: "ufs",
}

// maxDisks bounds the PhysicalDriveN probe.
const maxDisks = 64

func list() ([]Drive, error) {
	vols, err := Volumes()
	if err != nil {
		return nil, err
	}
	sysDisks := systemDisks(vols)

	var out []Drive
	for n := range maxDisks {
		d, ok := probeDisk(n)
		if !ok {
			continue
		}
		d.IsSystem = sysDisks[n]
		for _, v := range vols {
			if v.OnDisk(n) {
				d.Mounts = append(d.Mounts, v.Paths...)
			}
		}
		sort.Strings(d.Mounts)
		out = append(out, d)
	}
	return out, nil
}

// DiskPath is the device path of disk number n.
func DiskPath(n int) string { return fmt.Sprintf(`\\.\PhysicalDrive%d`, n) }

func openForQuery(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	// No access rights are needed for the queries used here, which also
	// keeps this working without elevation.
	return windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
}

func ioctl(h windows.Handle, code uint32, in []byte, out []byte) (int, error) {
	var inPtr, outPtr *byte
	if len(in) > 0 {
		inPtr = &in[0]
	}
	if len(out) > 0 {
		outPtr = &out[0]
	}
	var n uint32
	err := windows.DeviceIoControl(h, code, inPtr, uint32(len(in)), outPtr, uint32(len(out)), &n, nil)
	return int(n), err
}

func probeDisk(n int) (Drive, bool) {
	path := DiskPath(n)
	h, err := openForQuery(path)
	if err != nil {
		return Drive{}, false
	}
	defer windows.CloseHandle(h)

	d := Drive{Path: path, ID: fmt.Sprint(n)}

	// STORAGE_PROPERTY_QUERY{StorageDeviceProperty, PropertyStandardQuery}
	query := make([]byte, 12)
	desc := make([]byte, 1024)
	if m, err := ioctl(h, ioctlStorageQueryProperty, query, desc); err == nil && m >= 36 {
		desc = desc[:m]
		d.Removable = desc[10] != 0
		bus := binary.LittleEndian.Uint32(desc[28:])
		d.Bus = busNames[bus]
		if d.Bus == "" {
			d.Bus = fmt.Sprintf("bus %d", bus)
		}
		d.Vendor = cString(desc, binary.LittleEndian.Uint32(desc[12:]))
		d.Model = cString(desc, binary.LittleEndian.Uint32(desc[16:]))
	}

	// DISK_GEOMETRY_EX: DISK_GEOMETRY (24 bytes) then DiskSize.
	geo := make([]byte, 256)
	if m, err := ioctl(h, ioctlDiskGetDriveGeometryEx, nil, geo); err == nil && m >= 32 {
		d.Size = int64(binary.LittleEndian.Uint64(geo[24:]))
	}
	return d, true
}

// cString reads a NUL-terminated ASCII string at off within b.
func cString(b []byte, off uint32) string {
	if off == 0 || int(off) >= len(b) {
		return ""
	}
	s := b[off:]
	if i := strings.IndexByte(string(s), 0); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(string(s))
}

// Volume is a mounted (or mountable) volume and the disks it spans.
type Volume struct {
	Name  string   // \\?\Volume{GUID}\
	Paths []string // drive letters and folder mount points
	Disks []int
}

// OnDisk reports whether the volume has an extent on disk n.
func (v Volume) OnDisk(n int) bool { return slices.Contains(v.Disks, n) }

// Volumes lists every volume and the disks under it.
func Volumes() ([]Volume, error) {
	buf := make([]uint16, windows.MAX_PATH)
	h, err := windows.FindFirstVolume(&buf[0], uint32(len(buf)))
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}
	defer windows.FindVolumeClose(h)
	var out []Volume
	for {
		name := windows.UTF16ToString(buf)
		v := Volume{Name: name, Paths: volumePaths(name), Disks: volumeDisks(name)}
		out = append(out, v)
		if err := windows.FindNextVolume(h, &buf[0], uint32(len(buf))); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return out, nil
			}
			return out, err
		}
	}
}

func volumePaths(name string) []string {
	p, _ := windows.UTF16PtrFromString(name)
	buf := make([]uint16, 1024)
	var n uint32
	if err := windows.GetVolumePathNamesForVolumeName(p, &buf[0], uint32(len(buf)), &n); err != nil {
		return nil
	}
	// The names are separated by NULs, so walk the buffer rather than
	// stopping at the first one.
	var out []string
	start := 0
	for i := 0; i < int(n); i++ {
		if buf[i] == 0 {
			if i > start {
				out = append(out, windows.UTF16ToString(buf[start:i]))
			}
			start = i + 1
		}
	}
	return out
}

func volumeDisks(name string) []int {
	h, err := openForQuery(strings.TrimSuffix(name, `\`))
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(h)
	// VOLUME_DISK_EXTENTS: count (4), padding (4), then 24-byte extents.
	out := make([]byte, 8+24*32)
	m, err := ioctl(h, ioctlVolumeGetVolumeDiskExtents, nil, out)
	if err != nil || m < 8 {
		return nil
	}
	count := int(binary.LittleEndian.Uint32(out))
	var disks []int
	for i := 0; i < count && 8+24*(i+1) <= m; i++ {
		disks = append(disks, int(binary.LittleEndian.Uint32(out[8+24*i:])))
	}
	return disks
}

// systemDisks marks the disks holding the Windows directory.
func systemDisks(vols []Volume) map[int]bool {
	out := map[int]bool{}
	dir, err := windows.GetSystemWindowsDirectory()
	if err != nil || len(dir) < 3 {
		return out
	}
	root, _ := windows.UTF16PtrFromString(dir[:3]) // "C:\"
	buf := make([]uint16, windows.MAX_PATH)
	if err := windows.GetVolumeNameForVolumeMountPoint(root, &buf[0], uint32(len(buf))); err != nil {
		return out
	}
	sysVol := windows.UTF16ToString(buf)
	for _, v := range vols {
		if strings.EqualFold(v.Name, sysVol) {
			for _, d := range v.Disks {
				out[d] = true
			}
		}
	}
	return out
}
