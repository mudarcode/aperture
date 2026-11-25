package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"time"
)

// runTraffic simulates shoppers: 70% browse the catalog, 30% check out one to
// three items. Its HTTP client is deliberately not instrumented so each
// trace starts at the gateway, like real browser traffic would.
func runTraffic(ctx context.Context, svc *service) error {
	gateway := os.Getenv("GATEWAY_URL")
	rps, err := strconv.ParseFloat(envOr("TRAFFIC_RPS", "5"), 64)
	if err != nil || rps <= 0 || rps > 1000 {
		return fmt.Errorf("TRAFFIC_RPS must be in (0, 1000]")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	tick := time.NewTicker(time.Duration(float64(time.Second) / rps))
	defer tick.Stop()
	svc.log.Info("generating traffic", "gateway", gateway, "rps", rps)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		go func() {
			user := fmt.Sprintf("user-%03d", rand.IntN(200))
			var req *http.Request
			if rand.Float64() < 0.7 {
				req, _ = http.NewRequestWithContext(ctx, http.MethodGet, gateway+"/api/products", nil)
			} else {
				items := make([]lineItem, 1+rand.IntN(3))
				for i := range items {
					items[i] = lineItem{ProductID: fmt.Sprintf("p%d", 1+rand.IntN(8)), Quantity: 1 + rand.IntN(2)}
				}
				body, _ := json.Marshal(map[string]any{"userId": user, "items": items})
				req, _ = http.NewRequestWithContext(ctx, http.MethodPost, gateway+"/api/checkout", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("X-User", user)
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()
	}
}
