package filestore

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

type fakeRepository struct {
	record *FileRecord
}

func (r fakeRepository) FindByNameLike(context.Context, string) (*FileRecord, error) {
	return r.record, nil
}

func (r fakeRepository) FindByNameExact(context.Context, string) (*FileRecord, error) {
	return r.record, nil
}

func (fakeRepository) UpdateFilePath(context.Context, string, string, string) error {
	return nil
}

func TestQueryCopyCommandUsesConfiguredRootAndQuotesPaths(t *testing.T) {
	service := NewFileService(fakeRepository{record: &FileRecord{
		FileID: "id-1",
		Path:   "/reports/a file.txt",
	}}, AppConfig{ServerRootDir: "/srv/files"})

	command, err := service.QueryCopyCommand(context.Background(), "result's copy.txt")
	if err != nil {
		t.Fatal(err)
	}
	// 源路径由 filepath.Join 拼接，期望值需跟随平台分隔符（Windows 下为反斜杠）
	wantSource := filepath.Join("/srv/files", filepath.FromSlash("reports/a file.txt"))
	want := fmt.Sprintf("cp -- '%s' 'result'\"'\"'s copy.txt'", wantSource)
	if command != want {
		t.Fatalf("unexpected command: got %q want %q", command, want)
	}
}

func TestDownloadURLIsEscaped(t *testing.T) {
	service := NewFileService(fakeRepository{record: &FileRecord{
		FileID:     "id with space",
		CreatorUID: "user+1",
	}}, AppConfig{FileAPIBaseURL: "https://files.example.test"})

	got, err := service.DownloadURL(context.Background(), "report.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://files.example.test/admin-sjy/fileManager/downLoadFile?fileId=id+with+space&userId=user%2B1"
	if got != want {
		t.Fatalf("unexpected URL: got %q want %q", got, want)
	}
}

func TestLoadFromEnvHasNoCredentialDefaults(t *testing.T) {
	t.Setenv("DB_DSN", "")
	t.Setenv("FILE_API_BASE_URL", "")
	config := LoadFromEnv()
	if config.DBDSN != "" || config.FileAPIBaseURL != "" {
		t.Fatalf("sensitive configuration must not have built-in defaults: %+v", config)
	}
}
