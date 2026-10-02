package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBase64ArchiveEncodeDecodeFile(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	sourcePath := filepath.Join(tempDir, "demo.bin")
	encodedPath := filepath.Join(tempDir, "demo.b64.txt")
	decodedPath := filepath.Join(tempDir, "restored.bin")
	content := []byte("hello\x00world\xffbase64")

	if err := os.WriteFile(sourcePath, content, 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	svc := NewBase64ArchiveService()
	meta, err := svc.EncodePath(context.Background(), sourcePath, encodedPath)
	if err != nil {
		t.Fatalf("encode file: %v", err)
	}
	if meta.Kind != "file" || meta.Mode != "raw" || meta.Name != "demo.bin" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}

	inspectMeta, err := svc.InspectEncodedFile(encodedPath)
	if err != nil {
		t.Fatalf("inspect file: %v", err)
	}
	if inspectMeta != meta {
		t.Fatalf("inspect metadata mismatch: got %+v want %+v", inspectMeta, meta)
	}

	decodedMeta, err := svc.DecodeFile(context.Background(), encodedPath, decodedPath)
	if err != nil {
		t.Fatalf("decode file: %v", err)
	}
	if decodedMeta != meta {
		t.Fatalf("decode metadata mismatch: got %+v want %+v", decodedMeta, meta)
	}

	got, err := os.ReadFile(decodedPath)
	if err != nil {
		t.Fatalf("read decoded file: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("decoded file content mismatch: got %q want %q", got, content)
	}
}

func TestBase64ArchiveEncodeDecodeDirectory(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	sourceDir := filepath.Join(tempDir, "bundle")
	encodedPath := filepath.Join(tempDir, "bundle.b64.txt")
	restoreRoot := filepath.Join(tempDir, "restore")
	restoreDir := filepath.Join(restoreRoot, "bundle")

	mustMkdirAll(t, filepath.Join(sourceDir, "nested", "empty"))
	mustWriteFile(t, filepath.Join(sourceDir, "a.txt"), []byte("alpha"))
	mustWriteFile(t, filepath.Join(sourceDir, "nested", "b.txt"), []byte("beta"))

	svc := NewBase64ArchiveService()
	meta, err := svc.EncodePath(context.Background(), sourceDir, encodedPath)
	if err != nil {
		t.Fatalf("encode dir: %v", err)
	}
	if meta.Kind != "dir" || meta.Mode != "zip" || meta.Name != "bundle" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}

	if _, err := svc.DecodeFile(context.Background(), encodedPath, restoreDir); err != nil {
		t.Fatalf("decode dir: %v", err)
	}

	assertFileContent(t, filepath.Join(restoreDir, "a.txt"), "alpha")
	assertFileContent(t, filepath.Join(restoreDir, "nested", "b.txt"), "beta")
	info, err := os.Stat(filepath.Join(restoreDir, "nested", "empty"))
	if err != nil {
		t.Fatalf("stat empty dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected empty directory, got file")
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("content mismatch for %s: got %q want %q", path, data, want)
	}
}
