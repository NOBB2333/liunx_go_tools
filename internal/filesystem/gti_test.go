package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPackFastSnapshotProducesPortableSingleFile(t *testing.T) {
	snapshot := t.TempDir()
	root := filepath.Join(snapshot, "root")
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "report.txt"), []byte("report"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFastScanner().Scan(context.Background(), FastScanOptions{Root: root, OutputDir: snapshot, Metadata: FastMetadataBasic}); err != nil {
		t.Fatal(err)
	}
	if err := BuildFastIndex(context.Background(), snapshot, nil); err != nil {
		t.Fatal(err)
	}
	if err := PackFastSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(snapshot, "snapshot.gti")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"manifest.json", "files.seg", "directories.seg", "tree.index.json", "tree.dirs", "tree.files", "tree.names", "tree.children", "tree.ranges", "tree.dirids", "tree.extensions.json"} {
		if _, err := os.Stat(filepath.Join(snapshot, name)); !os.IsNotExist(err) {
			t.Fatalf("legacy file remains: %s", name)
		}
	}
	server, err := OpenServer(ServerOptions{SnapshotDir: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if server.fast == nil || server.fast.container == nil {
		t.Fatal("expected GTI container reader")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}

	portable := filepath.Join(t.TempDir(), "copied.gti")
	data, err := os.ReadFile(filepath.Join(snapshot, "snapshot.gti"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(portable, data, 0644); err != nil {
		t.Fatal(err)
	}
	copied, err := OpenServer(ServerOptions{SnapshotDir: portable, PathRoot: filepath.Join(string(filepath.Separator), "moved")})
	if err != nil {
		t.Fatalf("open copied GTI: %v", err)
	}
	defer copied.Close()
	item, err := copied.fast.nodeItem(uint32(copied.fast.manifest.DirectoryCount))
	if err != nil {
		t.Fatal(err)
	}
	if err := copied.fast.decoratePath(&item, copied.pathRootValue()); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(string(filepath.Separator), "moved", "docs", "report.txt")
	if item.Path != want {
		t.Fatalf("mapped path = %q, want %q", item.Path, want)
	}
}

func TestDirectoryRecursiveCountsSurviveIndexRoundTrip(t *testing.T) {
	snapshot := t.TempDir()
	root := filepath.Join(snapshot, "root")
	for _, dir := range []string{"docs", filepath.Join("docs", "deep")} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"a.txt", filepath.Join("docs", "b.txt"), filepath.Join("docs", "deep", "c.txt")} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewFastScanner().Scan(context.Background(), FastScanOptions{Root: root, OutputDir: snapshot, Metadata: FastMetadataBasic}); err != nil {
		t.Fatal(err)
	}
	if err := BuildFastIndex(context.Background(), snapshot, nil); err != nil {
		t.Fatal(err)
	}
	if err := PackFastSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	server, err := OpenServer(ServerOptions{SnapshotDir: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	// 根目录：递归 3 个文件、2 个目录
	crumbs, err := server.fast.breadcrumbs(1, root)
	if err != nil {
		t.Fatal(err)
	}
	top := crumbs[len(crumbs)-1]
	if got := top["file_count"].(int64); got != 3 {
		t.Fatalf("根目录递归文件数 = %d, 期望 3", got)
	}
	if got := top["dir_count"].(int64); got != 2 {
		t.Fatalf("根目录递归目录数 = %d, 期望 2", got)
	}

	// 子目录 docs：递归 2 个文件、1 个目录
	items, _, err := server.fast.childrenItems(1, "name", 100, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	var docsFound bool
	for _, item := range items {
		if item.Name != "docs" {
			continue
		}
		docsFound = true
		if !item.IsDir {
			t.Fatal("docs 应当是目录")
		}
		if item.FileCount != 2 || item.DirCount != 1 {
			t.Fatalf("docs 递归统计 = %d 文件 / %d 目录, 期望 2 / 1", item.FileCount, item.DirCount)
		}
		if item.SizeBytes == 0 {
			t.Fatal("docs 的递归大小不应为 0")
		}
	}
	if !docsFound {
		t.Fatal("未在根目录下找到 docs")
	}

	// 文件条目不应带目录统计值
	for _, item := range items {
		if item.IsDir {
			continue
		}
		if item.FileCount != 0 || item.DirCount != 0 {
			t.Fatalf("文件 %s 不应带目录统计值", item.Name)
		}
	}
}
