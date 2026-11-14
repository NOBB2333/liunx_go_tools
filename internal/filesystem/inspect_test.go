package filesystem

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeInspectSnapshot(t *testing.T, allocatedKnown bool) (string, string) {
	t.Helper()
	root := t.TempDir()
	files := map[string]int{
		"root-big.bin":            90000,
		"projects/cache.tmp":      40000,
		"projects/deep/cache.bin": 30000,
		"projects/deep/notes.txt": 20000,
		"other/cache-copy.bin":    10000,
	}
	for name, size := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Repeat("a", size)), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "projects", "deep", "cache"), 0755); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	scan, err := NewFastScanner().Scan(context.Background(), FastScanOptions{Root: root, OutputDir: out, Metadata: FastMetadataBasic, Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if allocatedKnown && !scan.AllocatedKnown {
		t.Skip("scanner does not provide allocated bytes on this platform")
	}
	manifestPath := filepath.Join(out, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest FastScanSummary
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.AllocatedKnown = allocatedKnown
	manifest.Errors = 2
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "errors.ndjson"), []byte("{\"path\":\"first\",\"error\":\"denied\"}\n{\"path\":\"second\",\"error\":\"timeout\"}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := BuildFastIndex(context.Background(), out, nil); err != nil {
		t.Fatal(err)
	}
	if err := PackFastSnapshot(out); err != nil {
		t.Fatal(err)
	}
	return out, root
}

func TestInspectOverviewAndQueries(t *testing.T) {
	out, root := makeInspectSnapshot(t, true)
	overview, err := InspectSnapshot(context.Background(), InspectOptions{Snapshot: out, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if overview.Schema != "golangtools.filesystem.inspect.v1" || overview.SizeMetric != "allocated_bytes" || overview.Scope.Path != root {
		t.Fatalf("unexpected overview metadata: %+v", overview)
	}
	data := overview.Data.(inspectOverview)
	if data.TopDirectories.Total != 2 || len(data.TopDirectories.Items) != 2 || data.TopDirectories.Items[0].Name != "projects" {
		t.Fatalf("top directories: %+v", data.TopDirectories)
	}
	if data.LargestFiles.Total != 5 || len(data.LargestFiles.Items) != 2 || data.LargestFiles.Items[0].Name != "root-big.bin" {
		t.Fatalf("largest files: %+v", data.LargestFiles)
	}
	if data.Errors.Total != 2 || len(data.Errors.Items) != 2 {
		t.Fatalf("error page: %+v", data.Errors)
	}
	projectID := uint64(data.TopDirectories.Items[0].ID)
	pageResult, err := InspectSnapshot(context.Background(), InspectOptions{Snapshot: out, View: "largest", DirectoryID: projectID, Kind: "files", Query: "CACHE", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	page := pageResult.Data.(inspectPage[listItem])
	if page.Total != 2 || len(page.Items) != 1 || page.Items[0].Path != filepath.Join(root, "projects", "deep", "cache.bin") {
		t.Fatalf("scoped search page: %+v", page)
	}
	children, err := InspectSnapshot(context.Background(), InspectOptions{Snapshot: out, View: "children", DirectoryID: projectID, Kind: "directories", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	childPage := children.Data.(inspectPage[listItem])
	if childPage.Total != 1 || childPage.Items[0].Name != "deep" {
		t.Fatalf("children: %+v", childPage)
	}
	dirResult, err := InspectSnapshot(context.Background(), InspectOptions{Snapshot: out, View: "largest", DirectoryID: projectID, Kind: "directories", Query: "cache", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	dirPage := dirResult.Data.(inspectPage[listItem])
	if dirPage.Total != 1 || !dirPage.Items[0].IsDir || dirPage.Items[0].Path != filepath.Join(root, "projects", "deep", "cache") {
		t.Fatalf("scoped directory search: %+v", dirPage)
	}
	errorsResult, err := InspectSnapshot(context.Background(), InspectOptions{Snapshot: out, View: "errors", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	errorPage := errorsResult.Data.(inspectPage[fastErrorRecord])
	if errorPage.Total != 2 || len(errorPage.Items) != 1 || errorPage.Items[0].Path != "second" {
		t.Fatalf("paged errors: %+v", errorPage)
	}
}

func TestInspectRejectsTreeOnlySizeAnalysis(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFastScanner().Scan(context.Background(), FastScanOptions{Root: root, OutputDir: out, Metadata: FastMetadataTree, Backend: FastScanBackendPortable}); err != nil {
		t.Fatal(err)
	}
	if err := BuildFastIndex(context.Background(), out, nil); err != nil {
		t.Fatal(err)
	}
	if err := PackFastSnapshot(out); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectSnapshot(context.Background(), InspectOptions{Snapshot: out}); err == nil || !strings.Contains(err.Error(), "tree-only") {
		t.Fatalf("expected tree-only size analysis to fail, got %v", err)
	}
	if _, err := InspectSnapshot(context.Background(), InspectOptions{Snapshot: out, View: "errors"}); err != nil {
		t.Fatalf("tree-only error records should remain readable: %v", err)
	}
}

func TestInspectLogicalFallbackAndInputValidation(t *testing.T) {
	out, _ := makeInspectSnapshot(t, false)
	result, err := InspectSnapshot(context.Background(), InspectOptions{Snapshot: out, View: "largest", Kind: "files", Limit: 2, MinBytes: 35000})
	if err != nil {
		t.Fatal(err)
	}
	page := result.Data.(inspectPage[listItem])
	if result.SizeMetric != "size_bytes" || page.Total != 2 || page.Items[0].Name != "root-big.bin" || page.Items[1].Name != "cache.tmp" {
		t.Fatalf("logical fallback: metric=%s page=%+v", result.SizeMetric, page)
	}
	for _, opt := range []InspectOptions{
		{Snapshot: out, View: "bogus"},
		{Snapshot: out, View: "largest", Kind: "bogus"},
		{Snapshot: out, View: "children", DirectoryID: 999999},
		{Snapshot: out, View: "largest", Limit: 501},
		{Snapshot: out, View: "overview", Offset: 1},
	} {
		if _, err := InspectSnapshot(context.Background(), opt); err == nil {
			t.Fatalf("expected invalid options to fail: %+v", opt)
		}
	}
}
