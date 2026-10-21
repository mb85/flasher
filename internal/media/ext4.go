package media

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
	"time"
)

// Ext4 is a planned, empty ext4 volume: its geometry and the regions that,
// written in order, produce it. It holds a root directory, lost+found, a
// journal and any small files given to PlanExt4.
//
// Only metadata is written, about 1 MiB plus the journal and two blocks
// per 128 MiB. Inode tables past the first group are left as they are and
// flagged uninitialised; the kernel zeroes them in the background on first
// mount, as it does for mkfs.ext4's lazy_itable_init.
type Ext4 struct {
	Blocks  int64 // 4 KiB blocks in the file system
	Groups  int64
	Regions []Region
}

const (
	extBlock      = 4096
	extPerGroup   = 8 * extBlock // blocks per group: one bitmap block's worth
	extInodeSize  = 256
	extPerInodes  = 32768 // bytes of volume per inode, mke2fs's "big" ratio
	extIPG        = extPerGroup * extBlock / extPerInodes
	extITBlocks   = extIPG * extInodeSize / extBlock // inode table blocks per group
	extFirstIno   = 11                               // first non-reserved inode
	extLPFBlocks  = 4                                // lost+found gets 16 KiB, as mke2fs makes it
	extExtraIsize = 32

	inoRoot    = 2
	inoJournal = 8
	inoLPF     = 11

	bgInodeUninit  = 0x1
	bgItableZeroed = 0x4

	extentsFlag = 0x80000
)

