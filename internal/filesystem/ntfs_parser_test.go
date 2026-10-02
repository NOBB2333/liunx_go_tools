package filesystem

import (
	"encoding/binary"
	"reflect"
	"testing"
	"unicode/utf16"
)

func TestParseNTFSRunlist(t *testing.T) {
	runs, err := parseNTFSRunlist([]byte{0x11, 0x03, 0x64, 0x11, 0x02, 0xf6, 0x01, 0x04, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	want := []ntfsRun{
		{VcnStart: 0, LcnStart: 100, Clusters: 3},
		{VcnStart: 3, LcnStart: 90, Clusters: 2},
		{VcnStart: 5, LcnStart: -1, Clusters: 4},
	}
	if !reflect.DeepEqual(runs, want) {
		t.Fatalf("runs = %#v, want %#v", runs, want)
	}
	if _, err := parseNTFSRunlist([]byte{0x21, 0x01}); err == nil {
		t.Fatal("truncated runlist was accepted")
	}
}

func TestParseNTFSRecord(t *testing.T) {
	record := syntheticNTFSRecord(t, "SHORT~1.TXT", "报告.txt", 42, 987654321, 4096)
	meta, ok := parseNTFSRecord(record)
	if !ok {
		t.Fatal("record was not parsed")
	}
	if got := decodeNTFSName(record, meta); got != "报告.txt" {
		t.Fatalf("name = %q", got)
	}
	if meta.parent != 42 || meta.logical != 987654321 || meta.allocated != 4096 {
		t.Fatalf("metadata = %+v", meta)
	}
	if meta.btimeNS != 100 || meta.mtimeNS != 200 || meta.ctimeNS != 300 {
		t.Fatalf("times = birth %d modify %d metadata %d", meta.btimeNS, meta.mtimeNS, meta.ctimeNS)
	}
	if meta.mode != 0x20 {
		t.Fatalf("attributes = %#x", meta.mode)
	}
}

func TestNTFSFixupRejectsCorruption(t *testing.T) {
	record := syntheticNTFSRecord(t, "FILE.TXT", "file.txt", 5, 1, 4096)
	record[510] ^= 0xff
	if fixupNTFSRecord(record) {
		t.Fatal("corrupt update sequence was accepted")
	}
}

func syntheticNTFSRecord(t *testing.T, dosName, win32Name string, parent, logical, allocated uint64) []byte {
	t.Helper()
	record := make([]byte, 1024)
	copy(record[:4], "FILE")
	binary.LittleEndian.PutUint16(record[4:6], 0x30)
	binary.LittleEndian.PutUint16(record[6:8], 3)
	binary.LittleEndian.PutUint16(record[20:22], 0x38)
	binary.LittleEndian.PutUint16(record[22:24], ntfsFileRecordActive)
	pos := 0x38

	standard := make([]byte, 72)
	const epoch = uint64(116444736000000000)
	binary.LittleEndian.PutUint64(standard[0:8], epoch+1)
	binary.LittleEndian.PutUint64(standard[8:16], epoch+2)
	binary.LittleEndian.PutUint64(standard[16:24], epoch+3)
	binary.LittleEndian.PutUint64(standard[24:32], epoch+4)
	binary.LittleEndian.PutUint32(standard[32:36], 0x20)
	pos = appendResidentAttribute(record, pos, ntfsAttrStandard, standard)
	pos = appendResidentAttribute(record, pos, ntfsAttrFileName, syntheticFileNameValue(dosName, parent, 2))
	pos = appendResidentAttribute(record, pos, ntfsAttrFileName, syntheticFileNameValue(win32Name, parent, 1))

	const nonResidentLength = 64
	binary.LittleEndian.PutUint32(record[pos:pos+4], ntfsAttrData)
	binary.LittleEndian.PutUint32(record[pos+4:pos+8], nonResidentLength)
	record[pos+8] = 1
	record[pos+9] = 0
	binary.LittleEndian.PutUint64(record[pos+40:pos+48], allocated)
	binary.LittleEndian.PutUint64(record[pos+48:pos+56], logical)
	pos += nonResidentLength
	binary.LittleEndian.PutUint32(record[pos:pos+4], 0xffffffff)
	pos += 8
	binary.LittleEndian.PutUint32(record[24:28], uint32(pos))
	binary.LittleEndian.PutUint32(record[28:32], uint32(len(record)))

	record[0x30], record[0x31] = 0xaa, 0xbb
	record[0x32], record[0x33] = record[510], record[511]
	record[0x34], record[0x35] = record[1022], record[1023]
	record[510], record[511] = 0xaa, 0xbb
	record[1022], record[1023] = 0xaa, 0xbb
	return record
}

func appendResidentAttribute(record []byte, pos int, kind uint32, value []byte) int {
	length := (24 + len(value) + 7) &^ 7
	binary.LittleEndian.PutUint32(record[pos:pos+4], kind)
	binary.LittleEndian.PutUint32(record[pos+4:pos+8], uint32(length))
	binary.LittleEndian.PutUint32(record[pos+16:pos+20], uint32(len(value)))
	binary.LittleEndian.PutUint16(record[pos+20:pos+22], 24)
	copy(record[pos+24:pos+24+len(value)], value)
	return pos + length
}

func syntheticFileNameValue(name string, parent uint64, namespace byte) []byte {
	units := utf16.Encode([]rune(name))
	value := make([]byte, 66+len(units)*2)
	binary.LittleEndian.PutUint64(value[0:8], parent)
	value[64] = byte(len(units))
	value[65] = namespace
	for i, unit := range units {
		binary.LittleEndian.PutUint16(value[66+i*2:66+i*2+2], unit)
	}
	return value
}
