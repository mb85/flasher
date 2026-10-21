// Package media holds the pure-Go pieces for building boot media file by
// file: the MBR, a write-once FAT32 filesystem, an empty ext4 filesystem
// and WIM splitting.
package media

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
)

// WIM on-disk layout, as documented by wimlib (include/wimlib/header.h and
// resource.h). All integers are little-endian.
const (
	wimHeaderSize   = 208
	wimReshdrSize   = 24
	wimBlobDescSize = 50 // reshdr + part number (2) + refcount (4) + SHA-1 (20)

	wimVersionDefault = 0x10d00

	hdrFlagReadOnly    = 0x00000004
	hdrFlagSpanned     = 0x00000008
	hdrFlagRPFix       = 0x00000080
	hdrFlagCompression = 0x00000002
	hdrFlagCompressAll = 0x00000002 | 0x00010000 | 0x00020000 | 0x00040000 | 0x00080000 | 0x00200000

	reshdrFlagFree     = 0x01
	reshdrFlagMetadata = 0x02
	reshdrFlagSpanned  = 0x08
	reshdrFlagSolid    = 0x10
)

var wimMagic = []byte("MSWIM\x00\x00\x00")

// FAT32 cannot hold a file of 4 GiB or more.
const FAT32MaxFile = 1<<32 - 1

// DefaultPartSize leaves headroom under the FAT32 limit for each part's
// header, blob table and XML, which are not counted against the part size.
const DefaultPartSize = 3800 << 20

type reshdr struct {
	SizeInWIM        uint64 // 56 bits on disk
	Flags            uint8
	Offset           uint64
	UncompressedSize uint64
}

func parseReshdr(b []byte) reshdr {
	var size [8]byte
	copy(size[:7], b[:7])
	return reshdr{
		SizeInWIM:        binary.LittleEndian.Uint64(size[:]),
		Flags:            b[7],
		Offset:           binary.LittleEndian.Uint64(b[8:]),
		UncompressedSize: binary.LittleEndian.Uint64(b[16:]),
	}
}

func (r reshdr) put(b []byte) {
	var size [8]byte
	binary.LittleEndian.PutUint64(size[:], r.SizeInWIM)
	copy(b[:7], size[:7])
	b[7] = r.Flags
	binary.LittleEndian.PutUint64(b[8:], r.Offset)
	binary.LittleEndian.PutUint64(b[16:], r.UncompressedSize)
}

type blobDesc struct {
	Res    reshdr
	RefCnt uint32
	Hash   [20]byte
}

// WIMInfo is the parsed header and blob table of a non-split WIM.
type WIMInfo struct {
	header []byte // raw 208-byte header, used as the template for parts
	Flags  uint32
	Images uint32
	xml    reshdr
	blobs  []blobDesc // metadata blobs and file blobs, in on-disk order
}

// maxBlobTable caps the blob table read into memory. Real tables are a few
// megabytes (about 2 MB for Windows 11 with eleven editions).
const maxBlobTable = 256 << 20

