package filesystem

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
)

// segmentReader is an internal scan-finalization reader. Segment files never
// form part of a published snapshot; they are consumed and packed into GTI.
type segmentReader struct {
	reader  *bufio.Reader
	version uint16
}

func newSegmentReader(path string, kind uint16) (*segmentReader, *os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	version, err := readSegmentHeader(file, kind)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return &segmentReader{reader: bufio.NewReaderSize(file, 8<<20), version: version}, file, nil
}

func (r *segmentReader) readRecord(headerSize, nameOffset int) ([]byte, string, error) {
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(r.reader, header); err != nil {
		return nil, "", err
	}
	nameLen := int(binary.LittleEndian.Uint32(header[nameOffset : nameOffset+4]))
	if nameLen < 0 || nameLen > 1<<20 {
		return nil, "", fmt.Errorf("invalid segment name length: %d", nameLen)
	}
	name := make([]byte, nameLen)
	if _, err := io.ReadFull(r.reader, name); err != nil {
		return nil, "", err
	}
	return header, string(name), nil
}

func fileCTime(header []byte) int64 {
	if len(header) < 56 {
		return 0
	}
	return int64(binary.LittleEndian.Uint64(header[48:56]))
}

func fileBirthTime(header []byte) int64 {
	if len(header) < 64 {
		return 0
	}
	return int64(binary.LittleEndian.Uint64(header[56:64]))
}

func fileMode(header []byte) uint32 {
	if len(header) >= 72 {
		return binary.LittleEndian.Uint32(header[64:68])
	}
	return 0
}

func fileExtension(name string) string {
	index := strings.LastIndexByte(name, '.')
	if index <= 0 || index == len(name)-1 {
		return ""
	}
	return strings.ToLower(name[index:])
}
