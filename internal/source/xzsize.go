package source

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"io"
)

// xzSize reads the uncompressed size of an xz file from the indexes at the
// end of its streams, without decompressing anything. It returns -1 when
// the file does not parse as expected.
//
// Layout (from the xz file format specification): a file is one or more
// streams, each optionally followed by zero padding in multiples of four
// bytes. A stream ends with an index, listing every block's unpadded and
// uncompressed size, and a 12-byte footer giving the index's size.
func xzSize(r io.ReaderAt, size int64) int64 {
	var total int64
	end := size
	for end > 0 {
		// Skip stream padding.
		var word [4]byte
		for end >= 4 {
			if _, err := r.ReadAt(word[:], end-4); err != nil {
				return -1
			}
			if word != [4]byte{} {
				break
			}
			end -= 4
		}
		if end < 24 { // the smallest stream is a 12-byte header and footer
			return -1
		}
		footer := make([]byte, 12)
		if _, err := r.ReadAt(footer, end-12); err != nil {
			return -1
		}
		if footer[10] != 'Y' || footer[11] != 'Z' || crc32.ChecksumIEEE(footer[4:10]) != binary.LittleEndian.Uint32(footer) {
			return -1
		}
		indexSize := (int64(binary.LittleEndian.Uint32(footer[4:])) + 1) * 4
		indexStart := end - 12 - indexSize
		if indexStart < 12 {
			return -1
		}
		index := make([]byte, indexSize)
		if _, err := r.ReadAt(index, indexStart); err != nil {
			return -1
		}
		if index[0] != 0 || crc32.ChecksumIEEE(index[:indexSize-4]) != binary.LittleEndian.Uint32(index[indexSize-4:]) {
			return -1
		}
		br := bytes.NewReader(index[1 : indexSize-4])
		records, err := binary.ReadUvarint(br)
		if err != nil {
			return -1
		}
		var blocks int64
		for range records {
			unpadded, err1 := binary.ReadUvarint(br)
			uncompressed, err2 := binary.ReadUvarint(br)
			if err1 != nil || err2 != nil || unpadded > 1<<62 || uncompressed > 1<<62 {
				return -1
			}
			blocks += (int64(unpadded) + 3) &^ 3
			total += int64(uncompressed)
		}
		end = indexStart - blocks - 12 // back over the blocks and the stream header
		if end < 0 {
			return -1
		}
	}
	return total
}
