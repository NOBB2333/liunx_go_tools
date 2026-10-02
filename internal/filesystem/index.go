package filesystem

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/glebarez/go-sqlite"
)

// SnapshotManifest is the stable metadata contract shared by the scanner,
// index builder and HTTP API. Unknown fields are intentionally ignored so the
// reader remains forward compatible with newer scanners.
type SnapshotManifest struct {
	SchemaVersion    int       `json:"schema_version,omitempty"`
	Root             string    `json:"root"`
	OutputDir        string    `json:"output_dir"`
	Metadata         string    `json:"metadata"`
	ScannerBackend   string    `json:"scanner_backend,omitempty"`
	AllocatedKnown   bool      `json:"allocated_bytes_known"`
	AllocationSource string    `json:"allocation_source,omitempty"`
	Workers          int       `json:"workers"`
	Files            uint64    `json:"files"`
	Directories      uint64    `json:"directories"`
	LogicalBytes     uint64    `json:"logical_bytes"`
	AllocatedBytes   uint64    `json:"allocated_bytes"`
	Errors           uint64    `json:"errors"`
	StartedAt        time.Time `json:"started_at"`
	FinishedAt       time.Time `json:"finished_at"`
	Duration         int64     `json:"duration_ns"`
	EntriesPerSecond float64   `json:"entries_per_second"`
	Complete         bool      `json:"complete"`
}

type BuildOptions struct {
	SnapshotDir      string
	DatabasePath     string
	BatchSize        int
	ProgressInterval time.Duration
	Progress         func(BuildProgress)
}

type BuildProgress struct {
	Phase         string        `json:"phase"`
	Current       uint64        `json:"current"`
	Total         uint64        `json:"total"`
	Percent       float64       `json:"percent"`
	Elapsed       time.Duration `json:"elapsed_ns"`
	Message       string        `json:"message"`
	Indeterminate bool          `json:"indeterminate"`
}

type BuildSummary struct {
	SnapshotDir   string        `json:"snapshot_dir"`
	Database      string        `json:"database"`
	Files         uint64        `json:"files"`
	Directories   uint64        `json:"directories"`
	Duration      time.Duration `json:"duration_ns"`
	DurationHuman string        `json:"duration"`
}

type IndexBuilder struct{}

type extensionAggregate struct {
	Files          uint64
	Bytes          int64
	AllocatedBytes int64
}

func NewIndexBuilder() *IndexBuilder { return &IndexBuilder{} }

func ReadManifest(snapshotDir string) (SnapshotManifest, error) {
	return readManifest(snapshotDir)
}

