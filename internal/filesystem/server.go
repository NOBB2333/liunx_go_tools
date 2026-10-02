package filesystem

import (
	"bufio"
	"context"
	"crypto/subtle"
	"database/sql"
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
	"unicode/utf8"

	_ "github.com/glebarez/go-sqlite"
)

type ServerOptions struct {
	SnapshotDir string
	Token       string
	OpenPath    func(path string, isDir bool) error
}

type Server struct {
	snapshotDir string
	manifest    SnapshotManifest
	db          *sql.DB
	token       string
	static      http.Handler
	hasSearch   bool
	hasTimes    bool
	hasExtAlloc bool
	hasDirAlloc bool
	openPath    func(path string, isDir bool) error
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
	snapshotDir, err := filepath.Abs(filepath.Clean(opt.SnapshotDir))
	if err != nil {
		return nil, err
	}
	manifest, err := readManifest(snapshotDir)
	if err != nil {
		return nil, err
	}
	databasePath := filepath.Join(snapshotDir, "query.db")
	if _, err := os.Stat(databasePath); err != nil {
		return nil, fmt.Errorf("query.db: %w; run filesystem index first", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	var hasSearch int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'file_search'`).Scan(&hasSearch); err != nil {
		_ = db.Close()
		return nil, err
	}
	fileColumns, err := tableColumns(db, "files")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	extensionColumns, err := tableColumns(db, "extension_stats")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	directoryColumns, err := tableColumns(db, "directories")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	static, err := embeddedStaticHandler()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Server{
		snapshotDir: snapshotDir,
		manifest:    manifest,
		db:          db,
		token:       opt.Token,
		static:      static,
		hasSearch:   hasSearch == 1,
		hasTimes:    fileColumns["ctime_ns"] && fileColumns["birthtime_ns"],
		hasExtAlloc: extensionColumns["allocated_bytes"],
		hasDirAlloc: directoryColumns["allocated_bytes"],
		openPath:    opt.OpenPath,
	}, nil
}

func (s *Server) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
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
	item := listItem{ID: request.ID, IsDir: request.IsDir}
	if request.IsDir {
		if err := s.db.QueryRowContext(r.Context(), `SELECT id,parent_id,name,1 FROM directories WHERE id = ?`, request.ID).Scan(&item.ID, &item.ParentID, &item.Name, new(int)); err != nil {
			writeError(w, http.StatusNotFound, "item_not_found", "directory is not indexed")
			return
		}
	} else {
		if err := s.db.QueryRowContext(r.Context(), `SELECT id,parent_id,name,0 FROM files WHERE id = ?`, request.ID).Scan(&item.ID, &item.ParentID, &item.Name, new(int)); err != nil {
			writeError(w, http.StatusNotFound, "item_not_found", "file is not indexed")
			return
		}
	}
	items := []listItem{item}
	if err := s.decoratePaths(r.Context(), items); err != nil {
		writeError(w, http.StatusInternalServerError, "path_query_failed", err.Error())
		return
	}
	item = items[0]
	path := item.Path
	if _, err := os.Stat(path); err != nil {
		writeError(w, http.StatusNotFound, "path_not_found", fmt.Sprintf("path is no longer available: %s", path))
		return
	}
	opener := s.openPath
	if opener == nil {
		opener = openInFileManager
	}
	if err := opener(path, request.IsDir); err != nil {
		writeError(w, http.StatusBadGateway, "open_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"path": path, "is_dir": request.IsDir}, "meta": map[string]any{}})
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
		"query_database":      filepath.Join(s.snapshotDir, "query.db"),
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
	type directoryInfo struct {
		ID       int64  `json:"id"`
		ParentID int64  `json:"parent_id"`
		Name     string `json:"name"`
		Depth    int64  `json:"depth"`
		Path     string `json:"path"`
	}
	rows, err := s.db.QueryContext(r.Context(), `WITH RECURSIVE ancestors(id,parent_id,name,depth) AS (
		SELECT id,parent_id,name,depth FROM directories WHERE id = ?
		UNION ALL
		SELECT d.id,d.parent_id,d.name,d.depth FROM directories d JOIN ancestors a ON d.id = a.parent_id
	) SELECT id,parent_id,name,depth FROM ancestors ORDER BY depth ASC`, directoryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	defer rows.Close()
	breadcrumbs := make([]directoryInfo, 0, 8)
	for rows.Next() {
		var item directoryInfo
		if err := rows.Scan(&item.ID, &item.ParentID, &item.Name, &item.Depth); err != nil {
			writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
			return
		}
		breadcrumbs = append(breadcrumbs, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	if len(breadcrumbs) == 0 {
		writeError(w, http.StatusNotFound, "directory_not_found", "directory does not exist")
		return
	}
	for i := range breadcrumbs {
		names := make([]string, i+1)
		for j := range names {
			names[j] = breadcrumbs[j].Name
		}
		breadcrumbs[i].Path = s.pathForNames(names)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"directory": breadcrumbs[len(breadcrumbs)-1], "breadcrumbs": breadcrumbs}, "meta": map[string]any{}})
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
	sortSQL := safeSort(sortValue)
	if !s.manifest.AllocatedKnown && sortValue != "name" && sortValue != "mtime" {
		sortSQL = "size_bytes DESC, is_dir DESC, id ASC"
	}
	directoryAllocated := "0"
	if s.hasDirAlloc {
		directoryAllocated = "allocated_bytes"
	}
	query := fmt.Sprintf(`SELECT id,parent_id,name,is_dir,size_bytes,allocated_bytes,file_count,dir_count,mtime_ns,ctime_ns,birthtime_ns,extension FROM (
		SELECT id,parent_id,name,1 AS is_dir,logical_bytes AS size_bytes,%s AS allocated_bytes,file_count,directory_count AS dir_count,0 AS mtime_ns,0 AS ctime_ns,0 AS birthtime_ns,'' AS extension FROM directories WHERE parent_id = ?
		UNION ALL
		SELECT id,parent_id,name,0 AS is_dir,size AS size_bytes,blocks * 512 AS allocated_bytes,0,0,mtime_ns,%s,%s,extension FROM files WHERE parent_id = ?
	) ORDER BY %s LIMIT ? OFFSET ?`, directoryAllocated, s.fileTimeExpr("ctime_ns"), s.fileTimeExpr("birthtime_ns"), sortSQL)
	rows, err := s.db.QueryContext(r.Context(), query, parentID, parentID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	defer rows.Close()
	items, err := scanItems(rows)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	if err := s.decoratePaths(r.Context(), items); err != nil {
		writeError(w, http.StatusInternalServerError, "path_query_failed", err.Error())
		return
	}
	var total int64
	if err := s.db.QueryRowContext(r.Context(), `SELECT (SELECT COUNT(*) FROM directories WHERE parent_id = ?) + (SELECT COUNT(*) FROM files WHERE parent_id = ?)`, parentID, parentID).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "meta": map[string]any{"total": total, "limit": limit, "offset": offset, "parent_id": parentID}})
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	order := "blocks DESC, id ASC"
	if !s.manifest.AllocatedKnown {
		order = "size DESC, id ASC"
	}
	switch r.URL.Query().Get("sort") {
	case "name":
		order = "name COLLATE NOCASE ASC, id ASC"
	case "mtime":
		order = "mtime_ns DESC, id ASC"
	}
	query := fmt.Sprintf(`SELECT id,parent_id,name,0,size,blocks * 512 AS allocated_bytes,0,0,mtime_ns,%s,%s,extension FROM files ORDER BY %s LIMIT ? OFFSET ?`, s.fileTimeExpr("ctime_ns"), s.fileTimeExpr("birthtime_ns"), order)
	rows, err := s.db.QueryContext(r.Context(), query, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	defer rows.Close()
	items, err := scanItems(rows)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	if err := s.decoratePaths(r.Context(), items); err != nil {
		writeError(w, http.StatusInternalServerError, "path_query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "meta": map[string]any{"limit": limit, "offset": offset}})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	term := strings.TrimSpace(r.URL.Query().Get("q"))
	if term == "" {
		writeJSON(w, http.StatusOK, map[string]any{"data": []listItem{}, "meta": map[string]any{"total": 0}})
		return
	}
	limit, offset := pagination(r)
	var (
		rows   *sql.Rows
		err    error
		engine = "scan"
	)
	if s.hasSearch && utf8.RuneCountInString(term) >= 3 {
		engine = "trigram"
		phrase := `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
		order := "f.blocks * 512 DESC"
		if !s.manifest.AllocatedKnown {
			order = "f.size DESC"
		}
		rows, err = s.db.QueryContext(r.Context(), fmt.Sprintf(`SELECT f.id,f.parent_id,f.name,0,f.size,f.blocks * 512,0,0,f.mtime_ns,%s,%s,f.extension
			FROM file_search JOIN files f ON f.id = file_search.rowid
			WHERE file_search MATCH ? ORDER BY %s LIMIT ? OFFSET ?`, s.fileTimeExpr("f.ctime_ns"), s.fileTimeExpr("f.birthtime_ns"), order), phrase, limit, offset)
	} else {
		pattern := "%" + term + "%"
		order := "blocks * 512 DESC"
		if !s.manifest.AllocatedKnown {
			order = "size DESC"
		}
		rows, err = s.db.QueryContext(r.Context(), fmt.Sprintf(`SELECT id,parent_id,name,0,size,blocks * 512,0,0,mtime_ns,%s,%s,extension FROM files WHERE name LIKE ? COLLATE NOCASE ORDER BY %s LIMIT ? OFFSET ?`, s.fileTimeExpr("ctime_ns"), s.fileTimeExpr("birthtime_ns"), order), pattern, limit, offset)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	defer rows.Close()
	items, err := scanItems(rows)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	if err := s.decoratePaths(r.Context(), items); err != nil {
		writeError(w, http.StatusInternalServerError, "path_query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "meta": map[string]any{"limit": limit, "offset": offset, "query": term, "engine": engine}})
}

func (s *Server) handleExtensions(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed < 500 {
			limit = parsed
		}
	}
	extensionQuery := `SELECT extension,files,bytes FROM extension_stats ORDER BY bytes DESC LIMIT ?`
	withAllocated := false
	if s.hasExtAlloc {
		extensionQuery = `SELECT extension,files,bytes,allocated_bytes FROM extension_stats ORDER BY bytes DESC LIMIT ?`
		withAllocated = true
	} else {
		// Old materialized tables did not store allocated bytes. Derive it from
		// file blocks so the UI keeps the same physical-space semantics.
		extensionQuery = `SELECT extension,COUNT(*),SUM(size),SUM(blocks * 512) FROM files WHERE extension <> '' GROUP BY extension ORDER BY SUM(size) DESC LIMIT ?`
		withAllocated = true
	}
	rows, err := s.db.QueryContext(r.Context(), extensionQuery, limit)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no such table") {
		// Snapshots built before the materialized extension table remain readable.
		rows, err = s.db.QueryContext(r.Context(), `SELECT extension,COUNT(*),SUM(size),SUM(blocks * 512) FROM files WHERE extension <> '' GROUP BY extension ORDER BY SUM(size) DESC LIMIT ?`, limit)
		withAllocated = true
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	defer rows.Close()
	type extensionStat struct {
		Extension      string `json:"extension"`
		Files          int64  `json:"files"`
		SizeBytes      int64  `json:"size_bytes"`
		AllocatedBytes int64  `json:"allocated_bytes"`
	}
	result := make([]extensionStat, 0, limit)
	for rows.Next() {
		var item extensionStat
		var scanErr error
		if withAllocated {
			scanErr = rows.Scan(&item.Extension, &item.Files, &item.SizeBytes, &item.AllocatedBytes)
		} else {
			scanErr = rows.Scan(&item.Extension, &item.Files, &item.SizeBytes)
		}
		if scanErr != nil {
			writeError(w, http.StatusInternalServerError, "query_failed", scanErr.Error())
			return
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result, "meta": map[string]any{}})
}

func (s *Server) handleErrors(w http.ResponseWriter, r *http.Request) {
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

func scanItems(rows *sql.Rows) ([]listItem, error) {
	items := make([]listItem, 0)
	for rows.Next() {
		var item listItem
		var isDir int
		if err := rows.Scan(&item.ID, &item.ParentID, &item.Name, &isDir, &item.SizeBytes, &item.AllocatedBytes, &item.FileCount, &item.DirCount, &item.MTimeNS, &item.CTimeNS, &item.BirthtimeNS, &item.Extension); err != nil {
			return nil, err
		}
		item.IsDir = isDir != 0
		items = append(items, item)
	}
	return items, rows.Err()
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[strings.ToLower(name)] = true
	}
	return columns, rows.Err()
}

func (s *Server) fileTimeExpr(column string) string {
	if s.hasTimes {
		return column
	}
	return "0"
}

func (s *Server) pathForNames(names []string) string {
	result := s.manifest.Root
	if result == "" {
		result = string(filepath.Separator)
	}
	start := 0
	rootName := filepath.Base(filepath.Clean(result))
	if len(names) > 0 && rootName != "." && names[0] == rootName {
		start = 1
	}
	for _, name := range names[start:] {
		result = filepath.Join(result, name)
	}
	return result
}

func (s *Server) decoratePaths(ctx context.Context, items []listItem) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(items))
	seen := make(map[int64]struct{}, len(items))
	for _, item := range items {
		id := item.ParentID
		if item.IsDir {
			id = item.ID
		}
		if id > 0 {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(`WITH RECURSIVE ancestors(origin_id,id,parent_id,name,depth) AS (
		SELECT id,id,parent_id,name,depth FROM directories WHERE id IN (%s)
		UNION ALL
		SELECT a.origin_id,d.id,d.parent_id,d.name,d.depth
		FROM directories d JOIN ancestors a ON d.id = a.parent_id
	) SELECT origin_id,name,depth FROM ancestors ORDER BY origin_id,depth ASC`, strings.Join(placeholders, ","))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	namesByOrigin := make(map[int64][]string, len(ids))
	for rows.Next() {
		var origin int64
		var name string
		var depth int64
		if err := rows.Scan(&origin, &name, &depth); err != nil {
			return err
		}
		namesByOrigin[origin] = append(namesByOrigin[origin], name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range items {
		origin := items[i].ParentID
		if items[i].IsDir {
			origin = items[i].ID
		}
		base := s.pathForNames(namesByOrigin[origin])
		if items[i].IsDir {
			items[i].Path = base
		} else {
			items[i].Path = filepath.Join(base, items[i].Name)
		}
	}
	return nil
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

func safeSort(value string) string {
	switch value {
	case "name":
		return "name COLLATE NOCASE ASC, is_dir DESC, id ASC"
	case "mtime":
		return "mtime_ns DESC, id ASC"
	default:
		return "allocated_bytes DESC, is_dir DESC, id ASC"
	}
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
