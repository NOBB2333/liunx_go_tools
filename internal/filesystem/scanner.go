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
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// FastScanOptions controls the metadata scanner. The scanner writes an
// append-friendly binary index instead of producing a CSV and importing it a
// second time.
type FastScanOptions struct {
	Root             string
	OutputDir        string
	Workers          int
	Metadata         FastMetadataMode
	ProgressInterval time.Duration
	Progress         func(ScanProgress)

	// FollowSymlinks is intentionally false by default. Following links makes
	// cycles and cross-device walks possible and is not useful for a tree index.
	FollowSymlinks bool
}

type ScanProgress struct {
	Phase            string        `json:"phase"`
	Files            uint64        `json:"files"`
	Directories      uint64        `json:"directories"`
	CompletedDirs    uint64        `json:"completed_directories"`
	PendingDirs      uint64        `json:"pending_directories"`
	Errors           uint64        `json:"errors"`
	LogicalBytes     uint64        `json:"logical_bytes"`
	AllocatedBytes   uint64        `json:"allocated_bytes"`
	EntriesPerSecond float64       `json:"entries_per_second"`
	Elapsed          time.Duration `json:"elapsed_ns"`
	Message          string        `json:"message"`
}

// FastMetadataMode controls how much per-entry metadata is requested.
type FastMetadataMode string

const (
	FastMetadataTree  FastMetadataMode = "tree"
	FastMetadataBasic FastMetadataMode = "basic"
)

// FastScanSummary is written to manifest.json and returned by Scan.
type FastScanSummary struct {
	SchemaVersion    int           `json:"schema_version"`
	Root             string        `json:"root"`
	OutputDir        string        `json:"output_dir"`
	Metadata         string        `json:"metadata"`
	Workers          int           `json:"workers"`
	Files            uint64        `json:"files"`
	Directories      uint64        `json:"directories"`
	LogicalBytes     uint64        `json:"logical_bytes"`
	AllocatedBytes   uint64        `json:"allocated_bytes"`
	Errors           uint64        `json:"errors"`
	StartedAt        time.Time     `json:"started_at"`
	FinishedAt       time.Time     `json:"finished_at"`
	Duration         time.Duration `json:"duration_ns"`
	EntriesPerSecond float64       `json:"entries_per_second"`
	Complete         bool          `json:"complete"`
}

type fastDirTask struct {
	id       uint64
	parentID uint64
	name     string
	path     string
	depth    uint32
}

// dispatchFastTasks decouples directory discovery from worker scheduling. A
// worker can discover an arbitrarily wide directory without blocking all other
// workers while trying to enqueue its children.
func dispatchFastTasks(ctx context.Context, submissions <-chan fastDirTask, jobs chan<- fastDirTask) {
	defer close(jobs)
	queue := make([]fastDirTask, 0, cap(jobs))
	submissionsOpen := true
	for submissionsOpen || len(queue) > 0 {
		if len(queue) == 0 {
			select {
			case <-ctx.Done():
				return
			case task, ok := <-submissions:
				if !ok {
					submissionsOpen = false
					continue
				}
				queue = append(queue, task)
			}
			continue
		}

		var input <-chan fastDirTask
		if submissionsOpen {
			input = submissions
		}
		select {
		case <-ctx.Done():
			return
		case task, ok := <-input:
			if !ok {
				submissionsOpen = false
				continue
			}
			queue = append(queue, task)
		case jobs <- queue[0]:
			queue = queue[1:]
		}
	}
}

type fastFileRecord struct {
	parentID uint64
	name     string
	inode    uint64
	device   uint64
	size     int64
	blocks   int64
	mtimeNS  int64
	ctimeNS  int64
	btimeNS  int64
	mode     uint32
}

type fastDirRecord struct {
	id             uint64
	parentID       uint64
	name           string
	depth          uint32
	logicalBytes   uint64
	allocatedBytes uint64
	fileCount      uint64
	directoryCount uint64
}

type fastErrorRecord struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

type fastDirState struct {
	parentID       uint64
	name           string
	depth          uint32
	children       uint64
	completed      bool
	logicalBytes   uint64
	allocatedBytes uint64
	fileCount      uint64
	directoryCount uint64
}