// ReadWIM parses the header and blob table of the size-byte WIM in r. Every
// size and offset taken from the file is checked against size, so a damaged
// or hostile image cannot trigger huge allocations or reads past its end.
func ReadWIM(r io.ReaderAt, size int64) (*WIMInfo, error) {
	hdr := make([]byte, wimHeaderSize)
	if _, err := r.ReadAt(hdr, 0); err != nil {
		return nil, fmt.Errorf("read WIM header: %w", err)
	}
	if !bytes.Equal(hdr[:8], wimMagic) {
		return nil, errors.New("not a WIM file")
	}
	if n := binary.LittleEndian.Uint32(hdr[8:]); n != wimHeaderSize {
		return nil, fmt.Errorf("unsupported WIM header size %d", n)
	}
	w := &WIMInfo{
		header: hdr,
		Flags:  binary.LittleEndian.Uint32(hdr[0x10:]),
		Images: binary.LittleEndian.Uint32(hdr[0x2c:]),
		xml:    parseReshdr(hdr[0x48:]),
	}
	if w.Flags&hdrFlagSpanned != 0 || binary.LittleEndian.Uint16(hdr[0x2a:]) != 1 {
		return nil, errors.New("WIM is already split")
	}

	table := parseReshdr(hdr[0x30:])
	if table.Flags&^reshdrFlagMetadata != 0 || table.SizeInWIM != table.UncompressedSize {
		return nil, errors.New("compressed WIM blob tables are not supported")
	}
	if !within(table, size) || table.SizeInWIM > maxBlobTable {
		return nil, fmt.Errorf("WIM blob table (%d bytes at %d) is out of range", table.SizeInWIM, table.Offset)
	}
	if !within(w.xml, size) || w.xml.SizeInWIM > FAT32MaxFile {
		return nil, errors.New("WIM XML data is out of range")
	}
	if table.SizeInWIM%wimBlobDescSize != 0 {
		return nil, fmt.Errorf("WIM blob table size %d is not a multiple of %d", table.SizeInWIM, wimBlobDescSize)
	}
	buf := make([]byte, table.SizeInWIM)
	if _, err := r.ReadAt(buf, int64(table.Offset)); err != nil {
		return nil, fmt.Errorf("read WIM blob table: %w", err)
	}
	for b := buf; len(b) > 0; b = b[wimBlobDescSize:] {
		d := blobDesc{Res: parseReshdr(b), RefCnt: binary.LittleEndian.Uint32(b[26:])}
		copy(d.Hash[:], b[30:50])
		switch {
		case d.Res.Flags&reshdrFlagFree != 0:
			continue
		case d.Res.Flags&reshdrFlagSolid != 0:
			// wimlib refuses to split solid WIMs as well.
			return nil, errors.New("WIM uses solid compression and cannot be split")
		case d.Res.Flags&reshdrFlagSpanned != 0:
			return nil, errors.New("WIM contains spanned resources")
		case !within(d.Res, size):
			return nil, fmt.Errorf("WIM resource at %d (%d bytes) lies outside the file", d.Res.Offset, d.Res.SizeInWIM)
		}
		w.blobs = append(w.blobs, d)
	}
	slices.SortStableFunc(w.blobs, func(a, b blobDesc) int {
		// Metadata resources first (part 1 needs them), then everything
		// else in on-disk order so the source is read sequentially.
		am, bm := a.Res.Flags&reshdrFlagMetadata != 0, b.Res.Flags&reshdrFlagMetadata != 0
		if am != bm {
			if am {
				return -1
			}
			return 1
		}
		return cmpU64(a.Res.Offset, b.Res.Offset)
	})
	return w, nil
}

// within reports whether a resource lies inside a file of the given size.
func within(r reshdr, size int64) bool {
	return r.Offset <= uint64(size) && r.SizeInWIM <= uint64(size)-r.Offset
}

