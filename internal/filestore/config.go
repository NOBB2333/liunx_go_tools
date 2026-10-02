package filestore

import "os"

// AppConfig 保存运行时配置。
type AppConfig struct {
	DBDSN             string
	FileAPIBaseURL    string
	FileUserID        string
	FileUserName      string
	ProjectName       string
	ProjectCode       string
	DefaultPath       string
	ServerRootDir     string
	ServerFallbackDir string
	ServerMirrorDir   string
	ServerRecordID    string
}

// LoadFromEnv 从环境变量加载配置，并补齐默认值。
func LoadFromEnv() AppConfig {
	return AppConfig{
		DBDSN:             getEnv("DB_DSN", ""),
		FileAPIBaseURL:    getEnv("FILE_API_BASE_URL", ""),
		FileUserID:        getEnv("FILE_USER_ID", ""),
		FileUserName:      getEnv("FILE_USER_NAME", ""),
		ProjectName:       getEnv("FILE_PROJECT_NAME", ""),
		ProjectCode:       getEnv("FILE_PROJECT_CODE", ""),
		DefaultPath:       getEnv("FILE_DEFAULT_PATH", "/"),
		ServerRootDir:     getEnv("SERVER_ROOT_DIR", ""),
		ServerFallbackDir: getEnv("SERVER_FALLBACK_DIR", ""),
		ServerMirrorDir:   getEnv("SERVER_MIRROR_DIR", ""),
		ServerRecordID:    getEnv("SERVER_RECORD_ID", ""),
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
