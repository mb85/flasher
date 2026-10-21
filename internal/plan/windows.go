package plan

import (
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"flasher/internal/source"
	"flasher/internal/winmedia"
)

const mib = 1 << 20

// Windows rebuilds a Windows installer ISO as UEFI-bootable media: an MBR
// with one FAT32 partition from 1 MiB to 1 MiB short of the end, holding
// every file on the ISO, with install.wim split into .swm parts when it is
// too large for FAT32. The first and last MiB are written as well (the
// partition table and zeros), so no old GPT, partition table or file system
// signature survives around the new one.
func Windows(img *source.Image, driveSize int64, sectorSize int) (_ *Plan, err error) {
	if sectorSize <= 0 {
		sectorSize = 512
	}
	ss := int64(sectorSize)
	if driveSize%ss != 0 || driveSize < 64*mib {
		return nil, fmt.Errorf("a %d-byte drive is too small for Windows installer media", driveSize)
	}

	iso, err := source.OpenISO(img.Path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", img.Path, err)
	}
	p := &Plan{DriveSize: driveSize, SectorSize: sectorSize, closers: []io.Closer{iso}}
	defer func() {
		if err != nil {
			p.Close()
		}
	}()

	root, err := isoTree(iso, "/")
	if err != nil {
		return nil, err
	}
	if err := splitInstallWIM(iso, root, p); err != nil {
		return nil, err
	}

	tail := driveSize/mib*mib - mib // last whole MiB, cleared
	partStart := int64(mib)
	partBytes := tail - partStart
	// MBR sector numbers are 32-bit, so the partition ends by 2 TiB with
	// 512-byte sectors; anything past that is left unused.
	partBytes = min(partBytes, ((1<<32)-partStart/ss)*ss/mib*mib)

	vol, err := winmedia.PlanFAT32(root, partBytes, sectorSize, iso.Vol.Label())
	if err != nil {
		if errors.Is(err, winmedia.ErrNoRoom) {
			return nil, fmt.Errorf("the drive is too small for this Windows image: %w", err)
		}
		return nil, err
	}
	mbr, err := winmedia.MBR(partStart/ss, partBytes/ss)
	if err != nil {
		return nil, err
	}
	head := make([]byte, partStart)
	copy(head, mbr)

	p.Segments = append(p.Segments, Bytes("partition table", 0, head))
	for _, r := range vol.Regions {
		off := partStart + r.Offset
		switch {
		case r.Data != nil:
			p.Segments = append(p.Segments, Bytes(r.Name, off, r.Data))
		case r.Open != nil:
			p.Segments = append(p.Segments, Segment{Name: r.Name, Offset: off, Length: r.Length, Open: r.Open})
		default:
			p.Segments = append(p.Segments, Zeros(r.Name, off, r.Length))
		}
	}
	p.Segments = append(p.Segments, Zeros("end of drive", tail, driveSize-tail))
	for _, s := range p.Segments {
		p.Total += s.Length
	}
	return p, nil
}

// isoTree reads the directory at dir and everything under it.
func isoTree(iso *source.ISO, dir string) (*winmedia.Node, error) {
	fi, err := iso.Vol.Stat(dir)
	if err != nil {
		return nil, err
	}
	n := &winmedia.Node{Name: path.Base(dir), Dir: true, ModTime: fi.ModTime()}
	if dir == "/" {
		n.Name = ""
	}
	ents, err := iso.Vol.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s on the ISO: %w", dir, err)
	}
	for _, e := range ents {
		p := path.Join(dir, e.Name())
		if e.IsDir() {
			c, err := isoTree(iso, p)
			if err != nil {
				return nil, err
			}
			n.Children = append(n.Children, c)
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("read %s on the ISO: %w", p, err)
		}
		n.Children = append(n.Children, &winmedia.Node{
			Name:    e.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
			Open:    func() (io.ReadCloser, error) { return iso.Vol.Open(p) },
		})
	}
	return n, nil
}

// splitInstallWIM replaces sources/install.wim with .swm parts when it is
// too large for FAT32. The parts are read straight from the ISO, so the WIM
// stays open until the plan is closed.
func splitInstallWIM(iso *source.ISO, root *winmedia.Node, p *Plan) error {
	sources := child(root, "sources")
	if sources == nil {
		return nil
	}
	wim := child(sources, "install.wim")
	if wim == nil || wim.Size <= winmedia.FAT32MaxFile {
		return nil
	}
	f, err := wim.Open()
	if err != nil {
		return err
	}
	p.closers = append(p.closers, f)
	src, ok := f.(io.ReaderAt)
	if !ok {
		return errors.New("install.wim on the ISO does not support random access")
	}
	info, err := winmedia.ReadWIM(src, wim.Size)
	if err != nil {
		return fmt.Errorf("install.wim: %w", err)
	}
	parts, err := info.Split("install", winmedia.DefaultPartSize)
	if err != nil {
		return fmt.Errorf("split install.wim: %w", err)
	}
	var kids []*winmedia.Node
	for _, c := range sources.Children {
		if c != wim {
			kids = append(kids, c)
		}
	}
	for _, part := range parts {
		kids = append(kids, &winmedia.Node{
			Name:    part.Name,
			Size:    part.Size,
			ModTime: wim.ModTime,
			Open:    func() (io.ReadCloser, error) { return io.NopCloser(part.Open(src)), nil },
		})
	}
	sources.Children = kids
	return nil
}

func child(dir *winmedia.Node, name string) *winmedia.Node {
	for _, c := range dir.Children {
		if strings.EqualFold(c.Name, name) {
			return c
		}
	}
	return nil
}
