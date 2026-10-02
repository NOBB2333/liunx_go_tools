//go:build windows

package filesystem

// This file contains the NTFS fast path. It reads the $MFT stream in large,
// sequential chunks and never opens one handle per user file. That is the
// important distinction from a normal directory walk (and the same basic
// technique used by WizTree). The parser intentionally handles the parts of
// NTFS needed by the snapshot format: fixups, STANDARD_INFORMATION,
// FILE_NAME, and the unnamed DATA attribute/runlist.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

const (
	ntfsMFTChunk = 8 << 20
)

type ntfsVolumeData struct {
	BytesPerSector     uint32
	BytesPerCluster    uint32
	BytesPerFileRecord uint32
	MftValidDataLength int64
	MftStartLcn        int64
	VolumeSerial       uint64
}

type ntfsVolumeReader struct {
	mu     sync.Mutex
	handle windows.Handle
}

func (r *ntfsVolumeReader) Close() { _ = windows.CloseHandle(r.handle) }

func (r *ntfsVolumeReader) ReadAt(p []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, io.EOF
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := windows.Seek(r.handle, offset, io.SeekStart); err != nil {
		return 0, err
	}
	read := 0
	for read < len(p) {
		n, err := windows.Read(r.handle, p[read:])
		read += n
		if err != nil {
			if read > 0 && (errors.Is(err, windows.ERROR_HANDLE_EOF) || errors.Is(err, io.EOF)) {
				return read, io.EOF
			}
			return read, err
		}
		if n == 0 {
			return read, io.EOF
		}
	}
	return read, nil
}

