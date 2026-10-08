package filesystem

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// skipOnWindows 说明：快速扫描在 Windows 上使用 windows-mft 后端，
// 目录 ID 来自真实 MFT 记录号、树模式会带 NTFS 分配量信息，与 Unix 后端
// 语义不同；且部分测试使用了 Windows 文件名非法字符（如 |）。相关断言
// 仅对 Unix 后端有意义，Windows 上一律跳过。
func skipOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fast scanner tests assume Unix backend semantics; windows-mft backend differs")
	}
}

func TestFastScannerAggregatesNestedDirectories(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "root.txt"), []byte("root"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "a.txt"), []byte("alpha"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "b", "b.txt"), []byte("bravo"), 0644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "index")
	summary, err := NewFastScanner().Scan(context.Background(), FastScanOptions{
		Root:      root,
		OutputDir: out,
		Workers:   3,
		Metadata:  FastMetadataBasic,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if summary.Files != 3 || summary.Directories != 2 {
		t.Fatalf("unexpected counts: files=%d dirs=%d", summary.Files, summary.Directories)
	}
	if summary.LogicalBytes != 14 {
		t.Fatalf("unexpected logical bytes: %d", summary.LogicalBytes)
	}
	if summary.Errors != 0 || !summary.Complete {
		t.Fatalf("unexpected scan state: errors=%d complete=%v", summary.Errors, summary.Complete)
	}
	if summary.SchemaVersion != snapshotSchemaVersion {
		t.Fatalf("unexpected schema version: %d", summary.SchemaVersion)
	}

	data, err := os.ReadFile(filepath.Join(out, "directories.seg"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < segmentHeaderSize+60 {
		t.Fatalf("directory index is empty: %d bytes", len(data))
	}
	if string(data[:8]) != string(segmentMagic[:]) {
		t.Fatalf("directory index has no segment header: %q", data[:8])
	}
	// The last directory record is the root because aggregation completes
	// bottom-up. Its file count includes descendants.
	var rootID, rootFiles uint64
	for offset := segmentHeaderSize; offset+60 <= len(data); {
		rootID = binary.LittleEndian.Uint64(data[offset : offset+8])
		rootFiles = binary.LittleEndian.Uint64(data[offset+40 : offset+48])
		nameLen := int(binary.LittleEndian.Uint32(data[offset+56 : offset+60]))
		offset += 60 + nameLen
	}
	if rootID != 1 {
		t.Fatalf("expected root directory id 1, got %d", rootID)
	}
	if rootFiles != 3 {
		t.Fatalf("expected root aggregate file count 3, got %d", rootFiles)
	}
}

func TestFastScannerTreeModeAvoidsMetadataTotals(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("payload"), 0644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "index")
	summary, err := NewFastScanner().Scan(context.Background(), FastScanOptions{
		Root:      root,
		OutputDir: out,
		Metadata:  FastMetadataTree,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if summary.Files != 1 || summary.LogicalBytes != 0 || summary.AllocatedBytes != 0 || summary.AllocatedKnown {
		t.Fatalf("tree mode unexpectedly collected sizes: %+v", summary)
	}
}

func TestFastScannerProgressDoesNotCancelCompletedScan(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("payload"), 0644); err != nil {
		t.Fatal(err)
	}
	var progressCalls atomic.Int64
	summary, err := NewFastScanner().Scan(context.Background(), FastScanOptions{
		Root:             root,
		OutputDir:        filepath.Join(t.TempDir(), "index"),
		Metadata:         FastMetadataBasic,
		ProgressInterval: time.Nanosecond,
		Progress: func(ScanProgress) {
			progressCalls.Add(1)
		},
	})
	if err != nil {
		t.Fatalf("scan with progress: %v", err)
	}
	if !summary.Complete || summary.Files != 1 || progressCalls.Load() == 0 {
		t.Fatalf("unexpected progress scan result: summary=%+v calls=%d", summary, progressCalls.Load())
	}
}

func TestFastScannerHandlesWideDirectoryTrees(t *testing.T) {
	// 子目录名会用到 '|' 等字符（'a'+63），Windows 文件名不允许
	skipOnWindows(t)
	root := t.TempDir()
	for top := 0; top < 8; top++ {
		for child := 0; child < 64; child++ {
			dir := filepath.Join(root, "top", string(rune('a'+top)), string(rune('a'+child)))
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "payload"), []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}

	out := filepath.Join(t.TempDir(), "index")
	done := make(chan error, 1)
	go func() {
		_, err := NewFastScanner().Scan(context.Background(), FastScanOptions{
			Root:      root,
			OutputDir: out,
			Workers:   8,
			Metadata:  FastMetadataTree,
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wide directory scan did not complete")
	}
}

func TestProbeDisk(t *testing.T) {
	skipOnWindows(t) // ProbeDisk 未在 Windows 上实现
	probe, err := ProbeDisk(t.TempDir())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if probe.BlockSize == 0 || probe.Blocks == 0 {
		t.Fatalf("probe did not return filesystem geometry: %+v", probe)
	}
}
