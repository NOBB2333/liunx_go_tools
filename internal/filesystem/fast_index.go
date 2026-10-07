package filesystem

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"golang.org/x/exp/mmap"
)

// The tree index is deliberately a set of simple, little-endian files. It is
// portable between operating systems and can be opened without importing the
// records into SQLite. The files are published by writing *.tmp files and
// renaming the manifest last.
const (
	fastIndexVersion    = 2
	fastDirRecordSize   = 56
	fastFileRecordSize  = 56
	fastRangeRecordSize = 16
	fastMapRecordSize   = 16
	fastFlagDirectory   = 1
	fastInvalidParent   = ^uint32(0)
)

var fastIndexFileNames = []string{
	"tree.dirs",
	"tree.files",
	"tree.names",
	"tree.children",
	"tree.ranges",
	"tree.dirids",
	"tree.extensions.json",
	"tree.index.json",
}

type FastIndexProgress struct {
	Phase   string
	Current uint64
	Total   uint64
	Message string
}

type fastIndexManifest struct {
	Version        int    `json:"version"`
	DirRecordSize  int    `json:"dir_record_size"`
	FileRecordSize int    `json:"file_record_size"`
	DirectoryCount uint64 `json:"directory_count"`
	FileCount      uint64 `json:"file_count"`
	FileIDBase     uint64 `json:"file_id_base"`
	ChildCount     uint64 `json:"child_count"`
	RootNode       uint32 `json:"root_node"`
}

type fastIndexNode struct {
	ID        uint64
	Parent    uint32
	NameOff   uint64
	NameLen   uint32
	Flags     uint32
	Size      uint64
	Allocated uint64
	FileCount uint64
	DirCount  uint64
	MTimeNS   int64
	CTimeNS   int64
	BirthNS   int64
}

type fastIndexRange struct {
	Start uint64
	Count uint64
}

type fastIndexMapEntry struct {
	ID    uint64
	Index uint32
}

type fastIndexExtension struct {
	Extension      string `json:"extension"`
	Files          uint64 `json:"files"`
	SizeBytes      uint64 `json:"size_bytes"`
	AllocatedBytes uint64 `json:"allocated_bytes"`
}

type fastIndexBuildNode struct {
	ID        uint64
	Parent    uint32
	NameOff   uint64
	NameLen   uint32
	Flags     uint32
	Size      uint64
	Allocated uint64
	FileCount uint64
	DirCount  uint64
	MTimeNS   int64
	CTimeNS   int64
	BirthNS   int64
}

type fastIndex struct {
	dir          string
	manifest     fastIndexManifest
	container    *mmap.ReaderAt
	sections     map[uint32]gtiSection
	extensions   []fastIndexExtension
	dirIDEntries []fastIndexMapEntry
}

