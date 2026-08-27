package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalStoreRoundTripRangeAndDelete(t *testing.T) {
	t.Parallel()
	s, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := "aB3xK9pQ"
	payload := []byte("0123456789")
	n, err := s.PutStream(ctx, id, bytes.NewReader(payload), "text/plain", 100)
	if err != nil || n != int64(len(payload)) {
		t.Fatalf("PutStream = %d, %v", n, err)
	}
	m := &Meta{ID: id, Filename: "file.txt", Size: n, ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.PutMeta(ctx, m); err != nil {
		t.Fatal(err)
	}

	gotMeta, err := s.GetMeta(ctx, id)
	if err != nil || gotMeta.Filename != m.Filename {
		t.Fatalf("GetMeta = %#v, %v", gotMeta, err)
	}
	blob, err := s.GetBlob(ctx, id, "bytes=2-5")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(blob.Body)
	blob.Body.Close()
	if err != nil || string(got) != "2345" || blob.ContentLength != 4 || blob.ContentRange != "bytes 2-5/10" {
		t.Fatalf("range = %q, len=%d, range=%q, err=%v", got, blob.ContentLength, blob.ContentRange, err)
	}

	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMeta(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestLocalStoreRejectsOversizeWithoutCommittedFile(t *testing.T) {
	t.Parallel()
	s, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := "Z9y8X7w6"
	if _, err := s.PutStream(context.Background(), id, bytes.NewReader([]byte("too large")), "", 3); err == nil {
		t.Fatal("expected size limit error")
	}
	if _, err := os.Stat(filepath.Join(s.blobs, id)); !os.IsNotExist(err) {
		t.Fatalf("partial file was committed: %v", err)
	}
	entries, err := os.ReadDir(s.uploads)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging files remain: %v, %v", entries, err)
	}
}

func TestParseByteRange(t *testing.T) {
	t.Parallel()
	tests := []struct {
		header     string
		start, end int64
		ok         bool
	}{
		{"bytes=0-0", 0, 0, true},
		{"bytes=5-", 5, 9, true},
		{"bytes=-3", 7, 9, true},
		{"bytes=20-30", 0, 0, false},
		{"bytes=0-1,3-4", 0, 0, false},
	}
	for _, tt := range tests {
		start, end, ranged, err := parseByteRange(tt.header, 10)
		if tt.ok && (err != nil || !ranged || start != tt.start || end != tt.end) {
			t.Errorf("%q = %d-%d ranged=%v err=%v", tt.header, start, end, ranged, err)
		}
		if !tt.ok && !errors.Is(err, ErrInvalidRange) {
			t.Errorf("%q expected ErrInvalidRange, got %v", tt.header, err)
		}
	}
}
