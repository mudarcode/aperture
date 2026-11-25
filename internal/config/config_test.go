package config

import (
	"strings"
	"testing"
	"time"
)

func lookup(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestLoadDefaults(t *testing.T) {
	c, err := load(lookup(map[string]string{"APERTURE_DATABASE_URL": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8080" || c.Pipeline.Workers != 4 || c.Pipeline.QueueCapacity != 1024 ||
		c.Pipeline.FlushInterval != 50*time.Millisecond || !c.AutoMigrate {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := load(lookup(map[string]string{
		"APERTURE_DATABASE_URL":   "postgres://x",
		"APERTURE_WORKERS":        "9",
		"APERTURE_FLUSH_INTERVAL": "5ms",
		"APERTURE_AUTO_MIGRATE":   "false",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Pipeline.Workers != 9 || c.Pipeline.FlushInterval != 5*time.Millisecond || c.AutoMigrate {
		t.Fatalf("overrides not applied: %+v", c)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := map[string]map[string]string{
		"missing database url": {},
		"bad int":              {"APERTURE_DATABASE_URL": "x", "APERTURE_WORKERS": "four"},
		"bad duration":         {"APERTURE_DATABASE_URL": "x", "APERTURE_FLUSH_INTERVAL": "soon"},
		"bad bool":             {"APERTURE_DATABASE_URL": "x", "APERTURE_AUTO_MIGRATE": "maybe"},
		"zero workers":         {"APERTURE_DATABASE_URL": "x", "APERTURE_WORKERS": "0"},
		"ack below flush":      {"APERTURE_DATABASE_URL": "x", "APERTURE_ACK_TIMEOUT": "1ms"},
		"bad log format":       {"APERTURE_DATABASE_URL": "x", "APERTURE_LOG_FORMAT": "xml"},
		"decompressed < wire":  {"APERTURE_DATABASE_URL": "x", "APERTURE_MAX_DECOMPRESSED_BYTES": "10"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := load(lookup(env)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	_, err := load(lookup(map[string]string{"APERTURE_WORKERS": "x", "APERTURE_BATCH_SIZE": "y"}))
	if err == nil || !strings.Contains(err.Error(), "APERTURE_WORKERS") || !strings.Contains(err.Error(), "APERTURE_BATCH_SIZE") {
		t.Fatalf("err = %v", err)
	}
}
