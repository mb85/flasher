package media

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf16"
)

// Node is a file or directory to put on a FAT32 volume.
type Node struct {
	Name     string
	Dir      bool
	Size     int64 // files only
	ModTime  time.Time
	Open     func() (io.ReadCloser, error) // files only
	Children []*Node                       // directories only

	cluster  uint32 // first cluster, 0 for an empty file
	clusters int64  // clusters allocated
	entries  []byte // directory contents, built once clusters are known
}

// Region is a run of bytes in the volume, at an offset from its first
// sector. Exactly one of Data and Open is set, or neither for zeros.
type Region struct {
	Name   string
	Offset int64
	Length int64
	Data   []byte
	Open   func() (io.ReadCloser, error)
}

// FAT32 is a planned FAT32 volume: its geometry and the regions that, written
// in order, produce it.
type FAT32 struct {
	SectorSize   int
	ClusterSize  int
	Reserved     int64 // sectors before the first FAT
	FATSectors   int64 // sectors per FAT
	TotalSectors int64
	Clusters     int64  // data clusters, numbered from 2
	Used         int64  // clusters taken by files and directories
	VolumeID     uint32 // serial number, random
	Regions      []Region
}

const (
	dirEntrySize = 32
	fatEOC       = 0x0FFFFFFF
	fatMedia     = 0x0FFFFFF8

	attrVolumeID = 0x08
	attrDir      = 0x10
	attrArchive  = 0x20
	attrLFN      = 0x0F

	minFAT32Clusters = 65525      // fewer and readers take it for FAT16
	maxFAT32Clusters = 0x0FFFFFF4 // highest usable cluster number is 0x0FFFFFF6
	dataAlign        = 1 << 20    // the data region starts on a 1 MiB boundary
)

// ClusterSizeFor picks the cluster size Windows' own formatter uses for a
// FAT32 volume of the given size.
func ClusterSizeFor(volumeBytes int64) int {
	switch {
	case volumeBytes <= 64<<20:
		return 512
	case volumeBytes <= 128<<20:
		return 1 << 10
	case volumeBytes <= 256<<20:
		return 2 << 10
	case volumeBytes <= 8<<30:
		return 4 << 10
	case volumeBytes <= 16<<30:
		return 8 << 10
	case volumeBytes <= 32<<30:
		return 16 << 10
	}
	return 32 << 10
}

// ErrNoRoom means the files do not fit on the volume.
var ErrNoRoom = errors.New("not enough room on the drive")

// NoRoomError says how much room the files need and how much the volume
// has. It matches ErrNoRoom.
type NoRoomError struct{ Need, Have int64 }

func (e *NoRoomError) Error() string {
	return fmt.Sprintf("the files need %d bytes and the volume holds %d", e.Need, e.Have)
}

func (e *NoRoomError) Is(target error) bool { return target == ErrNoRoom }