func (b *IndexBuilder) Build(ctx context.Context, opt BuildOptions) (BuildSummary, error) {
	if strings.TrimSpace(opt.SnapshotDir) == "" {
		return BuildSummary{}, errors.New("snapshot directory is required")
	}
	if opt.BatchSize <= 0 {
		opt.BatchSize = 10000
	}
	if opt.ProgressInterval <= 0 {
		opt.ProgressInterval = 2 * time.Second
	}
	snapshotDir, err := filepath.Abs(filepath.Clean(opt.SnapshotDir))
	if err != nil {
		return BuildSummary{}, err
	}
	if opt.DatabasePath == "" {
		opt.DatabasePath = filepath.Join(snapshotDir, "query.db")
	}
	if err := os.MkdirAll(filepath.Dir(opt.DatabasePath), 0755); err != nil {
		return BuildSummary{}, err
	}

	manifest, err := readManifest(snapshotDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return BuildSummary{}, fmt.Errorf(
				"not a golangtools snapshot: %s is missing manifest.json; run filesystem scan -root <source-directory> -output %s first",
				snapshotDir,
				snapshotDir,
			)
		}
		return BuildSummary{}, fmt.Errorf("read snapshot manifest: %w", err)
	}
	if !manifest.Complete {
		return BuildSummary{}, errors.New("snapshot scan is not complete")
	}
	if _, err := os.Stat(filepath.Join(snapshotDir, "files.seg")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return BuildSummary{}, fmt.Errorf("invalid golangtools snapshot: %s is missing files.seg; run filesystem scan again", snapshotDir)
		}
		return BuildSummary{}, fmt.Errorf("files.seg: %w", err)
	}
	if _, err := os.Stat(filepath.Join(snapshotDir, "directories.seg")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return BuildSummary{}, fmt.Errorf("invalid golangtools snapshot: %s is missing directories.seg; run filesystem scan again", snapshotDir)
		}
		return BuildSummary{}, fmt.Errorf("directories.seg: %w", err)
	}

	tmpPath := fmt.Sprintf("%s.tmp.%d", opt.DatabasePath, os.Getpid())
	_ = os.Remove(tmpPath)
	db, err := sql.Open("sqlite", tmpPath)
	if err != nil {
		return BuildSummary{}, err
	}
	defer db.Close()
	if err := configureBuildDatabase(db); err != nil {
		_ = os.Remove(tmpPath)
		return BuildSummary{}, err
	}
	if err := createSchema(ctx, db); err != nil {
		_ = os.Remove(tmpPath)
		return BuildSummary{}, err
	}

	started := time.Now()
	report := func(phase string, current, total uint64, message string, indeterminate bool) {
		if opt.Progress == nil {
			return
		}
		percent := 0.0
		if total > 0 {
			percent = float64(current) * 100 / float64(total)
		}
		opt.Progress(BuildProgress{Phase: phase, Current: current, Total: total, Percent: percent, Elapsed: time.Since(started), Message: message, Indeterminate: indeterminate})
	}
	report("index", 0, manifest.Directories+1, "开始构建查询索引", false)
	directoryProgress := rateLimitedBuildProgress(opt.ProgressInterval, func(current uint64) {
		report("directories", current, manifest.Directories+1, "导入目录记录", false)
	})
	dirCount, err := importDirectories(ctx, db, filepath.Join(snapshotDir, "directories.seg"), opt.BatchSize, func(current uint64) {
		directoryProgress(current)
	})
	if err != nil {
		_ = os.Remove(tmpPath)
		return BuildSummary{}, err
	}
	report("directories", dirCount, manifest.Directories+1, "目录记录导入完成", false)
	fileProgress := rateLimitedBuildProgress(opt.ProgressInterval, func(current uint64) {
		report("files", current, manifest.Files, "导入文件记录", false)
	})
	fileCount, extensionStats, err := importFiles(ctx, db, filepath.Join(snapshotDir, "files.seg"), opt.BatchSize, func(current uint64) {
		fileProgress(current)
	})
	if err != nil {
		_ = os.Remove(tmpPath)
		return BuildSummary{}, err
	}
	report("files", fileCount, manifest.Files, "文件记录导入完成", false)
	if err := writeExtensionStats(ctx, db, extensionStats); err != nil {
		_ = os.Remove(tmpPath)
		return BuildSummary{}, err
	}
	report("extensions", 1, 1, "扩展名统计完成", false)
	report("search", 0, 0, "开始构建文件名搜索索引", true)
	if opt.Progress != nil {
		stop := make(chan struct{})
		go func() {
			ticker := time.NewTicker(opt.ProgressInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					report("search", 0, 0, "正在构建文件名搜索索引", true)
				case <-stop:
					return
				}
			}
		}()
		err := buildSearchIndex(ctx, db)
		close(stop)
		if err != nil {
			_ = os.Remove(tmpPath)
			return BuildSummary{}, err
		}
	} else if err := buildSearchIndex(ctx, db); err != nil {
		_ = os.Remove(tmpPath)
		return BuildSummary{}, err
	}
	report("search", 1, 1, "文件名搜索索引完成", false)
	if err := createIndexes(ctx, db, opt.ProgressInterval, func(current, total uint64, message string, indeterminate bool) {
		report("sqlite", current, total, message, indeterminate)
	}); err != nil {
		_ = os.Remove(tmpPath)
		return BuildSummary{}, err
	}
	report("metadata", 0, 1, "正在写入索引元数据", false)
	if err := writeMetadata(ctx, db, manifest); err != nil {
		_ = os.Remove(tmpPath)
		return BuildSummary{}, err
	}
	report("metadata", 1, 1, "索引元数据写入完成", false)
	report("finalize", 0, 2, "正在刷新并关闭 SQLite 数据库", false)
	if err := db.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return BuildSummary{}, err
	}
	report("finalize", 1, 2, "SQLite 数据库已刷新，正在发布 query.db", false)
	if err := os.Rename(tmpPath, opt.DatabasePath); err != nil {
		// Unix rename replaces the target atomically. Windows may reject that
		// replacement when the previous sidecar is still open, so retry after
		// removing the intended target only on that fallback path.
		if removeErr := os.Remove(opt.DatabasePath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			_ = os.Remove(tmpPath)
			return BuildSummary{}, err
		}
		if retryErr := os.Rename(tmpPath, opt.DatabasePath); retryErr != nil {
			_ = os.Remove(tmpPath)
			return BuildSummary{}, retryErr
		}
	}
	report("finalize", 2, 2, "query.db 发布完成", false)
	duration := time.Since(started)
	return BuildSummary{
		SnapshotDir:   snapshotDir,
		Database:      opt.DatabasePath,
		Files:         fileCount,
		Directories:   dirCount,
		Duration:      duration,
		DurationHuman: humanDuration(duration),
	}, nil
}

