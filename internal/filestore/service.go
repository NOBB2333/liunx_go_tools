package filestore

import (
	"bytes"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// FileRecord 是 sa_file_info 表的精简映射。
type FileRecord struct {
	FileID     string
	FileName   string
	CreatorUID string
	Path       string
}

// FileRepository 定义文件元数据的存储接口。
type FileRepository interface {
	FindByNameLike(ctx context.Context, fileName string) (*FileRecord, error)
	FindByNameExact(ctx context.Context, fileName string) (*FileRecord, error)
	UpdateFilePath(ctx context.Context, fileID, path, fileName string) error
}

// MySQLRepository 使用 MySQL 实现 FileRepository。
type MySQLRepository struct {
	db *sql.DB
}

// NewMySQLRepository 创建带连接池的仓储对象。
func NewMySQLRepository(dsn string) (*MySQLRepository, error) {
	if dsn == "" {
		return nil, errors.New("DB_DSN is required")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(10 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &MySQLRepository{db: db}, nil
}

// Close 关闭数据库连接。
func (r *MySQLRepository) Close() error {
	return r.db.Close()
}

// FindByNameLike 按模糊文件名查一条记录。
func (r *MySQLRepository) FindByNameLike(ctx context.Context, fileName string) (*FileRecord, error) {
	const query = `SELECT file_id, file_name, creator_uid, path FROM sa_file_info WHERE file_name LIKE ? LIMIT 1`
	return r.queryOne(ctx, query, "%"+fileName+"%")
}

// FindByNameExact 按精确文件名查一条记录。
func (r *MySQLRepository) FindByNameExact(ctx context.Context, fileName string) (*FileRecord, error) {
	const query = `SELECT file_id, file_name, creator_uid, path FROM sa_file_info WHERE file_name = ? LIMIT 1`
	return r.queryOne(ctx, query, fileName)
}

// UpdateFilePath 更新单条文件记录的路径和文件名。
func (r *MySQLRepository) UpdateFilePath(ctx context.Context, fileID, path, fileName string) error {
	const query = `UPDATE sa_file_info SET path = ?, file_name = ? WHERE file_id = ?`
	_, err := r.db.ExecContext(ctx, query, path, fileName, fileID)
	return err
}

func (r *MySQLRepository) queryOne(ctx context.Context, query, arg string) (*FileRecord, error) {
	row := r.db.QueryRowContext(ctx, query, arg)
	rec := &FileRecord{}
	if err := row.Scan(&rec.FileID, &rec.FileName, &rec.CreatorUID, &rec.Path); err != nil {
		return nil, err
	}
	return rec, nil
}

// FileService 封装文件管理相关能力。
type FileService struct {
	repo   FileRepository
	cfg    AppConfig
	client *http.Client
}

// NewFileService 创建文件管理服务。
func NewFileService(repo FileRepository, cfg AppConfig) *FileService {
	return &FileService{
		repo: repo,
		cfg:  cfg,
		client: &http.Client{
			Timeout: 2 * time.Minute,
		},
	}
}

// QueryCopyCommand 返回拷贝服务器文件的命令。
func (s *FileService) QueryCopyCommand(ctx context.Context, fileName string) (string, error) {
	if strings.TrimSpace(fileName) == "" {
		return "", errors.New("file name is required")
	}

	rec, err := s.repo.FindByNameLike(ctx, fileName)
	if err != nil {
		return "", err
	}

	var source string
	if strings.TrimSpace(rec.Path) != "" {
		if strings.TrimSpace(s.cfg.ServerRootDir) == "" {
			return "", errors.New("SERVER_ROOT_DIR is required")
		}
		source = filepath.Join(s.cfg.ServerRootDir, filepath.FromSlash(strings.TrimLeft(rec.Path, "/")))
	} else {
		if strings.TrimSpace(s.cfg.ServerFallbackDir) == "" {
			return "", errors.New("record path is empty and SERVER_FALLBACK_DIR is not configured")
		}
		source = filepath.Join(s.cfg.ServerFallbackDir, rec.FileID+filepath.Ext(fileName))
	}
	return fmt.Sprintf("cp -- %s %s", shellQuote(source), shellQuote(fileName)), nil
}

// UploadServerFile 将服务器文件镜像到指定目录，并更新数据库记录。
func (s *FileService) UploadServerFile(ctx context.Context, sourcePath string) (string, error) {
	if strings.TrimSpace(sourcePath) == "" {
		return "", errors.New("source path is required")
	}
	if strings.TrimSpace(s.cfg.ServerRecordID) == "" {
		return "", errors.New("SERVER_RECORD_ID is required for upload-server")
	}
	if strings.TrimSpace(s.cfg.ServerMirrorDir) == "" {
		return "", errors.New("SERVER_MIRROR_DIR is required for upload-server")
	}
	if strings.TrimSpace(s.cfg.FileAPIBaseURL) == "" || strings.TrimSpace(s.cfg.FileUserID) == "" {
		return "", errors.New("FILE_API_BASE_URL and FILE_USER_ID are required for upload-server")
	}

	absPath, err := filepath.Abs(sourcePath)
	if err != nil {
		return "", err
	}

	baseName := filepath.Base(absPath)
	if err := os.MkdirAll(s.cfg.ServerMirrorDir, os.ModePerm); err != nil {
		return "", err
	}

	destPath := filepath.Join(s.cfg.ServerMirrorDir, baseName)
	if err := copyFile(absPath, destPath); err != nil {
		return "", err
	}

	dbFolder := strings.Trim(strings.ReplaceAll(filepath.ToSlash(s.cfg.ServerMirrorDir), "\\", "/"), "/")
	if idx := strings.LastIndex(dbFolder, "/"); idx >= 0 {
		dbFolder = dbFolder[idx+1:]
	}
	dbPath := "/" + dbFolder + "/" + baseName
	if err := s.repo.UpdateFilePath(ctx, s.cfg.ServerRecordID, dbPath, baseName); err != nil {
		return "", err
	}

	return s.buildDownloadURL(s.cfg.ServerRecordID, s.cfg.FileUserID), nil
}

// UploadLocalFile 通过 HTTP 接口上传本地文件。
func (s *FileService) UploadLocalFile(ctx context.Context, sourcePath string) (string, error) {
	if strings.TrimSpace(sourcePath) == "" {
		return "", errors.New("source path is required")
	}
	if strings.TrimSpace(s.cfg.FileAPIBaseURL) == "" || strings.TrimSpace(s.cfg.FileUserID) == "" {
		return "", errors.New("FILE_API_BASE_URL and FILE_USER_ID are required for upload-local")
	}

	hash, err := md5File(sourcePath)
	if err != nil {
		return "", err
	}

	payload := map[string]string{
		"userId":      s.cfg.FileUserID,
		"userName":    s.cfg.FileUserName,
		"projectName": s.cfg.ProjectName,
		"projectCode": s.cfg.ProjectCode,
		"fileMd5":     hash,
		"path":        s.cfg.DefaultPath,
	}
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("upLoadFileInfoReqStr", string(jsonBody)); err != nil {
		return "", err
	}

	file, err := os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	part, err := writer.CreateFormFile("file", filepath.Base(sourcePath))
	if err != nil {
		return "", err
	}

	if _, err := io.Copy(part, file); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	uploadURL := strings.TrimRight(s.cfg.FileAPIBaseURL, "/") + "/admin-sjy/fileManager/uploadFile"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("User-Agent", "golangtools/2.0")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("upload failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return string(respBody), nil
}

// DownloadURL 按文件名返回下载链接。
func (s *FileService) DownloadURL(ctx context.Context, fileName string) (string, error) {
	if strings.TrimSpace(fileName) == "" {
		return "", errors.New("file name is required")
	}
	if strings.TrimSpace(s.cfg.FileAPIBaseURL) == "" {
		return "", errors.New("FILE_API_BASE_URL is required for download")
	}
	rec, err := s.repo.FindByNameExact(ctx, fileName)
	if err != nil {
		rec, err = s.repo.FindByNameLike(ctx, fileName)
		if err != nil {
			return "", err
		}
	}

	uid := rec.CreatorUID
	if strings.TrimSpace(uid) == "" {
		uid = s.cfg.FileUserID
	}
	return s.buildDownloadURL(rec.FileID, uid), nil
}

func (s *FileService) buildDownloadURL(fileID, userID string) string {
	base := strings.TrimRight(s.cfg.FileAPIBaseURL, "/")
	values := url.Values{"fileId": {fileID}, "userId": {userID}}
	return base + "/admin-sjy/fileManager/downLoadFile?" + values.Encode()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func md5File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	h := md5.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(source, target string) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(target)
	if err != nil {
		return err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	return dst.Sync()
}