// PlanFAT32 lays out a FAT32 volume of volumeBytes holding the tree under
// root, which must be a directory. Every file and directory gets one
// contiguous run of clusters, directories first and files after them in
// tree order, so the volume is written front to back in one pass. Sectors
// that hold nothing are not part of any region; the FATs are written in
// full, so stale data there cannot be mistaken for allocations.
func PlanFAT32(root *Node, volumeBytes int64, sectorSize int, label string) (*FAT32, error) {
	if !root.Dir {
		return nil, errors.New("root must be a directory")
	}
	ss := int64(sectorSize)
	cs := max(ClusterSizeFor(volumeBytes), sectorSize)
	v := &FAT32{SectorSize: sectorSize, ClusterSize: cs, TotalSectors: volumeBytes / ss}
	spc := int64(cs) / ss

	// Size the FAT for the smallest reserved area, then grow the reserved
	// area to align the data region. Growing it only shrinks the data
	// region, so the FAT stays big enough. A FAT of F sectors holds
	// F*ss/4 entries and must cover clusters 0 to N+1, where
	// N = (total - reserved - 2F) / spc. Solving for F gives:
	const minReserved = 32
	perSector := ss / 4
	align := max(dataAlign/ss, 1)
	fatFor := func(reserved int64) int64 {
		return ceilDiv(v.TotalSectors-reserved+2*spc, perSector*spc+2)
	}
	reservedFor := func(fat int64) int64 {
		r := int64(minReserved)
		if m := (r + 2*fat) % align; m != 0 {
			r += align - m
		}
		return r
	}
	v.FATSectors = fatFor(minReserved)
	v.Reserved = reservedFor(v.FATSectors)
	// The padding usually leaves the FAT a few sectors larger than it
	// needs to be; one refit trims that, kept only if it still covers.
	if f := fatFor(v.Reserved); f < v.FATSectors {
		if r := reservedFor(f); fatFor(r) <= f {
			v.FATSectors, v.Reserved = f, r
		}
	}
	dataSectors := v.TotalSectors - v.Reserved - 2*v.FATSectors
	if dataSectors <= 0 {
		return nil, fmt.Errorf("%w: a %d-byte volume is too small for FAT32", ErrNoRoom, volumeBytes)
	}
	v.Clusters = dataSectors / spc
	if v.Clusters < minFAT32Clusters {
		return nil, fmt.Errorf("%w: a %d-byte volume is too small for FAT32", ErrNoRoom, volumeBytes)
	}
	if v.Clusters > maxFAT32Clusters {
		return nil, fmt.Errorf("a %d-byte volume is too large for FAT32", volumeBytes)
	}

	var id [4]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	v.VolumeID = binary.LittleEndian.Uint32(id[:])

	// Allocate directories (breadth first, root at cluster 2), then files.
	var dirs, files []*Node
	queue := []*Node{root}
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		dirs = append(dirs, d)
		for _, c := range d.Children {
			if c.Dir {
				queue = append(queue, c)
			}
		}
	}
	shorts := map[*Node]shortName{}
	for _, d := range dirs {
		n, err := assignShortNames(d, d == root, shorts)
		if err != nil {
			return nil, err
		}
		d.clusters = max(ceilDiv(int64(n)*dirEntrySize, int64(cs)), 1)
	}
	for _, d := range dirs {
		for _, c := range d.Children {
			if !c.Dir {
				if c.Size > FAT32MaxFile {
					return nil, fmt.Errorf("%s is %d bytes, larger than FAT32 allows", c.Name, c.Size)
				}
				c.clusters = ceilDiv(c.Size, int64(cs))
				files = append(files, c)
			}
		}
	}
	next := int64(2)
	for _, n := range append(dirs, files...) {
		if n.clusters == 0 {
			continue
		}
		n.cluster = uint32(next)
		next += n.clusters
	}
	v.Used = next - 2
	if v.Used > v.Clusters {
		return nil, &NoRoomError{Need: v.Used * int64(cs), Have: v.Clusters * int64(cs)}
	}

	parent := map[*Node]*Node{}
	for _, d := range dirs {
		for _, c := range d.Children {
			parent[c] = d
		}
	}
	for _, d := range dirs {
		d.entries = buildDir(d, d == root, parent[d], shorts, label, cs)
	}

	v.Regions = v.layout(dirs, files, label)
	return v, nil
}

func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }

// clusterOffset is the byte offset of cluster c from the volume's start.
func (v *FAT32) clusterOffset(c uint32) int64 {
	ss := int64(v.SectorSize)
	return (v.Reserved+2*v.FATSectors)*ss + int64(c-2)*int64(v.ClusterSize)
}

func (v *FAT32) layout(dirs, files []*Node, label string) []Region {
	ss := int64(v.SectorSize)
	var out []Region

	reserved := make([]byte, v.Reserved*ss)
	boot := v.bootSector(label)
	info := v.fsInfo()
	copy(reserved, boot)
	copy(reserved[1*ss:], info)
	copy(reserved[6*ss:], boot) // backup boot sector
	copy(reserved[7*ss:], info) // backup FSInfo
	out = append(out, Region{Name: "FAT32 boot sectors", Offset: 0, Length: int64(len(reserved)), Data: reserved})

	fat := v.fat(dirs, files)
	fatBytes := v.FATSectors * ss
	for i := range int64(2) {
		off := (v.Reserved + i*v.FATSectors) * ss
		name := fmt.Sprintf("FAT %d", i+1)
		used := int64(len(fat)) / ss * ss // keep the zero tail sector aligned
		out = append(out, Region{Name: name, Offset: off, Length: used, Data: fat[:used]})
		if rest := fat[used:]; len(rest) > 0 {
			tail := make([]byte, ss)
			copy(tail, rest)
			out = append(out, Region{Name: name, Offset: off + used, Length: ss, Data: tail})
			used += ss
		}
		if fatBytes > used {
			out = append(out, Region{Name: name, Offset: off + used, Length: fatBytes - used})
		}
	}

	for _, d := range dirs {
		out = append(out, Region{Name: dirName(d), Offset: v.clusterOffset(d.cluster), Length: int64(len(d.entries)), Data: d.entries})
	}
	for _, f := range files {
		if f.Size == 0 {
			continue
		}
		out = append(out, Region{Name: f.Name, Offset: v.clusterOffset(f.cluster), Length: f.Size, Open: f.Open})
	}
	return out
}

