package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	cfg := &Config{
		PublicBaseURL: "http://example.test",
		Retention:     24 * time.Hour,
		RatePerMin:    600,
		RateBurst:     100,
		UploadSlots:   4,
	}
	return NewServer(cfg, nil, NewAuditLog(t.TempDir(), time.Hour))
}

// The landing page must be served from the embedded static export.
func TestIndexIsServedFromEmbeddedAssets(t *testing.T) {
	srv := testServer(t)
	h := secureHeaders(srv.routes())

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200 (is web/out copied into server/static?)", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("GET / content-type = %q, want html", ct)
	}
	if !strings.Contains(rr.Body.String(), "push") {
		t.Fatal("index.html does not mention the product name")
	}
}

func TestSecurityHeadersArePresent(t *testing.T) {
	srv := testServer(t)
	h := secureHeaders(srv.routes())

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for k, v := range want {
		if got := rr.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
	if csp := rr.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP missing frame-ancestors: %q", csp)
	}
}

// Regression: a CSP without 'unsafe-inline' for scripts silently breaks the
// Next.js static export, because its bootstrap and RSC payload are inline
// <script> tags. The page then renders as a blank background.
func TestAppShellCSPAllowsNextInlineBootstrap(t *testing.T) {
	srv := testServer(t)
	h := secureHeaders(srv.routes())

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := rr.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("app shell CSP must allow inline bootstrap scripts, got %q", csp)
	}
	if !strings.Contains(csp, "object-src 'none'") || !strings.Contains(csp, "base-uri 'none'") {
		t.Errorf("app shell CSP lost its hardening: %q", csp)
	}
}

// Downloads must stay under the strict policy regardless of the shell's CSP.
func TestDownloadsKeepStrictSandboxCSP(t *testing.T) {
	srv := testServer(t)
	m := &Meta{
		ID: "aB3xK9pQ", Filename: "evil.html", Size: 10,
		ContentType: "text/html", ExpiresAt: time.Now().Add(time.Hour),
	}
	rr := httptest.NewRecorder()
	srv.writeFileHeaders(rr, httptest.NewRequest(http.MethodGet, "/aB3xK9pQ", nil), m, 10, "")

	if csp := rr.Header().Get("Content-Security-Policy"); csp != "default-src 'none'; sandbox" {
		t.Fatalf("download CSP = %q, want the strict sandbox policy", csp)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("html download content-type = %q, want octet-stream", ct)
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("html download must be an attachment, got %q", cd)
	}
}

// Hostile paths must never be treated as an object id.
func TestMalformedIDsAreRejectedBeforeStorage(t *testing.T) {
	srv := testServer(t) // store is nil: reaching it would panic
	h := srv.routes()

	for _, p := range []string{"/../../etc/passwd", "/meta%2Fabc", "/short", "/waytoolongid"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rr.Code)
		}
	}
}

func TestHealthz(t *testing.T) {
	srv := testServer(t)
	rr := httptest.NewRecorder()
	srv.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rr.Code)
	}
}

// X-Forwarded-For must be ignored unless the peer is a configured proxy.
func TestForwardedHeaderIsNotTrustedByDefault(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.5:1234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	if got := clientIP(req, nil); got != "203.0.113.5" {
		t.Fatalf("untrusted XFF honoured: got %q", got)
	}
	if got := clientIP(req, []string{"203.0.113.0/24"}); got != "1.2.3.4" {
		t.Fatalf("trusted XFF ignored: got %q", got)
	}
}
