package drives

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	udisksDest = "org.freedesktop.UDisks2"
	udisksRoot = "/org/freedesktop/UDisks2"

	ifBlock     = "org.freedesktop.UDisks2.Block"
	ifDrive     = "org.freedesktop.UDisks2.Drive"
	ifPartition = "org.freedesktop.UDisks2.Partition"
	ifFS        = "org.freedesktop.UDisks2.Filesystem"
	ifSwap      = "org.freedesktop.UDisks2.Swapspace"
	ifLoop      = "org.freedesktop.UDisks2.Loop"
)

// Objects is a snapshot of UDisks2's object tree.
type Objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant

// GetObjects fetches every UDisks2 object and its properties.
func GetObjects(conn *dbus.Conn) (Objects, error) {
	var objs Objects
	err := conn.Object(udisksDest, udisksRoot).
		Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objs)
	if err != nil {
		return nil, fmt.Errorf("query UDisks2 (is udisks2 running?): %w", err)
	}
	return objs, nil
}

func (o Objects) prop(path dbus.ObjectPath, iface, name string) any {
	if v, ok := o[path][iface][name]; ok {
		return v.Value()
	}
	return nil
}

func (o Objects) str(path dbus.ObjectPath, iface, name string) string {
	switch v := o.prop(path, iface, name).(type) {
	case string:
		return v
	case []byte:
		return strings.TrimRight(string(v), "\x00")
	}
	return ""
}

func (o Objects) objPath(path dbus.ObjectPath, iface, name string) dbus.ObjectPath {
	if v, ok := o.prop(path, iface, name).(dbus.ObjectPath); ok && v != "/" {
		return v
	}
	return ""
}

func (o Objects) u64(path dbus.ObjectPath, iface, name string) uint64 {
	v, _ := o.prop(path, iface, name).(uint64)
	return v
}

func (o Objects) boolean(path dbus.ObjectPath, iface, name string) bool {
	v, _ := o.prop(path, iface, name).(bool)
	return v
}

// MountPoints lists where a block device's filesystem is mounted.
func (o Objects) MountPoints(block dbus.ObjectPath) []string {
	raw, _ := o.prop(block, ifFS, "MountPoints").([][]byte)
	var out []string
	for _, m := range raw {
		out = append(out, strings.TrimRight(string(m), "\x00"))
	}
	return out
}

// WholeDisk follows partitions and unlocked encrypted volumes down to the
// whole-disk block device they live on.
func (o Objects) WholeDisk(block dbus.ObjectPath) dbus.ObjectPath {
	for range 8 { // bounded, in case of a cycle
		if b := o.objPath(block, ifBlock, "CryptoBackingDevice"); b != "" {
			block = b
			continue
		}
		if t := o.objPath(block, ifPartition, "Table"); t != "" {
			block = t
			continue
		}
		return block
	}
	return block
}

// Partitions lists the block devices that are partitions of disk.
func (o Objects) Partitions(disk dbus.ObjectPath) []dbus.ObjectPath {
	var out []dbus.ObjectPath
	for p := range o {
		if o.objPath(p, ifPartition, "Table") == disk {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

var systemMounts = []string{"/", "/boot", "/boot/efi", "/efi", "/usr", "/var", "/home"}

func list() ([]Drive, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	objs, err := GetObjects(conn)
	if err != nil {
		return nil, err
	}

	// Mounted filesystems, volume labels and active swap, keyed by the
	// whole disk under them.
	mounts := map[dbus.ObjectPath][]string{}
	labels := map[dbus.ObjectPath][]string{}
	system := map[dbus.ObjectPath]bool{}
	for p, ifaces := range objs {
		if _, ok := ifaces[ifBlock]; !ok {
			continue
		}
		disk := objs.WholeDisk(p)
		if l := objs.str(p, ifBlock, "IdLabel"); l != "" {
			labels[disk] = append(labels[disk], l)
		}
		for _, m := range objs.MountPoints(p) {
			mounts[disk] = append(mounts[disk], m)
			if slices.Contains(systemMounts, m) {
				system[disk] = true
			}
		}
		if objs.boolean(p, ifSwap, "Active") {
			system[disk] = true
		}
	}

	var out []Drive
	for p, ifaces := range objs {
		if _, ok := ifaces[ifBlock]; !ok {
			continue
		}
		if _, isPart := ifaces[ifPartition]; isPart {
			continue
		}
		if objs.objPath(p, ifBlock, "CryptoBackingDevice") != "" {
			continue
		}
		_, isLoop := ifaces[ifLoop]
		drive := objs.objPath(p, ifBlock, "Drive")
		if drive == "" && !isLoop {
			continue // device-mapper, zram and other virtual devices
		}
		size := int64(objs.u64(p, ifBlock, "Size"))
		if isLoop && size == 0 {
			continue // unused loop device
		}
		d := Drive{
			Path:     objs.str(p, ifBlock, "Device"),
			ID:       string(p),
			Size:     size,
			IsSystem: system[p],
			Mounts:   mounts[p],
		}
		if isLoop {
			d.Bus = "loop"
			d.Vendor = "Loop"
			d.Model = filepath.Base(objs.str(p, ifLoop, "BackingFile"))
		} else {
			d.Vendor = objs.str(drive, ifDrive, "Vendor")
			d.Model = objs.str(drive, ifDrive, "Model")
			d.Bus = objs.str(drive, ifDrive, "ConnectionBus")
			d.Removable = objs.boolean(drive, ifDrive, "Removable") || objs.boolean(drive, ifDrive, "MediaRemovable")
			if d.Bus == "" && strings.HasPrefix(d.Path, "/dev/nvme") {
				d.Bus = "nvme"
			}
		}
		sort.Strings(d.Mounts)
		// A hybrid ISO repeats its label on the whole disk and partition 1.
		d.Labels = slices.Compact(slices.Sorted(slices.Values(labels[p])))
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