func dirName(d *Node) string {
	if d.Name == "" {
		return "root directory"
	}
	return d.Name + "/"
}

// fat builds the used part of one FAT: the two reserved entries and a chain
// for every allocated run. Everything after it is zero (free).
func (v *FAT32) fat(dirs, files []*Node) []byte {
	b := make([]byte, (v.Used+2)*4)
	binary.LittleEndian.PutUint32(b[0:], fatMedia)
	binary.LittleEndian.PutUint32(b[4:], fatEOC)
	for _, n := range append(dirs, files...) {
		for i := range n.clusters {
			c := int64(n.cluster) + i
			next := uint32(c + 1)
			if i == n.clusters-1 {
				next = fatEOC
			}
			binary.LittleEndian.PutUint32(b[c*4:], next)
		}
	}
	return b
}

func (v *FAT32) bootSector(label string) []byte {
	b := make([]byte, v.SectorSize)
	copy(b, []byte{0xEB, 0x58, 0x90}) // jump over the BPB
	copy(b[3:11], "MSWIN4.1")
	binary.LittleEndian.PutUint16(b[11:], uint16(v.SectorSize))
	b[13] = byte(v.ClusterSize / v.SectorSize)
	binary.LittleEndian.PutUint16(b[14:], uint16(v.Reserved))
	b[16] = 2                                                             // FATs
	b[21] = 0xF8                                                          // fixed media
	binary.LittleEndian.PutUint16(b[24:], 63)                             // sectors per track
	binary.LittleEndian.PutUint16(b[26:], 255)                            // heads
	binary.LittleEndian.PutUint32(b[28:], uint32(dataAlign/v.SectorSize)) // hidden sectors: the partition's start
	binary.LittleEndian.PutUint32(b[32:], uint32(v.TotalSectors))
	binary.LittleEndian.PutUint32(b[36:], uint32(v.FATSectors))
	binary.LittleEndian.PutUint32(b[44:], 2) // root directory cluster
	binary.LittleEndian.PutUint16(b[48:], 1) // FSInfo sector
	binary.LittleEndian.PutUint16(b[50:], 6) // backup boot sector
	b[64] = 0x80                             // drive number
	b[66] = 0x29                             // extended boot signature
	binary.LittleEndian.PutUint32(b[67:], v.VolumeID)
	copy(b[71:82], fatLabel(label))
	copy(b[82:90], "FAT32   ")
	// The volume cannot boot a BIOS, so the code hands over to the next
	// boot device rather than running into zeros.
	copy(b[90:], []byte{0xCD, 0x18}) // int 18h
	b[510], b[511] = 0x55, 0xAA
	return b
}

func (v *FAT32) fsInfo() []byte {
	b := make([]byte, v.SectorSize)
	binary.LittleEndian.PutUint32(b[0:], 0x41615252)
	binary.LittleEndian.PutUint32(b[484:], 0x61417272)
	binary.LittleEndian.PutUint32(b[488:], uint32(v.Clusters-v.Used))
	binary.LittleEndian.PutUint32(b[492:], uint32(v.Used+2))
	binary.LittleEndian.PutUint32(b[508:], 0xAA550000)
	return b
}

// FATLabel is the volume label a FAT32 volume planned with label gets, as
// Linux reports it: upper case, at most 11 characters, spaces trimmed.
func FATLabel(label string) string {
	return strings.TrimRight(string(fatLabel(label)), " ")
}

