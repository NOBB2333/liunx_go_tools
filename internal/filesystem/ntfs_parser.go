package filesystem

import (
	"encoding/binary"
	"errors"
	"unicode/utf16"
)

const (
	ntfsFileRecordActive = 0x0001
	ntfsFileRecordDir    = 0x0002
	ntfsAttrStandard     = 0x10
	ntfsAttrFileName     = 0x30
	ntfsAttrData         = 0x80
	ntfsRootRecord       = 5
)

type ntfsRun struct {
	VcnStart int64
	LcnStart int64
	Clusters int64
}

func parseNTFSRunlist(value []byte) ([]ntfsRun, error) {
	var runs []ntfsRun
	var vcn, lcn int64
	for pos := 0; pos < len(value); {
		header := value[pos]
		pos++
		if header == 0 {
			break
		}
		lengthBytes, offsetBytes := int(header&0x0f), int(header>>4)
		if lengthBytes == 0 || pos+lengthBytes+offsetBytes > len(value) || lengthBytes > 8 || offsetBytes > 8 {
			return nil, errors.New("invalid NTFS runlist")
		}
		clusters := decodeUnsignedLE(value[pos : pos+lengthBytes])
		pos += lengthBytes
		if clusters <= 0 {
			return nil, errors.New("invalid NTFS run length")
		}
		if offsetBytes == 0 {
			runs = append(runs, ntfsRun{VcnStart: vcn, LcnStart: -1, Clusters: clusters})
			vcn += clusters
			continue
		}
		delta := decodeSignedLE(value[pos : pos+offsetBytes])
		pos += offsetBytes
		lcn += delta
		if lcn < 0 {
			return nil, errors.New("invalid NTFS run LCN")
		}
		runs = append(runs, ntfsRun{VcnStart: vcn, LcnStart: lcn, Clusters: clusters})
		vcn += clusters
	}
	if len(runs) == 0 {
		return nil, errors.New("empty NTFS runlist")
	}
	return runs, nil
}

func decodeUnsignedLE(value []byte) int64 {
	var raw uint64
	for i := len(value) - 1; i >= 0; i-- {
		raw = (raw << 8) | uint64(value[i])
	}
	return int64(raw)
}

func decodeSignedLE(value []byte) int64 {
	result := decodeUnsignedLE(value)
	if len(value) > 0 && value[len(value)-1]&0x80 != 0 {
		result |= int64(^uint64(0) << (uint(len(value)) * 8))
	}
	return result
}

func fixupNTFSRecord(record []byte) bool {
	return fixupNTFSRecordWithSector(record, 512)
}

func fixupNTFSRecordWithSector(record []byte, sectorSize uint32) bool {
	if len(record) < 24 || string(record[:4]) != "FILE" {
		return false
	}
	if sectorSize < 2 {
		return false
	}
	usaOffset := int(binary.LittleEndian.Uint16(record[4:6]))
	usaCount := int(binary.LittleEndian.Uint16(record[6:8]))
	if usaOffset < 8 || usaCount < 2 || usaOffset+usaCount*2 > len(record) {
		return false
	}
	stride := int(sectorSize)
	// NTFS volumes with 4K physical sectors can still use 512-byte logical
	// update-sequence boundaries for 1K MFT records. Fall back to that layout
	// when the reported sector size cannot fit the USA entries in this record.
	if stride*(usaCount-1) > len(record) {
		stride = 512
	}
	magic := record[usaOffset : usaOffset+2]
	for i := 1; i < usaCount; i++ {
		end := i*stride - 2
		if end < 0 || end+2 > len(record) || record[end] != magic[0] || record[end+1] != magic[1] {
			return false
		}
		source := usaOffset + i*2
		record[end], record[end+1] = record[source], record[source+1]
	}
	return true
}

type ntfsMeta struct {
	parent    uint64
	logical   uint64
	allocated uint64
	mtimeNS   int64
	ctimeNS   int64
	btimeNS   int64
	name      uint32
	nameLen   uint8
	namespace uint8
	flags     uint16
	mode      uint32
	fileCount uint32
	dirCount  uint32
}

func parseNTFSRecord(record []byte) (ntfsMeta, bool) {
	return parseNTFSRecordWithSector(record, 512)
}

