package media

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
)

// Partition is an MBR partition table entry, in sectors.
type Partition struct {
	Start, Count int64
	Type         byte // 0x0C for FAT32 with LBA addressing, 0x83 for Linux
	Active       bool
}

// MBR returns a master boot record holding up to four partitions and a
// random disk signature. There is no BIOS boot code: the media boots on
// UEFI, which reads the partition table and nothing else. The code that is
// there hands over to the next boot device.
func MBR(parts ...Partition) ([]byte, error) {
	if len(parts) > 4 {
		return nil, errors.New("an MBR holds at most four partitions")
	}
	b := make([]byte, 512)
	copy(b, []byte{0xCD, 0x18}) // int 18h

	// A zero signature makes Windows write its own, so pick a non-zero one.
	for binary.LittleEndian.Uint32(b[440:]) == 0 {
		if _, err := rand.Read(b[440:444]); err != nil {
			return nil, err
		}
	}

	for i, p := range parts {
		if p.Start <= 0 || p.Count <= 0 || p.Start+p.Count > 1<<32 {
			return nil, errors.New("partition does not fit an MBR partition table (2 TiB with 512-byte sectors)")
		}
		e := b[446+16*i : 462+16*i]
		if p.Active {
			e[0] = 0x80
		}
		copy(e[1:4], chs(p.Start))
		e[4] = p.Type
		copy(e[5:8], chs(p.Start+p.Count-1))
		binary.LittleEndian.PutUint32(e[8:], uint32(p.Start))
		binary.LittleEndian.PutUint32(e[12:], uint32(p.Count))
	}
	b[510], b[511] = 0x55, 0xAA
	return b, nil
}

// chs encodes a sector number as cylinder/head/sector for 255 heads and 63
// sectors per track, or the conventional maximum when it is out of range.
func chs(lba int64) []byte {
	const heads, sectors = 255, 63
	c := lba / (heads * sectors)
	if c > 1023 {
		return []byte{0xFE, 0xFF, 0xFF}
	}
	h := (lba / sectors) % heads
	s := lba%sectors + 1
	return []byte{byte(h), byte(s) | byte(c>>2)&0xC0, byte(c)}
}
