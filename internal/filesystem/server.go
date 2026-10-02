package filesystem

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type ServerOptions struct {
	SnapshotDir string
	PathRoot    string
	Token       string
	OpenPath    func(path string, isDir bool) error
}

type Server struct {
	snapshotDir  string
	snapshotFile string
	pathRoot     string
	manifest     SnapshotManifest
	fast         *fastIndex
	token        string
	static       http.Handler
	openPath     func(path string, isDir bool) error
}

type listItem struct {
	ID             int64  `json:"id"`
	ParentID       int64  `json:"parent_id"`
	Name           string `json:"name"`
	IsDir          bool   `json:"is_dir"`
	SizeBytes      int64  `json:"size_bytes"`
	AllocatedBytes int64  `json:"allocated_bytes"`
	FileCount      int64  `json:"file_count,omitempty"`
	DirCount       int64  `json:"dir_count,omitempty"`
	MTimeNS        int64  `json:"mtime_ns,omitempty"`
	CTimeNS        int64  `json:"ctime_ns,omitempty"`
	BirthtimeNS    int64  `json:"birthtime_ns,omitempty"`
	Extension      string `json:"extension,omitempty"`
	Path           string `json:"path,omitempty"`
}

func OpenServer(opt ServerOptions) (*Server, error) {
	if strings.TrimSpace(opt.SnapshotDir) == "" {
		return nil, errors.New("snapshot directory is required")
	}
	snapshotInput, err := filepath.Abs(filepath.Clean(opt.SnapshotDir))
	if err != nil {
		return nil, err
	}
	snapshotDir := snapshotInput
	snapshotFile := gtiPath(snapshotInput)
	if strings.HasSuffix(strings.ToLower(snapshotInput), ".gti") {
		snapshotDir = filepath.Dir(snapshotInput)
		snapshotFile = snapshotInput
	}
	manifest, err := readManifest(snapshotInput)
	if err != nil {
		return nil, err
	}
	fast, fastErr := openFastIndex(snapshotFile)
	if fastErr != nil || fast == nil || fast.container == nil {
		if fast != nil {
			_ = fast.Close()
		}
		return nil, fmt.Errorf("snapshot.gti unavailable (%v); run filesystem scan first", fastErr)
	}
	static, err := embeddedStaticHandler()
	if err != nil {
		_ = fast.Close()
		return nil, err
	}
	return &Server{
		snapshotDir:  snapshotDir,
		snapshotFile: snapshotFile,
		pathRoot:     strings.TrimSpace(opt.PathRoot),
		manifest:     manifest,
		fast:         fast,
		token:        opt.Token,
		static:       static,
		openPath:     opt.OpenPath,
	}, nil
}

func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	if s.fast != nil {
		return s.fast.Close()
	}
	return nil
}

func (s *Server) Handler() http.Handler { return s }

func (s *Server) Serve(ctx context.Context, listen string) error {
	if strings.TrimSpace(listen) == "" {
		listen = "127.0.0.1:8080"
	}
	server := &http.Server{
		Addr:              listen,
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.token != "" {
		queryAuthorized := false
		if queryToken := r.URL.Query().Get("token"); queryToken != "" && subtle.ConstantTimeCompare([]byte(queryToken), []byte(s.token)) == 1 {
			http.SetCookie(w, &http.Cookie{Name: "filesystem_viewer_token", Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			clone := r.Clone(r.Context())
			query := clone.URL.Query()
			query.Del("token")
			clone.URL.RawQuery = query.Encode()
			clone.AddCookie(&http.Cookie{Name: "filesystem_viewer_token", Value: s.token})
			r = clone
			queryAuthorized = true
		}
		if !queryAuthorized && !s.authorized(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
			return
		}
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		s.handleAPI(w, r)
		return
	}
	s.static.ServeHTTP(w, r)
}

func (s *Server) authorized(r *http.Request) bool {
	value := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
	if value == "" {
		if cookie, err := r.Cookie("filesystem_viewer_token"); err == nil {
			value = cookie.Value
		}
	}
	return subtle.ConstantTimeCompare([]byte(value), []byte(s.token)) == 1
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case path == "/api/v1/health":
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"status": "ok"}, "meta": map[string]any{}})
	case path == "/api/v1/snapshots":
		writeJSON(w, http.StatusOK, map[string]any{"data": []SnapshotManifest{s.manifest}, "meta": map[string]any{}})
	case path == "/api/v1/snapshots/active/summary":
		s.handleSummary(w)
	case path == "/api/v1/snapshots/active/extensions":
		s.handleExtensions(w, r)
	case path == "/api/v1/snapshots/active/files":
		s.handleFiles(w, r)
	case path == "/api/v1/snapshots/active/search":
		s.handleSearch(w, r)
	case path == "/api/v1/snapshots/active/errors":
		s.handleErrors(w, r)
	case path == "/api/v1/snapshots/active/open":
		s.handleOpen(w, r)
	case strings.HasPrefix(path, "/api/v1/snapshots/active/directories/") && strings.HasSuffix(path, "/children"):
		s.handleChildren(w, r, path)
	case strings.HasPrefix(path, "/api/v1/snapshots/active/directories/"):
		s.handleDirectory(w, r, path)
	default:
		writeError(w, http.StatusNotFound, "not_found", "API route not found")
	}
}