func rateLimitedBuildProgress(interval time.Duration, report func(uint64)) func(uint64) {
	var last time.Time
	return func(current uint64) {
		now := time.Now()
		if current == 1 || last.IsZero() || now.Sub(last) >= interval {
			last = now
			report(current)
		}
	}
}

func humanDuration(duration time.Duration) string {
	if duration < time.Second {
		return duration.Round(time.Millisecond).String()
	}
	return duration.Round(time.Second).String()
}

func readManifest(snapshotDir string) (SnapshotManifest, error) {
	data, err := os.ReadFile(filepath.Join(snapshotDir, "manifest.json"))
	if err != nil {
		return SnapshotManifest{}, err
	}
	var manifest SnapshotManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return SnapshotManifest{}, err
	}
	// Snapshots predating the explicit capability flag can still expose their
	// collected block total. Old Windows snapshots have a zero total and remain
	// unknown; Unix snapshots with a nonzero total retain their old behavior.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err == nil {
		if _, present := fields["allocated_bytes_known"]; !present && manifest.AllocatedBytes > 0 {
			manifest.AllocatedKnown = true
			if manifest.AllocationSource == "" {
				manifest.AllocationSource = "legacy-stat"
			}
		}
	}
	return manifest, nil
}

func configureBuildDatabase(db *sql.DB) error {
	for _, statement := range []string{
		"PRAGMA journal_mode=OFF",
		"PRAGMA synchronous=OFF",
		"PRAGMA temp_store=MEMORY",
		"PRAGMA locking_mode=EXCLUSIVE",
		"PRAGMA cache_size=-262144",
		"PRAGMA mmap_size=1073741824",
		"PRAGMA threads=4",
		"PRAGMA user_version=4",
	} {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func createSchema(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE directories (
			id INTEGER PRIMARY KEY,
			parent_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			depth INTEGER NOT NULL,
			logical_bytes INTEGER NOT NULL,
			allocated_bytes INTEGER NOT NULL,
			file_count INTEGER NOT NULL,
			directory_count INTEGER NOT NULL
		)`,
		`CREATE TABLE files (
			id INTEGER PRIMARY KEY,
			parent_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			inode INTEGER NOT NULL,
			device INTEGER NOT NULL,
			size INTEGER NOT NULL,
			blocks INTEGER NOT NULL,
			mtime_ns INTEGER NOT NULL,
			ctime_ns INTEGER NOT NULL,
			birthtime_ns INTEGER NOT NULL,
			mode INTEGER NOT NULL,
			extension TEXT NOT NULL
		)`,
		`CREATE TABLE extension_stats (
				extension TEXT PRIMARY KEY,
				files INTEGER NOT NULL,
				bytes INTEGER NOT NULL,
				allocated_bytes INTEGER NOT NULL
		)`,
		`CREATE VIRTUAL TABLE file_search USING fts5(
			name,
			content='files',
			content_rowid='id',
			tokenize='trigram'
		)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func buildSearchIndex(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `INSERT INTO file_search(file_search) VALUES('rebuild')`)
	return err
}

type indexDefinition struct {
	name      string
	statement string
}

func createIndexes(ctx context.Context, db *sql.DB, interval time.Duration, progress func(uint64, uint64, string, bool)) error {
	indexes := []indexDefinition{
		{name: "目录名称", statement: `CREATE INDEX idx_directories_parent_name ON directories(parent_id, name COLLATE NOCASE, id)`},
		{name: "目录实际占用", statement: `CREATE INDEX idx_directories_parent_allocated ON directories(parent_id, allocated_bytes DESC, id)`},
		{name: "目录内文件名称", statement: `CREATE INDEX idx_files_parent_name ON files(parent_id, name COLLATE NOCASE, id)`},
		{name: "目录内文件实际占用", statement: `CREATE INDEX idx_files_parent_blocks ON files(parent_id, blocks DESC, id)`},
		{name: "目录内文件修改时间", statement: `CREATE INDEX idx_files_parent_mtime ON files(parent_id, mtime_ns DESC, id)`},
	}
	total := uint64(len(indexes))
	for index, definition := range indexes {
		current := uint64(index)
		if progress != nil {
			progress(current, total, "开始创建 SQLite 索引："+definition.name, false)
		}
		stop := make(chan struct{})
		if progress != nil {
			go func(current uint64, name string) {
				ticker := time.NewTicker(interval)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						progress(current, total, "正在创建 SQLite 索引："+name, true)
					case <-stop:
						return
					}
				}
			}(current, definition.name)
		}
		_, err := db.ExecContext(ctx, definition.statement)
		if progress != nil {
			close(stop)
		}
		if err != nil {
			return err
		}
		if progress != nil {
			progress(current+1, total, "SQLite 索引完成："+definition.name, false)
		}
	}
	return nil
}

