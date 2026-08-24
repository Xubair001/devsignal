// Package config is the single source of truth for environment configuration.
// Nothing outside this package may read the environment — that rule is what
// keeps configuration discoverable and testable (CLAUDE.md convention).
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Env      string
	LogLevel string
	LogFmt   string

	DatabaseURL      string
	DatabaseMaxConns int32
	DatabaseMinConns int32

	RedisURL string

	S3Endpoint  string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
	S3PathStyle bool

	HTTPAddr         string
	HTTPReadTimeout  time.Duration
	HTTPWriteTimeout time.Duration
	ShutdownTimeout  time.Duration

	// Extraction. The model is a config value so tiers can be compared without a
	// code change; an empty key lets the SDK resolve credentials itself.
	AnthropicAPIKey string
	ExtractionModel string

	OTelEnabled     bool
	OTelExporter    string
	OTelServiceName string
	OTelSampleRatio float64
}

// Load reads configuration from the environment and validates it. It fails
// loudly at startup rather than surfacing a nil field ten minutes into a run.
func Load() (*Config, error) {
	c := &Config{
		Env:              str("DEVSIGNAL_ENV", "local"),
		LogLevel:         str("DEVSIGNAL_LOG_LEVEL", "info"),
		LogFmt:           str("DEVSIGNAL_LOG_FORMAT", "json"),
		DatabaseURL:      str("DATABASE_URL", ""),
		DatabaseMaxConns: int32(num("DATABASE_MAX_CONNS", 10)),
		DatabaseMinConns: int32(num("DATABASE_MIN_CONNS", 2)),
		RedisURL:         str("REDIS_URL", ""),
		S3Endpoint:       str("S3_ENDPOINT", ""),
		S3Bucket:         str("S3_BUCKET", "devsignal-raw"),
		S3AccessKey:      str("S3_ACCESS_KEY", ""),
		S3SecretKey:      str("S3_SECRET_KEY", ""),
		S3PathStyle:      boolean("S3_USE_PATH_STYLE", true),
		HTTPAddr:         str("HTTP_ADDR", ":8080"),
		HTTPReadTimeout:  dur("HTTP_READ_TIMEOUT", 15*time.Second),
		HTTPWriteTimeout: dur("HTTP_WRITE_TIMEOUT", 30*time.Second),
		ShutdownTimeout:  dur("SHUTDOWN_TIMEOUT", 30*time.Second),
		AnthropicAPIKey:  str("ANTHROPIC_API_KEY", ""),
		ExtractionModel:  str("EXTRACTION_MODEL", "claude-opus-5"),
		OTelEnabled:      boolean("OTEL_ENABLED", true),
		OTelExporter:     str("OTEL_EXPORTER", "stdout"),
		OTelServiceName:  str("OTEL_SERVICE_NAME", "devsignal"),
		OTelSampleRatio:  ratio("OTEL_SAMPLE_RATIO", 1.0),
	}
	return c, c.validate()
}

func (c *Config) validate() error {
	var missing []string
	if c.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if len(missing) > 0 {
		return fmt.Errorf("config: required but unset: %s", strings.Join(missing, ", "))
	}
	if c.DatabaseMinConns > c.DatabaseMaxConns {
		return fmt.Errorf("config: DATABASE_MIN_CONNS (%d) > DATABASE_MAX_CONNS (%d)",
			c.DatabaseMinConns, c.DatabaseMaxConns)
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("config: SHUTDOWN_TIMEOUT must be positive")
	}
	return nil
}

func str(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func num(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func boolean(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func dur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func ratio(k string, def float64) float64 {
	if v := os.Getenv(k); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			return f
		}
	}
	return def
}
