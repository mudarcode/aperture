// Package config loads Aperture's configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/mudarcode/aperture/internal/otlpingest"
	"github.com/mudarcode/aperture/internal/pipeline"
)

// Config is the complete server configuration.
type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	DBMaxConns      int32
	AutoMigrate     bool
	LogLevel        string
	LogFormat       string // "json" or "text"
	ShutdownTimeout time.Duration

	Pipeline pipeline.Config
	Handler  otlpingest.HandlerConfig
	Limits   otlpingest.Limits
}

// Load reads configuration from the environment, applying defaults, and
// validates it. Every variable is documented in docs/configuration.md.
func Load() (Config, error) { return load(os.LookupEnv) }

func load(lookup func(string) (string, bool)) (Config, error) {
	e := &env{lookup: lookup}
	lim := otlpingest.DefaultLimits()
	c := Config{
		HTTPAddr:        e.str("APERTURE_HTTP_ADDR", ":8080"),
		DatabaseURL:     e.str("APERTURE_DATABASE_URL", ""),
		DBMaxConns:      int32(e.int("APERTURE_DB_MAX_CONNS", 16)),
		AutoMigrate:     e.bool("APERTURE_AUTO_MIGRATE", true),
		LogLevel:        e.str("APERTURE_LOG_LEVEL", "info"),
		LogFormat:       e.str("APERTURE_LOG_FORMAT", "json"),
		ShutdownTimeout: e.dur("APERTURE_SHUTDOWN_TIMEOUT", 30*time.Second),
		Pipeline: pipeline.Config{
			QueueCapacity: e.int("APERTURE_QUEUE_CAPACITY", 1024),
			Workers:       e.int("APERTURE_WORKERS", 4),
			BatchSize:     e.int("APERTURE_BATCH_SIZE", 2000),
			FlushInterval: e.dur("APERTURE_FLUSH_INTERVAL", 50*time.Millisecond),
			WriteTimeout:  e.dur("APERTURE_WRITE_TIMEOUT", 10*time.Second),
		},
		Handler: otlpingest.HandlerConfig{
			MaxRequestBytes:      int64(e.int("APERTURE_MAX_REQUEST_BYTES", 8<<20)),
			MaxDecompressedBytes: int64(e.int("APERTURE_MAX_DECOMPRESSED_BYTES", 32<<20)),
			MaxSpansPerRequest:   e.int("APERTURE_MAX_SPANS_PER_REQUEST", 10000),
			AckTimeout:           e.dur("APERTURE_ACK_TIMEOUT", 15*time.Second),
			RetryAfter:           e.dur("APERTURE_RETRY_AFTER", time.Second),
		},
		Limits: otlpingest.Limits{
			MaxAttributesPerSpan:   e.int("APERTURE_MAX_ATTRIBUTES_PER_SPAN", lim.MaxAttributesPerSpan),
			MaxResourceAttributes:  e.int("APERTURE_MAX_RESOURCE_ATTRIBUTES", lim.MaxResourceAttributes),
			MaxAttributeValueBytes: e.int("APERTURE_MAX_ATTRIBUTE_VALUE_BYTES", lim.MaxAttributeValueBytes),
			MaxEventsPerSpan:       e.int("APERTURE_MAX_EVENTS_PER_SPAN", lim.MaxEventsPerSpan),
			MaxLinksPerSpan:        e.int("APERTURE_MAX_LINKS_PER_SPAN", lim.MaxLinksPerSpan),
			MaxNameBytes:           e.int("APERTURE_MAX_NAME_BYTES", lim.MaxNameBytes),
			MaxFutureSkew:          e.dur("APERTURE_MAX_FUTURE_SKEW", lim.MaxFutureSkew),
			MaxSpanDuration:        e.dur("APERTURE_MAX_SPAN_DURATION", lim.MaxSpanDuration),
		},
	}
	if len(e.errs) > 0 {
		return Config{}, errors.Join(e.errs...)
	}
	return c, c.Validate()
}

// Validate checks cross-field constraints.
func (c Config) Validate() error {
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("APERTURE_DATABASE_URL is required"))
	}
	if err := c.Pipeline.Validate(); err != nil {
		errs = append(errs, err)
	}
	if c.DBMaxConns < 1 {
		errs = append(errs, errors.New("APERTURE_DB_MAX_CONNS must be >= 1"))
	}
	h := c.Handler
	if h.MaxRequestBytes < 1 || h.MaxDecompressedBytes < h.MaxRequestBytes {
		errs = append(errs, errors.New("need 1 <= APERTURE_MAX_REQUEST_BYTES <= APERTURE_MAX_DECOMPRESSED_BYTES"))
	}
	if h.MaxSpansPerRequest < 1 {
		errs = append(errs, errors.New("APERTURE_MAX_SPANS_PER_REQUEST must be >= 1"))
	}
	if h.AckTimeout <= c.Pipeline.FlushInterval {
		errs = append(errs, errors.New("APERTURE_ACK_TIMEOUT must exceed APERTURE_FLUSH_INTERVAL"))
	}
	if c.LogFormat != "json" && c.LogFormat != "text" {
		errs = append(errs, fmt.Errorf("APERTURE_LOG_FORMAT must be json or text, got %q", c.LogFormat))
	}
	l := c.Limits
	if l.MaxAttributesPerSpan < 1 || l.MaxResourceAttributes < 1 || l.MaxAttributeValueBytes < 1 ||
		l.MaxEventsPerSpan < 0 || l.MaxLinksPerSpan < 0 || l.MaxNameBytes < 1 ||
		l.MaxFutureSkew < 0 || l.MaxSpanDuration <= 0 {
		errs = append(errs, errors.New("span limits must be positive"))
	}
	return errors.Join(errs...)
}

type env struct {
	lookup func(string) (string, bool)
	errs   []error
}

func (e *env) str(k, def string) string {
	if v, ok := e.lookup(k); ok && v != "" {
		return v
	}
	return def
}

func (e *env) int(k string, def int) int {
	v, ok := e.lookup(k)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: invalid integer %q", k, v))
		return def
	}
	return n
}

func (e *env) bool(k string, def bool) bool {
	v, ok := e.lookup(k)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: invalid boolean %q", k, v))
		return def
	}
	return b
}

func (e *env) dur(k string, def time.Duration) time.Duration {
	v, ok := e.lookup(k)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: invalid duration %q", k, v))
		return def
	}
	return d
}
