package cleanup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupRunnerRun(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	keepDir := filepath.Join(root, "upgrade")
	goDir := filepath.Join(root, "gomod")
	pipDir := filepath.Join(root, "pip")
	mustWriteSizedFile(t, filepath.Join(keepDir, "old.tar"), 100)
	mustWriteSizedFile(t, filepath.Join(goDir, "pkg.zip"), 80)
	mustWriteSizedFile(t, filepath.Join(pipDir, "wheel.whl"), 20)

	report, err := NewCleanupRunner().Run(context.Background(), CleanupRunOptions{
		HomeDir: root,
		Targets: []string{"1panel-upgrade", "go-mod-cache", "pip-cache"},
		actions: []cleanupRunAction{
			{
				Key:   "1panel-upgrade",
				Label: "1Panel upgrade cache",
				Path:  keepDir,
				Run: func(_ context.Context) ([]string, error) {
					return []string{"upgrade cleared"}, removeDirContents(keepDir)
				},
			},
			{
				Key:   "go-mod-cache",
				Label: "Go module cache",
				Path:  goDir,
				Run: func(_ context.Context) ([]string, error) {
					return []string{"gomod cleared"}, removeDirContents(goDir)
				},
			},
			{
				Key:   "pip-cache",
				Label: "pip cache",
				Path:  pipDir,
				Run: func(_ context.Context) ([]string, error) {
					return []string{"pip cleared"}, removeDirContents(pipDir)
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("run cleanup: %v", err)
	}

	if report.TotalReclaimedBytes != 200 {
		t.Fatalf("unexpected reclaimed bytes: %d", report.TotalReclaimedBytes)
	}
	if len(report.Items) != 3 {
		t.Fatalf("expected 3 cleanup items, got %d", len(report.Items))
	}

	for _, dir := range []string{keepDir, goDir, pipDir} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir %s: %v", dir, err)
		}
		if len(entries) != 0 {
			t.Fatalf("expected %s to be empty, got %d entries", dir, len(entries))
		}
	}
}

func TestNormalizeCleanupTargets(t *testing.T) {
	t.Parallel()

	got := normalizeCleanupTargets([]string{"go-mod-cache,pip-cache", "pip-cache", "1panel-upgrade"})
	want := []string{"1panel-upgrade", "go-mod-cache", "pip-cache"}
	if len(got) != len(want) {
		t.Fatalf("unexpected target count: %v", got)
	}
	for idx := range want {
		if got[idx] != want[idx] {
			t.Fatalf("unexpected target order: %v", got)
		}
	}
}

func mustWriteSizedFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := make([]byte, size)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}