func cmpU64(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// SWMPart is one file of a split WIM, described as a sequence of in-memory
// chunks and ranges of the source WIM, so it can be streamed to its
// destination without staging a copy on disk.
type SWMPart struct {
	Name   string // install.swm, install2.swm, ...
	Size   int64
	chunks []chunk
}

type chunk struct {
	data   []byte // in-memory bytes, or nil for a source range
	offset int64  // source offset when data is nil
	length int64
}

// Open returns a reader for the part's bytes, pulling resource data from src.
func (p *SWMPart) Open(src io.ReaderAt) io.Reader {
	rs := make([]io.Reader, len(p.chunks))
	for i, c := range p.chunks {
		if c.data != nil {
			rs[i] = bytes.NewReader(c.data)
		} else {
			rs[i] = io.NewSectionReader(src, c.offset, c.length)
		}
	}
	return io.MultiReader(rs...)
}

// Split plans how to split the WIM into parts whose resource data is at most
// partSize bytes each, following wimlib's algorithm: metadata resources and
// the XML go in part 1, and every part carries its own blob table and a copy
// of the XML. baseName "install" yields install.swm, install2.swm, and so on.
func (w *WIMInfo) Split(baseName string, partSize int64) ([]*SWMPart, error) {
	type group struct {
		blobs []blobDesc
		size  int64
	}
	groups := []*group{{}}
	for _, b := range w.blobs {
		cur := groups[len(groups)-1]
		size := int64(b.Res.SizeInWIM)
		isMeta := b.Res.Flags&reshdrFlagMetadata != 0
		if cur.size+size >= partSize && !isMeta && cur.size != 0 {
			cur = &group{}
			groups = append(groups, cur)
		}
		cur.blobs = append(cur.blobs, b)
		cur.size += size
	}

	var guid [16]byte
	if _, err := rand.Read(guid[:]); err != nil {
		return nil, err
	}
	xmlLen := int64(w.xml.SizeInWIM)

	parts := make([]*SWMPart, len(groups))
	for i, g := range groups {
		name := baseName + ".swm"
		if i > 0 {
			name = fmt.Sprintf("%s%d.swm", baseName, i+1)
		}
		part := &SWMPart{Name: name}
		partNo := uint16(i + 1)

		// Resources are copied verbatim, so only their offsets change.
		pos := int64(wimHeaderSize)
		table := make([]byte, 0, len(g.blobs)*wimBlobDescSize)
		var data []chunk
		var meta []reshdr
		for _, b := range g.blobs {
			res := b.Res
			res.Offset = uint64(pos)
			if res.Flags&reshdrFlagMetadata != 0 {
				meta = append(meta, res)
			}
			e := make([]byte, wimBlobDescSize)
			res.put(e)
			binary.LittleEndian.PutUint16(e[24:], partNo)
			binary.LittleEndian.PutUint32(e[26:], b.RefCnt)
			copy(e[30:], b.Hash[:])
			table = append(table, e...)
			data = append(data, chunk{offset: int64(b.Res.Offset), length: int64(b.Res.SizeInWIM)})
			pos += int64(b.Res.SizeInWIM)
		}
		if len(meta) != 0 && i != 0 {
			return nil, errors.New("internal error: metadata resource outside part 1")
		}
		tableRes := reshdr{SizeInWIM: uint64(len(table)), Flags: reshdrFlagMetadata, Offset: uint64(pos), UncompressedSize: uint64(len(table))}
		pos += int64(len(table))
		xmlRes := w.xml
		xmlRes.Offset = uint64(pos)
		pos += xmlLen

		hdr := slices.Clone(w.header)
		binary.LittleEndian.PutUint32(hdr[0x0c:], wimVersionDefault)
		flags := w.Flags&(hdrFlagRPFix|hdrFlagReadOnly|hdrFlagCompressAll) | hdrFlagSpanned
		binary.LittleEndian.PutUint32(hdr[0x10:], flags)
		copy(hdr[0x18:0x28], guid[:])
		binary.LittleEndian.PutUint16(hdr[0x28:], partNo)
		binary.LittleEndian.PutUint16(hdr[0x2a:], uint16(len(groups)))
		tableRes.put(hdr[0x30:])
		xmlRes.put(hdr[0x48:])
		clear(hdr[0x60:0x78])                        // boot metadata: none in split WIMs
		binary.LittleEndian.PutUint32(hdr[0x78:], 0) // boot index
		clear(hdr[0x7c:0x94])                        // no integrity table
		clear(hdr[0x94:wimHeaderSize])               // unused

		part.chunks = append(part.chunks, chunk{data: hdr, length: wimHeaderSize})
		part.chunks = append(part.chunks, data...)
		part.chunks = append(part.chunks, chunk{data: table, length: int64(len(table))})
		part.chunks = append(part.chunks, chunk{offset: int64(w.xml.Offset), length: xmlLen})
		part.Size = pos
		if part.Size > FAT32MaxFile {
			return nil, fmt.Errorf("%s would be %d bytes, too large for FAT32 (a single resource exceeds the part size)", name, part.Size)
		}
		parts[i] = part
	}
	return parts, nil
}