func BuildFastIndex(ctx context.Context, snapshotDir string, progress func(FastIndexProgress)) error {
	snapshotDir, err := filepath.Abs(filepath.Clean(snapshotDir))
	if err != nil {
		return err
	}
	manifest, err := readManifest(snapshotDir)
	if err != nil {
		return err
	}
	if !manifest.Complete {
		return errors.New("snapshot scan is not complete")
	}

	report := func(phase string, current, total uint64, message string) {
		if progress != nil {
			progress(FastIndexProgress{Phase: phase, Current: current, Total: total, Message: message})
		}
	}
	for _, name := range fastIndexFileNames {
		_ = os.Remove(filepath.Join(snapshotDir, name+".tmp"))
	}

	// The first pass only builds the directory ID map. It avoids keeping the
	// variable-length directory names in memory and lets all later records use a
	// compact uint32 parent index.
	dirIDs := make([]uint64, 0, manifest.Directories+1)
	dirIDToIndex := make(map[uint64]uint32, manifest.Directories+1)
	dirReader, dirFile, err := newSegmentReader(filepath.Join(snapshotDir, "directories.seg"), segmentKindDirectories)
	if err != nil {
		return err
	}
	for {
		header, _, readErr := dirReader.readRecord(60, 56)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = dirFile.Close()
			return readErr
		}
		id := binary.LittleEndian.Uint64(header[0:8])
		if _, exists := dirIDToIndex[id]; exists {
			_ = dirFile.Close()
			return fmt.Errorf("duplicate directory id in snapshot: %d", id)
		}
		index := uint32(len(dirIDs))
		dirIDs = append(dirIDs, id)
		dirIDToIndex[id] = index
	}
	_ = dirFile.Close()
	if len(dirIDs) == 0 {
		return errors.New("snapshot contains no directory records")
	}

	// File IDs intentionally use the same 1..N sequence as the SQLite
	// materialization. Directory IDs carry their own MFT/scan IDs and are
	// disambiguated by the IsDir bit in API requests.
	fileIDBase := uint64(1)

	dirsPath := filepath.Join(snapshotDir, "tree.dirs.tmp")
	filesPath := filepath.Join(snapshotDir, "tree.files.tmp")
	namesPath := filepath.Join(snapshotDir, "tree.names.tmp")
	dirsFile, err := os.Create(dirsPath)
	if err != nil {
		return err
	}
	filesFile, err := os.Create(filesPath)
	if err != nil {
		_ = dirsFile.Close()
		return err
	}
	namesFile, err := os.Create(namesPath)
	if err != nil {
		_ = dirsFile.Close()
		_ = filesFile.Close()
		return err
	}
	dirWriter := bufio.NewWriterSize(dirsFile, 8<<20)
	fileWriter := bufio.NewWriterSize(filesFile, 8<<20)
	nameWriter := bufio.NewWriterSize(namesFile, 8<<20)
	var nextNameOffset uint64
	closeBuildFiles := func() error {
		if err := dirWriter.Flush(); err != nil {
			_ = namesFile.Close()
			_ = filesFile.Close()
			_ = dirsFile.Close()
			return err
		}
		if err := fileWriter.Flush(); err != nil {
			_ = namesFile.Close()
			_ = filesFile.Close()
			_ = dirsFile.Close()
			return err
		}
		if err := nameWriter.Flush(); err != nil {
			_ = namesFile.Close()
			_ = filesFile.Close()
			_ = dirsFile.Close()
			return err
		}
		if err := namesFile.Close(); err != nil {
			_ = filesFile.Close()
			_ = dirsFile.Close()
			return err
		}
		if err := filesFile.Close(); err != nil {
			_ = dirsFile.Close()
			return err
		}
		return dirsFile.Close()
	}

	parentIndices := make([]uint32, 0, manifest.Files+manifest.Directories)
	childCounts := make([]uint64, len(dirIDs))
	writeName := func(name string) (uint64, uint32, error) {
		data := []byte(name)
		offset := nextNameOffset
		if _, err := nameWriter.Write(data); err != nil {
			return 0, 0, err
		}
		nextNameOffset += uint64(len(data))
		return uint64(offset), uint32(len(data)), nil
	}
	writeDir := func(node fastIndexBuildNode) error {
		var raw [fastDirRecordSize]byte
		binary.LittleEndian.PutUint64(raw[0:8], node.ID)
		binary.LittleEndian.PutUint32(raw[8:12], node.Parent)
		binary.LittleEndian.PutUint32(raw[12:16], node.NameLen)
		binary.LittleEndian.PutUint64(raw[16:24], node.NameOff)
		binary.LittleEndian.PutUint64(raw[24:32], node.Size)
		binary.LittleEndian.PutUint64(raw[32:40], node.Allocated)
		binary.LittleEndian.PutUint64(raw[40:48], node.FileCount)
		binary.LittleEndian.PutUint64(raw[48:56], node.DirCount)
		_, err := dirWriter.Write(raw[:])
		return err
	}
	writeFile := func(node fastIndexBuildNode) error {
		var raw [fastFileRecordSize]byte
		binary.LittleEndian.PutUint32(raw[0:4], node.Parent)
		binary.LittleEndian.PutUint32(raw[4:8], node.NameLen)
		binary.LittleEndian.PutUint64(raw[8:16], node.NameOff)
		binary.LittleEndian.PutUint64(raw[16:24], node.Size)
		binary.LittleEndian.PutUint64(raw[24:32], node.Allocated)
		binary.LittleEndian.PutUint64(raw[32:40], uint64(node.MTimeNS))
		binary.LittleEndian.PutUint64(raw[40:48], uint64(node.CTimeNS))
		binary.LittleEndian.PutUint64(raw[48:56], uint64(node.BirthNS))
		_, err := fileWriter.Write(raw[:])
		return err
	}
	addParent := func(parentID uint64) (uint32, error) {
		if parentID == 0 {
			return fastInvalidParent, nil
		}
		parent, ok := dirIDToIndex[parentID]
		if !ok {
			return 0, fmt.Errorf("directory parent %d is missing from snapshot", parentID)
		}
		childCounts[parent]++
		return parent, nil
	}

	report("tree", 0, uint64(len(dirIDs))+manifest.Files, "生成快速树节点")
	// Second directory pass writes the fixed-width directory records.
	dirReader, dirFile, err = newSegmentReader(filepath.Join(snapshotDir, "directories.seg"), segmentKindDirectories)
	if err != nil {
		_ = dirsFile.Close()
		_ = filesFile.Close()
		_ = namesFile.Close()
		return err
	}
	for current := uint64(0); ; current++ {
		if err := ctx.Err(); err != nil {
			_ = dirFile.Close()
			_ = dirsFile.Close()
			_ = filesFile.Close()
			_ = namesFile.Close()
			return err
		}
		header, name, readErr := dirReader.readRecord(60, 56)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = dirFile.Close()
			_ = dirsFile.Close()
			_ = filesFile.Close()
			_ = namesFile.Close()
			return readErr
		}
		id := binary.LittleEndian.Uint64(header[0:8])
		index := dirIDToIndex[id]
		parent, err := addParent(binary.LittleEndian.Uint64(header[8:16]))
		if err != nil {
			_ = dirFile.Close()
			_ = dirsFile.Close()
			_ = filesFile.Close()
			_ = namesFile.Close()
			return err
		}
		nameOff, nameLen, err := writeName(name)
		if err != nil {
			_ = dirFile.Close()
			_ = dirsFile.Close()
			_ = filesFile.Close()
			_ = namesFile.Close()
			return err
		}
		node := fastIndexBuildNode{
			ID: id, Parent: parent, NameOff: nameOff, NameLen: nameLen,
			Flags:     fastFlagDirectory,
			Size:      binary.LittleEndian.Uint64(header[24:32]),
			Allocated: binary.LittleEndian.Uint64(header[32:40]),
			// 扫描器算好的递归统计值，写入位置见 scanner.go 的 writeDir。
			// 目录记录本来就有这 16 个预留字节，读端早已按此读回，这里只是补齐写入。
			FileCount: binary.LittleEndian.Uint64(header[40:48]),
			DirCount:  binary.LittleEndian.Uint64(header[48:56]),
		}
		if uint32(index) != uint32(len(parentIndices)) {
			_ = dirFile.Close()
			_ = dirsFile.Close()
			_ = filesFile.Close()
			_ = namesFile.Close()
			return fmt.Errorf("directory records are not in stable index order")
		}
		if err := writeDir(node); err != nil {
			_ = dirFile.Close()
			_ = dirsFile.Close()
			_ = filesFile.Close()
			_ = namesFile.Close()
			return err
		}
		parentIndices = append(parentIndices, parent)
		if current%10000 == 0 || current+1 == uint64(len(dirIDs)) {
			report("tree", current+1, uint64(len(dirIDs))+manifest.Files, "写入目录节点")
		}
	}
	_ = dirFile.Close()

	extensionStats := make(map[string]fastIndexExtension)
	fileReader, fileFile, err := newSegmentReader(filepath.Join(snapshotDir, "files.seg"), segmentKindFiles)
	if err != nil {
		_ = dirsFile.Close()
		_ = filesFile.Close()
		_ = namesFile.Close()
		return err
	}
	fileHeaderSize, fileNameOffset := 72, 68
	for fileCount := uint64(0); ; fileCount++ {
		if err := ctx.Err(); err != nil {
			_ = fileFile.Close()
			_ = dirsFile.Close()
			_ = filesFile.Close()
			_ = namesFile.Close()
			return err
		}
		header, name, readErr := fileReader.readRecord(fileHeaderSize, fileNameOffset)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = fileFile.Close()
			_ = dirsFile.Close()
			_ = filesFile.Close()
			_ = namesFile.Close()
			return readErr
		}
		parent, err := addParent(binary.LittleEndian.Uint64(header[0:8]))
		if err != nil {
			_ = fileFile.Close()
			_ = dirsFile.Close()
			_ = filesFile.Close()
			_ = namesFile.Close()
			return err
		}
		nameOff, nameLen, err := writeName(name)
		if err != nil {
			_ = fileFile.Close()
			_ = dirsFile.Close()
			_ = filesFile.Close()
			_ = namesFile.Close()
			return err
		}
		extension := fileExtension(name)
		if extension != "" {
			stat := extensionStats[extension]
			stat.Extension = extension
			stat.Files++
			stat.SizeBytes += binary.LittleEndian.Uint64(header[24:32])
			stat.AllocatedBytes += binary.LittleEndian.Uint64(header[32:40]) * 512
			extensionStats[extension] = stat
		}
		if err := writeFile(fastIndexBuildNode{
			ID: fileIDBase + fileCount, Parent: parent, NameOff: nameOff, NameLen: nameLen,
			Size:      binary.LittleEndian.Uint64(header[24:32]),
			Allocated: binary.LittleEndian.Uint64(header[32:40]) * 512,
			MTimeNS:   int64(binary.LittleEndian.Uint64(header[40:48])),
			CTimeNS:   fileCTime(header), BirthNS: fileBirthTime(header),
		}); err != nil {
			_ = fileFile.Close()
			_ = dirsFile.Close()
			_ = filesFile.Close()
			_ = namesFile.Close()
			return err
		}
		parentIndices = append(parentIndices, parent)
		if fileCount%100000 == 0 || fileCount+1 == manifest.Files {
			report("tree", uint64(len(dirIDs))+fileCount+1, uint64(len(dirIDs))+manifest.Files, "写入文件节点")
		}
	}
	_ = fileFile.Close()
	if err := closeBuildFiles(); err != nil {
		return err
	}

	childrenCount := uint64(0)
	ranges := make([]fastIndexRange, len(dirIDs))
	for index, count := range childCounts {
		ranges[index] = fastIndexRange{Start: childrenCount, Count: count}
		childrenCount += count
	}
	children := make([]uint32, childrenCount)
	cursors := make([]uint64, len(childCounts))
	for index := range ranges {
		cursors[index] = ranges[index].Start
	}
	for nodeIndex, parent := range parentIndices {
		if parent == fastInvalidParent {
			continue
		}
		children[cursors[parent]] = uint32(nodeIndex)
		cursors[parent]++
	}

	if err := writeFastRanges(filepath.Join(snapshotDir, "tree.ranges.tmp"), ranges); err != nil {
		return err
	}
	if err := writeFastChildren(filepath.Join(snapshotDir, "tree.children.tmp"), children); err != nil {
		return err
	}
	if err := writeFastDirIDs(filepath.Join(snapshotDir, "tree.dirids.tmp"), dirIDs, dirIDToIndex); err != nil {
		return err
	}
	exts := make([]fastIndexExtension, 0, len(extensionStats))
	for _, item := range extensionStats {
		exts = append(exts, item)
	}
	sort.Slice(exts, func(i, j int) bool { return exts[i].AllocatedBytes > exts[j].AllocatedBytes })
	extensionData, err := json.Marshal(exts)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "tree.extensions.json.tmp"), extensionData, 0644); err != nil {
		return err
	}
	indexManifest := fastIndexManifest{
		Version: fastIndexVersion, DirRecordSize: fastDirRecordSize, FileRecordSize: fastFileRecordSize,
		DirectoryCount: uint64(len(dirIDs)), FileCount: uint64(len(parentIndices)) - uint64(len(dirIDs)),
		FileIDBase: fileIDBase, ChildCount: childrenCount, RootNode: 0,
	}
	manifestData, err := json.MarshalIndent(indexManifest, "", "  ")
	if err != nil {
		return err
	}
	// Publish data sections before the manifest. A reader only trusts the
	// manifest, so an interrupted build is ignored instead of partially served.
	for _, name := range fastIndexFileNames[:len(fastIndexFileNames)-1] {
		if err := publishFastFile(filepath.Join(snapshotDir, name+".tmp"), filepath.Join(snapshotDir, name)); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "tree.index.json.tmp"), append(manifestData, '\n'), 0644); err != nil {
		return err
	}
	if err := publishFastFile(filepath.Join(snapshotDir, "tree.index.json.tmp"), filepath.Join(snapshotDir, "tree.index.json")); err != nil {
		return err
	}
	report("tree", uint64(len(parentIndices)), uint64(len(parentIndices)), "快速树索引完成")
	return nil
}

