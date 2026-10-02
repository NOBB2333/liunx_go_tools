package filesystem

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerOpenItemUsesLocalFileManager(t *testing.T) {
	snapshot := t.TempDir()
	root := filepath.Join(snapshot, "root")
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "report.txt"), []byte("report"), 0644); err != nil {
		t.Fatal(err)
	}
	manifestData, err := json.Marshal(SnapshotManifest{Root: root, OutputDir: snapshot, Metadata: "basic", Files: 1, Directories: 1, Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshot, "manifest.json"), manifestData, 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeTestSegments(snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := NewIndexBuilder().Build(context.Background(), BuildOptions{SnapshotDir: snapshot, BatchSize: 2}); err != nil {
		t.Fatalf("build index: %v", err)
	}
	var openedPath string
	server, err := OpenServer(ServerOptions{SnapshotDir: snapshot, OpenPath: func(path string, isDir bool) error {
		openedPath = path
		if isDir {
			t.Fatal("expected a file open request")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	request := httptest.NewRequest("POST", "/api/v1/snapshots/active/open", bytes.NewBufferString(`{"id":1,"is_dir":false}`))
	request.RemoteAddr = "127.0.0.1:12345"
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != 200 || openedPath != filepath.Join(root, "docs", "report.txt") {
		t.Fatalf("open response: status=%d path=%q body=%s", recorder.Code, openedPath, recorder.Body.String())
	}

	request = httptest.NewRequest("POST", "/api/v1/snapshots/active/open", bytes.NewBufferString(`{"id":1,"is_dir":false}`))
	request.RemoteAddr = "192.0.2.10:12345"
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != 403 {
		t.Fatalf("remote open response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestIndexBuilderAndServer(t *testing.T) {
	snapshot := t.TempDir()
	manifest := SnapshotManifest{Root: "/data", OutputDir: snapshot, Metadata: "basic", Files: 1, Directories: 1, Complete: true}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshot, "manifest.json"), manifestData, 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeTestSegments(snapshot); err != nil {
		t.Fatal(err)
	}

	if _, err := NewIndexBuilder().Build(context.Background(), BuildOptions{SnapshotDir: snapshot, BatchSize: 2}); err != nil {
		t.Fatalf("build index: %v", err)
	}
	server, err := OpenServer(ServerOptions{SnapshotDir: snapshot})
	if err != nil {
		t.Fatalf("open server: %v", err)
	}
	defer server.Close()
	if !server.hasSearch {
		t.Fatal("expected trigram search index")
	}

	request := httptest.NewRequest("GET", "/api/v1/snapshots/active/directories/1/children?limit=10", nil)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "docs") {
		t.Fatalf("children response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest("GET", "/api/v1/snapshots/active/directories/2", nil)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), `"breadcrumbs"`) || !strings.Contains(recorder.Body.String(), `"docs"`) {
		t.Fatalf("directory response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest("GET", "/search", nil)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "id=\"app\"") {
		t.Fatalf("SPA response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest("GET", "/api/v1/snapshots/active/extensions?limit=8", nil)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), ".txt") {
		t.Fatalf("extensions response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest("GET", "/api/v1/snapshots/active/search?q=port&limit=10", nil)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "report.txt") || !strings.Contains(recorder.Body.String(), `"engine":"trigram"`) {
		t.Fatalf("search response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest("GET", "/api/v1/snapshots/active/files?limit=10", nil)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "report.txt") || !strings.Contains(recorder.Body.String(), `"allocated_bytes"`) {
		t.Fatalf("files response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestIndexBuilderRejectsRawDirectoryWithActionableError(t *testing.T) {
	result, err := NewIndexBuilder().Build(context.Background(), BuildOptions{SnapshotDir: t.TempDir()})
	if err == nil {
		t.Fatal("expected raw directory to be rejected")
	}
	if result != (BuildSummary{}) {
		t.Fatalf("unexpected summary on failure: %+v", result)
	}
	message := err.Error()
	if !strings.Contains(message, "not a golangtools snapshot") ||
		!strings.Contains(message, "missing manifest.json") ||
		!strings.Contains(message, "filesystem scan -root") {
		t.Fatalf("error does not explain how to recover: %q", message)
	}
}

func TestReadManifestInfersLegacyAllocationCapability(t *testing.T) {
	for _, test := range []struct {
		name      string
		allocated uint64
		known     bool
	}{
		{name: "unix snapshot with blocks", allocated: 4096, known: true},
		{name: "windows snapshot without blocks", allocated: 0, known: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := t.TempDir()
			data := []byte(`{"root":"/data","allocated_bytes":` + fmt.Sprintf("%d", test.allocated) + `,"complete":true}`)
			if err := os.WriteFile(filepath.Join(snapshot, "manifest.json"), data, 0644); err != nil {
				t.Fatal(err)
			}
			manifest, err := readManifest(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if manifest.AllocatedKnown != test.known {
				t.Fatalf("allocated known = %v, want %v", manifest.AllocatedKnown, test.known)
			}
		})
	}
}

func TestServerTokenQuerySetsCookie(t *testing.T) {
	snapshot := t.TempDir()
	manifestData, err := json.Marshal(SnapshotManifest{Root: "/data", OutputDir: snapshot, Metadata: "basic", Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshot, "manifest.json"), manifestData, 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeTestSegments(snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := NewIndexBuilder().Build(context.Background(), BuildOptions{SnapshotDir: snapshot, BatchSize: 2}); err != nil {
		t.Fatalf("build index: %v", err)
	}
	server, err := OpenServer(ServerOptions{SnapshotDir: snapshot, Token: "test-token"})
	if err != nil {
		t.Fatalf("open server: %v", err)
	}
	defer server.Close()

	request := httptest.NewRequest("GET", "/?token=test-token", nil)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("token page response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	cookie := recorder.Result().Cookies()
	if len(cookie) != 1 || cookie[0].Name != "filesystem_viewer_token" || cookie[0].HttpOnly != true {
		t.Fatalf("unexpected auth cookie: %+v", cookie)
	}

	request = httptest.NewRequest("GET", "/api/v1/health", nil)
	request.AddCookie(cookie[0])
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("cookie API response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest("GET", "/api/v1/health", nil)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != 401 {
		t.Fatalf("unauthenticated API response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func writeTestSegments(snapshot string) error {
	directoryFile, err := os.Create(filepath.Join(snapshot, "directories.seg"))
	if err != nil {
		return err
	}
	var directoryHeader [60]byte
	binary.LittleEndian.PutUint64(directoryHeader[0:8], 1)
	binary.LittleEndian.PutUint32(directoryHeader[56:60], 4)
	if _, err := directoryFile.Write(directoryHeader[:]); err != nil {
		_ = directoryFile.Close()
		return err
	}
	if _, err := directoryFile.WriteString("root"); err != nil {
		_ = directoryFile.Close()
		return err
	}
	var childHeader [60]byte
	binary.LittleEndian.PutUint64(childHeader[0:8], 2)
	binary.LittleEndian.PutUint64(childHeader[8:16], 1)
	binary.LittleEndian.PutUint32(childHeader[16:20], 1)
	binary.LittleEndian.PutUint64(childHeader[24:32], 128)
	binary.LittleEndian.PutUint64(childHeader[40:48], 1)
	binary.LittleEndian.PutUint32(childHeader[56:60], 4)
	if _, err := directoryFile.Write(childHeader[:]); err != nil {
		_ = directoryFile.Close()
		return err
	}
	if _, err := directoryFile.WriteString("docs"); err != nil {
		_ = directoryFile.Close()
		return err
	}
	if err := directoryFile.Close(); err != nil {
		return err
	}

	file, err := os.Create(filepath.Join(snapshot, "files.seg"))
	if err != nil {
		return err
	}
	var fileHeader [56]byte
	binary.LittleEndian.PutUint64(fileHeader[0:8], 2)
	binary.LittleEndian.PutUint64(fileHeader[8:16], 99)
	binary.LittleEndian.PutUint64(fileHeader[24:32], 42)
	binary.LittleEndian.PutUint32(fileHeader[48:52], 0644)
	binary.LittleEndian.PutUint32(fileHeader[52:56], 10)
	if _, err := file.Write(fileHeader[:]); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.WriteString("report.txt"); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