type fastAggregator struct {
	mu     sync.Mutex
	states map[uint64]*fastDirState
	ready  chan<- fastDirRecord
	done   <-chan struct{}
	root   fastDirRecord
}

func newFastAggregator(ready chan<- fastDirRecord, done <-chan struct{}) *fastAggregator {
	return &fastAggregator{states: make(map[uint64]*fastDirState, 4096), ready: ready, done: done}
}

func (a *fastAggregator) addDirectory(id, parentID uint64, name string, depth uint32) {
	a.mu.Lock()
	a.states[id] = &fastDirState{parentID: parentID, name: name, depth: depth}
	if parent := a.states[parentID]; parent != nil {
		parent.children++
		parent.directoryCount++
	}
	a.mu.Unlock()
}

// finish closes a directory and propagates its complete aggregate upward. A
// directory is emitted only after all descendants have finished, so no second
// aggregation pass is needed.
func (a *fastAggregator) finish(id uint64, logical, allocated, files uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.states[id]
	if state == nil || state.completed {
		return
	}
	state.completed = true
	state.logicalBytes += logical
	state.allocatedBytes += allocated
	state.fileCount += files
	a.tryFinalizeLocked(id)
}

func (a *fastAggregator) childFinishedLocked(parentID uint64, child *fastDirState) {
	parent := a.states[parentID]
	if parent == nil {
		return
	}
	parent.children--
	parent.logicalBytes += child.logicalBytes
	parent.allocatedBytes += child.allocatedBytes
	parent.fileCount += child.fileCount
	parent.directoryCount += child.directoryCount
	a.tryFinalizeLocked(parentID)
}

func (a *fastAggregator) tryFinalizeLocked(id uint64) {
	state := a.states[id]
	if !state.completed || state.children != 0 {
		return
	}
	record := fastDirRecord{
		id:             id,
		parentID:       state.parentID,
		name:           state.name,
		depth:          state.depth,
		logicalBytes:   state.logicalBytes,
		allocatedBytes: state.allocatedBytes,
		fileCount:      state.fileCount,
		directoryCount: state.directoryCount,
	}
	if id == 1 {
		a.root = record
	}
	if a.ready != nil {
		select {
		case a.ready <- record:
		case <-a.done:
		}
	}
	delete(a.states, id)
	if id != 1 {
		parentID := record.parentID
		if parent := a.states[parentID]; parent != nil {
			a.childFinishedLocked(parentID, &fastDirState{
				logicalBytes:   record.logicalBytes,
				allocatedBytes: record.allocatedBytes,
				fileCount:      record.fileCount,
				directoryCount: record.directoryCount,
			})
		}
	}
}

// FastScanner performs a bounded parallel directory walk. It deliberately
// avoids filepath.WalkDir: workers own directory handles and only metadata
// records cross the channels.
type FastScanner struct{}

func NewFastScanner() *FastScanner { return &FastScanner{} }