// PlanExt4 lays out an ext4 volume of volumeBytes labelled label, with the
// given files in its root directory. Each file must fit in one block.
func PlanExt4(volumeBytes int64, label string, files map[string][]byte) (*Ext4, error) {
	if len(label) > 16 {
		return nil, fmt.Errorf("ext4 label %q is longer than 16 bytes", label)
	}
	names := slices.Sorted(maps.Keys(files))
	for _, n := range names {
		if len(files[n]) > extBlock || len(n) > 255 || n == "" {
			return nil, fmt.Errorf("ext4: cannot add %q", n)
		}
	}

	blocks := volumeBytes / extBlock
	groups := ceilDiv(blocks, extPerGroup)
	// A last group too small to hold its own metadata and a little data is
	// left off, as mke2fs does.
	if last := blocks - (groups-1)*extPerGroup; groups > 1 && last < extOverhead(groups-1, groups)+50 {
		groups--
		blocks = groups * extPerGroup
	}
	journal := extJournalBlocks(blocks)
	dataStart := extOverhead(0, groups)
	rootBlock := dataStart
	lpfBlock := rootBlock + 1
	fileBlock := lpfBlock + extLPFBlocks
	journalStart := fileBlock + int64(len(names))
	used0 := journalStart + journal // blocks in use in group 0
	if used0+50 > min(blocks, extPerGroup) {
		return nil, fmt.Errorf("%w: a %d-byte volume is too small for ext4", ErrNoRoom, volumeBytes)
	}
	if blocks >= 1<<32 {
		return nil, fmt.Errorf("a %d-byte volume is too large for ext4 without 64-bit block numbers", volumeBytes)
	}

	v := &Ext4{Blocks: blocks, Groups: groups}
	var uuid, hashSeed [16]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		return nil, err
	}
	if _, err := rand.Read(hashSeed[:]); err != nil {
		return nil, err
	}
	now := uint32(time.Now().Unix())

	// Inodes in group 0's table, which is written in full.
	itable := make([]byte, extITBlocks*extBlock)
	inode := func(n int) []byte { return itable[(n-1)*extInodeSize : n*extInodeSize] }
	usedInodes := extFirstIno + len(names)
	putInode(inode(inoRoot), 0o040755, 3, extBlock, now, rootBlock, 1)
	putInode(inode(inoLPF), 0o040700, 2, extLPFBlocks*extBlock, now, lpfBlock, extLPFBlocks)
	putInode(inode(inoJournal), 0o100600, 1, journal*extBlock, now, journalStart, journal)
	for i, n := range names {
		putInode(inode(extFirstIno+1+i), 0o100644, 1, int64(len(files[n])), now, fileBlock+int64(i), 1)
	}

	// Directory blocks.
	root := make([]byte, extBlock)
	ents := []dirent{{inoRoot, ".", 2}, {inoRoot, "..", 2}, {inoLPF, "lost+found", 2}}
	for i, n := range names {
		ents = append(ents, dirent{uint32(extFirstIno + 1 + i), n, 1})
	}
	putDirents(root, ents)
	lpf := make([]byte, extLPFBlocks*extBlock)
	putDirents(lpf[:extBlock], []dirent{{inoLPF, ".", 2}, {inoRoot, "..", 2}})
	for b := 1; b < extLPFBlocks; b++ {
		binary.LittleEndian.PutUint16(lpf[b*extBlock+4:], extBlock) // one empty entry
	}

	// Group descriptors.
	gdtBlocks := ceilDiv(groups*32, extBlock)
	gdt := make([]byte, gdtBlocks*extBlock)
	var freeBlocks, freeInodes int64
	for g := range groups {
		base := g * extPerGroup
		size := min(extPerGroup, blocks-base)
		meta := extOverhead(g, groups)
		free := size - meta
		inodesFree := int64(extIPG)
		var dirs, flags uint16 = 0, bgInodeUninit
		unused := extIPG
		if g == 0 {
			free = size - used0
			inodesFree -= int64(usedInodes)
			dirs, flags = 2, bgItableZeroed
			unused = extIPG - usedInodes
		}
		freeBlocks += free
		freeInodes += inodesFree
		d := gdt[g*32 : g*32+32]
		bitmap := base + meta - 2 - extITBlocks
		binary.LittleEndian.PutUint32(d[0:], uint32(bitmap))
		binary.LittleEndian.PutUint32(d[4:], uint32(bitmap+1))
		binary.LittleEndian.PutUint32(d[8:], uint32(bitmap+2))
		binary.LittleEndian.PutUint16(d[12:], uint16(free))
		binary.LittleEndian.PutUint16(d[14:], uint16(inodesFree))
		binary.LittleEndian.PutUint16(d[16:], dirs)
		binary.LittleEndian.PutUint16(d[18:], flags)
		binary.LittleEndian.PutUint16(d[28:], uint16(unused))
		binary.LittleEndian.PutUint16(d[30:], groupDescCsum(uuid[:], uint32(g), d))
	}

	sb := func(group int64) []byte {
		b := make([]byte, 1024)
		le := binary.LittleEndian
		le.PutUint32(b[0x00:], uint32(groups*extIPG))
		le.PutUint32(b[0x04:], uint32(blocks))
		le.PutUint32(b[0x08:], uint32(blocks/100)) // 1% reserved for root
		le.PutUint32(b[0x0C:], uint32(freeBlocks))
		le.PutUint32(b[0x10:], uint32(freeInodes))
		le.PutUint32(b[0x18:], 2) // log2(block size) - 10
		le.PutUint32(b[0x1C:], 2) // cluster size, the same
		le.PutUint32(b[0x20:], extPerGroup)
		le.PutUint32(b[0x24:], extPerGroup)
		le.PutUint32(b[0x28:], extIPG)
		le.PutUint32(b[0x30:], now)    // write time
		le.PutUint16(b[0x36:], 0xFFFF) // no forced checks by mount count
		le.PutUint16(b[0x38:], 0xEF53)
		le.PutUint16(b[0x3A:], 1) // clean
		le.PutUint16(b[0x3C:], 1) // on errors, continue
		le.PutUint32(b[0x40:], now)
		le.PutUint32(b[0x4C:], 1) // dynamic inode sizes
		le.PutUint32(b[0x54:], extFirstIno)
		le.PutUint16(b[0x58:], extInodeSize)
		le.PutUint16(b[0x5A:], uint16(group))
		le.PutUint32(b[0x5C:], 0x04|0x08|0x20)                // has_journal, ext_attr, dir_index
		le.PutUint32(b[0x60:], 0x02|0x40)                     // filetype, extents
		le.PutUint32(b[0x64:], 0x01|0x02|0x08|0x10|0x20|0x40) // sparse_super, large_file, huge_file, gdt_csum, dir_nlink, extra_isize
		copy(b[0x68:0x78], uuid[:])
		copy(b[0x78:0x88], label)
		le.PutUint32(b[0xE0:], inoJournal)
		copy(b[0xEC:0xFC], hashSeed[:])
		b[0xFC] = 1                        // half MD4 directory hashes
		b[0xFD] = 1                        // s_jnl_blocks holds a copy of the journal's block map
		le.PutUint32(b[0x100:], 0x04|0x08) // user_xattr, acl
		le.PutUint32(b[0x108:], now)
		copy(b[0x10C:0x148], inode(inoJournal)[0x28:0x64])
		le.PutUint32(b[0x14C:], uint32(journal*extBlock))
		le.PutUint16(b[0x15C:], extExtraIsize)
		le.PutUint16(b[0x15E:], extExtraIsize)
		le.PutUint32(b[0x160:], 1) // signed directory hashes, as x86 mke2fs sets
		return b
	}

	// Group 0, from the start of the volume up to the journal. Its first
	// kilobyte stays zero, which also clears any old boot sector.
	head := make([]byte, journalStart*extBlock)
	copy(head[1024:], sb(0))
	copy(head[extBlock:], gdt)
	bb := bitmapBlock(used0, min(blocks, extPerGroup))
	ib := bitmapBlock(int64(usedInodes), extIPG)
	off := (dataStart - 2 - extITBlocks) * extBlock
	copy(head[off:], bb)
	copy(head[off+extBlock:], ib)
	copy(head[off+2*extBlock:], itable)
	copy(head[rootBlock*extBlock:], root)
	copy(head[lpfBlock*extBlock:], lpf)
	for i, n := range names {
		copy(head[(fileBlock+int64(i))*extBlock:], files[n])
	}
	v.Regions = append(v.Regions, Region{Name: "ext4 metadata", Offset: 0, Length: int64(len(head)), Data: head})
	v.Regions = append(v.Regions,
		Region{Name: "ext4 journal", Offset: journalStart * extBlock, Length: extBlock, Data: journalSuperblock(journal, uuid[:])},
		Region{Name: "ext4 journal", Offset: (journalStart + 1) * extBlock, Length: (journal - 1) * extBlock})
	end := used0 * extBlock

	// The other groups: superblock and descriptor backups where sparse_super
	// puts them, then the two bitmaps.
	for g := int64(1); g < groups; g++ {
		base := g * extPerGroup
		var b []byte
		if hasSuperBackup(g) {
			b = append(b, sb(g)...)
			b = append(b, make([]byte, extBlock-1024)...)
			b = append(b, gdt...)
		}
		size := min(extPerGroup, blocks-base)
		b = append(b, bitmapBlock(extOverhead(g, groups), size)...)
		b = append(b, bitmapBlock(0, extIPG)...)
		v.Regions = append(v.Regions, Region{Name: "ext4 group metadata", Offset: base * extBlock, Length: int64(len(b)), Data: b})
		end = base*extBlock + int64(len(b))
	}

	// Clear the volume's last MiB too, where some RAID and volume manager
	// signatures live, so nothing but ext4 is detected on it.
	if tail := max(end, (volumeBytes-1<<20)/extBlock*extBlock); tail < volumeBytes {
		v.Regions = append(v.Regions, Region{Name: "end of ext4 volume", Offset: tail, Length: volumeBytes - tail})
	}
	return v, nil
}

