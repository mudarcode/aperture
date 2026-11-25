package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type service struct {
	role   string
	faults *faultState
	client *http.Client
	log    *slog.Logger
}

func (svc *service) tracer() trace.Tracer { return otel.Tracer("aperture/demo/" + svc.role) }

func (svc *service) routes(mux *http.ServeMux) error {
	switch svc.role {
	case "gateway":
		catalog, checkout := os.Getenv("CATALOG_URL"), os.Getenv("CHECKOUT_URL")
		mux.HandleFunc("GET /api/products", func(w http.ResponseWriter, r *http.Request) {
			if !svc.authorize(w, r) {
				return
			}
			svc.proxy(w, r, http.MethodGet, catalog+"/products", nil)
		})
		mux.HandleFunc("POST /api/checkout", func(w http.ResponseWriter, r *http.Request) {
			if !svc.authorize(w, r) {
				return
			}
			body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
			svc.proxy(w, r, http.MethodPost, checkout+"/checkout", body)
		})
	case "catalog":
		c := newCatalog()
		mux.HandleFunc("GET /products", svc.listProducts(c))
		mux.HandleFunc("GET /products/{id}", svc.getProduct(c))
	case "inventory":
		inv := newInventory()
		mux.HandleFunc("POST /reserve", svc.reserve(inv))
		mux.HandleFunc("POST /release", svc.release(inv))
	case "payments":
		mux.HandleFunc("POST /charge", svc.charge)
	case "checkout":
		mux.HandleFunc("POST /checkout", svc.checkout(os.Getenv("CATALOG_URL"), os.Getenv("INVENTORY_URL"), os.Getenv("PAYMENTS_URL")))
	default:
		return fmt.Errorf("unknown role %q", svc.role)
	}
	return nil
}

// ---- shared helpers ----

// step runs fn inside a child span. If inject is true, configured faults are
// applied inside the span so failures and latency show up on the dependency
// that caused them.
func (svc *service) step(ctx context.Context, name string, kind trace.SpanKind, base time.Duration, inject bool,
	attrs []attribute.KeyValue, fn func(context.Context) error) error {
	ctx, span := svc.tracer().Start(ctx, name, trace.WithSpanKind(kind), trace.WithAttributes(attrs...))
	defer span.End()

	if err := sleep(ctx, jitter(base)); err != nil {
		return fail(span, err)
	}
	if inject {
		bad, err := svc.faults.inject(ctx)
		if err != nil {
			return fail(span, err)
		}
		if bad {
			return fail(span, fmt.Errorf("%s: injected failure", name))
		}
	}
	if fn != nil {
		if err := fn(ctx); err != nil {
			return fail(span, err)
		}
	}
	return nil
}

func fail(span trace.Span, err error) error {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	return err
}

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.status, e.msg) }

// call performs an instrumented HTTP request (otelhttp creates the CLIENT span
// and injects traceparent).
func (svc *service) call(ctx context.Context, method, url string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := svc.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		_ = json.Unmarshal(b, &e)
		return &httpError{status: resp.StatusCode, msg: e.Error}
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func (svc *service) proxy(w http.ResponseWriter, r *http.Request, method, url string, body []byte) {
	var in any
	if body != nil {
		in = json.RawMessage(body)
	}
	var out json.RawMessage
	if err := svc.call(r.Context(), method, url, in, &out); err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func (svc *service) authorize(w http.ResponseWriter, r *http.Request) bool {
	err := svc.step(r.Context(), "auth.verify_token", trace.SpanKindInternal, time.Millisecond, true,
		[]attribute.KeyValue{attribute.String("enduser.id", r.Header.Get("X-User"))}, nil)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "auth unavailable"})
		return false
	}
	return true
}

func writeErr(w http.ResponseWriter, err error) {
	var he *httpError
	status := http.StatusBadGateway
	msg := err.Error()
	if errors.As(err, &he) {
		status, msg = he.status, he.msg
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func dbAttrs(op, table, query string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("db.system.name", "postgresql"),
		attribute.String("db.operation.name", op),
		attribute.String("db.collection.name", table),
		attribute.String("db.query.text", query),
	}
}

// ---- catalog ----

type product struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PriceCents int    `json:"priceCents"`
}

type catalog struct{ products map[string]product }

func newCatalog() *catalog {
	c := &catalog{products: map[string]product{}}
	for i, n := range []string{"Lens cap", "Tripod", "Camera strap", "SD card 128GB", "Lens cloth", "Battery grip", "ND filter", "Camera bag"} {
		id := fmt.Sprintf("p%d", i+1)
		c.products[id] = product{ID: id, Name: n, PriceCents: 500 + i*1250}
	}
	return c
}

func (svc *service) listProducts(c *catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := svc.step(r.Context(), "SELECT products", trace.SpanKindClient, 8*time.Millisecond, true,
			dbAttrs("SELECT", "products", "SELECT id, name, price_cents FROM products ORDER BY id"), nil)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "catalog database error"})
			return
		}
		out := make([]product, 0, len(c.products))
		for i := 1; i <= len(c.products); i++ {
			out = append(out, c.products[fmt.Sprintf("p%d", i)])
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func (svc *service) getProduct(c *catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		hit := rand.Float64() < 0.8
		_ = svc.step(ctx, "cache GET product", trace.SpanKindClient, time.Millisecond, false,
			[]attribute.KeyValue{attribute.String("db.system.name", "redis"), attribute.Bool("cache.hit", hit)}, nil)
		if !hit {
			err := svc.step(ctx, "SELECT products", trace.SpanKindClient, 6*time.Millisecond, true,
				dbAttrs("SELECT", "products", "SELECT id, name, price_cents FROM products WHERE id = $1"), nil)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "catalog database error"})
				return
			}
		}
		p, ok := c.products[id]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown product " + id})
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
}

