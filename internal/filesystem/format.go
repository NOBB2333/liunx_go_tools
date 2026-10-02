package filesystem

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	snapshotSchemaVersion  = 3
	segmentFormatVersion   = 2
	legacySegmentVersion   = 1
	segmentHeaderSize      = 16
	segmentKindFiles       = 1
	segmentKindDirectories = 2
)

var segmentMagic = [8]byte{'G', 'T', 'S', 'S', 'E', 'G', '0', '1'}

func writeSegmentHeader(writer io.Writer, kind uint16) error {
	var header [segmentHeaderSize]byte
	copy(header[:8], segmentMagic[:])
	binary.LittleEndian.PutUint16(header[8:10], segmentFormatVersion)
	binary.LittleEndian.PutUint16(header[10:12], kind)
	_, err := writer.Write(header[:])
	return err
}

func readSegmentHeader(reader io.ReadSeeker, expectedKind uint16) (uint16, error) {
	var header [segmentHeaderSize]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		if _, seekErr := reader.Seek(0, io.SeekStart); seekErr != nil {
			return 0, seekErr
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return 0, nil
		}
		return 0, err
	}
	if string(header[:8]) != string(segmentMagic[:]) {
		_, err := reader.Seek(0, io.SeekStart)
		return 0, err
	}
	version := binary.LittleEndian.Uint16(header[8:10])
	kind := binary.LittleEndian.Uint16(header[10:12])
	if version != legacySegmentVersion && version != segmentFormatVersion {
		return version, fmt.Errorf("unsupported segment format version: %d", version)
	}
	if kind != expectedKind {
		return version, fmt.Errorf("unexpected segment kind: got %d want %d", kind, expectedKind)
	}
	return version, nil
}