func openNTFSVolume(root string) (*ntfsVolumeReader, ntfsVolumeData, error) {
	volume := filepath.VolumeName(root)
	if volume == "" || strings.HasPrefix(volume, `\\`) || len(volume) < 2 || volume[1] != ':' {
		return nil, ntfsVolumeData{}, errors.New("root is not a local drive volume")
	}
	device := `\\.\` + volume
	h, err := windows.CreateFile(windows.StringToUTF16Ptr(device), windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_SEQUENTIAL_SCAN, 0)
	if err != nil {
		return nil, ntfsVolumeData{}, err
	}
	reader := &ntfsVolumeReader{handle: h}
	var raw [128]byte
	var returned uint32
	err = windows.DeviceIoControl(h, windows.FSCTL_GET_NTFS_VOLUME_DATA, nil, 0, &raw[0], uint32(len(raw)), &returned, nil)
	if err != nil || returned < 96 {
		reader.Close()
		if err == nil {
			err = errors.New("short NTFS volume data response")
		}
		return nil, ntfsVolumeData{}, err
	}
	data := ntfsVolumeData{
		BytesPerSector:     binary.LittleEndian.Uint32(raw[40:44]),
		BytesPerCluster:    binary.LittleEndian.Uint32(raw[44:48]),
		BytesPerFileRecord: binary.LittleEndian.Uint32(raw[48:52]),
		MftValidDataLength: int64(binary.LittleEndian.Uint64(raw[56:64])),
		MftStartLcn:        int64(binary.LittleEndian.Uint64(raw[64:72])),
		VolumeSerial:       binary.LittleEndian.Uint64(raw[0:8]),
	}
	if data.BytesPerSector == 0 || data.BytesPerCluster == 0 || data.BytesPerFileRecord == 0 || data.MftStartLcn < 0 {
		reader.Close()
		return nil, ntfsVolumeData{}, errors.New("invalid NTFS volume geometry")
	}
	return reader, data, nil
}

func ntfsMFTDataRuns(volume *ntfsVolumeReader, data ntfsVolumeData) ([]ntfsRun, int64, error) {
	recordSize := int64(data.BytesPerFileRecord)
	first := make([]byte, recordSize)
	offset := data.MftStartLcn * int64(data.BytesPerCluster)
	if _, err := volume.ReadAt(first, offset); err != nil {
		return nil, 0, err
	}
	if !fixupNTFSRecordWithSector(first, data.BytesPerSector) {
		return nil, 0, errors.New("invalid NTFS $MFT record fixup")
	}
	attrOffset := int(binary.LittleEndian.Uint16(first[20:22]))
	used := int(binary.LittleEndian.Uint32(first[24:28]))
	if attrOffset < 0x20 || used > len(first) || used <= attrOffset {
		return nil, 0, errors.New("invalid NTFS $MFT record")
	}
	var runs []ntfsRun
	valid := data.MftValidDataLength
	for pos := attrOffset; pos+16 <= used; {
		kind := binary.LittleEndian.Uint32(first[pos : pos+4])
		length := int(binary.LittleEndian.Uint32(first[pos+4 : pos+8]))
		if kind == 0xffffffff || length < 16 || pos+length > used {
			break
		}
		if kind == ntfsAttrData && first[pos+8] != 0 && first[pos+9] == 0 && length >= 56 {
			runOffset := int(binary.LittleEndian.Uint16(first[pos+32 : pos+34]))
			if runOffset >= 0 && runOffset < length {
				extent, err := parseNTFSRunlist(first[pos+runOffset : pos+length])
				if err != nil {
					return nil, 0, err
				}
				startVCN := int64(binary.LittleEndian.Uint64(first[pos+16 : pos+24]))
				for i := range extent {
					extent[i].VcnStart += startVCN
				}
				runs = append(runs, extent...)
				if attrValid := int64(binary.LittleEndian.Uint64(first[pos+48 : pos+56])); attrValid > valid {
					valid = attrValid
				}
			}
		}
		pos += length
	}
	if len(runs) == 0 {
		return nil, 0, errors.New("NTFS $MFT unnamed DATA attribute not found")
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].VcnStart < runs[j].VcnStart })
	expectedVCN := int64(0)
	for _, run := range runs {
		if run.VcnStart != expectedVCN {
			return nil, 0, errors.New("NTFS $MFT runlist has an uncovered attribute-list extent")
		}
		expectedVCN += run.Clusters
	}
	if valid <= 0 || expectedVCN*int64(data.BytesPerCluster) < valid {
		return nil, 0, errors.New("NTFS $MFT runlist does not cover its valid data length")
	}
	return runs, valid, nil
}

func scanWindowsMFT(ctx context.Context, opt FastScanOptions, root string) (FastScanSummary, bool, error) {
	started := time.Now()
	volume, geometry, err := openNTFSVolume(root)
	if err != nil {
		return FastScanSummary{}, false, err
	}
	defer volume.Close()
	runs, validLength, err := ntfsMFTDataRuns(volume, geometry)
	if err != nil {
		return FastScanSummary{}, false, err
	}
	recordSize := int64(geometry.BytesPerFileRecord)
	if validLength <= 0 {
		return FastScanSummary{}, false, errors.New("NTFS $MFT has no valid records")
	}
	recordCount := (validLength + recordSize - 1) / recordSize
	if recordCount <= ntfsRootRecord || recordCount > int64(^uint(0)>>1) {
		return FastScanSummary{}, false, errors.New("invalid NTFS $MFT record count")
	}
	rootID := uint64(ntfsRootRecord)
	if candidate, candidateErr := ntfsPathFileID(root); candidateErr == nil && candidate < uint64(recordCount) {
		rootID = candidate
	}
	metas := make([]ntfsMeta, int(recordCount))
	selected := make([]uint8, int(recordCount))
	lastReport := time.Time{}
	var parsed uint64
	var files, dirs uint64
	report := func(message string, force bool) {
		if opt.Progress == nil || (!force && time.Since(lastReport) < opt.ProgressInterval) {
			return
		}
		lastReport = time.Now()
		elapsed := time.Since(started)
		rate := float64(parsed) / elapsed.Seconds()
		opt.Progress(ScanProgress{Phase: "mft", Files: files, Directories: dirs, EntriesPerSecond: rate, Elapsed: elapsed, Message: message})
	}
	recordsDone := int64(0)
	for _, run := range runs {
		if recordsDone >= recordCount {
			break
		}
		remainingBytes := run.Clusters * int64(geometry.BytesPerCluster)
		if remainingBytes > validLength-recordsDone*recordSize {
			remainingBytes = validLength - recordsDone*recordSize
		}
		if run.LcnStart < 0 {
			recordsDone += remainingBytes / recordSize
			continue
		}
		chunkBytes := int64(ntfsMFTChunk)
		if chunkBytes < recordSize {
			chunkBytes = recordSize
		}
		chunkBytes = (chunkBytes / recordSize) * recordSize
		buffer := make([]byte, chunkBytes)
		for consumed := int64(0); consumed < remainingBytes; {
			if err := ctx.Err(); err != nil {
				return FastScanSummary{}, true, err
			}
			want := remainingBytes - consumed
			if want > chunkBytes {
				want = chunkBytes
			}
			n, readErr := volume.ReadAt(buffer[:want], (run.LcnStart*int64(geometry.BytesPerCluster))+consumed)
			if readErr != nil && n == 0 {
				return FastScanSummary{}, false, readErr
			}
			for offset := int64(0); offset+recordSize <= int64(n) && recordsDone < recordCount; offset += recordSize {
				id := recordsDone
				if meta, ok := parseNTFSRecordWithSector(buffer[offset:offset+recordSize], geometry.BytesPerSector); ok {
					metas[id] = meta
					if meta.flags&ntfsFileRecordDir != 0 {
						dirs++
					} else {
						files++
					}
				}
				parsed++
				recordsDone++
				report("读取 NTFS MFT", false)
			}
			consumed += int64(n)
			if n == 0 || (readErr != nil && consumed < remainingBytes) {
				if readErr == nil {
					readErr = io.ErrUnexpectedEOF
				}
				return FastScanSummary{}, false, readErr
			}
		}
	}
	report("MFT 记录读取完成，正在重建目录树", true)
	if rootID >= uint64(len(metas)) || metas[rootID].flags&ntfsFileRecordDir == 0 {
		return FastScanSummary{}, false, errors.New("target directory MFT record is unavailable")
	}
	selected[rootID] = 1
	for id := 0; id < len(metas); id++ {
		if uint64(id) == rootID || metas[id].flags&ntfsFileRecordActive == 0 {
			continue
		}
		var trailStorage [64]uint32
		trail := trailStorage[:0]
		cur := uint64(id)
		include := false
		for {
			if cur == rootID || (cur < uint64(len(selected)) && selected[cur] == 1) {
				include = true
				break
			}
			if cur >= uint64(len(metas)) || metas[cur].flags&ntfsFileRecordActive == 0 || selected[cur] == 2 || metas[cur].parent == cur {
				break
			}
			duplicate := false
			for _, seenID := range trail {
				if uint64(seenID) == cur {
					duplicate = true
					break
				}
			}
			if duplicate {
				break
			}
			trail = append(trail, uint32(cur))
			cur = metas[cur].parent
		}
		for _, child := range trail {
			if include {
				selected[child] = 1
			} else {
				selected[child] = 2
			}
		}
	}

	depth := make([]uint32, len(metas))
	maxDepth := uint32(0)
	for id := range metas {
		if selected[id] != 1 || uint64(id) == rootID {
			continue
		}
		var trailStorage [64]uint32
		trail := trailStorage[:0]
		cur := uint64(id)
		for cur != rootID && cur < uint64(len(metas)) && depth[cur] == 0 {
			trail = append(trail, uint32(cur))
			cur = metas[cur].parent
		}
		base := uint32(0)
		if cur < uint64(len(metas)) {
			base = depth[cur]
		}
		for i := len(trail) - 1; i >= 0; i-- {
			base++
			depth[trail[i]] = base
			if base > maxDepth {
				maxDepth = base
			}
		}
	}
	selectedFiles, selectedDirs := uint64(0), uint64(0)
	logicalTotal, allocatedTotal := uint64(0), uint64(0)
	for id, meta := range metas {
		if selected[id] != 1 {
			continue
		}
		if meta.flags&ntfsFileRecordDir != 0 {
			selectedDirs++
			continue
		}
		selectedFiles++
		logicalTotal += meta.logical
		blocks := (meta.allocated + 511) / 512
		allocatedTotal += blocks * 512
		parent := meta.parent
		if parent < uint64(len(metas)) && selected[parent] == 1 {
			metas[parent].logical += meta.logical
			metas[parent].allocated += blocks * 512
			metas[parent].fileCount++
		}
	}
	buckets := make([][]uint32, int(maxDepth)+1)
	for id, meta := range metas {
		if selected[id] == 1 && meta.flags&ntfsFileRecordDir != 0 && uint64(id) != rootID {
			buckets[depth[id]] = append(buckets[depth[id]], uint32(id))
		}
	}
	for d := int(maxDepth); d > 0; d-- {
		for _, id := range buckets[d] {
			parent := metas[id].parent
			if parent < uint64(len(metas)) && selected[parent] == 1 {
				metas[parent].logical += metas[id].logical
				metas[parent].allocated += metas[id].allocated
				metas[parent].fileCount += metas[id].fileCount
				metas[parent].dirCount += metas[id].dirCount + 1
			}
		}
	}

	filesPath := filepath.Join(opt.OutputDir, "files.seg")
	dirsPath := filepath.Join(opt.OutputDir, "directories.seg")
	filesOut, err := os.OpenFile(filesPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return FastScanSummary{}, true, err
	}
	dirsOut, err := os.OpenFile(dirsPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		_ = filesOut.Close()
		return FastScanSummary{}, true, err
	}
	errsOut, err := os.OpenFile(filepath.Join(opt.OutputDir, "errors.ndjson"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		_ = filesOut.Close()
		_ = dirsOut.Close()
		return FastScanSummary{}, true, err
	}
	if err := writeSegmentHeader(filesOut, segmentKindFiles); err != nil {
		return FastScanSummary{}, true, err
	}
	if err := writeSegmentHeader(dirsOut, segmentKindDirectories); err != nil {
		return FastScanSummary{}, true, err
	}
	fileWriter, dirWriter := newFastBinaryWriter(filesOut), newFastBinaryWriter(dirsOut)
	_ = errsOut.Close() // reserved for future parser diagnostics
	secondPass := func() error {
		recordsDone = 0
		for _, run := range runs {
			remainingBytes := run.Clusters * int64(geometry.BytesPerCluster)
			if remainingBytes > validLength-recordsDone*recordSize {
				remainingBytes = validLength - recordsDone*recordSize
			}
			if run.LcnStart < 0 {
				recordsDone += remainingBytes / recordSize
				continue
			}
			chunkBytes := int64(ntfsMFTChunk)
			chunkBytes = (chunkBytes / recordSize) * recordSize
			if chunkBytes < recordSize {
				chunkBytes = recordSize
			}
			buffer := make([]byte, chunkBytes)
			for consumed := int64(0); consumed < remainingBytes; {
				if err := ctx.Err(); err != nil {
					return err
				}
				want := remainingBytes - consumed
				if want > chunkBytes {
					want = chunkBytes
				}
				n, readErr := volume.ReadAt(buffer[:want], run.LcnStart*int64(geometry.BytesPerCluster)+consumed)
				for offset := int64(0); offset+recordSize <= int64(n) && recordsDone < recordCount; offset += recordSize {
					id := recordsDone
					if selected[id] == 1 {
						record := buffer[offset : offset+recordSize]
						if !fixupNTFSRecordWithSector(record, geometry.BytesPerSector) {
							recordsDone++
							continue
						}
						meta := metas[id]
						name := decodeNTFSName(record, meta)
						if name == "" {
							recordsDone++
							continue
						}
						mappedParent := meta.parent
						if mappedParent == rootID {
							mappedParent = 1
						}
						if meta.flags&ntfsFileRecordDir != 0 {
							mappedID := uint64(id)
							if mappedID == rootID {
								mappedID = 1
								name = filepath.Base(root)
							}
							parentID := mappedParent
							if uint64(id) == rootID {
								parentID = 0
							}
							if err := dirWriter.writeDir(fastDirRecord{id: mappedID, parentID: parentID, name: name, depth: depth[id], logicalBytes: metas[id].logical, allocatedBytes: metas[id].allocated, fileCount: uint64(metas[id].fileCount), directoryCount: uint64(metas[id].dirCount)}); err != nil {
								return err
							}
						} else {
							blocks := int64((meta.allocated + 511) / 512)
							if err := fileWriter.writeFile(fastFileRecord{parentID: mappedParent, name: name, inode: uint64(id), device: geometry.VolumeSerial, size: int64(meta.logical), blocks: blocks, mtimeNS: meta.mtimeNS, ctimeNS: meta.ctimeNS, btimeNS: meta.btimeNS, mode: meta.mode}); err != nil {
								return err
							}
						}
					}
					recordsDone++
				}
				consumed += int64(n)
				if n == 0 || (readErr != nil && consumed < remainingBytes) {
					return readErr
				}
			}
		}
		return nil
	}
	if err := secondPass(); err != nil {
		_ = filesOut.Close()
		_ = dirsOut.Close()
		return FastScanSummary{}, true, err
	}
	if err := fileWriter.flush(); err != nil {
		return FastScanSummary{}, true, err
	}
	if err := dirWriter.flush(); err != nil {
		return FastScanSummary{}, true, err
	}
	_ = filesOut.Close()
	_ = dirsOut.Close()
	finished := time.Now()
	directoryCount := uint64(0)
	if selectedDirs > 0 {
		directoryCount = selectedDirs - 1 // the root itself is represented as id 1
	}
	summary := FastScanSummary{SchemaVersion: snapshotSchemaVersion, Root: root, OutputDir: opt.OutputDir, Metadata: string(opt.Metadata), ScannerBackend: string(FastScanBackendWindowsMFT), AllocatedKnown: true, AllocationSource: "ntfs-mft", Workers: 1, Files: selectedFiles, Directories: directoryCount, LogicalBytes: logicalTotal, AllocatedBytes: allocatedTotal, StartedAt: started, FinishedAt: finished, Duration: finished.Sub(started), Complete: true}
	if summary.Duration > 0 {
		summary.EntriesPerSecond = float64(summary.Files+summary.Directories) / summary.Duration.Seconds()
	}
	report("NTFS MFT 扫描完成", true)
	manifest, err := os.Create(filepath.Join(opt.OutputDir, "manifest.json"))
	if err != nil {
		return FastScanSummary{}, true, err
	}
	defer manifest.Close()
	if err := writeJSONLine(manifest, summary); err != nil {
		return FastScanSummary{}, true, err
	}
	return summary, true, nil
}

func writeJSONLine(w io.Writer, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

func ntfsPathFileID(path string) (uint64, error) {
	h, err := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return 0, err
	}
	return (uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)) & 0x0000ffffffffffff, nil
}
