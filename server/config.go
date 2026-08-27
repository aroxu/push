package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds every knob the service exposes. Everything is environment
// driven so the whole stack stays declarative inside docker-compose.
type Config struct {
	Addr           string
	PublicBaseURL  string
	TrustedProxies []string
	StorageBackend string
	DataDir        string

	S3Endpoint  string
	S3Region    string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string

	MaxUploadBytes  int64
	PartSize        int64
	PartConcurrency int
	UploadSlots     int

	Retention    time.Duration
	ReaperEvery  time.Duration
	LogRetention time.Duration
	LogDir       string

	RateBurst   int
	RatePerMin  float64
	MaxBodyIdle time.Duration
	ReadTimeout time.Duration
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBytes(key string, def int64) int64 {
	v := strings.TrimSpace(strings.ToUpper(os.Getenv(key)))
	if v == "" {
		return def
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(v, "TB"), strings.HasSuffix(v, "T"):
		mult, v = 1<<40, strings.TrimRight(strings.TrimSuffix(v, "B"), "T")
	case strings.HasSuffix(v, "GB"), strings.HasSuffix(v, "G"):
		mult, v = 1<<30, strings.TrimRight(strings.TrimSuffix(v, "B"), "G")
	case strings.HasSuffix(v, "MB"), strings.HasSuffix(v, "M"):
		mult, v = 1<<20, strings.TrimRight(strings.TrimSuffix(v, "B"), "M")
	case strings.HasSuffix(v, "KB"), strings.HasSuffix(v, "K"):
		mult, v = 1<<10, strings.TrimRight(strings.TrimSuffix(v, "B"), "K")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return def
	}
	return n * mult
}

func envDur(key string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func LoadConfig() (*Config, error) {
	c := &Config{
		Addr:           env("PUSH_ADDR", ":3234"),
		PublicBaseURL:  strings.TrimRight(env("PUSH_PUBLIC_URL", "http://localhost:3234"), "/"),
		StorageBackend: strings.ToLower(env("PUSH_STORAGE", "local")),
		DataDir:        env("PUSH_DATA_DIR", "/data"),

		S3Endpoint:  env("PUSH_S3_ENDPOINT", "http://garage:3900"),
		S3Region:    env("PUSH_S3_REGION", "garage"),
		S3Bucket:    env("PUSH_S3_BUCKET", "push"),
		S3AccessKey: os.Getenv("PUSH_S3_ACCESS_KEY"),
		S3SecretKey: os.Getenv("PUSH_S3_SECRET_KEY"),

		MaxUploadBytes:  envBytes("PUSH_MAX_UPLOAD", 32<<30), // 32 GiB
		PartSize:        envBytes("PUSH_PART_SIZE", 16<<20),  // 16 MiB
		PartConcurrency: envInt("PUSH_PART_CONCURRENCY", 8),
		UploadSlots:     envInt("PUSH_UPLOAD_SLOTS", 32),

		Retention:    envDur("PUSH_RETENTION", 24*time.Hour),
		ReaperEvery:  envDur("PUSH_REAPER_INTERVAL", 10*time.Minute),
		LogRetention: envDur("PUSH_LOG_RETENTION", 7*24*time.Hour),
		LogDir:       env("PUSH_LOG_DIR", "/var/log/push"),

		RateBurst:   envInt("PUSH_RATE_BURST", 30),
		RatePerMin:  float64(envInt("PUSH_RATE_PER_MIN", 60)),
		ReadTimeout: envDur("PUSH_READ_TIMEOUT", 6*time.Hour),
	}

	for _, p := range strings.Split(env("PUSH_TRUSTED_PROXIES", ""), ",") {
		if p = strings.TrimSpace(p); p != "" {
			c.TrustedProxies = append(c.TrustedProxies, p)
		}
	}

	if c.StorageBackend != "local" && c.StorageBackend != "garage" && c.StorageBackend != "s3" {
		return nil, fmt.Errorf("PUSH_STORAGE must be local, garage, or s3")
	}
	if c.StorageBackend != "local" && (c.S3AccessKey == "" || c.S3SecretKey == "") {
		return nil, fmt.Errorf("PUSH_S3_ACCESS_KEY and PUSH_S3_SECRET_KEY are required")
	}
	if c.StorageBackend != "local" && c.PartSize < 5<<20 {
		return nil, fmt.Errorf("PUSH_PART_SIZE must be at least 5MiB (S3 multipart minimum)")
	}
	if c.PartConcurrency < 1 {
		c.PartConcurrency = 1
	}
	return c, nil
}
