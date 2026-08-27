package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LocalStore keeps payloads and sidecar metadata below one private directory.
// Uploads land in .uploads first and are atomically renamed only after fsync,
// so an interrupted or oversized request never exposes a partial file.
type LocalStore struct {
	root    string
	blobs   string
	meta    string
	uploads string
}

func NewLocalStore(root string) (*LocalStore, error) {
	if root == "" {
		return nil, fmt.Errorf("PUSH_DATA_DIR is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve data directory: %w", err)
	}
	s := &LocalStore{
		root:    abs,
		blobs:   filepath.Join(abs, "objects"),
		meta:    filepath.Join(abs, "meta"),
		uploads: filepath.Join(abs, ".uploads"),
	}
	for _, dir := range []string{s.root, s.blobs, s.meta, s.uploads} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("create data directory: %w", err)
		}
	}
	return s, nil
}

func (s *LocalStore) blobPath(id string) (string, error) {
	if !ValidID(id) {
		return "", ErrNotFound
	}
	return filepath.Join(s.blobs, id), nil
}

func (s *LocalStore) metaPath(id string) (string, error) {
	if !ValidID(id) {
		return "", ErrNotFound
	}
	return filepath.Join(s.meta, id+".json"), nil
}

func (s *LocalStore) PutStream(ctx context.Context, id string, r io.Reader, _ string, limit int64) (total int64, retErr error) {
	dst, err := s.blobPath(id)
	if err != nil {
		return 0, err
	}
	tmp := filepath.Join(s.uploads, id+".part")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return 0, fmt.Errorf("create upload: %w", err)
	}
	defer func() {
		_ = f.Close()
		if retErr != nil {
			_ = os.Remove(tmp)
		}
	}()

	reader := io.Reader(&contextReader{ctx: ctx, r: r})
	if limit > 0 {
		reader = io.LimitReader(reader, limit+1)
	}
	total, err = io.CopyBuffer(f, reader, make([]byte, 1<<20))
	if err != nil {
		return total, fmt.Errorf("write upload: %w", err)
	}
	if limit > 0 && total > limit {
		return total, fmt.Errorf("upload exceeds limit of %d bytes", limit)
	}
	if err := f.Sync(); err != nil {
		return total, fmt.Errorf("sync upload: %w", err)
	}
	if err := f.Close(); err != nil {
		return total, fmt.Errorf("close upload: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return total, fmt.Errorf("commit upload: %w", err)
	}
	_ = syncDir(s.blobs)
	return total, nil
}

func (s *LocalStore) PutMeta(_ context.Context, m *Meta) error {
	dst, err := s.metaPath(m.ID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.uploads, m.ID+".json.part")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(body); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, dst); err != nil {
		return err
	}
	ok = true
	_ = syncDir(s.meta)
	return nil
}

func (s *LocalStore) GetMeta(_ context.Context, id string) (*Meta, error) {
	path, err := s.metaPath(id)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var m Meta
	if err := json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *LocalStore) GetBlob(_ context.Context, id, rangeHeader string) (*BlobResult, error) {
	path, err := s.blobPath(id)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	start, end, ranged, err := parseByteRange(rangeHeader, st.Size())
	if err != nil {
		f.Close()
		return nil, err
	}
	if !ranged {
		return &BlobResult{Body: f, ContentLength: st.Size()}, nil
	}
	length := end - start + 1
	body := &sectionReadCloser{Reader: io.NewSectionReader(f, start, length), close: f}
	return &BlobResult{
		Body:          body,
		ContentLength: length,
		ContentRange:  fmt.Sprintf("bytes %d-%d/%d", start, end, st.Size()),
	}, nil
}

func (s *LocalStore) Delete(_ context.Context, id string) error {
	blob, err := s.blobPath(id)
	if err != nil {
		return err
	}
	meta, _ := s.metaPath(id)
	for _, path := range []string{blob, meta} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *LocalStore) ListMeta(ctx context.Context, fn func(*Meta) error) error {
	entries, err := os.ReadDir(s.meta)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !ValidID(id) {
			continue
		}
		m, err := s.GetMeta(ctx, id)
		if err != nil {
			continue
		}
		if err := fn(m); err != nil {
			return err
		}
	}
	return nil
}

func (s *LocalStore) AbortStaleUploads(_ context.Context, olderThan time.Duration) int {
	cutoff := time.Now().Add(-olderThan)
	removed := 0
	if entries, err := os.ReadDir(s.uploads); err == nil {
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			if err := os.Remove(filepath.Join(s.uploads, entry.Name())); err == nil {
				removed++
			}
		}
	}

	// A crash after committing the blob but before committing its metadata can
	// leave an unreachable object. Reclaim it once it is safely older than the
	// same stale-upload window.
	if entries, err := os.ReadDir(s.blobs); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !ValidID(entry.Name()) {
				continue
			}
			info, err := entry.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			meta, _ := s.metaPath(entry.Name())
			if _, err := os.Stat(meta); !os.IsNotExist(err) {
				continue
			}
			if err := os.Remove(filepath.Join(s.blobs, entry.Name())); err == nil {
				removed++
			}
		}
	}
	return removed
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.r.Read(p)
	}
}

type sectionReadCloser struct {
	io.Reader
	close io.Closer
}

func (r *sectionReadCloser) Close() error { return r.close.Close() }

func parseByteRange(header string, size int64) (start, end int64, ranged bool, err error) {
	if header == "" {
		return 0, size - 1, false, nil
	}
	if !strings.HasPrefix(header, "bytes=") || strings.Contains(header, ",") || size == 0 {
		return 0, 0, false, ErrInvalidRange
	}
	parts := strings.SplitN(strings.TrimPrefix(header, "bytes="), "-", 2)
	if len(parts) != 2 {
		return 0, 0, false, ErrInvalidRange
	}
	if parts[0] == "" {
		suffix, e := strconv.ParseInt(parts[1], 10, 64)
		if e != nil || suffix <= 0 {
			return 0, 0, false, ErrInvalidRange
		}
		if suffix > size {
			suffix = size
		}
		return size - suffix, size - 1, true, nil
	}
	start, err = strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false, ErrInvalidRange
	}
	end = size - 1
	if parts[1] != "" {
		end, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || end < start {
			return 0, 0, false, ErrInvalidRange
		}
		if end >= size {
			end = size - 1
		}
	}
	return start, end, true, nil
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
