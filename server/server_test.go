package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tamnd/vec"
	"github.com/tamnd/vec/server/vecpb"
)

// newTestServer opens an in-memory database, builds a server over it, and starts
// the writer pipeline so write paths have a goroutine to run on. The returned
// stop func cancels the pipeline and closes the database.
func newTestServer(t *testing.T, cfg Config) (*Server, func()) {
	t.Helper()
	db, err := vec.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	s, err := NewWithDB(cfg, db, true)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runWriter(ctx)
	}()
	stop := func() {
		cancel()
		s.wg.Wait()
		_ = db.Close()
	}
	return s, stop
}

func noAuthConfig() Config {
	cfg := DefaultConfig()
	cfg.AuthMode = "none"
	return cfg
}

// doJSON runs one request against a handler and decodes the JSON response.
func doJSON(t *testing.T, h http.Handler, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

func TestRESTRoundTrip(t *testing.T) {
	s, stop := newTestServer(t, noAuthConfig())
	defer stop()
	h := s.restHandler()

	// Create a collection.
	code, _ := doJSON(t, h, http.MethodPost, "/v1/collections", "", map[string]any{
		"name":   "docs",
		"config": map[string]any{"dim": 3, "metric": "l2"},
	})
	if code != http.StatusCreated {
		t.Fatalf("create: got %d", code)
	}

	// Upsert two points.
	code, _ = doJSON(t, h, http.MethodPost, "/v1/collections/docs/points", "", map[string]any{
		"points": []map[string]any{
			{"id": 1, "vector": []float32{1, 0, 0}, "payload": map[string]any{"tag": "a"}},
			{"id": 2, "vector": []float32{0, 1, 0}, "payload": map[string]any{"tag": "b"}},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("upsert: got %d", code)
	}

	// Query nearest to [1,0,0]; id 1 should rank first.
	code, body := doJSON(t, h, http.MethodPost, "/v1/collections/docs/query", "", map[string]any{
		"vector": []float32{1, 0, 0}, "top_k": 2, "with_payload": true,
	})
	if code != http.StatusOK {
		t.Fatalf("query: got %d", code)
	}
	results, ok := body["results"].([]any)
	if !ok || len(results) == 0 {
		t.Fatalf("query returned no results: %v", body)
	}
	first := results[0].(map[string]any)
	if first["id"].(float64) != 1 {
		t.Fatalf("expected id 1 first, got %v", first["id"])
	}

	// Get the point back.
	code, got := doJSON(t, h, http.MethodGet, "/v1/collections/docs/points/1", "", nil)
	if code != http.StatusOK {
		t.Fatalf("get point: got %d body=%v", code, got)
	}
	if got["id"].(float64) != 1 {
		t.Fatalf("get returned wrong id: %v", got)
	}

	// Delete it, then a fetch should 404.
	code, _ = doJSON(t, h, http.MethodDelete, "/v1/collections/docs/points/1", "", nil)
	if code != http.StatusOK {
		t.Fatalf("delete: got %d", code)
	}
	code, _ = doJSON(t, h, http.MethodGet, "/v1/collections/docs/points/1", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("get after delete: got %d want 404", code)
	}
}

func TestRESTFilteredQuery(t *testing.T) {
	s, stop := newTestServer(t, noAuthConfig())
	defer stop()
	h := s.restHandler()

	doJSON(t, h, http.MethodPost, "/v1/collections", "", map[string]any{
		"name":   "items",
		"config": map[string]any{"dim": 2, "metric": "l2", "column_types": map[string]any{"price": "int64"}},
	})
	doJSON(t, h, http.MethodPost, "/v1/collections/items/points", "", map[string]any{
		"points": []map[string]any{
			{"id": 1, "vector": []float32{1, 0}, "payload": map[string]any{"price": 10}},
			{"id": 2, "vector": []float32{1, 0}, "payload": map[string]any{"price": 100}},
		},
	})

	// Filter price > 50 leaves only id 2.
	code, body := doJSON(t, h, http.MethodPost, "/v1/collections/items/query", "", map[string]any{
		"vector": []float32{1, 0}, "top_k": 10,
		"filter": map[string]any{
			"field": "price",
			"range": map[string]any{"gt": 50},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("filtered query: got %d body=%v", code, body)
	}
	results, _ := body["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d: %v", len(results), body)
	}
	if results[0].(map[string]any)["id"].(float64) != 2 {
		t.Fatalf("expected id 2, got %v", results[0])
	}
}

func TestRESTAuth(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AuthMode = "token"
	cfg.Tokens = []Token{
		{ID: "admin", Secret: "admin-secret", Role: "admin"},
		{ID: "reader", Secret: "reader-secret", Role: "reader"},
	}
	s, stop := newTestServer(t, cfg)
	defer stop()
	h := s.restHandler()

	// No token is rejected.
	code, _ := doJSON(t, h, http.MethodGet, "/v1/collections", "", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d want 401", code)
	}

	// A reader token cannot create a collection (admin only).
	code, _ = doJSON(t, h, http.MethodPost, "/v1/collections", "reader-secret", map[string]any{
		"name": "x", "config": map[string]any{"dim": 2},
	})
	if code != http.StatusForbidden {
		t.Fatalf("reader create: got %d want 403", code)
	}

	// The admin token can.
	code, _ = doJSON(t, h, http.MethodPost, "/v1/collections", "admin-secret", map[string]any{
		"name": "x", "config": map[string]any{"dim": 2},
	})
	if code != http.StatusCreated {
		t.Fatalf("admin create: got %d want 201", code)
	}

	// A reader token can list.
	code, _ = doJSON(t, h, http.MethodGet, "/v1/collections", "reader-secret", nil)
	if code != http.StatusOK {
		t.Fatalf("reader list: got %d want 200", code)
	}
}

func TestRESTHealth(t *testing.T) {
	s, stop := newTestServer(t, DefaultConfig())
	defer stop()
	h := s.restHandler()

	// Health needs no token even under token auth.
	code, body := doJSON(t, h, http.MethodGet, "/v1/health", "", nil)
	if code != http.StatusOK {
		t.Fatalf("health: got %d", code)
	}
	if body["status"] != "ok" {
		t.Fatalf("health status: %v", body)
	}
}

func TestGRPCRoundTrip(t *testing.T) {
	s, stop := newTestServer(t, noAuthConfig())
	defer stop()
	ctx := context.Background()
	id := anonymous

	// CreateCollection.
	out, code, msg := s.dispatchGRPC(ctx, id, "CreateCollection",
		(&vecpb.CreateCollectionRequest{Name: "g", Config: &vecpb.CollectionConfig{Dim: 3, Metric: vecpb.MetricL2}}).Marshal())
	if code != grpcOK {
		t.Fatalf("create: code=%d msg=%s", code, msg)
	}
	cresp, err := vecpb.UnmarshalCreateCollectionResponse(out)
	if err != nil || cresp.Info == nil || cresp.Info.Name != "g" {
		t.Fatalf("create response: %v %+v", err, cresp)
	}

	// Upsert.
	up := &vecpb.UpsertRequest{Collection: "g", Points: []*vecpb.Point{
		{ID: 1, Vector: &vecpb.Vector{Data: vecpb.EncodeVector([]float32{1, 0, 0})}},
		{ID: 2, Vector: &vecpb.Vector{Data: vecpb.EncodeVector([]float32{0, 1, 0})}},
	}}
	out, code, msg = s.dispatchGRPC(ctx, id, "Upsert", up.Marshal())
	if code != grpcOK {
		t.Fatalf("upsert: code=%d msg=%s", code, msg)
	}
	uresp, _ := vecpb.UnmarshalUpsertResponse(out)
	if uresp.Upserted != 2 {
		t.Fatalf("upserted: %d", uresp.Upserted)
	}

	// Query.
	q := &vecpb.QueryRequest{Collection: "g", TopK: 2, QueryVector: &vecpb.Vector{Data: vecpb.EncodeVector([]float32{1, 0, 0})}}
	out, code, msg = s.dispatchGRPC(ctx, id, "Query", q.Marshal())
	if code != grpcOK {
		t.Fatalf("query: code=%d msg=%s", code, msg)
	}
	qresp, _ := vecpb.UnmarshalQueryResponse(out)
	if len(qresp.Results) == 0 || qresp.Results[0].Point.ID != 1 {
		t.Fatalf("query results: %+v", qresp.Results)
	}
}

func TestGRPCUnauthenticated(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AuthMode = "token"
	cfg.Tokens = []Token{{ID: "a", Secret: "sec", Role: "admin"}}
	s, stop := newTestServer(t, cfg)
	defer stop()

	srv := httptest.NewServer(http.HandlerFunc(s.serveGRPC))
	defer srv.Close()

	// A gRPC call with no token gets the Unauthenticated status in the trailer.
	body := frameGRPC((&vecpb.HealthRequest{}).Marshal())
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/vec.VecService/Query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/grpc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if got := resp.Trailer.Get("grpc-status"); got != "16" {
		t.Fatalf("grpc-status trailer = %q want 16", got)
	}
}

func TestGRPCHealthOverHTTP(t *testing.T) {
	s, stop := newTestServer(t, DefaultConfig())
	defer stop()
	srv := httptest.NewServer(http.HandlerFunc(s.serveGRPC))
	defer srv.Close()

	body := frameGRPC((&vecpb.HealthRequest{}).Marshal())
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/vec.VecService/Health", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/grpc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := resp.Trailer.Get("grpc-status"); got != "0" {
		t.Fatalf("grpc-status = %q want 0", got)
	}
	msg, err := unframeGRPC(payload)
	if err != nil {
		t.Fatalf("unframe: %v", err)
	}
	hresp, err := vecpb.UnmarshalHealthResponse(msg)
	if err != nil || hresp.Status != "ok" {
		t.Fatalf("health response: %v %+v", err, hresp)
	}
}

func TestMetricsExposition(t *testing.T) {
	s, stop := newTestServer(t, noAuthConfig())
	defer stop()
	s.metrics.observeRequest("GET", "docs", "ok")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	s.metrics.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics: got %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("vec_requests_total")) {
		t.Fatalf("metrics body missing counter:\n%s", rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("vec_go_goroutines")) {
		t.Fatalf("metrics body missing runtime gauge")
	}
}

func TestConfigPrecedence(t *testing.T) {
	t.Setenv("VEC_REST_ADDR", "127.0.0.1:9999")
	cfg, err := ParseConfig([]string{"-grpc", "127.0.0.1:8000", "/tmp/data.vec"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.GRPCAddr != "127.0.0.1:8000" {
		t.Fatalf("grpc flag not applied: %q", cfg.GRPCAddr)
	}
	if cfg.RESTAddr != "127.0.0.1:9999" {
		t.Fatalf("rest env not applied: %q", cfg.RESTAddr)
	}
	if cfg.Path != "/tmp/data.vec" {
		t.Fatalf("path not applied: %q", cfg.Path)
	}
}

// frameGRPC wraps a message in the gRPC length-prefixed framing for a test call.
func frameGRPC(msg []byte) []byte {
	out := make([]byte, 5+len(msg))
	out[0] = 0
	out[1] = byte(len(msg) >> 24)
	out[2] = byte(len(msg) >> 16)
	out[3] = byte(len(msg) >> 8)
	out[4] = byte(len(msg))
	copy(out[5:], msg)
	return out
}

// unframeGRPC reads the first gRPC frame's message body from a response.
func unframeGRPC(b []byte) ([]byte, error) {
	return readGRPCMessage(bytes.NewReader(b))
}

var _ = time.Second
