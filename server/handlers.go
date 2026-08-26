package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

type Server struct {
	cfg   *Config
	store *Store
	log   *AuditLog
	rl    *RateLimiter
	slots chan struct{}
}

func NewServer(cfg *Config, store *Store, log *AuditLog) *Server {
	return &Server{
		cfg:   cfg,
		store: store,
		log:   log,
		rl:    NewRateLimiter(cfg.RatePerMin, cfg.RateBurst),
		slots: make(chan struct{}, cfg.UploadSlots),
	}
}

func (s *Server) ip(r *http.Request) string { return clientIP(r, s.cfg.TrustedProxies) }

// wantsJSON is false for a bare curl so the response stays copy-pasteable.
func wantsJSON(r *http.Request) bool {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		return true
	}
	return r.URL.Query().Get("json") == "1"
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, code int, msg string) {
	if wantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		fmt.Fprintf(w, "{\"error\":%q}\n", msg)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	fmt.Fprintln(w, msg)
}

// HandleUpload accepts three shapes so that the simplest possible curl works:
//
//	curl -X POST --data-binary @file.jpg host      (raw body)
//	curl -F file=@file.jpg host                    (multipart form)
//	curl -T file.jpg host                          (PUT raw body)
func (s *Server) HandleUpload(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ip := s.ip(r)

	if !s.rl.Allow(ip) {
		s.log.Write(Event{Kind: "upload.ratelimited", IP: ip, Status: 429})
		w.Header().Set("Retry-After", "60")
		s.fail(w, r, http.StatusTooManyRequests, "rate limit exceeded, slow down")
		return
	}

	// Bound concurrent in-flight uploads to protect the node from overload.
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-r.Context().Done():
		return
	case <-time.After(30 * time.Second):
		s.fail(w, r, http.StatusServiceUnavailable, "server busy, retry shortly")
		return
	}

	if r.ContentLength > 0 && s.cfg.MaxUploadBytes > 0 && r.ContentLength > s.cfg.MaxUploadBytes {
		s.log.Write(Event{Kind: "upload.rejected", IP: ip, Size: r.ContentLength, Status: 413,
			Error: "declared size over limit"})
		s.fail(w, r, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("file too large (max %s)", humanBytes(s.cfg.MaxUploadBytes)))
		return
	}

	var (
		body     io.Reader = r.Body
		filename           = ""
		ctype              = r.Header.Get("Content-Type")
	)

	if mt, params, err := mime.ParseMediaType(ctype); err == nil && strings.HasPrefix(mt, "multipart/") {
		if _, ok := params["boundary"]; !ok {
			s.fail(w, r, http.StatusBadRequest, "malformed multipart request")
			return
		}
		mr, err := r.MultipartReader()
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, "malformed multipart request")
			return
		}
		found := false
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				s.fail(w, r, http.StatusBadRequest, "malformed multipart request")
				return
			}
			if p.FileName() != "" {
				body = p
				filename = p.FileName()
				ctype = p.Header.Get("Content-Type")
				found = true
				break
			}
		}
		if !found {
			s.fail(w, r, http.StatusBadRequest, "no file part found in form")
			return
		}
	}

	// Filename hints, in order of preference.
	if filename == "" {
		if q := r.URL.Query().Get("filename"); q != "" {
			filename = q
		} else if p := strings.Trim(r.URL.Path, "/"); p != "" && p != "upload" {
			filename = p
		} else if cd := r.Header.Get("Content-Disposition"); cd != "" {
			if _, params, err := mime.ParseMediaType(cd); err == nil && params["filename"] != "" {
				filename = params["filename"]
			}
		} else if xf := r.Header.Get("X-Filename"); xf != "" {
			filename = xf
		}
	}
	filename = safeFilename(filename)

	if ctype == "" || strings.HasPrefix(ctype, "multipart/") {
		ctype = "application/octet-stream"
	}
	if ctype == "application/x-www-form-urlencoded" {
		// curl's default for --data-binary; it tells us nothing useful.
		ctype = "application/octet-stream"
	}

	id, err := NewID()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not allocate id")
		return
	}
	token, err := NewDeleteToken()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not allocate token")
		return
	}

	hasher := sha256.New()
	tee := io.TeeReader(body, hasher)

	size, err := s.store.PutStream(r.Context(), id, tee, ctype, s.cfg.MaxUploadBytes)
	if err != nil {
		status := http.StatusInternalServerError
		msg := "upload failed"
		if strings.Contains(err.Error(), "exceeds limit") {
			status, msg = http.StatusRequestEntityTooLarge,
				fmt.Sprintf("file too large (max %s)", humanBytes(s.cfg.MaxUploadBytes))
		}
		if r.Context().Err() != nil {
			status, msg = 499, "client disconnected"
		}
		s.log.Write(Event{Kind: "upload.failed", ID: id, Filename: filename, Size: size,
			IP: ip, Agent: r.UserAgent(), Status: status, Error: err.Error(),
			Duration: msSince(start)})
		if status != 499 {
			s.fail(w, r, status, msg)
		}
		return
	}

	meta := &Meta{
		ID:          id,
		Filename:    filename,
		Size:        size,
		ContentType: ctype,
		SHA256:      hex.EncodeToString(hasher.Sum(nil)),
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   time.Now().UTC().Add(s.cfg.Retention),
		DeleteHash:  token,
	}
	if err := s.store.PutMeta(r.Context(), meta); err != nil {
		_ = s.store.Delete(r.Context(), id)
		s.log.Write(Event{Kind: "upload.failed", ID: id, IP: ip, Status: 500, Error: err.Error()})
		s.fail(w, r, http.StatusInternalServerError, "upload failed")
		return
	}

	s.log.Write(Event{Kind: "upload", ID: id, Filename: filename, Size: size, IP: ip,
		Agent: r.UserAgent(), Status: 201, Duration: msSince(start)})

	link := s.cfg.PublicBaseURL + "/" + id
	w.Header().Set("X-Delete-Token", token)
	w.Header().Set("Location", link)

	if wantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, "{\"id\":%q,\"url\":%q,\"filename\":%q,\"size\":%d,\"sha256\":%q,\"expires_at\":%q,\"delete_token\":%q}\n",
			id, link, filename, size, meta.SHA256, meta.ExpiresAt.Format(time.RFC3339), token)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, "%s\n", link)
}

