package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Logger writes one structured JSON event per line and mirrors the event to
// stderr in a compact, human-readable form. It is shared by every CLI module.
type Logger struct {
	mu      sync.Mutex
	started time.Time
	runID   string
	module  string
	command string
	files   []*os.File
	paths   []string
	closed  bool
}

type Event struct {
	Timestamp string         `json:"timestamp"`
	RunID     string         `json:"run_id"`
	Module    string         `json:"module"`
	Command   string         `json:"command"`
	Level     string         `json:"level"`
	Phase     string         `json:"phase,omitempty"`
	Event     string         `json:"event"`
	Message   string         `json:"message,omitempty"`
	ElapsedMS int64          `json:"elapsed_ms"`
	Fields    map[string]any `json:"fields,omitempty"`
}

func New(args []string) (*Logger, error) {
	started := time.Now()
	module, command := commandName(args)
	runID := make([]byte, 8)
	if _, err := rand.Read(runID); err != nil {
		return nil, err
	}
	logDir := strings.TrimSpace(os.Getenv("GOLANGTOOLS_LOG_DIR"))
	if logDir == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			logDir = filepath.Join(home, ".golangtools", "logs")
		} else {
			logDir = filepath.Join("go-tool-result", "logs")
		}
	}
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("%s-%s-%s-%s.jsonl", started.Format("20060102-150405"), module, command, hex.EncodeToString(runID))
	path := filepath.Join(logDir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	logger := &Logger{
		started: started,
		runID:   runIDString(runID),
		module:  module,
		command: command,
		files:   []*os.File{file},
		paths:   []string{path},
	}
	logger.Log("info", "startup", "command.start", "命令开始", map[string]any{"args": args, "log_file": path})
	fmt.Fprintf(os.Stderr, "log file: %s\n", path)
	return logger, nil
}

func (l *Logger) Attach(path string) error {
	if l == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.files = append(l.files, file)
	l.paths = append(l.paths, path)
	l.mu.Unlock()
	l.Log("info", "startup", "log.attach", "已附加任务日志", map[string]any{"log_file": path})
	fmt.Fprintf(os.Stderr, "task log: %s\n", path)
	return nil
}

func (l *Logger) Log(level, phase, event, message string, fields map[string]any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	e := Event{
		Timestamp: time.Now().Format(time.RFC3339Nano),
		RunID:     l.runID,
		Module:    l.module,
		Command:   l.command,
		Level:     level,
		Phase:     phase,
		Event:     event,
		Message:   message,
		ElapsedMS: time.Since(l.started).Milliseconds(),
		Fields:    fields,
	}
	data, err := json.Marshal(e)
	if err == nil {
		data = append(data, '\n')
		for _, file := range l.files {
			_, _ = file.Write(data)
		}
	}
	stamp := e.Timestamp
	if len(stamp) > 19 {
		stamp = stamp[:19]
	}
	fmt.Fprintf(os.Stderr, "%s %-5s %-12s %-20s %s\n", stamp, strings.ToUpper(level), phase, event, message)
}

func (l *Logger) Phase(phase, message string, fields map[string]any) {
	l.Log("info", phase, "phase.start", message, fields)
}

func (l *Logger) Progress(phase, message string, fields map[string]any) {
	l.Log("info", phase, "progress", message, fields)
}

func (l *Logger) Error(phase, message string, fields map[string]any) {
	l.Log("error", phase, "error", message, fields)
}

func (l *Logger) Complete(err error) {
	if err != nil {
		l.Log("error", "complete", "command.failed", "命令失败", map[string]any{"error": err.Error()})
		return
	}
	l.Log("info", "complete", "command.complete", "命令完成", nil)
}

func (l *Logger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	for _, file := range l.files {
		_ = file.Close()
	}
	l.mu.Unlock()
}

func (l *Logger) RunID() string {
	if l == nil {
		return ""
	}
	return l.runID
}

func commandName(args []string) (string, string) {
	if len(args) == 0 {
		return "cli", "help"
	}
	module := safeName(args[0])
	command := "run"
	if len(args) > 1 {
		command = safeName(args[1])
	}
	return module, command
}

func safeName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			builder.WriteRune(r)
		}
	}
	if builder.Len() == 0 {
		return "unknown"
	}
	return builder.String()
}

func runIDString(value []byte) string { return hex.EncodeToString(value) }
