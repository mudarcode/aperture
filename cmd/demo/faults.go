package main

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// Faults configures injected failures and latency for one service.
type Faults struct {
	ErrorRate   float64 `json:"errorRate"`   // probability an operation fails
	LatencyMs   int     `json:"latencyMs"`   // extra latency added to every operation
	SlowRate    float64 `json:"slowRate"`    // probability of a latency spike
	SlowLatency int     `json:"slowLatency"` // spike size in ms
}

func (f Faults) validate() error {
	if f.ErrorRate < 0 || f.ErrorRate > 1 || f.SlowRate < 0 || f.SlowRate > 1 {
		return errors.New("rates must be in [0, 1]")
	}
	if f.LatencyMs < 0 || f.SlowLatency < 0 || f.LatencyMs > 60_000 || f.SlowLatency > 60_000 {
		return errors.New("latencies must be in [0, 60000] ms")
	}
	return nil
}

type faultState struct {
	mu sync.RWMutex
	f  Faults
}

func faultsFromEnv() (*faultState, error) {
	var f Faults
	var err error
	parse := func(k string, dst any) {
		v := os.Getenv(k)
		if v == "" || err != nil {
			return
		}
		switch d := dst.(type) {
		case *float64:
			*d, err = strconv.ParseFloat(v, 64)
		case *int:
			*d, err = strconv.Atoi(v)
		}
	}
	parse("FAULT_ERROR_RATE", &f.ErrorRate)
	parse("FAULT_LATENCY_MS", &f.LatencyMs)
	parse("FAULT_SLOW_RATE", &f.SlowRate)
	parse("FAULT_SLOW_MS", &f.SlowLatency)
	if err != nil {
		return nil, err
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	return &faultState{f: f}, nil
}

func (s *faultState) get() Faults {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.f
}

// inject sleeps for the configured latency and reports whether this
// operation should fail.
func (s *faultState) inject(ctx context.Context) (fail bool, err error) {
	f := s.get()
	d := time.Duration(f.LatencyMs) * time.Millisecond
	if f.SlowRate > 0 && rand.Float64() < f.SlowRate {
		d += time.Duration(f.SlowLatency) * time.Millisecond
	}
	if err := sleep(ctx, d); err != nil {
		return false, err
	}
	return f.ErrorRate > 0 && rand.Float64() < f.ErrorRate, nil
}

func (svc *service) getFaults(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, svc.faults.get())
}

func (svc *service) setFaults(w http.ResponseWriter, r *http.Request) {
	var f Faults
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&f); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := f.validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	svc.faults.mu.Lock()
	svc.faults.f = f
	svc.faults.mu.Unlock()
	svc.log.Info("faults updated", "faults", f)
	writeJSON(w, http.StatusOK, f)
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// jitter returns base ± 50%.
func jitter(base time.Duration) time.Duration {
	return base/2 + time.Duration(rand.Int64N(int64(base)+1))
}