// fatLabel turns an ISO label into an 11-byte FAT volume label.
func fatLabel(label string) []byte {
	b := []byte("NO NAME    ")
	if label == "" {
		return b
	}
	for i := range b {
		b[i] = ' '
	}
	i := 0
	for _, r := range strings.ToUpper(label) {
		if i == len(b) {
			break
		}
		if r > 0x7E || r < 0x20 || strings.ContainsRune(`"*+,./:;<=>?[\]|`, r) {
			r = '_'
		}
		b[i] = byte(r)
		i++
	}
	return b
}

// shortName is a node's 8.3 directory entry name, and whether a long-name
// entry carries its real name.
type shortName struct {
	name [11]byte
	lfn  bool
}

// assignShortNames gives every child of d an 8.3 name, unique within d, and
// returns how many directory entries d needs.
func assignShortNames(d *Node, isRoot bool, shorts map[*Node]shortName) (int, error) {
	taken := map[[11]byte]bool{}
	n := 0
	if isRoot {
		n++ // volume label
	} else {
		n += 2 // "." and ".."
		taken[dotName(".")] = true
		taken[dotName("..")] = true
	}
	// Names that are already valid 8.3 names keep them; claim those first
	// so generated aliases cannot take them.
	var rest []*Node
	seen := map[string]bool{}
	for _, c := range d.Children {
		key := strings.ToUpper(c.Name)
		if c.Name == "" || c.Name == "." || c.Name == ".." || seen[key] {
			return 0, fmt.Errorf("bad or duplicate name %q in %s", c.Name, dirName(d))
		}
		seen[key] = true
		if s, ok := exactShortName(c.Name); ok && !taken[s] {
			shorts[c] = shortName{name: s}
			taken[s] = true
			n++
			continue
		}
		rest = append(rest, c)
	}
	for _, c := range rest {
		s, err := aliasFor(c.Name, taken)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", c.Name, err)
		}
		shorts[c] = shortName{name: s, lfn: true}
		taken[s] = true
		n += 1 + lfnEntries(c.Name)
	}
	return n, nil
}

func dotName(s string) [11]byte {
	var b [11]byte
	copy(b[:], s+"          ")
	return b
}

const shortChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!#$%&'()-@^_`{}~"

// exactShortName returns name as an 8.3 entry if it already is one: upper
// case, at most eight characters, an optional extension of at most three,
// and only characters allowed in short names.
func exactShortName(name string) ([11]byte, bool) {
	base, ext, _ := strings.Cut(name, ".")
	if base == "" || len(base) > 8 || len(ext) > 3 || strings.Contains(ext, ".") || strings.HasSuffix(name, ".") {
		return [11]byte{}, false
	}
	for _, r := range base + ext {
		if !strings.ContainsRune(shortChars, r) {
			return [11]byte{}, false
		}
	}
	var s [11]byte
	copy(s[:], fmt.Sprintf("%-8s%-3s", base, ext))
	return s, true
}

// aliasFor makes a numbered short alias such as "SETUPH~1EXE" for a name
// that needs a long-name entry.
func aliasFor(name string, taken map[[11]byte]bool) ([11]byte, error) {
	clean := func(s string) string {
		var b strings.Builder
		for _, r := range strings.ToUpper(s) {
			switch {
			case r == ' ' || r == '.':
				// dropped, as Windows does
			case strings.ContainsRune(shortChars, r):
				b.WriteRune(r)
			default:
				b.WriteByte('_')
			}
		}
		return b.String()
	}
	base, ext := name, ""
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		base, ext = name[:i], name[i+1:]
	}
	base, ext = clean(base), clean(ext)
	if len(ext) > 3 {
		ext = ext[:3]
	}
	if base == "" {
		base = "_"
	}
	for n := 1; n < 1_000_000; n++ {
		tail := fmt.Sprintf("~%d", n)
		b := base
		if len(b)+len(tail) > 8 {
			b = b[:8-len(tail)]
		}
		var s [11]byte
		copy(s[:], fmt.Sprintf("%-8s%-3s", b+tail, ext))
		if !taken[s] {
			return s, nil
		}
	}
	return [11]byte{}, errors.New("no free short name")
}

// lfnEntries is how many long-name entries name needs, 13 UTF-16 units each.
func lfnEntries(name string) int {
	return (len(utf16.Encode([]rune(name))) + 12) / 13
}