func (s *FastScanner) Scan(ctx context.Context, opt FastScanOptions) (FastScanSummary, error) {
	if strings.TrimSpace(opt.Root) == "" {
		return FastScanSummary{}, errors.New("root folder is required")
	}
	root, err := filepath.Abs(filepath.Clean(opt.Root))
	if err != nil {
		return FastScanSummary{}, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return FastScanSummary{}, err
	}
	if !info.IsDir() {
		return FastScanSummary{}, fmt.Errorf("root is not a directory: %s", root)
	}
	if opt.OutputDir == "" {
		opt.OutputDir = filepath.Join("go-tool-result", "disk-index")
	}
	if opt.Workers <= 0 {
		opt.Workers = defaultFastScanWorkers()
	}
	if opt.Metadata == "" {
		opt.Metadata = FastMetadataBasic
	}
	if opt.Metadata != FastMetadataTree && opt.Metadata != FastMetadataBasic {
		return FastScanSummary{}, fmt.Errorf("unsupported metadata mode: %s", opt.Metadata)
	}
	if opt.ProgressInterval <= 0 {
		opt.ProgressInterval = 2 * time.Second
	}
	if opt.FollowSymlinks {
		return FastScanSummary{}, errors.New("follow-symlinks is not supported by the bounded scanner")
	}
	if err := os.MkdirAll(opt.OutputDir, 0755); err != nil {
		return FastScanSummary{}, err
	}

	started := time.Now()
	summary := FastScanSummary{
		SchemaVersion: snapshotSchemaVersion,
		Root:          root,
		OutputDir:     opt.OutputDir,
		Metadata:      string(opt.Metadata),
		Workers:       opt.Workers,
		StartedAt:     started,
	}

	files, err := os.OpenFile(filepath.Join(opt.OutputDir, "files.seg"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return FastScanSummary{}, err
	}
	dirs, err := os.OpenFile(filepath.Join(opt.OutputDir, "directories.seg"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		_ = files.Close()
		return FastScanSummary{}, err
	}
	if err := writeSegmentHeader(files, segmentKindFiles); err != nil {
		_ = files.Close()
		_ = dirs.Close()
		return FastScanSummary{}, err
	}
	if err := writeSegmentHeader(dirs, segmentKindDirectories); err != nil {
		_ = files.Close()
		_ = dirs.Close()
		return FastScanSummary{}, err
	}
	errs, err := os.OpenFile(filepath.Join(opt.OutputDir, "errors.ndjson"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		_ = files.Close()
		_ = dirs.Close()
		return FastScanSummary{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	fileWriter := newFastBinaryWriter(files)
	dirWriter := newFastBinaryWriter(dirs)
	errWriter := bufio.NewWriterSize(errs, 1<<20)
	var writerErrMu sync.Mutex
	var writerErr error
	setWriterErr := func(err error) {
		if err == nil {
			return
		}
		writerErrMu.Lock()
		if writerErr == nil {
			writerErr = err
		}
		writerErrMu.Unlock()
		cancel()
	}
	var writers sync.WaitGroup
	writers.Add(3)
	fileRecords := make(chan fastFileRecord, opt.Workers*128)
	dirRecords := make(chan fastDirRecord, opt.Workers*8)
	errorRecords := make(chan fastErrorRecord, opt.Workers*8)
	go func() {
		defer writers.Done()
		for record := range fileRecords {
			if err := fileWriter.writeFile(record); err != nil {
				setWriterErr(err)
				return
			}
		}
		setWriterErr(fileWriter.flush())
	}()
	go func() {
		defer writers.Done()
		for record := range dirRecords {
			if err := dirWriter.writeDir(record); err != nil {
				setWriterErr(err)
				return
			}
		}
		setWriterErr(dirWriter.flush())
	}()
	go func() {
		defer writers.Done()
		defer errs.Close()
		for record := range errorRecords {
			data, err := json.Marshal(record)
			if err != nil {
				setWriterErr(err)
				return
			}
			if _, err := errWriter.Write(append(data, '\n')); err != nil {
				setWriterErr(err)
				return
			}
		}
		if err := errWriter.Flush(); err != nil {
			setWriterErr(err)
		}
	}()

	jobs := make(chan fastDirTask, opt.Workers)
	submissions := make(chan fastDirTask, opt.Workers*2)
	var pending sync.WaitGroup
	pending.Add(1)
	var nextID atomic.Uint64
	nextID.Store(1)
	aggregator := newFastAggregator(dirRecords, ctx.Done())
	aggregator.addDirectory(1, 0, filepath.Base(root), 0)
	go dispatchFastTasks(ctx, submissions, jobs)
	submissions <- fastDirTask{id: 1, name: filepath.Base(root), path: root, depth: 0}

	var filesSeen atomic.Uint64
	var dirsSeen atomic.Uint64
	var completedDirs atomic.Uint64
	var logicalBytes atomic.Uint64
	var allocatedBytes atomic.Uint64
	var errorCount atomic.Uint64
	var workers sync.WaitGroup
	progressDone := make(chan struct{})
	progressStop := make(chan struct{})
	if opt.Progress != nil {
		go func() {
			ticker := time.NewTicker(opt.ProgressInterval)
			defer ticker.Stop()
			defer close(progressDone)
			for {
				select {
				case <-ticker.C:
					emitScanProgress(opt.Progress, started, filesSeen.Load(), dirsSeen.Load(), completedDirs.Load(), errorCount.Load(), logicalBytes.Load(), allocatedBytes.Load(), "扫描目录项中")
				case <-progressStop:
					return
				}
			}
		}()
	} else {
		close(progressDone)
	}
	worker := func() {
		defer workers.Done()
		for task := range jobs {
			if ctx.Err() != nil {
				pending.Done()
				continue
			}
			localLogical, localAllocated, localFiles := s.scanDirectory(ctx, task, opt, &nextID, &pending, submissions, fileRecords, errorRecords, &dirsSeen, &filesSeen, &logicalBytes, &allocatedBytes, &errorCount, aggregator)
			aggregator.finish(task.id, localLogical, localAllocated, localFiles)
			completedDirs.Add(1)
			pending.Done()
		}
	}
	for i := 0; i < opt.Workers; i++ {
		workers.Add(1)
		go worker()
	}
	go func() {
		pending.Wait()
		close(submissions)
	}()
	workers.Wait()
	if opt.Progress != nil {
		close(progressStop)
		<-progressDone
		emitScanProgress(opt.Progress, started, filesSeen.Load(), dirsSeen.Load(), completedDirs.Load(), errorCount.Load(), logicalBytes.Load(), allocatedBytes.Load(), "目录枚举完成")
	}
	close(fileRecords)
	close(dirRecords)
	close(errorRecords)
	writers.Wait()
	_ = files.Close()
	_ = dirs.Close()
	writerErrMu.Lock()
	writeErr := writerErr
	writerErrMu.Unlock()
	if writeErr != nil {
		return summary, writeErr
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}

	summary.Files = filesSeen.Load()
	summary.Directories = dirsSeen.Load()
	summary.LogicalBytes = logicalBytes.Load()
	summary.AllocatedBytes = allocatedBytes.Load()
	summary.Errors = errorCount.Load()
	summary.FinishedAt = time.Now()
	summary.Duration = summary.FinishedAt.Sub(summary.StartedAt)
	if summary.Duration > 0 {
		summary.EntriesPerSecond = float64(summary.Files+summary.Directories) / summary.Duration.Seconds()
	}
	summary.Complete = true
	manifest, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return summary, err
	}
	if err := os.WriteFile(filepath.Join(opt.OutputDir, "manifest.json"), append(manifest, '\n'), 0644); err != nil {
		return summary, err
	}
	return summary, nil
}

func emitScanProgress(progress func(ScanProgress), started time.Time, files, dirs, completed, errors, logical, allocated uint64, message string) {
	if progress == nil {
		return
	}
	elapsed := time.Since(started)
	rate := 0.0
	if elapsed > 0 {
		rate = float64(files+dirs) / elapsed.Seconds()
	}
	pending := uint64(0)
	if dirs+1 > completed {
		pending = dirs + 1 - completed
	}
	progress(ScanProgress{Phase: "scan", Files: files, Directories: dirs, CompletedDirs: completed, PendingDirs: pending, Errors: errors, LogicalBytes: logical, AllocatedBytes: allocated, EntriesPerSecond: rate, Elapsed: elapsed, Message: message})
}

func (s *FastScanner) scanDirectory(
	ctx context.Context,
	task fastDirTask,
	opt FastScanOptions,
	nextID *atomic.Uint64,
	pending *sync.WaitGroup,
	jobs chan<- fastDirTask,
	fileRecords chan<- fastFileRecord,
	errorRecords chan<- fastErrorRecord,
	dirsSeen, filesSeen, logicalBytes, allocatedBytes, errorCount *atomic.Uint64,
	aggregator *fastAggregator,
) (uint64, uint64, uint64) {
	return scanDirectoryNative(ctx, task, opt, nextID, pending, jobs, fileRecords, errorRecords, dirsSeen, filesSeen, logicalBytes, allocatedBytes, errorCount, aggregator)
}

func scanDirectoryPortable(
	ctx context.Context,
	task fastDirTask,
	opt FastScanOptions,
	nextID *atomic.Uint64,
	pending *sync.WaitGroup,
	jobs chan<- fastDirTask,
	fileRecords chan<- fastFileRecord,
	errorRecords chan<- fastErrorRecord,
	dirsSeen, filesSeen, logicalBytes, allocatedBytes, errorCount *atomic.Uint64,
	aggregator *fastAggregator,
) (uint64, uint64, uint64) {
	dir, err := os.Open(task.path)
	if err != nil {
		errorCount.Add(1)
		sendFastError(ctx, errorRecords, task.path, err)
		return 0, 0, 0
	}
	defer dir.Close()

	var localLogical, localAllocated, localFiles uint64
	for {
		entries, readErr := dir.ReadDir(256)
		for _, entry := range entries {
			if ctx.Err() != nil {
				return localLogical, localAllocated, localFiles
			}
			name := entry.Name()
			if name == "." || name == ".." {
				continue
			}
			childPath := filepath.Join(task.path, name)
			var info os.FileInfo
			isDir := entry.IsDir()
			if !isDir && entry.Type() == 0 {
				if statInfo, statErr := entry.Info(); statErr == nil {
					info = statInfo
					isDir = info.IsDir()
				}
			}
			if isDir {
				id := nextID.Add(1)
				if id == 0 {
					id = nextID.Add(1)
				}
				pending.Add(1)
				// Register before enqueueing: the parent cannot finalize while a
				// child task is outstanding.
				aggregator.addDirectory(id, task.id, name, task.depth+1)
				dirsSeen.Add(1)
				select {
				case jobs <- fastDirTask{id: id, parentID: task.id, name: name, path: childPath, depth: task.depth + 1}:
				case <-ctx.Done():
					pending.Done()
				}
				continue
			}

			var record fastFileRecord
			record.parentID = task.id
			record.name = name
			if opt.Metadata == FastMetadataBasic {
				if info == nil {
					var statErr error
					info, statErr = entry.Info()
					if statErr != nil {
						errorCount.Add(1)
						sendFastError(ctx, errorRecords, childPath, statErr)
						continue
					}
				}
				if info == nil {
					errorCount.Add(1)
					sendFastError(ctx, errorRecords, childPath, errors.New("metadata unavailable"))
					continue
				}
				record.size = info.Size()
				record.blocks = allocatedBlocks(info)
				record.mtimeNS = info.ModTime().UnixNano()
				record.ctimeNS, record.btimeNS = fileTimes(info)
				record.mode = uint32(info.Mode())
				record.inode, record.device = inodeDevice(info)
				if record.size > 0 {
					localLogical += uint64(record.size)
					logicalBytes.Add(uint64(record.size))
				}
				if record.blocks > 0 {
					localAllocated += uint64(record.blocks) * 512
					allocatedBytes.Add(uint64(record.blocks) * 512)
				}
			}
			localFiles++
			filesSeen.Add(1)
			select {
			case fileRecords <- record:
			case <-ctx.Done():
				return localLogical, localAllocated, localFiles
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				errorCount.Add(1)
				sendFastError(ctx, errorRecords, task.path, readErr)
			}
			break
		}
		if len(entries) == 0 {
			break
		}
	}
	return localLogical, localAllocated, localFiles
}

func sendFastError(ctx context.Context, ch chan<- fastErrorRecord, path string, err error) {
	select {
	case ch <- fastErrorRecord{Path: path, Error: err.Error()}:
	case <-ctx.Done():
	}
}

type fastBinaryWriter struct {
	w *bufio.Writer
}

func newFastBinaryWriter(out io.Writer) *fastBinaryWriter {
	return &fastBinaryWriter{w: bufio.NewWriterSize(out, 8<<20)}
}

func (w *fastBinaryWriter) writeFile(record fastFileRecord) error {
	var header [72]byte
	binary.LittleEndian.PutUint64(header[0:8], record.parentID)
	binary.LittleEndian.PutUint64(header[8:16], record.inode)
	binary.LittleEndian.PutUint64(header[16:24], record.device)
	binary.LittleEndian.PutUint64(header[24:32], uint64(record.size))
	binary.LittleEndian.PutUint64(header[32:40], uint64(record.blocks))
	binary.LittleEndian.PutUint64(header[40:48], uint64(record.mtimeNS))
	binary.LittleEndian.PutUint64(header[48:56], uint64(record.ctimeNS))
	binary.LittleEndian.PutUint64(header[56:64], uint64(record.btimeNS))
	binary.LittleEndian.PutUint32(header[64:68], record.mode)
	binary.LittleEndian.PutUint32(header[68:72], uint32(len(record.name)))
	if _, err := w.w.Write(header[:72]); err != nil {
		return err
	}
	_, err := w.w.WriteString(record.name)
	return err
}

func (w *fastBinaryWriter) writeDir(record fastDirRecord) error {
	var header [72]byte
	binary.LittleEndian.PutUint64(header[0:8], record.id)
	binary.LittleEndian.PutUint64(header[8:16], record.parentID)
	binary.LittleEndian.PutUint32(header[16:20], record.depth)
	binary.LittleEndian.PutUint64(header[24:32], record.logicalBytes)
	binary.LittleEndian.PutUint64(header[32:40], record.allocatedBytes)
	binary.LittleEndian.PutUint64(header[40:48], record.fileCount)
	binary.LittleEndian.PutUint64(header[48:56], record.directoryCount)
	binary.LittleEndian.PutUint32(header[56:60], uint32(len(record.name)))
	if _, err := w.w.Write(header[:60]); err != nil {
		return err
	}
	_, err := w.w.WriteString(record.name)
	return err
}

// flush is kept separate so future segment rotation can reuse the writer.
func (w *fastBinaryWriter) flush() error { return w.w.Flush() }

func allocatedBlocks(info os.FileInfo) int64 {
	value := reflect.ValueOf(info.Sys())
	for value.IsValid() && (value.Kind() == reflect.Ptr || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return 0
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0
	}
	return reflectInt64(value.FieldByName("Blocks"))
}

func inodeDevice(info os.FileInfo) (uint64, uint64) {
	value := reflect.ValueOf(info.Sys())
	for value.IsValid() && (value.Kind() == reflect.Ptr || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return 0, 0
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0, 0
	}
	return uint64(reflectInt64(value.FieldByName("Ino"))), uint64(reflectInt64(value.FieldByName("Dev")))
}

func reflectInt64(value reflect.Value) int64 {
	if !value.IsValid() {
		return 0
	}
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(value.Uint())
	default:
		return 0
	}
}

func fileTimes(info os.FileInfo) (ctimeNS, birthtimeNS int64) {
	value := reflect.ValueOf(info.Sys())
	for value.IsValid() && (value.Kind() == reflect.Ptr || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return 0, 0
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0, 0
	}
	ctimeNS = reflectTimestamp(value.FieldByName("Ctimespec"))
	if ctimeNS == 0 {
		ctimeNS = reflectTimestamp(value.FieldByName("Ctim"))
	}
	birthtimeNS = reflectTimestamp(value.FieldByName("Birthtimespec"))
	if birthtimeNS == 0 {
		birthtimeNS = reflectWindowsFiletime(value.FieldByName("CreationTime"))
	}
	return ctimeNS, birthtimeNS
}

func reflectTimestamp(value reflect.Value) int64 {
	for value.IsValid() && (value.Kind() == reflect.Ptr || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return 0
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0
	}
	sec := reflectInt64(value.FieldByName("Sec"))
	nsec := reflectInt64(value.FieldByName("Nsec"))
	if sec != 0 || nsec != 0 {
		return sec*1e9 + nsec
	}
	return reflectWindowsFiletime(value)
}

func reflectWindowsFiletime(value reflect.Value) int64 {
	for value.IsValid() && (value.Kind() == reflect.Ptr || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return 0
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0
	}
	low := uint64(reflectInt64(value.FieldByName("LowDateTime")))
	high := uint64(reflectInt64(value.FieldByName("HighDateTime")))
	if low == 0 && high == 0 {
		return 0
	}
	const windowsToUnix100ns = 116444736000000000
	value100ns := (high << 32) | low
	if value100ns <= windowsToUnix100ns {
		return 0
	}
	return int64(value100ns-windowsToUnix100ns) * 100
}