func publishFastFile(source, target string) error {
	if err := os.Rename(source, target); err == nil {
		return nil
	} else {
		// Unix rename replaces atomically. Windows refuses to replace an
		// existing file, so use the same fallback as the SQLite publisher.
		if removeErr := os.Remove(target); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return err
		}
		return os.Rename(source, target)
	}
}

func writeFastRanges(path string, ranges []fastIndexRange) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriterSize(file, 8<<20)
	var raw [fastRangeRecordSize]byte
	for _, item := range ranges {
		binary.LittleEndian.PutUint64(raw[0:8], item.Start)
		binary.LittleEndian.PutUint64(raw[8:16], item.Count)
		if _, err := writer.Write(raw[:]); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func writeFastChildren(path string, children []uint32) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriterSize(file, 8<<20)
	var raw [4]byte
	for _, child := range children {
		binary.LittleEndian.PutUint32(raw[:], child)
		if _, err := writer.Write(raw[:]); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func writeFastDirIDs(path string, ids []uint64, index map[uint64]uint32) error {
	entries := make([]fastIndexMapEntry, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, fastIndexMapEntry{ID: id, Index: index[id]})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriterSize(file, 8<<20)
	var raw [fastMapRecordSize]byte
	for _, entry := range entries {
		binary.LittleEndian.PutUint64(raw[0:8], entry.ID)
		binary.LittleEndian.PutUint32(raw[8:12], entry.Index)
		if _, err := writer.Write(raw[:]); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func openFastIndex(snapshotDir string) (*fastIndex, error) {
	containerPath := gtiPath(snapshotDir)
	return openGTIFastIndex(containerPath)
}

func openGTIFastIndex(path string) (*fastIndex, error) {
	file, sections, err := openGTIContainer(path)
	if err != nil {
		return nil, err
	}
	_ = file.Close()
	container, err := mmap.Open(path)
	if err != nil {
		return nil, fmt.Errorf("memory-map snapshot.gti: %w", err)
	}
	readSection := func(id uint32) ([]byte, error) {
		section, ok := sections[id]
		if !ok {
			return nil, fmt.Errorf("gti section %d is missing", id)
		}
		if section.Length > uint64(^uint(0)>>1) {
			return nil, errors.New("gti section is too large")
		}
		data := make([]byte, int(section.Length))
		_, err := container.ReadAt(data, int64(section.Offset))
		return data, err
	}
	manifestData, err := readSection(gtiManifest)
	if err != nil {
		_ = container.Close()
		return nil, err
	}
	var scan SnapshotManifest
	if err := json.Unmarshal(manifestData, &scan); err != nil {
		_ = container.Close()
		return nil, fmt.Errorf("read gti snapshot manifest: %w", err)
	}
	indexManifest := fastIndexManifest{Version: fastIndexVersion, DirRecordSize: fastDirRecordSize, FileRecordSize: fastFileRecordSize, DirectoryCount: scan.Directories + 1, FileCount: scan.Files, FileIDBase: 1, ChildCount: scan.Directories + scan.Files + 1, RootNode: 0}
	if raw, ok := sections[gtiDirIDs]; ok && raw.Length >= fastMapRecordSize {
		indexManifest.DirectoryCount = raw.Length / fastMapRecordSize
	}
	dirIDData, err := readSection(gtiDirIDs)
	if err != nil {
		_ = container.Close()
		return nil, err
	}
	if len(dirIDData)%fastMapRecordSize != 0 {
		_ = container.Close()
		return nil, errors.New("invalid gti directory ID section")
	}
	entries := make([]fastIndexMapEntry, len(dirIDData)/fastMapRecordSize)
	for i := range entries {
		offset := i * fastMapRecordSize
		entries[i] = fastIndexMapEntry{ID: binary.LittleEndian.Uint64(dirIDData[offset : offset+8]), Index: binary.LittleEndian.Uint32(dirIDData[offset+8 : offset+12])}
	}
	var extensions []fastIndexExtension
	if data, readErr := readSection(gtiExtensions); readErr == nil {
		_ = json.Unmarshal(data, &extensions)
	}
	return &fastIndex{dir: filepath.Dir(path), manifest: indexManifest, container: container, sections: sections, extensions: extensions, dirIDEntries: entries}, nil
}

func (f *fastIndex) Close() error {
	if f == nil {
		return nil
	}
	if f.container == nil {
		return nil
	}
	return f.container.Close()
}

func (f *fastIndex) sectionOffset(id uint32) int64 {
	if section, ok := f.sections[id]; ok {
		return int64(section.Offset)
	}
	return 0
}

func (f *fastIndex) sectionBytes(id uint32) ([]byte, error) {
	section, ok := f.sections[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	if section.Length > uint64(^uint(0)>>1) {
		return nil, errors.New("gti section is too large")
	}
	data := make([]byte, int(section.Length))
	if f.container != nil {
		_, err := f.container.ReadAt(data, int64(section.Offset))
		return data, err
	}
	return nil, os.ErrNotExist
}

func (f *fastIndex) directoryIndex(id uint64) (uint32, bool) {
	index := sort.Search(len(f.dirIDEntries), func(i int) bool { return f.dirIDEntries[i].ID >= id })
	if index >= len(f.dirIDEntries) || f.dirIDEntries[index].ID != id {
		return 0, false
	}
	return f.dirIDEntries[index].Index, true
}

func (f *fastIndex) readNode(index uint32) (fastIndexNode, error) {
	if uint64(index) < f.manifest.DirectoryCount {
		var raw [fastDirRecordSize]byte
		if _, err := f.container.ReadAt(raw[:], f.sectionOffset(gtiDirs)+int64(index)*fastDirRecordSize); err != nil {
			return fastIndexNode{}, err
		}
		return fastIndexNode{
			ID: binary.LittleEndian.Uint64(raw[0:8]), Parent: binary.LittleEndian.Uint32(raw[8:12]), NameLen: binary.LittleEndian.Uint32(raw[12:16]), NameOff: binary.LittleEndian.Uint64(raw[16:24]), Flags: fastFlagDirectory, Size: binary.LittleEndian.Uint64(raw[24:32]), Allocated: binary.LittleEndian.Uint64(raw[32:40]), FileCount: binary.LittleEndian.Uint64(raw[40:48]), DirCount: binary.LittleEndian.Uint64(raw[48:56]),
		}, nil
	}
	fileIndex := uint64(index) - f.manifest.DirectoryCount
	if fileIndex >= f.manifest.FileCount {
		return fastIndexNode{}, io.ErrUnexpectedEOF
	}
	var raw [fastFileRecordSize]byte
	if _, err := f.container.ReadAt(raw[:], f.sectionOffset(gtiFiles)+int64(fileIndex)*fastFileRecordSize); err != nil {
		return fastIndexNode{}, err
	}
	return fastIndexNode{
		ID: f.manifest.FileIDBase + fileIndex, Parent: binary.LittleEndian.Uint32(raw[0:4]), NameLen: binary.LittleEndian.Uint32(raw[4:8]), NameOff: binary.LittleEndian.Uint64(raw[8:16]), Size: binary.LittleEndian.Uint64(raw[16:24]), Allocated: binary.LittleEndian.Uint64(raw[24:32]), MTimeNS: int64(binary.LittleEndian.Uint64(raw[32:40])), CTimeNS: int64(binary.LittleEndian.Uint64(raw[40:48])), BirthNS: int64(binary.LittleEndian.Uint64(raw[48:56])),
	}, nil
}

func (f *fastIndex) readName(offset uint64, length uint32) (string, error) {
	if length == 0 {
		return "", nil
	}
	data := make([]byte, length)
	if _, err := f.container.ReadAt(data, f.sectionOffset(gtiNames)+int64(offset)); err != nil {
		return "", err
	}
	return string(data), nil
}

func (f *fastIndex) readChildren(directoryIndex uint32) ([]uint32, error) {
	var raw [fastRangeRecordSize]byte
	if _, err := f.container.ReadAt(raw[:], f.sectionOffset(gtiRanges)+int64(directoryIndex)*fastRangeRecordSize); err != nil {
		return nil, err
	}
	start := binary.LittleEndian.Uint64(raw[0:8])
	count := binary.LittleEndian.Uint64(raw[8:16])
	if count > uint64(^uint(0)>>2) {
		return nil, errors.New("tree index child range is too large")
	}
	children := make([]uint32, count)
	if count == 0 {
		return children, nil
	}
	data := make([]byte, count*4)
	if _, err := f.container.ReadAt(data, f.sectionOffset(gtiChildren)+int64(start*4)); err != nil {
		return nil, err
	}
	for i := range children {
		children[i] = binary.LittleEndian.Uint32(data[i*4 : i*4+4])
	}
	return children, nil
}

func (f *fastIndex) nodeItem(index uint32) (listItem, error) {
	node, err := f.readNode(index)
	if err != nil {
		return listItem{}, err
	}
	name, err := f.readName(node.NameOff, node.NameLen)
	if err != nil {
		return listItem{}, err
	}
	extension := ""
	if node.Flags&fastFlagDirectory == 0 {
		extension = fileExtension(name)
	}
	parentID := int64(0)
	if node.Parent != fastInvalidParent {
		parent, parentErr := f.readNode(node.Parent)
		if parentErr != nil {
			return listItem{}, parentErr
		}
		parentID = int64(parent.ID)
	}
	return listItem{ID: int64(node.ID), ParentID: parentID, Name: name, IsDir: node.Flags&fastFlagDirectory != 0, SizeBytes: int64(node.Size), AllocatedBytes: int64(node.Allocated), FileCount: int64(node.FileCount), DirCount: int64(node.DirCount), MTimeNS: node.MTimeNS, CTimeNS: node.CTimeNS, BirthtimeNS: node.BirthNS, Extension: extension}, nil
}

func (f *fastIndex) decoratePath(item *listItem, root string) error {
	index, ok := f.indexForItem(item.ID, item.IsDir)
	if !ok {
		return errors.New("tree index item not found")
	}
	names := make([]string, 0, 8)
	for index != fastInvalidParent {
		node, err := f.readNode(index)
		if err != nil {
			return err
		}
		name, err := f.readName(node.NameOff, node.NameLen)
		if err != nil {
			return err
		}
		names = append(names, name)
		index = node.Parent
	}
	for left, right := 0, len(names)-1; left < right; left, right = left+1, right-1 {
		names[left], names[right] = names[right], names[left]
	}
	item.Path = root
	if item.Path == "" {
		item.Path = string(filepath.Separator)
	}
	start := 0
	if len(names) > 0 {
		// The first indexed name is the scan root. The serving machine may use
		// a different path root, so it must not be appended twice.
		start = 1
	}
	for _, name := range names[start:] {
		item.Path = filepath.Join(item.Path, name)
	}
	return nil
}

func (f *fastIndex) indexForItem(id int64, isDir bool) (uint32, bool) {
	if id < 1 {
		return 0, false
	}
	value := uint64(id)
	if isDir {
		return f.directoryIndex(value)
	}
	if value < f.manifest.FileIDBase {
		return 0, false
	}
	fileIndex := value - f.manifest.FileIDBase
	if fileIndex >= f.manifest.FileCount {
		return 0, false
	}
	return uint32(f.manifest.DirectoryCount + fileIndex), true
}

func (f *fastIndex) childrenItems(parentID uint64, sortValue string, limit, offset int, allocatedKnown bool) ([]listItem, int, error) {
	parent, ok := f.directoryIndex(parentID)
	if !ok {
		return nil, 0, os.ErrNotExist
	}
	children, err := f.readChildren(parent)
	if err != nil {
		return nil, 0, err
	}
	items := make([]listItem, 0, len(children))
	for _, child := range children {
		item, err := f.nodeItem(child)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	sortFastItems(items, sortValue, allocatedKnown)
	total := len(items)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return items[offset:end], total, nil
}

func sortFastItems(items []listItem, sortValue string, allocatedKnown bool) {
	sort.SliceStable(items, func(i, j int) bool {
		switch sortValue {
		case "name":
			li, lj := strings.ToLower(items[i].Name), strings.ToLower(items[j].Name)
			if li == lj {
				return items[i].ID < items[j].ID
			}
			return li < lj
		case "mtime":
			if items[i].MTimeNS == items[j].MTimeNS {
				return items[i].ID < items[j].ID
			}
			return items[i].MTimeNS > items[j].MTimeNS
		default:
			left, right := fastItemSize(items[i], allocatedKnown), fastItemSize(items[j], allocatedKnown)
			if left == right {
				if items[i].IsDir != items[j].IsDir {
					return items[i].IsDir
				}
				return items[i].ID < items[j].ID
			}
			return left > right
		}
	})
}

// 排序与「最大文件」统计统一走这个口径：实际占用不可用时回退到逻辑大小。
func fastItemSize(item listItem, allocatedKnown bool) int64 {
	if allocatedKnown {
		return item.AllocatedBytes
	}
	return item.SizeBytes
}

func pageFastItems(items []listItem, limit, offset int) []listItem {
	if offset < 0 {
		offset = 0
	}
	if offset > len(items) {
		offset = len(items)
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

func (f *fastIndex) fileItems(ctx context.Context, sortValue string, limit, offset int, allocatedKnown bool) ([]listItem, error) {
	items := make([]listItem, 0, minInt(int(f.manifest.FileCount), 100000))
	for index := uint64(0); index < f.manifest.FileCount; index++ {
		if index%8192 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		item, err := f.nodeItem(uint32(f.manifest.DirectoryCount + index))
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	sortFastItems(items, sortValue, allocatedKnown)
	return pageFastItems(items, limit, offset), nil
}

func (f *fastIndex) search(ctx context.Context, term string, limit, offset int, allocatedKnown bool) ([]listItem, int, error) {
	term = strings.ToLower(strings.TrimSpace(term))
	if term == "" {
		return []listItem{}, 0, nil
	}
	workerCount := runtime.NumCPU()
	if workerCount > 16 {
		workerCount = 16
	}
	if uint64(workerCount) > f.manifest.FileCount {
		workerCount = int(f.manifest.FileCount)
	}
	if workerCount < 1 {
		workerCount = 1
	}
	results := make(chan []listItem, workerCount)
	errorsFound := make(chan error, workerCount)
	var workers sync.WaitGroup
	chunk := (f.manifest.FileCount + uint64(workerCount) - 1) / uint64(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		start := uint64(worker) * chunk
		end := start + chunk
		if end > f.manifest.FileCount {
			end = f.manifest.FileCount
		}
		workers.Add(1)
		go func(start, end uint64) {
			defer workers.Done()
			local := make([]listItem, 0, 256)
			for index := start; index < end; index++ {
				if index%8192 == 0 {
					if err := ctx.Err(); err != nil {
						errorsFound <- err
						return
					}
				}
				item, err := f.nodeItem(uint32(f.manifest.DirectoryCount + index))
				if err != nil {
					errorsFound <- err
					return
				}
				if strings.Contains(strings.ToLower(item.Name), term) {
					local = append(local, item)
				}
			}
			results <- local
		}(start, end)
	}
	go func() {
		workers.Wait()
		close(results)
		close(errorsFound)
	}()
	items := make([]listItem, 0)
	for matches := range results {
		items = append(items, matches...)
	}
	if err := <-errorsFound; err != nil {
		return nil, 0, err
	}
	sortFastItems(items, "size", allocatedKnown)
	total := len(items)
	return pageFastItems(items, limit, offset), total, nil
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func (f *fastIndex) breadcrumbs(id uint64, root string) ([]map[string]any, error) {
	index, ok := f.directoryIndex(id)
	if !ok {
		return nil, os.ErrNotExist
	}
	result := make([]map[string]any, 0, 8)
	for index != fastInvalidParent {
		node, err := f.readNode(index)
		if err != nil {
			return nil, err
		}
		name, err := f.readName(node.NameOff, node.NameLen)
		if err != nil {
			return nil, err
		}
		// 面包屑顺带带上该目录自身的递归统计值，前端「当前目录概览」就能免掉额外请求。
		// 旧快照的 FileCount/DirCount 恒为 0，调用方要按「不可用」而不是「空目录」处理。
		result = append(result, map[string]any{
			"id": int64(node.ID), "parent_id": parentIDFromNode(f, node), "name": name, "depth": len(result),
			"size_bytes": int64(node.Size), "allocated_bytes": int64(node.Allocated),
			"file_count": int64(node.FileCount), "dir_count": int64(node.DirCount),
		})
		index = node.Parent
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	for i := range result {
		result[i]["depth"] = i
		if i == 0 {
			result[i]["path"] = root
		} else {
			result[i]["path"] = filepath.Join(result[i-1]["path"].(string), result[i]["name"].(string))
		}
	}
	return result, nil
}

func parentIDFromNode(f *fastIndex, node fastIndexNode) int64 {
	if node.Parent == fastInvalidParent {
		return 0
	}
	parent, err := f.readNode(node.Parent)
	if err != nil {
		return 0
	}
	return int64(parent.ID)
}
