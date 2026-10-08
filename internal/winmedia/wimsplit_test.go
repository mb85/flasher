package winmedia

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"io"
	"testing"
)

type testBlob struct {
	data []byte
	meta bool
}

// buildWIM lays out a minimal WIM: header, resources, blob table, XML.
func buildWIM(t *testing.T, blobs []testBlob, xml []byte) []byte {
	t.Helper()
	var body bytes.Buffer
	body.Write(make([]byte, wimHeaderSize))
	var table []byte
	for i, b := range blobs {
		res := reshdr{SizeInWIM: uint64(len(b.data)), Offset: uint64(body.Len()), UncompressedSize: uint64(len(b.data))}
		if b.meta {
			res.Flags = reshdrFlagMetadata
		}
		e := make([]byte, wimBlobDescSize)
		res.put(e)
		binary.LittleEndian.PutUint16(e[24:], 1)
		binary.LittleEndian.PutUint32(e[26:], 1)
		e[30] = byte(i)
		table = append(table, e...)
		body.Write(b.data)
	}
	tableRes := reshdr{SizeInWIM: uint64(len(table)), Flags: reshdrFlagMetadata, Offset: uint64(body.Len()), UncompressedSize: uint64(len(table))}
	body.Write(table)
	xmlRes := reshdr{SizeInWIM: uint64(len(xml)), Flags: reshdrFlagMetadata, Offset: uint64(body.Len()), UncompressedSize: uint64(len(xml))}
	body.Write(xml)

	b := body.Bytes()
	copy(b, wimMagic)
	binary.LittleEndian.PutUint32(b[0x08:], wimHeaderSize)
	binary.LittleEndian.PutUint32(b[0x0c:], wimVersionDefault)
	binary.LittleEndian.PutUint32(b[0x10:], hdrFlagCompression|0x00040000|hdrFlagRPFix)
	binary.LittleEndian.PutUint16(b[0x28:], 1)
	binary.LittleEndian.PutUint16(b[0x2a:], 1)
	binary.LittleEndian.PutUint32(b[0x2c:], 1)
	tableRes.put(b[0x30:])
	xmlRes.put(b[0x48:])
	binary.LittleEndian.PutUint32(b[0x78:], 1)
	return b
}

func randBlob(n int, meta bool) testBlob {
	b := make([]byte, n)
	rand.Read(b)
	return testBlob{data: b, meta: meta}
}

func TestSplitLayout(t *testing.T) {
	blobs := []testBlob{randBlob(300, false), randBlob(500, true), randBlob(400, false), randBlob(400, false), randBlob(450, false)}
	xml := []byte("<\x00W\x00I\x00M\x00>\x00")
	wim := buildWIM(t, blobs, xml)
	w, err := ReadWIM(bytes.NewReader(wim), int64(len(wim)))
	if err != nil {
		t.Fatal(err)
	}
	parts, err := w.Split("install", 900)
	if err != nil {
		t.Fatal(err)
	}
	// Part 1 holds the metadata (500) and then 300. Part 2 takes 400 and
	// 400, and the final 450 would push it past 900, so it starts part 3.
	if len(parts) != 3 || parts[0].Name != "install.swm" || parts[2].Name != "install3.swm" {
		t.Fatalf("got %d parts", len(parts))
	}

	seen := map[byte][]byte{}
	var guid []byte
	for i, p := range parts {
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, p.Open(bytes.NewReader(wim))); err != nil {
			t.Fatal(err)
		}
		b := buf.Bytes()
		if int64(len(b)) != p.Size {
			t.Fatalf("%s: %d bytes, planned %d", p.Name, len(b), p.Size)
		}
		flags := binary.LittleEndian.Uint32(b[0x10:])
		if flags&hdrFlagSpanned == 0 || binary.LittleEndian.Uint16(b[0x28:]) != uint16(i+1) || binary.LittleEndian.Uint16(b[0x2a:]) != 3 {
			t.Fatalf("%s: bad header flags or part numbers", p.Name)
		}
		if binary.LittleEndian.Uint32(b[0x78:]) != 0 {
			t.Fatalf("%s: boot index should be cleared", p.Name)
		}
		if guid == nil {
			guid = b[0x18:0x28]
		} else if !bytes.Equal(guid, b[0x18:0x28]) {
			t.Fatalf("%s: GUID differs between parts", p.Name)
		}
		xr := parseReshdr(b[0x48:])
		if !bytes.Equal(b[xr.Offset:xr.Offset+xr.SizeInWIM], xml) {
			t.Fatalf("%s: XML not copied", p.Name)
		}
		tr := parseReshdr(b[0x30:])
		for e := b[tr.Offset : tr.Offset+tr.SizeInWIM]; len(e) > 0; e = e[wimBlobDescSize:] {
			r := parseReshdr(e)
			if binary.LittleEndian.Uint16(e[24:]) != uint16(i+1) {
				t.Fatalf("%s: blob entry has the wrong part number", p.Name)
			}
			if r.Flags&reshdrFlagMetadata != 0 && i != 0 {
				t.Fatal("metadata outside part 1")
			}
			seen[e[30]] = b[r.Offset : r.Offset+r.SizeInWIM]
		}
	}
	for i, bl := range blobs {
		if !bytes.Equal(seen[byte(i)], bl.data) {
			t.Fatalf("blob %d missing or altered", i)
		}
	}
}

func TestReadWIMRejectsBadSizes(t *testing.T) {
	wim := buildWIM(t, []testBlob{randBlob(100, true)}, []byte("x\x00"))
	cases := map[string]func(b []byte){
		"huge blob table": func(b []byte) {
			reshdr{SizeInWIM: 1 << 50, Flags: reshdrFlagMetadata, Offset: 208, UncompressedSize: 1 << 50}.put(b[0x30:])
		},
		"table past end": func(b []byte) {
			r := parseReshdr(b[0x30:])
			r.Offset = uint64(len(b)) - 10
			r.put(b[0x30:])
		},
		"offset overflow": func(b []byte) {
			r := parseReshdr(b[0x48:])
			r.Offset = ^uint64(0) - 1
			r.put(b[0x48:])
		},
		"resource past end": func(b []byte) {
			tr := parseReshdr(b[0x30:])
			e := b[tr.Offset:]
			r := parseReshdr(e)
			r.SizeInWIM = 1 << 40
			r.put(e)
		},
	}
	for name, mutate := range cases {
		b := bytes.Clone(wim)
		mutate(b)
		if _, err := ReadWIM(bytes.NewReader(b), int64(len(b))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
