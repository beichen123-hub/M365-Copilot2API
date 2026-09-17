package web

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxSysLogEntries = 2000

type sysLogEntry struct {
	ID      int64     `json:"id"`
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

type sysLogBuffer struct {
	mu      sync.RWMutex
	records []sysLogEntry
	nextID  int64
}

var globalSysLogs = &sysLogBuffer{
	records: make([]sysLogEntry, 0, maxSysLogEntries),
	nextID:  1,
}

type sysLogWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *sysLogWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n, err = w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			if len(line) > 0 {
				w.buf.WriteString(line)
			}
			break
		}
		globalSysLogs.addRawLine(line)
	}
	return n, err
}

func inferLogLevel(msg string) string {
	low := strings.ToLower(msg)
	if strings.Contains(low, "panic") || strings.Contains(low, "error") || strings.Contains(low, "failed") || strings.Contains(low, "warning") || strings.Contains(low, "err=") || strings.Contains(low, "status=5") || strings.Contains(low, "status=40") || strings.Contains(low, "status=429") {
		return "ERROR"
	}
	if strings.Contains(low, "warn") || strings.Contains(low, "limited") || strings.Contains(low, "cooldown") || strings.Contains(low, "quota") || strings.Contains(low, "retry") {
		return "WARN"
	}
	if strings.Contains(low, "debug") || strings.Contains(low, "trace") || strings.Contains(low, "timing") || strings.Contains(low, "payload") {
		return "DEBUG"
	}
	return "INFO"
}

func (b *sysLogBuffer) addRawLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	entry := sysLogEntry{
		ID:      b.nextID,
		Time:    time.Now(),
		Level:   inferLogLevel(line),
		Message: line,
	}
	b.nextID++

	if len(b.records) >= maxSysLogEntries {
		b.records = append(b.records[1:], entry)
	} else {
		b.records = append(b.records, entry)
	}
}

func init() {
	writer := &sysLogWriter{}
	log.SetOutput(io.MultiWriter(os.Stderr, writer))
}

func (s *Server) handleSysLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}

	q := r.URL.Query()
	var sinceID int64
	if v := q.Get("since_id"); v != "" {
		sinceID, _ = strconv.ParseInt(v, 10, 64)
	}

	limit := 200
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxSysLogEntries {
			limit = n
		}
	}

	levelFilter := strings.ToUpper(strings.TrimSpace(q.Get("level")))
	searchQuery := strings.ToLower(strings.TrimSpace(q.Get("q")))

	globalSysLogs.mu.RLock()
	defer globalSysLogs.mu.RUnlock()

	out := make([]sysLogEntry, 0, limit)
	var maxID int64

	for _, rec := range globalSysLogs.records {
		if rec.ID > maxID {
			maxID = rec.ID
		}
		if sinceID > 0 && rec.ID <= sinceID {
			continue
		}
		if levelFilter != "" && levelFilter != "ALL" && rec.Level != levelFilter {
			continue
		}
		if searchQuery != "" && !strings.Contains(strings.ToLower(rec.Message), searchQuery) {
			continue
		}
		out = append(out, rec)
	}

	if len(out) > limit {
		out = out[len(out)-limit:]
	}

	jsonOut(w, map[string]any{
		"logs":   out,
		"max_id": maxID,
		"count":  len(out),
	})
}

func (s *Server) handleSysLogsClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}

	globalSysLogs.mu.Lock()
	globalSysLogs.records = make([]sysLogEntry, 0, maxSysLogEntries)
	globalSysLogs.mu.Unlock()

	jsonOut(w, map[string]any{"status": "cleared"})
}