func parseNTFSRecordWithSector(record []byte, sectorSize uint32) (ntfsMeta, bool) {
	var meta ntfsMeta
	if !fixupNTFSRecordWithSector(record, sectorSize) || len(record) < 32 {
		return meta, false
	}
	meta.flags = binary.LittleEndian.Uint16(record[22:24])
	if meta.flags&ntfsFileRecordActive == 0 {
		return meta, false
	}
	attrOffset := int(binary.LittleEndian.Uint16(record[20:22]))
	used := int(binary.LittleEndian.Uint32(record[24:28]))
	if attrOffset < 0x20 || used > len(record) || used <= attrOffset {
		return meta, false
	}
	bestNamespace := uint8(0xff)
	for pos := attrOffset; pos+16 <= used; {
		kind := binary.LittleEndian.Uint32(record[pos : pos+4])
		length := int(binary.LittleEndian.Uint32(record[pos+4 : pos+8]))
		if kind == 0xffffffff || length < 16 || pos+length > used {
			break
		}
		switch {
		case kind == ntfsAttrStandard && record[pos+8] == 0:
			valueOffset := int(binary.LittleEndian.Uint16(record[pos+20 : pos+22]))
			valueLength := int(binary.LittleEndian.Uint32(record[pos+16 : pos+20]))
			if valueOffset >= 0 && valueOffset+valueLength <= length && valueLength >= 36 {
				value := record[pos+valueOffset : pos+valueOffset+valueLength]
				meta.btimeNS = ntfsFiletimeNS(binary.LittleEndian.Uint64(value[0:8]))
				meta.mtimeNS = ntfsFiletimeNS(binary.LittleEndian.Uint64(value[8:16]))
				meta.ctimeNS = ntfsFiletimeNS(binary.LittleEndian.Uint64(value[16:24]))
				meta.mode = binary.LittleEndian.Uint32(value[32:36])
			}
		case kind == ntfsAttrFileName && record[pos+8] == 0:
			valueOffset := int(binary.LittleEndian.Uint16(record[pos+20 : pos+22]))
			valueLength := int(binary.LittleEndian.Uint32(record[pos+16 : pos+20]))
			if valueOffset >= 0 && valueOffset+valueLength <= length && valueLength >= 66 {
				value := record[pos+valueOffset : pos+valueOffset+valueLength]
				parent := binary.LittleEndian.Uint64(value[0:8]) & 0x0000ffffffffffff
				nameLen, namespace := value[64], value[65]
				if 66+int(nameLen)*2 <= len(value) && (bestNamespace == 0xff || ntfsNamespaceRank(namespace) < ntfsNamespaceRank(bestNamespace)) {
					meta.parent = parent
					meta.name = uint32(pos + valueOffset + 66)
					meta.nameLen = nameLen
					meta.namespace = namespace
					bestNamespace = namespace
				}
			}
		case kind == ntfsAttrData && record[pos+9] == 0 && record[pos+8] == 0:
			valueLength := int(binary.LittleEndian.Uint32(record[pos+16 : pos+20]))
			valueOffset := int(binary.LittleEndian.Uint16(record[pos+20 : pos+22]))
			if valueOffset >= 0 && valueOffset+valueLength <= length {
				meta.logical = uint64(valueLength)
			}
		case kind == ntfsAttrData && record[pos+9] == 0 && record[pos+8] != 0 && length >= 56:
			meta.allocated = binary.LittleEndian.Uint64(record[pos+40 : pos+48])
			meta.logical = binary.LittleEndian.Uint64(record[pos+48 : pos+56])
		}
		pos += length
	}
	return meta, meta.nameLen > 0 && meta.parent != 0
}

func ntfsNamespaceRank(namespace uint8) int {
	switch namespace {
	case 1:
		return 0
	case 3:
		return 1
	case 0:
		return 2
	default:
		return 3
	}
}

func ntfsFiletimeNS(value uint64) int64 {
	const unixEpoch100ns = 116444736000000000
	if value <= unixEpoch100ns {
		return 0
	}
	return int64(value-unixEpoch100ns) * 100
}

func decodeNTFSName(record []byte, meta ntfsMeta) string {
	start := int(meta.name)
	end := start + int(meta.nameLen)*2
	if start < 0 || end > len(record) {
		return ""
	}
	units := make([]uint16, meta.nameLen)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(record[start+i*2 : start+i*2+2])
	}
	return string(utf16.Decode(units))
}
