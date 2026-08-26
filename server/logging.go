package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Event is one structured audit record. It is written twice: to stdout so
// Dozzle can render it live, and to a daily rotated JSONL file kept for 7 days.
type Event struct {
	Time     time.Time `json:"ts"`
	Kind     string    `json:"event"`
	ID       string    `json:"id,omitempty"`
	Filename string    `json:"filename,omitempty"`
	Size     int64     `json:"size,omitempty"`
	IP       string    `json:"ip,omitempty"`
	Agent    string    `json:"ua,omitempty"`
	Status   int       `json:"status,omitempty"`
	Duration float64   `json:"ms,omitempty"`
	Error    string    `json:"error,omitempty"`
}

type AuditLog struct {
	dir       string
	retention time.Duration

	mu   sync.Mutex
	day  string
	file *os.File
}

func NewAuditLog(dir string, retention time.Duration) *AuditLog {
	a := &AuditLog{dir: dir, retention: retention}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "audit: cannot create %s: %v (stdout only)\n", dir, err)
		a.dir = ""
	}
	return a
}

func (a *AuditLog) Write(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	// stdout -> docker json-file -> Dozzle
	fmt.Println(string(line))

	if a.dir == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	day := e.Time.Format("2006-01-02")
	if a.file == nil || a.day != day {
		if a.file != nil {
			a.file.Close()
		}
		f, ferr := os.OpenFile(filepath.Join(a.dir, "push-"+day+".jsonl"),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
		if ferr != nil {
			return
		}
		a.file, a.day = f, day
	}
	a.file.Write(append(line, '\n'))
}

// Prune deletes rotated logs older than the retention window.
func (a *AuditLog) Prune() {
	if a.dir == "" {
		return
	}
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().UTC().Add(-a.retention)
	for _, en := range entries {
		name := en.Name()
		if !strings.HasPrefix(name, "push-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, "push-"), ".jsonl")
		t, perr := time.Parse("2006-01-02", day)
		if perr != nil || !t.Before(cutoff) {
			continue
		}
		os.Remove(filepath.Join(a.dir, name))
	}
}

func (a *AuditLog) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file != nil {
		a.file.Close()
		a.file = nil
	}
}
