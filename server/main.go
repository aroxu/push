package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

//go:embed all:static
var staticFiles embed.FS

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	audit := NewAuditLog(cfg.LogDir, cfg.LogRetention)
	defer audit.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := waitForStore(ctx, cfg, audit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "storage error: %v\n", err)
		os.Exit(1)
	}

	srv := NewServer(cfg, store, audit)
	go reaper(ctx, cfg, store, audit)

	handler := secureHeaders(srv.routes())

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       0, // large uploads stream for a long time
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}

	go func() {
		audit.Write(Event{Kind: "server.start", Filename: cfg.Addr})
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "listen: %v\n", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	audit.Write(Event{Kind: "server.stop"})
	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
}

// waitForStore initializes local storage or retries while a remote S3 backend starts.
func waitForStore(ctx context.Context, cfg *Config, audit *AuditLog) (Storage, error) {
	var lastErr error
	for i := 0; i < 60; i++ {
		var store Storage
		var err error
		if cfg.StorageBackend == "local" {
			store, err = NewLocalStore(cfg.DataDir)
		} else {
			store, err = NewStore(ctx, cfg)
		}
		if err == nil {
			return store, nil
		}
		lastErr = err
		audit.Write(Event{Kind: "storage.waiting", Error: err.Error()})
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return nil, lastErr
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	assets := http.FileServer(http.FS(sub))

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "ok")
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.Trim(r.URL.Path, "/")

		switch r.Method {
		case http.MethodPost, http.MethodPut:
			s.HandleUpload(w, r)
			return

		case http.MethodGet, http.MethodHead:
			// Root or any static asset.
			if path == "" || isAsset(sub, path) {
				assets.ServeHTTP(w, r)
				return
			}
			// /<id>/info
			if rest, ok := strings.CutSuffix(path, "/info"); ok && ValidID(rest) {
				s.HandleInfo(w, r, rest)
				return
			}
			// /<id> or /<id>/original-name.ext
			id := path
			if i := strings.Index(path, "/"); i > 0 {
				id = path[:i]
			}
			if ValidID(id) {
				s.HandleGet(w, r, id, r.Method == http.MethodHead)
				return
			}
			s.fail(w, r, http.StatusNotFound, "not found")
			return

		case http.MethodDelete:
			if ValidID(path) {
				s.HandleDelete(w, r, path)
				return
			}
			s.fail(w, r, http.StatusNotFound, "not found")
			return

		case http.MethodOptions:
			w.Header().Set("Allow", "GET, HEAD, POST, PUT, DELETE, OPTIONS")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		s.fail(w, r, http.StatusMethodNotAllowed, "method not allowed")
	})

	return rejectDirtyPaths(mux)
}

// rejectDirtyPaths runs before ServeMux so traversal-looking requests get a flat
// 404 instead of a 301/307 redirect that hints at the internal layout.
func rejectDirtyPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.EscapedPath()
		if strings.Contains(raw, "..") || strings.Contains(raw, "//") ||
			strings.Contains(raw, "\\") || strings.Contains(r.URL.Path, "..") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isAsset(sub fs.FS, path string) bool {
	if path == "" {
		return false
	}
	if strings.Contains(path, "..") {
		return false
	}
	f, err := sub.Open(path)
	if err != nil {
		// Next.js static export serves /foo as /foo.html
		f2, err2 := sub.Open(path + ".html")
		if err2 != nil {
			return false
		}
		f2.Close()
		return true
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return false
	}
	return true
}

// reaper enforces the 24h retention window and prunes rotated audit logs.
func reaper(ctx context.Context, cfg *Config, store Storage, audit *AuditLog) {
	run := func() {
		now := time.Now()
		removed := 0
		err := store.ListMeta(ctx, func(m *Meta) error {
			if now.After(m.ExpiresAt) {
				if derr := store.Delete(ctx, m.ID); derr == nil {
					removed++
					audit.Write(Event{Kind: "expire", ID: m.ID, Filename: m.Filename, Size: m.Size})
				}
			}
			return nil
		})
		aborted := store.AbortStaleUploads(ctx, 12*time.Hour)
		audit.Write(Event{Kind: "reaper.sweep", Size: int64(removed), Status: aborted,
			Error: errString(err)})
		audit.Prune()
	}

	run()
	t := time.NewTicker(cfg.ReaperEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
