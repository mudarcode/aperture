// Command benchreport renders the JSON results written by bench/run.sh as a
// Markdown report. It only formats measured data; it computes nothing that
// was not measured except simple ratios (averages from Prometheus sums and
// counts).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type result struct {
	Label  string `json:"label"`
	Config struct {
		Clients     int  `json:"Clients"`
		SpansPerReq int  `json:"SpansPerReq"`
		Gzip        bool `json:"Gzip"`
		Duration    int64
	} `json:"config"`
	Environment     map[string]string  `json:"environment"`
	ElapsedSeconds  float64            `json:"elapsedSeconds"`
	Requests        int64              `json:"requests"`
	AckedSpans      int64              `json:"ackedSpans"`
	StatusCounts    map[string]int64   `json:"statusCounts"`
	TransportErrors int64              `json:"transportErrors"`
	SpansPerSecond  float64            `json:"spansPerSecond"`
	RequestsPerSec  float64            `json:"requestsPerSecond"`
	LatencyMs       map[string]float64 `json:"ackLatencyMs"`
	RejectLatencyMs map[string]float64 `json:"rejectLatencyMs"`
	RequestBytes    int                `json:"requestBytesOnWire"`
	Durability      string             `json:"durabilityCheck"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: benchreport <results-dir>")
		os.Exit(2)
	}
	dir := os.Args[1]
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "no results in", dir)
		os.Exit(1)
	}
	var results []result
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			panic(err)
		}
		var r result
		if err := json.Unmarshal(b, &r); err != nil {
			panic(fmt.Errorf("%s: %w", f, err))
		}
		results = append(results, r)
	}
	// Keep the order the scenarios ran in.
	sort.SliceStable(results, func(i, j int) bool {
		return modTime(dir, results[i].Label) < modTime(dir, results[j].Label)
	})

	env := results[0].Environment
	fmt.Printf("# Ingestion benchmark results\n\n")
	fmt.Printf("Run directory: `%s`\n\n", filepath.Base(dir))
	fmt.Printf("| Environment | |\n|---|---|\n")
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("| %s | %s |\n", k, env[k])
	}
	fmt.Printf("\nLoad generator, Aperture, and PostgreSQL ran on the same machine and share its CPUs.\n")
	fmt.Printf("Throughput counts only spans acknowledged with HTTP 200, i.e. committed to PostgreSQL.\n\n")

	fmt.Printf("| Scenario | Aperture settings | Clients | Spans/req | Spans/s acked | Req/s | p50 ms | p95 ms | p99 ms | Non-200 | Avg batch (spans / reqs) | Avg commit ms | Durability check |\n")
	fmt.Printf("|---|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|---|\n")
	for _, r := range results {
		settings := strings.TrimSpace(readFile(filepath.Join(dir, r.Label+".env")))
		settings = strings.ReplaceAll(settings, "APERTURE_", "")
		m := readMetrics(filepath.Join(dir, r.Label+".metrics.txt"))
		non200 := int64(0)
		var parts []string
		for code, n := range r.StatusCounts {
			if code != "200" {
				non200 += n
				parts = append(parts, fmt.Sprintf("%s×%d", code, n))
			}
		}
		non := fmt.Sprint(non200)
		if len(parts) > 0 {
			sort.Strings(parts)
			non = strings.Join(parts, ", ")
		}
		if r.RejectLatencyMs != nil {
			non += fmt.Sprintf(" (503 p50 %.1f ms, p99 %.1f ms)", r.RejectLatencyMs["p50"], r.RejectLatencyMs["p99"])
		}
		if r.TransportErrors > 0 {
			non += fmt.Sprintf(" (+%d transport errors)", r.TransportErrors)
		}
		batch := "–"
		if c := m["aperture_pipeline_batch_spans_count"]; c > 0 {
			batch = fmt.Sprintf("%.0f / %.1f", m["aperture_pipeline_batch_spans_sum"]/c, m["aperture_pipeline_batch_requests_sum"]/m["aperture_pipeline_batch_requests_count"])
		}
		commit := "–"
		if c := m["aperture_pipeline_flush_duration_seconds_count"]; c > 0 {
			commit = fmt.Sprintf("%.1f", 1000*m["aperture_pipeline_flush_duration_seconds_sum"]/c)
		}
		fmt.Printf("| %s | %s | %d | %d | %s | %.0f | %.1f | %.1f | %.1f | %s | %s | %s | %s |\n",
			r.Label, settings, r.Config.Clients, r.Config.SpansPerReq, thousands(r.SpansPerSecond), r.RequestsPerSec,
			r.LatencyMs["p50"], r.LatencyMs["p95"], r.LatencyMs["p99"], non, batch, commit, r.Durability)
	}
	fmt.Printf("\nAverage batch and commit figures come from Aperture's own Prometheus metrics and include the warm-up period.\n")
}

func modTime(dir, label string) int64 {
	st, err := os.Stat(filepath.Join(dir, label+".json"))
	if err != nil {
		return 0
	}
	return st.ModTime().UnixNano()
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func readMetrics(p string) map[string]float64 {
	out := map[string]float64{}
	f, err := os.Open(p)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 {
			v, _ := strconv.ParseFloat(fields[1], 64)
			out[fields[0]] = v
		}
	}
	return out
}

func thousands(f float64) string {
	s := strconv.FormatInt(int64(f+0.5), 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