// ---- inventory ----

type inventory struct {
	mu    sync.Mutex
	stock map[string]int
}

func newInventory() *inventory {
	inv := &inventory{stock: map[string]int{}}
	for i := 1; i <= 8; i++ {
		inv.stock[fmt.Sprintf("p%d", i)] = 50
	}
	go func() { // restock so the demo keeps running indefinitely
		for range time.Tick(10 * time.Second) {
			inv.mu.Lock()
			for k, v := range inv.stock {
				inv.stock[k] = min(v+10, 50)
			}
			inv.mu.Unlock()
		}
	}()
	return inv
}

type lineItem struct {
	ProductID string `json:"productId"`
	Quantity  int    `json:"quantity"`
}

func (svc *service) reserve(inv *inventory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Items []lineItem }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Items) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid reservation"})
			return
		}
		ctx := r.Context()
		err := svc.step(ctx, "SELECT stock", trace.SpanKindClient, 4*time.Millisecond, true,
			dbAttrs("SELECT", "stock", "SELECT product_id, quantity FROM stock WHERE product_id = ANY($1) FOR UPDATE"), nil)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "inventory database error"})
			return
		}
		inv.mu.Lock()
		short := ""
		for _, it := range req.Items {
			if inv.stock[it.ProductID] < it.Quantity {
				short = it.ProductID
				break
			}
		}
		if short == "" {
			for _, it := range req.Items {
				inv.stock[it.ProductID] -= it.Quantity
			}
		}
		inv.mu.Unlock()
		if short != "" {
			trace.SpanFromContext(ctx).AddEvent("out_of_stock", trace.WithAttributes(attribute.String("product.id", short)))
			writeJSON(w, http.StatusConflict, map[string]string{"error": "out of stock: " + short})
			return
		}
		_ = svc.step(ctx, "UPDATE stock", trace.SpanKindClient, 3*time.Millisecond, false,
			dbAttrs("UPDATE", "stock", "UPDATE stock SET quantity = quantity - $2 WHERE product_id = $1"), nil)
		writeJSON(w, http.StatusOK, map[string]string{"status": "reserved"})
	}
}

func (svc *service) release(inv *inventory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Items []lineItem }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid release"})
			return
		}
		inv.mu.Lock()
		for _, it := range req.Items {
			inv.stock[it.ProductID] += it.Quantity
		}
		inv.mu.Unlock()
		_ = svc.step(r.Context(), "UPDATE stock", trace.SpanKindClient, 3*time.Millisecond, false,
			dbAttrs("UPDATE", "stock", "UPDATE stock SET quantity = quantity + $2 WHERE product_id = $1"), nil)
		writeJSON(w, http.StatusOK, map[string]string{"status": "released"})
	}
}

// ---- payments ----

func (svc *service) charge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrderID     string `json:"orderId"`
		AmountCents int    `json:"amountCents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AmountCents <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid charge"})
		return
	}
	ctx := r.Context()
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("order.id", req.OrderID), attribute.Int("payment.amount_cents", req.AmountCents))

	_ = svc.step(ctx, "fraud.score", trace.SpanKindInternal, 5*time.Millisecond, false,
		[]attribute.KeyValue{attribute.Float64("fraud.score", rand.Float64()*0.3)}, nil)
	// The external payment provider is simulated: faults injected here model
	// provider timeouts and declines.
	err := svc.step(ctx, "POST /v1/charges", trace.SpanKindClient, 35*time.Millisecond, true,
		[]attribute.KeyValue{
			attribute.String("server.address", "psp.example.com"),
			attribute.String("http.request.method", "POST"),
			attribute.String("url.full", "https://psp.example.com/v1/charges"),
		}, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "payment provider error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "charged"})
}

// ---- checkout ----

func (svc *service) checkout(catalogURL, inventoryURL, paymentsURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID string     `json:"userId"`
			Items  []lineItem `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Items) == 0 || len(req.Items) > 20 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid checkout request"})
			return
		}
		ctx := r.Context()
		orderID := fmt.Sprintf("ord_%08x", rand.Uint32())
		trace.SpanFromContext(ctx).SetAttributes(attribute.String("order.id", orderID), attribute.Int("order.items", len(req.Items)))

		// Price lookups are deliberately sequential (an N+1 pattern that is
		// easy to spot in a waterfall).
		total := 0
		for _, it := range req.Items {
			var p product
			if err := svc.call(ctx, http.MethodGet, catalogURL+"/products/"+it.ProductID, nil, &p); err != nil {
				writeErr(w, err)
				return
			}
			total += p.PriceCents * it.Quantity
		}

		if err := svc.call(ctx, http.MethodPost, inventoryURL+"/reserve", map[string]any{"items": req.Items}, nil); err != nil {
			writeErr(w, err)
			return
		}

		if err := svc.call(ctx, http.MethodPost, paymentsURL+"/charge", map[string]any{"orderId": orderID, "amountCents": total}, nil); err != nil {
			// Compensate: give the stock back. Use a detached context so the
			// release still happens if the client has gone away.
			rel := context.WithoutCancel(ctx)
			if rerr := svc.call(rel, http.MethodPost, inventoryURL+"/release", map[string]any{"items": req.Items}, nil); rerr != nil {
				svc.log.Error("release failed", "order", orderID, "err", rerr)
			}
			writeErr(w, err)
			return
		}

		_ = svc.step(ctx, "orders publish", trace.SpanKindProducer, 2*time.Millisecond, false,
			[]attribute.KeyValue{attribute.String("messaging.system", "kafka"), attribute.String("messaging.destination.name", "orders")}, nil)
		writeJSON(w, http.StatusCreated, map[string]any{"orderId": orderID, "totalCents": total})
	}
}
