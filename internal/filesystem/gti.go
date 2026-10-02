package filesystem

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// GTI is the portable, mmap-friendly snapshot container.  Its sections keep
// the fixed-width tree records used by the HTTP reader while eliminating the
// externally visible segment/tree/SQLite copies.
const (
	gtiVersion    = uint16(1)
	gtiHeaderSize = 32
	gtiEntrySize  = 32
	gtiMagic      = "GOLGTI01"
	gtiManifest   = 1
	gtiDirs       = 2
	gtiFiles      = 3
	gtiNames      = 4
	gtiChildren   = 5
	gtiRanges     = 6
	gtiDirIDs     = 7
	gtiExtensions = 8
	gtiErrors     = 9
)

type gtiSection struct {
	ID     uint32
	Offset uint64
	Length uint64
}

func gtiPath(snapshot string) string {
	if strings.HasSuffix(strings.ToLower(snapshot), ".gti") {
		return snapshot
	}
	return filepath.Join(snapshot, "snapshot.gti")
}

// SnapshotPath returns the canonical portable snapshot file for a directory
// or returns the input unchanged when it already points at a .gti file.
func SnapshotPath(snapshot string) string { return gtiPath(snapshot) }

func readGTISection(path string, id uint32) ([]byte, error) {
	file, sections, err := openGTIContainer(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	section, ok := sections[id]
	if !ok {
		return nil, fmt.Errorf("gti section %d is missing", id)
	}
	if section.Length > uint64(^uint(0)>>1) {
		return nil, errors.New("gti section is too large")
	}
	data := make([]byte, int(section.Length))
	if _, err := file.ReadAt(data, int64(section.Offset)); err != nil {
		return nil, err
	}
	return data, nil
}

func openGTIContainer(path string) (*os.File, map[uint32]gtiSection, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	var header [gtiHeaderSize]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if string(header[:8]) != gtiMagic {
		_ = file.Close()
		return nil, nil, errors.New("invalid gti magic")
	}
	if binary.LittleEndian.Uint16(header[8:10]) != gtiVersion {
		_ = file.Close()
		return nil, nil, fmt.Errorf("unsupported gti version: %d", binary.LittleEndian.Uint16(header[8:10]))
	}
	count := binary.LittleEndian.Uint16(header[10:12])
	tableOffset := binary.LittleEndian.Uint64(header[16:24])
	if count == 0 || tableOffset < gtiHeaderSize {
		_ = file.Close()
		return nil, nil, errors.New("invalid gti section table")
	}
	sections := make(map[uint32]gtiSection, count)
	var raw [gtiEntrySize]byte
	for i := uint16(0); i < count; i++ {
		if _, err := file.ReadAt(raw[:], int64(tableOffset)+int64(i)*gtiEntrySize); err != nil {
			_ = file.Close()
			return nil, nil, err
		}
		id := binary.LittleEndian.Uint32(raw[0:4])
		section := gtiSection{ID: id, Offset: binary.LittleEndian.Uint64(raw[8:16]), Length: binary.LittleEndian.Uint64(raw[16:24])}
		if section.Offset < gtiHeaderSize || section.Length > ^uint64(0)-section.Offset || section.Offset+section.Length > uint64(info.Size()) {
			_ = file.Close()
			return nil, nil, errors.New("invalid gti section bounds")
		}
		sections[id] = section
	}
	return file, sections, nil
}

// PackFastSnapshot publishes the fixed-width tree and metadata as one file.
// The source files are only an implementation detail and are removed after
// the container has been atomically published.
func PackFastSnapshot(snapshotDir string) error {
	snapshotDir, err := filepath.Abs(filepath.Clean(snapshotDir))
	if err != nil {
		return err
	}
	manifest, err := os.ReadFile(filepath.Join(snapshotDir, "manifest.json"))
	if err != nil {
		return err
	}
	var parsed SnapshotManifest
	if err := json.Unmarshal(manifest, &parsed); err != nil || !parsed.Complete {
		if err != nil {
			return fmt.Errorf("invalid manifest: %w", err)
		}
		return errors.New("cannot publish an incomplete snapshot")
	}
	files := []struct {
		id   uint32
		name string
	}{
		{gtiManifest, "manifest.json"}, {gtiDirs, "tree.dirs"}, {gtiFiles, "tree.files"},
		{gtiNames, "tree.names"}, {gtiChildren, "tree.children"}, {gtiRanges, "tree.ranges"},
		{gtiDirIDs, "tree.dirids"}, {gtiExtensions, "tree.extensions.json"}, {gtiErrors, "errors.ndjson"},
	}
	tmp := filepath.Join(snapshotDir, "snapshot.gti.tmp")
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	closeRemove := func() error {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err = out.Write(make([]byte, gtiHeaderSize+len(files)*gtiEntrySize)); err != nil {
		return closeRemove()
	}
	sections := make([]gtiSection, 0, len(files))
	position := uint64(gtiHeaderSize + len(files)*gtiEntrySize)
	for _, item := range files {
		if _, err = out.Seek(int64(position), io.SeekStart); err != nil {
			return closeRemove()
		}
		input, openErr := os.Open(filepath.Join(snapshotDir, item.name))
		if openErr != nil {
			if item.id == gtiErrors && errors.Is(openErr, os.ErrNotExist) {
				sections = append(sections, gtiSection{ID: item.id, Offset: position})
				continue
			}
			err = openErr
			return closeRemove()
		}
		start := position
		written, copyErr := io.Copy(out, input)
		_ = input.Close()
		if copyErr != nil {
			err = copyErr
			return closeRemove()
		}
		position += uint64(written)
		sections = append(sections, gtiSection{ID: item.id, Offset: start, Length: uint64(written)})
	}
	var header [gtiHeaderSize]byte
	copy(header[:8], gtiMagic)
	binary.LittleEndian.PutUint16(header[8:10], gtiVersion)
	binary.LittleEndian.PutUint16(header[10:12], uint16(len(sections)))
	binary.LittleEndian.PutUint64(header[16:24], gtiHeaderSize)
	if _, err = out.WriteAt(header[:], 0); err != nil {
		return closeRemove()
	}
	var raw [gtiEntrySize]byte
	for i, section := range sections {
		for j := range raw {
			raw[j] = 0
		}
		binary.LittleEndian.PutUint32(raw[0:4], section.ID)
		binary.LittleEndian.PutUint64(raw[8:16], section.Offset)
		binary.LittleEndian.PutUint64(raw[16:24], section.Length)
		if _, err = out.WriteAt(raw[:], int64(gtiHeaderSize+i*gtiEntrySize)); err != nil {
			return closeRemove()
		}
	}
	if err = out.Sync(); err != nil {
		return closeRemove()
	}
	if err = out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, filepath.Join(snapshotDir, "snapshot.gti")); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	for _, item := range files {
		_ = os.Remove(filepath.Join(snapshotDir, item.name))
		_ = os.Remove(filepath.Join(snapshotDir, item.name+".tmp"))
	}
	for _, name := range []string{"files.seg", "directories.seg", "tree.index.json"} {
		_ = os.Remove(filepath.Join(snapshotDir, name))
	}
	return nil
}