type openRequest struct {
	ID    int64 `json:"id"`
	IsDir bool  `json:"is_dir"`
}

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST is required")
		return
	}
	if !isLoopbackRequest(r) {
		writeError(w, http.StatusForbidden, "local_access_required", "opening a local path requires a loopback browser connection")
		return
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host != r.Host {
			writeError(w, http.StatusForbidden, "origin_not_allowed", "the open request must come from this viewer")
			return
		}
	}
	var request openRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	if err := decoder.Decode(&request); err != nil || request.ID < 1 {
		writeError(w, http.StatusBadRequest, "invalid_open_request", "a valid indexed item is required")
		return
	}
	if s.fast != nil {
		item, ok := s.fast.indexForItem(request.ID, request.IsDir)
		if !ok {
			writeError(w, http.StatusNotFound, "item_not_found", "item is not indexed")
			return
		}
		indexed, err := s.fast.nodeItem(item)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "tree_read_failed", err.Error())
			return
		}
		if indexed.IsDir != request.IsDir {
			writeError(w, http.StatusNotFound, "item_not_found", "item type does not match")
			return
		}
		if err := s.fast.decoratePath(&indexed, s.pathRootValue()); err != nil {
			writeError(w, http.StatusInternalServerError, "path_query_failed", err.Error())
			return
		}
		if _, err := os.Stat(indexed.Path); err != nil {
			writeError(w, http.StatusNotFound, "path_not_found", fmt.Sprintf("path is no longer available: %s", indexed.Path))
			return
		}
		opener := s.openPath
		if opener == nil {
			opener = openInFileManager
		}
		if err := opener(indexed.Path, request.IsDir); err != nil {
			writeError(w, http.StatusBadGateway, "open_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"path": indexed.Path, "is_dir": request.IsDir}, "meta": map[string]any{}})
		return
	}
	writeError(w, http.StatusNotFound, "item_not_found", "snapshot.gti is not available")
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) handleSummary(w http.ResponseWriter) {
	data := map[string]any{
		"manifest":            s.manifest,
		"indexed_files":       s.manifest.Files,
		"indexed_directories": s.manifest.Directories + 1,
		"snapshot_file":       s.snapshotFile,
	}
	if s.pathRoot != "" {
		data["path_root"] = s.pathRoot
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "meta": map[string]any{}})
}

func (s *Server) handleDirectory(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 6 {
		writeError(w, http.StatusBadRequest, "invalid_directory", "directory id is required")
		return
	}
	directoryID, err := strconv.ParseInt(parts[5], 10, 64)
	if err != nil || directoryID < 1 {
		writeError(w, http.StatusBadRequest, "invalid_directory", "directory id is invalid")
		return
	}
	if s.fast != nil {
		breadcrumbs, err := s.fast.breadcrumbs(uint64(directoryID), s.pathRootValue())
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeError(w, http.StatusNotFound, "directory_not_found", "directory does not exist")
			} else {
				writeError(w, http.StatusInternalServerError, "tree_read_failed", err.Error())
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"directory": breadcrumbs[len(breadcrumbs)-1], "breadcrumbs": breadcrumbs}, "meta": map[string]any{}})
		return
	}
	writeError(w, http.StatusNotFound, "directory_not_found", "snapshot.gti is not available")
}

