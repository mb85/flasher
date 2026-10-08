package device

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unsafe"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"

	"flasher/internal/drives"
)

// Open unmounts every filesystem on the drive and opens it for exclusive,
// unbuffered access through UDisks2. UDisks asks polkit, which shows the
// desktop's password prompt, so the app itself never runs as root.
func Open(d drives.Drive) (Device, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	objs, err := drives.GetObjects(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	disk := dbus.ObjectPath(d.ID)
	if _, ok := objs[disk]; !ok {
		conn.Close()
		return nil, fmt.Errorf("%s has been removed", d.Path)
	}
	if err := unmountAll(conn, objs, disk); err != nil {
		conn.Close()
		return nil, err
	}

	// A polkit prompt can sit on screen for a while before the user answers.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var fd dbus.UnixFD
	err = conn.Object("org.freedesktop.UDisks2", disk).CallWithContext(ctx,
		"org.freedesktop.UDisks2.Block.OpenDevice", dbus.FlagAllowInteractiveAuthorization,
		"rw", map[string]dbus.Variant{
			"flags": dbus.MakeVariant(int32(unix.O_EXCL | unix.O_DIRECT | unix.O_CLOEXEC)),
		}).Store(&fd)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("open %s: %w", d.Path, friendlyDBusError(err))
	}

	f := os.NewFile(uintptr(fd), d.Path)
	sector, err := unix.IoctlGetInt(int(fd), unix.BLKSSZGET)
	if err != nil {
		f.Close()
		conn.Close()
		return nil, fmt.Errorf("sector size of %s: %w", d.Path, err)
	}
	var size uint64
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.BLKGETSIZE64, uintptr(unsafe.Pointer(&size))); e != 0 {
		f.Close()
		conn.Close()
		return nil, fmt.Errorf("size of %s: %w", d.Path, e)
	}
	return &blockDevice{f: f, conn: conn, obj: disk, size: int64(size), sector: sector}, nil
}

func unmountAll(conn *dbus.Conn, objs drives.Objects, disk dbus.ObjectPath) error {
	blocks := append([]dbus.ObjectPath{disk}, objs.Partitions(disk)...)
	for p := range objs {
		if objs.WholeDisk(p) == disk && p != disk && !slices.Contains(blocks, p) {
			// An unlocked encrypted volume (or similar) sits on this disk.
			if len(objs.MountPoints(p)) > 0 {
				return fmt.Errorf("an encrypted volume on this drive is unlocked and mounted at %s; unmount and lock it first", strings.Join(objs.MountPoints(p), ", "))
			}
			return errors.New("an encrypted volume on this drive is unlocked; lock it first")
		}
	}
	for _, b := range blocks {
		if len(objs.MountPoints(b)) == 0 {
			continue
		}
		err := conn.Object("org.freedesktop.UDisks2", b).Call(
			"org.freedesktop.UDisks2.Filesystem.Unmount", dbus.FlagAllowInteractiveAuthorization,
			map[string]dbus.Variant{}).Err
		if err != nil {
			return fmt.Errorf("unmount %s: %w", strings.Join(objs.MountPoints(b), ", "), friendlyDBusError(err))
		}
	}
	return nil
}

func friendlyDBusError(err error) error {
	if de, ok := errors.AsType[dbus.Error](err); ok {
		msg := de.Name
		if len(de.Body) > 0 {
			if s, ok := de.Body[0].(string); ok {
				msg = s
			}
		}
		switch {
		case strings.HasSuffix(de.Name, ".NotAuthorizedDismissed"):
			return errors.New("authorisation was cancelled")
		case strings.HasSuffix(de.Name, ".NotAuthorized"):
			return errors.New("not authorised to open the drive")
		case strings.HasSuffix(de.Name, ".DeviceBusy"):
			return fmt.Errorf("drive is busy (close any windows or programs using it): %s", msg)
		}
		return errors.New(msg)
	}
	return err
}

type blockDevice struct {
	f      *os.File
	conn   *dbus.Conn
	obj    dbus.ObjectPath
	size   int64
	sector int
}

func (d *blockDevice) ReadAt(p []byte, off int64) (int, error)  { return d.f.ReadAt(p, off) }
func (d *blockDevice) WriteAt(p []byte, off int64) (int, error) { return d.f.WriteAt(p, off) }
func (d *blockDevice) Size() int64                              { return d.size }
func (d *blockDevice) SectorSize() int                          { return d.sector }
func (d *blockDevice) Sync() error                              { return d.f.Sync() }

func (d *blockDevice) Close() error {
	err := d.f.Close()
	// Have the kernel pick up the new partition table. udev usually does
	// this on its own after a write, so a failure here is not fatal.
	d.conn.Object("org.freedesktop.UDisks2", d.obj).Call(
		"org.freedesktop.UDisks2.Block.Rescan", 0, map[string]dbus.Variant{})
	d.conn.Close()
	return err
}
