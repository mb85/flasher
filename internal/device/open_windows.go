package device

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"

	"flasher/internal/drives"
)

const (
	fsctlLockVolume            = 0x00090018
	fsctlDismountVolume        = 0x00090020
	fsctlUnlockVolume          = 0x0009001C
	ioctlDiskGetDriveGeomEx    = 0x000700A0
	ioctlDiskDeleteDriveLayout = 0x0007C100
	ioctlDiskUpdateProperties  = 0x00070140
)

// headSize is the region at the start of the disk that is written last.
// Once a valid partition table and boot sector land there, Windows may
// mount the new volumes and then refuse raw writes inside them, so they
// are held back until everything else is on the disk.
const headSize = 4 << 20

// Open locks and dismounts every volume on the drive, clears its partition
// table and opens the disk for unbuffered, write-through access. The
// process must be elevated.
func Open(d drives.Drive) (Device, error) {
	n, err := strconv.Atoi(d.ID)
	if err != nil {
		return nil, fmt.Errorf("bad disk number %q", d.ID)
	}
	vols, err := drives.Volumes()
	if err != nil {
		return nil, err
	}
	dev := &winDevice{}
	for _, v := range vols {
		if !v.OnDisk(n) {
			continue
		}
		h, err := lockVolume(v)
		if err != nil {
			dev.releaseVolumes()
			return nil, err
		}
		dev.volumes = append(dev.volumes, h)
	}

	p, _ := windows.UTF16PtrFromString(drives.DiskPath(n))
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_NO_BUFFERING|windows.FILE_FLAG_WRITE_THROUGH, 0)
	if err != nil {
		dev.releaseVolumes()
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, errors.New("access denied: run Flasher as administrator")
		}
		return nil, fmt.Errorf("open %s: %w", d.Path, err)
	}
	dev.h = h
	dev.f = os.NewFile(uintptr(h), d.Path)

	geo := make([]byte, 256)
	var got uint32
	if err := windows.DeviceIoControl(h, ioctlDiskGetDriveGeomEx, nil, 0, &geo[0], uint32(len(geo)), &got, nil); err != nil {
		dev.Close()
		return nil, fmt.Errorf("query %s: %w", d.Path, err)
	}
	dev.sector = int(binary.LittleEndian.Uint32(geo[20:]))
	dev.size = int64(binary.LittleEndian.Uint64(geo[24:]))
	if dev.sector == 0 {
		dev.sector = 512
	}

	// Forget the old partitions (MBR and both GPT headers) so Windows does
	// not try to remount them while the new image goes on.
	if err := windows.DeviceIoControl(h, ioctlDiskDeleteDriveLayout, nil, 0, nil, 0, &got, nil); err != nil {
		dev.Close()
		return nil, fmt.Errorf("clear partition table on %s: %w", d.Path, err)
	}
	dev.head = AlignedBuffer(headSize)
	return dev, nil
}

func lockVolume(v drives.Volume) (windows.Handle, error) {
	name := strings.TrimSuffix(v.Name, `\`)
	p, _ := windows.UTF16PtrFromString(name)
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("open volume %s: %w", volumeLabel(v), err)
	}
	var got uint32
	if err := windows.DeviceIoControl(h, fsctlLockVolume, nil, 0, nil, 0, &got, nil); err != nil {
		windows.CloseHandle(h)
		return 0, fmt.Errorf("volume %s is in use; close any windows or programs using it: %w", volumeLabel(v), err)
	}
	if err := windows.DeviceIoControl(h, fsctlDismountVolume, nil, 0, nil, 0, &got, nil); err != nil {
		windows.CloseHandle(h)
		return 0, fmt.Errorf("dismount %s: %w", volumeLabel(v), err)
	}
	return h, nil
}

func volumeLabel(v drives.Volume) string {
	if s := v.Describe(); s != "" {
		return s
	}
	if len(v.Paths) > 0 {
		return v.Paths[0]
	}
	return v.Name
}

type winDevice struct {
	h       windows.Handle
	f       *os.File // wraps h for positioned reads and writes
	volumes []windows.Handle
	size    int64
	sector  int

	head    []byte // held-back copy of the first headSize bytes
	headLen int    // how much of head has been written
	flushed bool
}

func (d *winDevice) WriteAt(p []byte, off int64) (int, error) {
	n := 0
	if off < headSize && !d.flushed {
		k := copy(d.head[off:], p)
		d.headLen = max(d.headLen, int(off)+k)
		n, off, p = k, off+int64(k), p[k:]
	}
	if len(p) == 0 {
		return n, nil
	}
	m, err := d.f.WriteAt(p, off)
	return n + m, err
}

func (d *winDevice) ReadAt(p []byte, off int64) (int, error) {
	return d.f.ReadAt(p, off)
}

func (d *winDevice) Size() int64     { return d.size }
func (d *winDevice) SectorSize() int { return d.sector }

// Sync writes the held-back head, then flushes the drive's cache.
func (d *winDevice) Sync() error {
	if !d.flushed && d.headLen > 0 {
		n := (d.headLen + d.sector - 1) / d.sector * d.sector
		if _, err := d.f.WriteAt(d.head[:n], 0); err != nil {
			return fmt.Errorf("write partition table: %w", err)
		}
	}
	d.flushed = true
	return windows.FlushFileBuffers(d.h)
}

func (d *winDevice) Close() error {
	var err error
	if d.h != 0 {
		var got uint32
		windows.DeviceIoControl(d.h, ioctlDiskUpdateProperties, nil, 0, nil, 0, &got, nil)
		if d.f != nil {
			err = d.f.Close() // also closes d.h
		} else {
			err = windows.CloseHandle(d.h)
		}
		d.h = 0
	}
	d.releaseVolumes()
	return err
}

func (d *winDevice) releaseVolumes() {
	for _, h := range d.volumes {
		var got uint32
		windows.DeviceIoControl(h, fsctlUnlockVolume, nil, 0, nil, 0, &got, nil)
		windows.CloseHandle(h)
	}
	d.volumes = nil
}