// extOverhead is the number of metadata blocks at the start of group g:
// any superblock and descriptor backup, two bitmaps and the inode table.
func extOverhead(g, groups int64) int64 {
	n := int64(2 + extITBlocks)
	if hasSuperBackup(g) {
		n += 1 + ceilDiv(groups*32, extBlock)
	}
	return n
}

// hasSuperBackup reports whether sparse_super keeps a superblock copy in
// group g: groups 0 and 1 and powers of 3, 5 and 7.
func hasSuperBackup(g int64) bool {
	if g <= 1 {
		return true
	}
	for _, p := range []int64{3, 5, 7} {
		n := g
		for n%p == 0 {
			n /= p
		}
		if n == 1 {
			return true
		}
	}
	return false
}

// extJournalBlocks follows mke2fs's journal sizes up to 64 MiB, which is
// plenty for a live system's changes and keeps the journal in group 0.
func extJournalBlocks(blocks int64) int64 {
	switch {
	case blocks < 32768:
		return 1024
	case blocks < 256*1024:
		return 4096
	case blocks < 512*1024:
		return 8192
	}
	return 16384
}

// bitmapBlock is a bitmap block with the first used bits set, and every bit
// from size on set too, as padding past the end of the group.
func bitmapBlock(used, size int64) []byte {
	b := make([]byte, extBlock)
	for i := range int64(extBlock * 8) {
		if i < used || i >= size {
			b[i/8] |= 1 << (i % 8)
		}
	}
	return b
}