// HandleGet streams a blob back, supporting Range requests.
func (s *Server) HandleGet(w http.ResponseWriter, r *http.Request, id string, headOnly bool) {
	start := time.Now()
	ip := s.ip(r)

	meta, err := s.store.GetMeta(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			s.fail(w, r, http.StatusNotFound, "not found or expired")
			return
		}
		s.fail(w, r, http.StatusInternalServerError, "storage error")
		return
	}
	if time.Now().After(meta.ExpiresAt) {
		s.fail(w, r, http.StatusGone, "expired")
		return
	}

	if headOnly {
		s.writeFileHeaders(w, r, meta, meta.Size, "")
		w.WriteHeader(http.StatusOK)
		return
	}

	res, err := s.store.GetBlob(r.Context(), id, r.Header.Get("Range"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			s.fail(w, r, http.StatusNotFound, "not found or expired")
			return
		}
		s.fail(w, r, http.StatusInternalServerError, "storage error")
		return
	}
	defer res.Body.Close()

	length := aws.ToInt64(res.ContentLength)
	s.writeFileHeaders(w, r, meta, length, aws.ToString(res.ContentRange))

	status := http.StatusOK
	if res.ContentRange != nil {
		status = http.StatusPartialContent
	}
	w.WriteHeader(status)

	n, _ := io.Copy(w, res.Body)

	s.log.Write(Event{Kind: "download", ID: id, Filename: meta.Filename, Size: n, IP: ip,
		Agent: r.UserAgent(), Status: status, Duration: msSince(start)})
}

func (s *Server) writeFileHeaders(w http.ResponseWriter, r *http.Request, m *Meta, length int64, contentRange string) {
	ct, inlineOK := downloadContentType(m.ContentType)
	disp := "attachment"
	if inlineOK && r.URL.Query().Get("dl") != "1" {
		disp = "inline"
	}

	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.FormatInt(length, 10))
	h.Set("Accept-Ranges", "bytes")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, max-age=300")
	h.Set("X-Checksum-Sha256", m.SHA256)
	h.Set("X-Expires-At", m.ExpiresAt.Format(time.RFC3339))
	if contentRange != "" {
		h.Set("Content-Range", contentRange)
	}
	h.Set("Content-Disposition", fmt.Sprintf("%s; filename=\"%s\"; filename*=UTF-8''%s",
		disp, asciiFallback(m.Filename), url.PathEscape(m.Filename)))
}

// HandleDelete lets the uploader revoke a file early using their token.
func (s *Server) HandleDelete(w http.ResponseWriter, r *http.Request, id string) {
	token := r.Header.Get("X-Delete-Token")
	if token == "" {
		token = r.URL.Query().Get("token")
	}

	meta, err := s.store.GetMeta(r.Context(), id)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "not found or expired")
		return
	}
	if token == "" || !ConstantTimeEqual(token, meta.DeleteHash) {
		s.log.Write(Event{Kind: "delete.denied", ID: id, IP: s.ip(r), Status: 403})
		s.fail(w, r, http.StatusForbidden, "invalid delete token")
		return
	}
	if err := s.store.Delete(r.Context(), id); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "delete failed")
		return
	}
	s.log.Write(Event{Kind: "delete", ID: id, Filename: meta.Filename, IP: s.ip(r), Status: 200})
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "deleted")
}

// HandleInfo exposes metadata without transferring the payload.
func (s *Server) HandleInfo(w http.ResponseWriter, r *http.Request, id string) {
	meta, err := s.store.GetMeta(r.Context(), id)
	if err != nil || time.Now().After(meta.ExpiresAt) {
		s.fail(w, r, http.StatusNotFound, "not found or expired")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, "{\"id\":%q,\"filename\":%q,\"size\":%d,\"content_type\":%q,\"sha256\":%q,\"created_at\":%q,\"expires_at\":%q}\n",
		meta.ID, meta.Filename, meta.Size, meta.ContentType, meta.SHA256,
		meta.CreatedAt.Format(time.RFC3339), meta.ExpiresAt.Format(time.RFC3339))
}

func asciiFallback(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if out == "" {
		return "file"
	}
	return out
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.0f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func msSince(t time.Time) float64 {
	return float64(time.Since(t).Microseconds()) / 1000.0
}
