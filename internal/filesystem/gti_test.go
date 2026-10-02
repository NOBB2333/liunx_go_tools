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