func lfnChecksum(s [11]byte) byte {
	var sum byte
	for _, c := range s {
		sum = (sum>>1 | sum<<7) + c
	}
	return sum
}

// buildDir encodes d's entries, padded with zeros to whole clusters (a
// zero entry marks the end of a directory).
func buildDir(d *Node, isRoot bool, parent *Node, shorts map[*Node]shortName, label string, clusterSize int) []byte {
	var b []byte
	mod := d.ModTime
	if isRoot {
		var l [11]byte
		copy(l[:], fatLabel(label))
		b = append(b, shortEntry(l, attrVolumeID, 0, 0, mod)...)
	} else {
		b = append(b, shortEntry(dotName("."), attrDir, d.cluster, 0, mod)...)
		pc := uint32(0) // ".." of a top-level directory points at cluster 0
		if parent != nil && parent.Name != "" {
			pc = parent.cluster
		}
		b = append(b, shortEntry(dotName(".."), attrDir, pc, 0, mod)...)
	}
	for _, c := range d.Children {
		s := shorts[c]
		if s.lfn {
			b = append(b, lfnEntriesFor(c.Name, lfnChecksum(s.name))...)
		}
		attr, size := byte(attrArchive), uint32(c.Size)
		if c.Dir {
			attr, size = attrDir, 0
		}
		b = append(b, shortEntry(s.name, attr, c.cluster, size, c.ModTime)...)
	}
	full := d.clusters * int64(clusterSize)
	return append(b, make([]byte, full-int64(len(b)))...)
}

func shortEntry(name [11]byte, attr byte, cluster uint32, size uint32, mod time.Time) []byte {
	e := make([]byte, dirEntrySize)
	copy(e, name[:])
	if e[0] == 0xE5 {
		e[0] = 0x05 // 0xE5 marks a deleted entry
	}
	e[11] = attr
	date, tm := fatTime(mod)
	binary.LittleEndian.PutUint16(e[14:], tm)
	binary.LittleEndian.PutUint16(e[16:], date)
	binary.LittleEndian.PutUint16(e[18:], date)
	binary.LittleEndian.PutUint16(e[20:], uint16(cluster>>16))
	binary.LittleEndian.PutUint16(e[22:], tm)
	binary.LittleEndian.PutUint16(e[24:], date)
	binary.LittleEndian.PutUint16(e[26:], uint16(cluster))
	binary.LittleEndian.PutUint32(e[28:], size)
	return e
}

// lfnEntriesFor encodes name as long-name entries, last part first, as they
// appear on disk before the short entry.
func lfnEntriesFor(name string, sum byte) []byte {
	u := utf16.Encode([]rune(name))
	n := (len(u) + 12) / 13
	padded := make([]uint16, n*13)
	for i := range padded {
		switch {
		case i < len(u):
			padded[i] = u[i]
		case i == len(u):
			padded[i] = 0
		default:
			padded[i] = 0xFFFF
		}
	}
	slots := [13]int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30}
	var b []byte
	for seq := n; seq >= 1; seq-- {
		e := make([]byte, dirEntrySize)
		e[0] = byte(seq)
		if seq == n {
			e[0] |= 0x40
		}
		e[11] = attrLFN
		e[13] = sum
		for i, off := range slots {
			binary.LittleEndian.PutUint16(e[off:], padded[(seq-1)*13+i])
		}
		b = append(b, e...)
	}
	return b
}

// fatTime encodes t as FAT date and time fields. FAT keeps local time with
// no zone, so the time is kept as the ISO recorded it. Dates outside FAT's
// range of 1980 to 2107 are clamped to its ends.
func fatTime(t time.Time) (date, tm uint16) {
	if t.IsZero() {
		t = time.Now()
	}
	switch {
	case t.Year() < 1980:
		return 1<<5 | 1, 0
	case t.Year() > 2107:
		return 127<<9 | 12<<5 | 31, 23<<11 | 59<<5 | 29
	}
	date = uint16((t.Year()-1980)<<9 | int(t.Month())<<5 | t.Day())
	tm = uint16(t.Hour()<<11 | t.Minute()<<5 | t.Second()/2)
	return date, tm
}
