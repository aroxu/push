package main

import "testing"

func TestNewIDShape(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 5000; i++ {
		id, err := NewID()
		if err != nil {
			t.Fatal(err)
		}
		if len(id) != 8 {
			t.Fatalf("want length 8, got %d (%q)", len(id), id)
		}
		if !ValidID(id) {
			t.Fatalf("generated id rejected by validator: %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id within 5000 draws: %q", id)
		}
		seen[id] = true
	}
}

func TestValidIDRejectsHostileInput(t *testing.T) {
	bad := []string{"", "short", "toolongvalue", "../../etc", "abcdefg/", "abcdef-h", "meta/aaa", "aaaa aaa"}
	for _, b := range bad {
		if ValidID(b) {
			t.Fatalf("validator accepted hostile input %q", b)
		}
	}
}

func TestSafeFilename(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":      "passwd",
		"C:\\Windows\\evil.exe": "evil.exe",
		"":                      "file",
		"...":                   "file",
		"ok name.txt":           "ok name.txt",
		"quote\"inject.txt":     "quoteinject.txt",
	}
	for in, want := range cases {
		if got := safeFilename(in); got != want {
			t.Errorf("safeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDownloadContentTypeNeutralisesHTML(t *testing.T) {
	for _, ct := range []string{"text/html", "image/svg+xml", "application/xhtml+xml", "text/html; charset=utf-8"} {
		got, inline := downloadContentType(ct)
		if inline || got != "application/octet-stream" {
			t.Errorf("%q must not be servable inline, got %q inline=%v", ct, got, inline)
		}
	}
	if got, inline := downloadContentType("image/png"); !inline || got != "image/png" {
		t.Errorf("png should stay inline, got %q %v", got, inline)
	}
}
