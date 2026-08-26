package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Meta is the sidecar document stored next to every blob. Keeping metadata in
// the object store means the service itself is completely stateless and can be
// scaled horizontally without a database.
type Meta struct {
	ID          string    `json:"id"`
	Filename    string    `json:"filename"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type"`
	SHA256      string    `json:"sha256"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	DeleteHash  string    `json:"delete_hash"`
}

var ErrNotFound = errors.New("not found")

type Store struct {
	c        *s3.Client
	bucket   string
	partSize int64
	partConc int
	bufPool  sync.Pool
}

func NewStore(ctx context.Context, cfg *Config) (*Store, error) {
	client := s3.New(s3.Options{
		Region:       cfg.S3Region,
		BaseEndpoint: aws.String(cfg.S3Endpoint),
		Credentials: credentials.NewStaticCredentialsProvider(
			cfg.S3AccessKey, cfg.S3SecretKey, ""),
		UsePathStyle: true,
		HTTPClient: &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				MaxIdleConns:        256,
				MaxIdleConnsPerHost: 256,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	})

	s := &Store{
		c:        client,
		bucket:   cfg.S3Bucket,
		partSize: cfg.PartSize,
		partConc: cfg.PartConcurrency,
	}
	s.bufPool.New = func() any {
		b := make([]byte, cfg.PartSize)
		return &b
	}

	if err := s.ensureBucket(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) ensureBucket(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	_, err := s.c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	if err == nil {
		return nil
	}
	_, cerr := s.c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(s.bucket)})
	if cerr != nil {
		var owned *types.BucketAlreadyOwnedByYou
		var exists *types.BucketAlreadyExists
		if errors.As(cerr, &owned) || errors.As(cerr, &exists) {
			return nil
		}
		return fmt.Errorf("bucket %q unreachable: %w (head: %v)", s.bucket, cerr, err)
	}
	return nil
}

func blobKey(id string) string { return "blob/" + id }
func metaKey(id string) string { return "meta/" + id + ".json" }

// part is a chunk of the incoming stream handed to an upload worker.
type part struct {
	num  int32
	buf  *[]byte
	size int
}

// PutStream copies r into the object store using a concurrent multipart upload.
//
// The request body is read strictly sequentially (it is a single stream) but
// each 16 MiB chunk is handed to one of N workers that PUT in parallel. This is
// what keeps throughput high for very large files while memory stays bounded at
// partSize * (partConc + 1).
//
// Any failure aborts the multipart upload so Garage never keeps orphaned parts.
func (s *Store) PutStream(ctx context.Context, id string, r io.Reader, contentType string, limit int64) (int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	create, err := s.c.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(blobKey(id)),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return 0, fmt.Errorf("create multipart: %w", err)
	}
	uploadID := create.UploadId

	abort := func() {
		// Detached context: the request context is usually already dead here.
		actx, acancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer acancel()
		_, _ = s.c.AbortMultipartUpload(actx, &s3.AbortMultipartUploadInput{
			Bucket:   aws.String(s.bucket),
			Key:      aws.String(blobKey(id)),
			UploadId: uploadID,
		})
	}

	var (
		mu        sync.Mutex
		completed []types.CompletedPart
		firstErr  error
		once      sync.Once
	)
	fail := func(e error) {
		once.Do(func() {
			mu.Lock()
			firstErr = e
			mu.Unlock()
			cancel()
		})
	}

	jobs := make(chan part, s.partConc)
	var wg sync.WaitGroup
	for i := 0; i < s.partConc; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				body := (*p.buf)[:p.size]
				res, uerr := s.c.UploadPart(ctx, &s3.UploadPartInput{
					Bucket:        aws.String(s.bucket),
					Key:           aws.String(blobKey(id)),
					UploadId:      uploadID,
					PartNumber:    aws.Int32(p.num),
					Body:          bytes.NewReader(body),
					ContentLength: aws.Int64(int64(p.size)),
				})
				s.bufPool.Put(p.buf)
				if uerr != nil {
					fail(fmt.Errorf("upload part %d: %w", p.num, uerr))
					continue
				}
				mu.Lock()
				completed = append(completed, types.CompletedPart{
					ETag:       res.ETag,
					PartNumber: aws.Int32(p.num),
				})
				mu.Unlock()
			}
		}()
	}

	var total int64
	var partNum int32
	readErr := func() error {
		for {
			buf := s.bufPool.Get().(*[]byte)
			n, rerr := io.ReadFull(r, *buf)
			if n > 0 {
				total += int64(n)
				if limit > 0 && total > limit {
					s.bufPool.Put(buf)
					return fmt.Errorf("upload exceeds limit of %d bytes", limit)
				}
				partNum++
				select {
				case jobs <- part{num: partNum, buf: buf, size: n}:
				case <-ctx.Done():
					s.bufPool.Put(buf)
					return context.Cause(ctx)
				}
			} else {
				s.bufPool.Put(buf)
			}
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				return nil
			}
			if rerr != nil {
				return rerr
			}
		}
	}()

	close(jobs)
	wg.Wait()

	if readErr != nil {
		abort()
		return total, readErr
	}
	mu.Lock()
	ferr := firstErr
	mu.Unlock()
	if ferr != nil {
		abort()
		return total, ferr
	}

	// Zero byte uploads still need one (empty) part to be a valid object.
	if partNum == 0 {
		res, uerr := s.c.UploadPart(ctx, &s3.UploadPartInput{
			Bucket:        aws.String(s.bucket),
			Key:           aws.String(blobKey(id)),
			UploadId:      uploadID,
			PartNumber:    aws.Int32(1),
			Body:          bytes.NewReader(nil),
			ContentLength: aws.Int64(0),
		})
		if uerr != nil {
			abort()
			return 0, uerr
		}
		completed = append(completed, types.CompletedPart{ETag: res.ETag, PartNumber: aws.Int32(1)})
	}

	sort.Slice(completed, func(i, j int) bool {
		return *completed[i].PartNumber < *completed[j].PartNumber
	})

	if _, err := s.c.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(s.bucket),
		Key:             aws.String(blobKey(id)),
		UploadId:        uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	}); err != nil {
		abort()
		return total, fmt.Errorf("complete multipart: %w", err)
	}
	return total, nil
}

func (s *Store) PutMeta(ctx context.Context, m *Meta) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = s.c.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(metaKey(m.ID)),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("application/json"),
	})
	return err
}

func (s *Store) GetMeta(ctx context.Context, id string) (*Meta, error) {
	res, err := s.c.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(metaKey(id)),
	})
	if err != nil {
		var nsk *types.NoSuchKey
		var nf *types.NotFound
		if errors.As(err, &nsk) || errors.As(err, &nf) {
			return nil, ErrNotFound
		}
		if strings.Contains(err.Error(), "StatusCode: 404") {
			return nil, ErrNotFound
		}
		return nil, err
	}
	defer res.Body.Close()

	var m Meta
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// GetBlob returns a streaming reader, optionally honouring an HTTP Range header
// so large downloads are resumable.
func (s *Store) GetBlob(ctx context.Context, id, rangeHeader string) (*s3.GetObjectOutput, error) {
	in := &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(blobKey(id)),
	}
	if rangeHeader != "" {
		in.Range = aws.String(rangeHeader)
	}
	res, err := s.c.GetObject(ctx, in)
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) || strings.Contains(err.Error(), "StatusCode: 404") {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return res, nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	for _, k := range []string{blobKey(id), metaKey(id)} {
		if _, err := s.c.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(k),
		}); err != nil {
			return err
		}
	}
	return nil
}

// ListMeta walks every sidecar document; used by the expiry reaper.
func (s *Store) ListMeta(ctx context.Context, fn func(*Meta) error) error {
	p := s3.NewListObjectsV2Paginator(s.c, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String("meta/"),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			id := strings.TrimSuffix(strings.TrimPrefix(key, "meta/"), ".json")
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
	}
	return nil
}

// AbortStaleUploads cleans multipart uploads left behind by crashed requests.
func (s *Store) AbortStaleUploads(ctx context.Context, olderThan time.Duration) int {
	res, err := s.c.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
		Bucket: aws.String(s.bucket),
	})
	if err != nil {
		return 0
	}
	n := 0
	cutoff := time.Now().Add(-olderThan)
	for _, u := range res.Uploads {
		if u.Initiated != nil && u.Initiated.After(cutoff) {
			continue
		}
		if _, err := s.c.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
			Bucket:   aws.String(s.bucket),
			Key:      u.Key,
			UploadId: u.UploadId,
		}); err == nil {
			n++
		}
	}
	return n
}