func (s *Server) handleChildren(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 7 {
		writeError(w, http.StatusBadRequest, "invalid_directory", "directory id is required")
		return
	}
	parentID, err := strconv.ParseInt(parts[5], 10, 64)
	if err != nil || parentID < 1 {
		writeError(w, http.StatusBadRequest, "invalid_directory", "directory id is invalid")
		return
	}
	limit, offset := pagination(r)
	sortValue := r.URL.Query().Get("sort")
	if s.fast != nil {
		items, total, err := s.fast.childrenItems(uint64(parentID), sortValue, limit, offset, s.manifest.AllocatedKnown)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeError(w, http.StatusNotFound, "directory_not_found", "directory does not exist")
			} else {
				writeError(w, http.StatusInternalServerError, "tree_read_failed", err.Error())
			}
			return
		}
		if err := s.decorateFastPaths(items); err != nil {
			writeError(w, http.StatusInternalServerError, "path_query_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": items, "meta": map[string]any{"total": total, "limit": limit, "offset": offset, "parent_id": parentID, "engine": "tree"}})
		return
	}
	writeError(w, http.StatusInternalServerError, "snapshot_unavailable", "snapshot.gti is not available")
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	if s.fast != nil {
		limit, offset := pagination(r)
		items, err := s.fast.fileItems(r.Context(), r.URL.Query().Get("sort"), limit, offset, s.manifest.AllocatedKnown)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "tree_read_failed", err.Error())
			return
		}
		if err := s.decorateFastPaths(items); err != nil {
			writeError(w, http.StatusInternalServerError, "path_query_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": items, "meta": map[string]any{"limit": limit, "offset": offset, "engine": "tree"}})
		return
	}
	writeError(w, http.StatusInternalServerError, "snapshot_unavailable", "snapshot.gti is not available")
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	term := strings.TrimSpace(r.URL.Query().Get("q"))
	if term == "" {
		writeJSON(w, http.StatusOK, map[string]any{"data": []listItem{}, "meta": map[string]any{"total": 0}})
		return
	}
	if s.fast != nil {
		limit, offset := pagination(r)
		items, total, err := s.fast.search(r.Context(), term, limit, offset, s.manifest.AllocatedKnown)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "tree_search_failed", err.Error())
			return
		}
		if err := s.decorateFastPaths(items); err != nil {
			writeError(w, http.StatusInternalServerError, "path_query_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": items, "meta": map[string]any{"limit": limit, "offset": offset, "total": total, "query": term, "engine": "tree-scan"}})
		return
	}
	writeError(w, http.StatusInternalServerError, "snapshot_unavailable", "snapshot.gti is not available")
}

func (s *Server) handleExtensions(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed < 500 {
			limit = parsed
		}
	}
	if s.fast != nil {
		result := make([]map[string]any, 0, limit)
		for _, item := range s.fast.extensions {
			if len(result) >= limit {
				break
			}
			result = append(result, map[string]any{"extension": item.Extension, "files": item.Files, "size_bytes": item.SizeBytes, "allocated_bytes": item.AllocatedBytes})
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result, "meta": map[string]any{"engine": "tree"}})
		return
	}
	writeError(w, http.StatusInternalServerError, "snapshot_unavailable", "snapshot.gti is not available")
}

func (s *Server) handleErrors(w http.ResponseWriter, r *http.Request) {
	if s.fast != nil && s.fast.container != nil {
		data, err := s.fast.sectionBytes(gtiErrors)
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusOK, map[string]any{"data": []map[string]any{}, "meta": map[string]any{"limit": 100, "offset": 0}})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read_failed", err.Error())
			return
		}
		limit, offset := pagination(r)
		var result []map[string]any
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 64<<10), 4<<20)
		lineNumber := 0
		for scanner.Scan() {
			if lineNumber < offset {
				lineNumber++
				continue
			}
			if len(result) >= limit {
				break
			}
			var item map[string]any
			if json.Unmarshal(scanner.Bytes(), &item) == nil {
				result = append(result, item)
			}
			lineNumber++
		}
		if err := scanner.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "read_failed", err.Error())
			return
		}
		if result == nil {
			result = []map[string]any{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result, "meta": map[string]any{"limit": limit, "offset": offset}})
		return
	}
	file, err := os.Open(filepath.Join(s.snapshotDir, "errors.ndjson"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusOK, map[string]any{"data": []map[string]any{}, "meta": map[string]any{"limit": 100, "offset": 0}})
		return
	}
	defer file.Close()
	limit, offset := pagination(r)
	var result []map[string]any
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	lineNumber := 0
	for scanner.Scan() {
		if lineNumber < offset {
			lineNumber++
			continue
		}
		if len(result) >= limit {
			break
		}
		var item map[string]any
		if json.Unmarshal(scanner.Bytes(), &item) == nil {
			result = append(result, item)
		}
		lineNumber++
	}
	if err := scanner.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	if result == nil {
		result = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result, "meta": map[string]any{"limit": limit, "offset": offset}})
}

func (s *Server) decorateFastPaths(items []listItem) error {
	if s.fast == nil {
		return nil
	}
	for i := range items {
		if err := s.fast.decoratePath(&items[i], s.pathRootValue()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) pathRootValue() string {
	if s.pathRoot != "" {
		return s.pathRoot
	}
	return s.manifest.Root
}

func pagination(r *http.Request) (int, int) {
	limit := 100
	offset := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			limit = value
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value >= 0 {
			offset = value
		}
	}
	if limit > 500 {
		limit = 500
	}
	return limit, offset
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"data":  nil,
		"meta":  map[string]any{},
		"error": map[string]string{"code": code, "message": message},
	})
}