// putInode fills in a directory or regular file inode whose data is one
// run of count blocks starting at block start.
func putInode(b []byte, mode uint16, links uint16, size int64, now uint32, start, count int64) {
	le := binary.LittleEndian
	le.PutUint16(b[0x00:], mode)
	le.PutUint32(b[0x04:], uint32(size))
	le.PutUint32(b[0x08:], now)
	le.PutUint32(b[0x0C:], now)
	le.PutUint32(b[0x10:], now)
	le.PutUint16(b[0x1A:], links)
	le.PutUint32(b[0x1C:], uint32(count*extBlock/512))
	le.PutUint32(b[0x20:], extentsFlag)
	// Extent tree: a header and one leaf extent in i_block.
	le.PutUint16(b[0x28:], 0xF30A)
	le.PutUint16(b[0x2A:], 1) // entries
	le.PutUint16(b[0x2C:], 4) // capacity
	le.PutUint32(b[0x34:], 0) // logical block
	le.PutUint16(b[0x38:], uint16(count))
	le.PutUint32(b[0x3C:], uint32(start))
	le.PutUint32(b[0x6C:], uint32(size>>32))
	le.PutUint16(b[0x80:], extExtraIsize)
	le.PutUint32(b[0x90:], now) // creation time
}

type dirent struct {
	ino  uint32
	name string
	typ  byte // 1 file, 2 directory
}

// putDirents writes linear directory entries into one block, the last one
// stretched to the end of the block.
func putDirents(b []byte, ents []dirent) {
	off := 0
	for i, e := range ents {
		n := (8 + len(e.name) + 3) &^ 3
		if i == len(ents)-1 {
			n = len(b) - off
		}
		binary.LittleEndian.PutUint32(b[off:], e.ino)
		binary.LittleEndian.PutUint16(b[off+4:], uint16(n))
		b[off+6] = byte(len(e.name))
		b[off+7] = e.typ
		copy(b[off+8:], e.name)
		off += n
	}
}

// journalSuperblock is the first block of an empty, clean jbd2 journal.
// Its fields are big-endian.
func journalSuperblock(blocks int64, uuid []byte) []byte {
	b := make([]byte, extBlock)
	be := binary.BigEndian
	be.PutUint32(b[0x00:], 0xC03B3998)
	be.PutUint32(b[0x04:], 4) // superblock v2
	be.PutUint32(b[0x0C:], extBlock)
	be.PutUint32(b[0x10:], uint32(blocks))
	be.PutUint32(b[0x14:], 1) // first log block
	be.PutUint32(b[0x18:], 1) // first expected sequence
	copy(b[0x30:0x40], uuid)
	be.PutUint32(b[0x40:], 1) // one user: this file system
	return b
}

// groupDescCsum is the gdt_csum checksum of a group descriptor: CRC-16
// over the file system UUID, the group number and the descriptor up to
// its checksum field.
func groupDescCsum(uuid []byte, group uint32, desc []byte) uint16 {
	var g [4]byte
	binary.LittleEndian.PutUint32(g[:], group)
	crc := crc16(0xFFFF, uuid)
	crc = crc16(crc, g[:])
	return crc16(crc, desc[:30])
}

var crc16Table = func() (t [256]uint16) {
	for i := range t {
		c := uint16(i)
		for range 8 {
			if c&1 != 0 {
				c = c>>1 ^ 0xA001
			} else {
				c >>= 1
			}
		}
		t[i] = c
	}
	return
}()

// crc16 is the kernel's lib/crc16 (polynomial 0x8005, bit-reversed).
func crc16(crc uint16, b []byte) uint16 {
	for _, c := range b {
		crc = crc>>8 ^ crc16Table[byte(crc)^c]
	}
	return crc
}