func writeMetadata(ctx context.Context, db *sql.DB, manifest SnapshotManifest) error {
	values := map[string]string{
		"schema_version":      fmt.Sprintf("%d", snapshotSchemaVersion),
		"root":                manifest.Root,
		"metadata":            manifest.Metadata,
		"scanner_backend":     manifest.ScannerBackend,
		"allocated_known":     fmt.Sprintf("%t", manifest.AllocatedKnown),
		"allocation_source":   manifest.AllocationSource,
		"files":               fmt.Sprintf("%d", manifest.Files),
		"directories":         fmt.Sprintf("%d", manifest.Directories),
		"logical_bytes":       fmt.Sprintf("%d", manifest.LogicalBytes),
		"allocated_bytes":     fmt.Sprintf("%d", manifest.AllocatedBytes),
		"scan_errors":         fmt.Sprintf("%d", manifest.Errors),
		"scan_entries_second": fmt.Sprintf("%f", manifest.EntriesPerSecond),
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO metadata(key, value) VALUES(?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for key, value := range values {
		if _, err := stmt.ExecContext(ctx, key, value); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

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

func importDirectories(ctx context.Context, db *sql.DB, path string, batchSize int, progress func(uint64)) (uint64, error) {
	reader, file, err := newSegmentReader(path, segmentKindDirectories)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	var count uint64
	var tx *sql.Tx
	var stmt *sql.Stmt
	closeBatch := func(commit bool) error {
		if stmt != nil {
			_ = stmt.Close()
			stmt = nil
		}
		if tx == nil {
			return nil
		}
		var err error
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		tx = nil
		return err
	}
	for {
		header, name, readErr := reader.readRecord(60, 56)
		if errors.Is(readErr, io.EOF) {
			return count, closeBatch(true)
		}
		if readErr != nil {
			_ = closeBatch(false)
			return count, readErr
		}
		if tx == nil {
			tx, err = db.BeginTx(ctx, nil)
			if err != nil {
				return count, err
			}
			stmt, err = tx.PrepareContext(ctx, `INSERT INTO directories(id,parent_id,name,depth,logical_bytes,allocated_bytes,file_count,directory_count) VALUES(?,?,?,?,?,?,?,?)`)
			if err != nil {
				_ = closeBatch(false)
				return count, err
			}
		}
		if _, err := stmt.ExecContext(ctx,
			binary.LittleEndian.Uint64(header[0:8]),
			binary.LittleEndian.Uint64(header[8:16]),
			name,
			binary.LittleEndian.Uint32(header[16:20]),
			binary.LittleEndian.Uint64(header[24:32]),
			binary.LittleEndian.Uint64(header[32:40]),
			binary.LittleEndian.Uint64(header[40:48]),
			binary.LittleEndian.Uint64(header[48:56]),
		); err != nil {
			_ = closeBatch(false)
			return count, err
		}
		count++
		if progress != nil && (count%10000 == 0 || count == 1) {
			progress(count)
		}
		if count%uint64(batchSize) == 0 {
			if err := closeBatch(true); err != nil {
				return count, err
			}
		}
		if count%10000 == 0 {
			select {
			case <-ctx.Done():
				_ = closeBatch(false)
				return count, ctx.Err()
			default:
			}
		}
	}
}

func importFiles(ctx context.Context, db *sql.DB, path string, batchSize int, progress func(uint64)) (uint64, map[string]extensionAggregate, error) {
	reader, file, err := newSegmentReader(path, segmentKindFiles)
	if err != nil {
		return 0, nil, err
	}
	defer file.Close()
	headerSize, nameOffset := 56, 52
	if reader.version >= segmentFormatVersion {
		headerSize, nameOffset = 72, 68
	}
	var count uint64
	extensionStats := make(map[string]extensionAggregate)
	var tx *sql.Tx
	var stmt *sql.Stmt
	closeBatch := func(commit bool) error {
		if stmt != nil {
			_ = stmt.Close()
			stmt = nil
		}
		if tx == nil {
			return nil
		}
		var err error
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		tx = nil
		return err
	}
	for {
		header, name, readErr := reader.readRecord(headerSize, nameOffset)
		if errors.Is(readErr, io.EOF) {
			return count, extensionStats, closeBatch(true)
		}
		if readErr != nil {
			_ = closeBatch(false)
			return count, nil, readErr
		}
		if tx == nil {
			tx, err = db.BeginTx(ctx, nil)
			if err != nil {
				return count, nil, err
			}
			stmt, err = tx.PrepareContext(ctx, `INSERT INTO files(id,parent_id,name,inode,device,size,blocks,mtime_ns,ctime_ns,birthtime_ns,mode,extension) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`)
			if err != nil {
				_ = closeBatch(false)
				return count, nil, err
			}
		}
		extension := fileExtension(name)
		if _, err := stmt.ExecContext(ctx,
			count+1,
			binary.LittleEndian.Uint64(header[0:8]),
			name,
			binary.LittleEndian.Uint64(header[8:16]),
			binary.LittleEndian.Uint64(header[16:24]),
			int64(binary.LittleEndian.Uint64(header[24:32])),
			int64(binary.LittleEndian.Uint64(header[32:40])),
			int64(binary.LittleEndian.Uint64(header[40:48])),
			fileCTime(header),
			fileBirthTime(header),
			fileMode(header),
			extension,
		); err != nil {
			_ = closeBatch(false)
			return count, nil, err
		}
		if extension != "" {
			aggregate := extensionStats[extension]
			aggregate.Files++
			aggregate.Bytes += int64(binary.LittleEndian.Uint64(header[24:32]))
			aggregate.AllocatedBytes += int64(binary.LittleEndian.Uint64(header[32:40])) * 512
			extensionStats[extension] = aggregate
		}
		count++
		if progress != nil && (count%10000 == 0 || count == 1) {
			progress(count)
		}
		if count%uint64(batchSize) == 0 {
			if err := closeBatch(true); err != nil {
				return count, nil, err
			}
		}
		if count%10000 == 0 {
			select {
			case <-ctx.Done():
				_ = closeBatch(false)
				return count, nil, ctx.Err()
			default:
			}
		}
	}
}

func fileCTime(header []byte) int64 {
	if len(header) < 56+8 {
		return 0
	}
	return int64(binary.LittleEndian.Uint64(header[48:56]))
}

func fileBirthTime(header []byte) int64 {
	if len(header) < 64+8 {
		return 0
	}
	return int64(binary.LittleEndian.Uint64(header[56:64]))
}

func fileMode(header []byte) uint32 {
	if len(header) >= 72 {
		return binary.LittleEndian.Uint32(header[64:68])
	}
	return binary.LittleEndian.Uint32(header[48:52])
}

func writeExtensionStats(ctx context.Context, db *sql.DB, stats map[string]extensionAggregate) error {
	if len(stats) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO extension_stats(extension, files, bytes, allocated_bytes) VALUES(?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for extension, aggregate := range stats {
		if _, err := stmt.ExecContext(ctx, extension, aggregate.Files, aggregate.Bytes, aggregate.AllocatedBytes); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func fileExtension(name string) string {
	index := strings.LastIndexByte(name, '.')
	if index <= 0 || index == len(name)-1 {
		return ""
	}
	return strings.ToLower(name[index:])
}
