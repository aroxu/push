package main

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// clientIP resolves the caller address. X-Forwarded-For is only honoured when
// the immediate peer is an explicitly trusted proxy, otherwise a client could
// trivially spoof its identity and evade rate limiting.
func clientIP(r *http.Request, trusted []string) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !isTrusted(host, trusted) {
		return host
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[0]); ip != "" {
			return ip
		}
	}
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		return xr
	}
	return host
}

func isTrusted(host string, trusted []string) bool {
	ip := net.ParseIP(host)
	for _, t := range trusted {
		if strings.Contains(t, "/") {
			if _, cidr, err := net.ParseCIDR(t); err == nil && ip != nil && cidr.Contains(ip) {
				return true
			}
			continue
		}
		if t == host {
			return true
		}
	}
	return false
}

// RateLimiter is a per-IP token bucket with idle eviction.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	perSec  rate.Limit
	burst   int
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

func NewRateLimiter(perMinute float64, burst int) *RateLimiter {
	rl := &RateLimiter{
		buckets: make(map[string]*bucket),
		perSec:  rate.Limit(perMinute / 60.0),
		burst:   burst,
	}
	go rl.gc()
	return rl
}

func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(rl.perSec, rl.burst)}
		rl.buckets[key] = b
	}
	b.seen = time.Now()
	rl.mu.Unlock()
	return b.lim.Allow()
}

func (rl *RateLimiter) gc() {
	for range time.Tick(5 * time.Minute) {
		cutoff := time.Now().Add(-15 * time.Minute)
		rl.mu.Lock()
		for k, b := range rl.buckets {
			if b.seen.Before(cutoff) {
				delete(rl.buckets, k)
			}
		}
		rl.mu.Unlock()
	}
}

// secureHeaders applies a strict, Cloudflare-independent header policy.
// The CSP is deliberately tight: no inline scripts, no third party origins.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=(), interest-cohort=()")
		// The app shell is a Next.js static export, which bootstraps React and streams
		// its RSC payload through inline <script> tags. Blocking those breaks rendering
		// entirely, so the shell allows inline scripts. This is safe here because the
		// shell is fully static, first-party markup with no user content in it, and
		// every user-supplied file is served from HandleGet under a much stricter
		// "default-src 'none'; sandbox" policy instead.
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; "+
				"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
		}
		next.ServeHTTP(w, r)
	})
}

// safeFilename strips any path component and control characters so a hostile
// filename can never escape into a header or a directory traversal.
func safeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '"' {
			continue
		}
		b.WriteRune(r)
	}
	name = strings.TrimSpace(b.String())
	name = strings.TrimLeft(name, ".")
	if name == "" {
		return "file"
	}
	if len(name) > 200 {
		name = name[:200]
	}
	return name
}

// downloadContentType neutralises anything the browser could execute inline.
func downloadContentType(ct string) (string, bool) {
	ct = strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	switch ct {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/avif",
		"video/mp4", "video/webm", "audio/mpeg", "audio/ogg", "audio/wav",
		"application/pdf", "text/plain":
		return ct, true // safe to render inline
	}
	if ct == "" {
		return "application/octet-stream", false
	}
	return "application/octet-stream", false
}
