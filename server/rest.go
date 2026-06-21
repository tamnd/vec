package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/tamnd/vec"
)

// restHandler builds the REST/JSON router (spec 16 §3). Go's method-aware
// ServeMux carries the routing; each handler decodes JSON into the neutral
// request types, runs the engine operation, and encodes the result.
func (s *Server) restHandler() http.Handler {
	mux := http.NewServeMux()

	// Unauthenticated operational endpoints.
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /v1/ready", s.handleReady)
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.Handle("GET /metrics", s.metrics)

	// Authenticated API endpoints.
	auth := func(role Role, h http.HandlerFunc) http.HandlerFunc {
		return s.withAuth(role, h)
	}
	mux.HandleFunc("POST /v1/collections", auth(RoleAdmin, s.handleCreateCollection))
	mux.HandleFunc("GET /v1/collections", auth(RoleReader, s.handleListCollections))
	mux.HandleFunc("GET /v1/collections/{name}", auth(RoleReader, s.handleGetCollection))
	mux.HandleFunc("DELETE /v1/collections/{name}", auth(RoleAdmin, s.handleDropCollection))

	mux.HandleFunc("POST /v1/collections/{name}/points", auth(RoleReadWrite, s.handleUpsert))
	mux.HandleFunc("GET /v1/collections/{name}/points/{id}", auth(RoleReader, s.handleGetPoint))
	mux.HandleFunc("POST /v1/collections/{name}/points/get", auth(RoleReader, s.handleGetPoints))
	mux.HandleFunc("DELETE /v1/collections/{name}/points/{id}", auth(RoleReadWrite, s.handleDeletePoint))
	mux.HandleFunc("POST /v1/collections/{name}/points/delete", auth(RoleReadWrite, s.handleDeletePoints))

	mux.HandleFunc("POST /v1/collections/{name}/query", auth(RoleReader, s.handleQuery))
	mux.HandleFunc("POST /v1/collections/{name}/scroll", auth(RoleReader, s.handleScroll))

	mux.HandleFunc("POST /v1/collections/{name}/reindex", auth(RoleAdmin, s.handleReindex))
	mux.HandleFunc("POST /v1/collections/{name}/vacuum", auth(RoleAdmin, s.handleVacuum))
	mux.HandleFunc("POST /v1/admin/backup", auth(RoleAdmin, s.handleBackup))
	mux.HandleFunc("GET /v1/admin/operations/{id}", auth(RoleReader, s.handleOperation))

	return mux
}

// withAuth wraps a handler with token verification, a collection ACL check, and
// request accounting. Health, readiness, and metrics skip this.
func (s *Server) withAuth(minRole Role, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := s.auth.verify(bearer(r.Header.Get("Authorization")))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", err)
			return
		}
		if id.Role < minRole {
			writeError(w, http.StatusForbidden, "PERMISSION_DENIED", errPermDenied)
			return
		}
		if name := r.PathValue("name"); name != "" && !id.CanAccess(name) {
			writeError(w, http.StatusForbidden, "PERMISSION_DENIED", errNoCollAcces)
			return
		}
		s.metrics.observeRequest(r.Method, r.PathValue("name"), "ok")
		h(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
	}
}

type identityKey struct{}

// jsonCollectionConfig is the wire shape of a collection config (spec 16 §3).
type jsonCollectionConfig struct {
	Dim         int               `json:"dim"`
	Metric      string            `json:"metric"`
	IndexType   string            `json:"index_type"`
	IndexParams map[string]string `json:"index_params,omitempty"`
	ColumnTypes map[string]string `json:"column_types,omitempty"`
}

type jsonCreateCollection struct {
	Name   string               `json:"name"`
	Config jsonCollectionConfig `json:"config"`
}

type jsonCollectionInfo struct {
	Name       string               `json:"name"`
	Config     jsonCollectionConfig `json:"config"`
	Count      int64                `json:"count"`
	IndexState string               `json:"index_state"`
}

func toJSONInfo(ci collectionInfo) jsonCollectionInfo {
	return jsonCollectionInfo{
		Name:       ci.Name,
		Count:      ci.Count,
		IndexState: ci.IndexState,
		Config: jsonCollectionConfig{
			Dim:         ci.Config.Dim,
			Metric:      ci.Config.Metric,
			IndexType:   ci.Config.IndexType,
			IndexParams: ci.Config.IndexParams,
			ColumnTypes: ci.Config.ColumnTypes,
		},
	}
}

func (s *Server) handleCreateCollection(w http.ResponseWriter, r *http.Request) {
	var req jsonCreateCollection
	if !decode(w, r, &req) {
		return
	}
	info, err := s.createCollection(r.Context(), req.Name, collectionConfig{
		Dim:         req.Config.Dim,
		Metric:      req.Config.Metric,
		IndexType:   req.Config.IndexType,
		IndexParams: req.Config.IndexParams,
		ColumnTypes: req.Config.ColumnTypes,
	})
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toJSONInfo(info))
}

func (s *Server) handleListCollections(w http.ResponseWriter, r *http.Request) {
	id := identityOf(r)
	infos, err := s.listCollections(r.Context())
	if err != nil {
		writeEngineError(w, err)
		return
	}
	out := make([]jsonCollectionInfo, 0, len(infos))
	for _, ci := range infos {
		if id.CanAccess(ci.Name) {
			out = append(out, toJSONInfo(ci))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"collections": out})
}

func (s *Server) handleGetCollection(w http.ResponseWriter, r *http.Request) {
	info, err := s.getCollection(r.Context(), r.PathValue("name"))
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toJSONInfo(info))
}

func (s *Server) handleDropCollection(w http.ResponseWriter, r *http.Request) {
	if err := s.dropCollection(r.Context(), r.PathValue("name")); err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dropped": true})
}

type jsonPoint struct {
	ID      uint64         `json:"id"`
	Vector  []float32      `json:"vector,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
}

type jsonUpsert struct {
	Points []jsonPoint `json:"points"`
	Wait   bool        `json:"wait"`
}

func (s *Server) handleUpsert(w http.ResponseWriter, r *http.Request) {
	var req jsonUpsert
	if !decode(w, r, &req) {
		return
	}
	if len(req.Points) > s.cfg.MaxUpsertBatch {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			fmt.Errorf("batch of %d exceeds max %d", len(req.Points), s.cfg.MaxUpsertBatch))
		return
	}
	pts := make([]point, len(req.Points))
	for i, p := range req.Points {
		pts[i] = point(p)
	}
	n, err := s.upsertPoints(r.Context(), r.PathValue("name"), pts)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"upserted": n, "updated": 0})
}

func (s *Server) handleGetPoint(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err)
		return
	}
	pts, err := s.getPoints(r.Context(), r.PathValue("name"), []uint64{id}, true, true)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	if len(pts) == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", vec.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, toJSONPoint(pts[0]))
}

type jsonIDs struct {
	IDs         []uint64 `json:"ids"`
	WithVectors bool     `json:"with_vectors"`
	WithPayload bool     `json:"with_payload"`
}

func (s *Server) handleGetPoints(w http.ResponseWriter, r *http.Request) {
	var req jsonIDs
	if !decode(w, r, &req) {
		return
	}
	pts, err := s.getPoints(r.Context(), r.PathValue("name"), req.IDs, true, true)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	out := make([]jsonPoint, len(pts))
	for i, p := range pts {
		out[i] = toJSONPoint(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"points": out})
}

func (s *Server) handleDeletePoint(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err)
		return
	}
	n, err := s.deletePoints(r.Context(), r.PathValue("name"), []uint64{id})
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

func (s *Server) handleDeletePoints(w http.ResponseWriter, r *http.Request) {
	var req jsonIDs
	if !decode(w, r, &req) {
		return
	}
	n, err := s.deletePoints(r.Context(), r.PathValue("name"), req.IDs)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

type jsonQuery struct {
	Vector      []float32         `json:"vector"`
	TopK        int               `json:"top_k"`
	Filter      *jsonFilter       `json:"filter"`
	Params      map[string]string `json:"params"`
	WithVectors bool              `json:"with_vectors"`
	WithPayload bool              `json:"with_payload"`
	IndexName   string            `json:"index_name"`
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req jsonQuery
	if !decode(w, r, &req) {
		return
	}
	qr := queryRequest{
		Vector:      req.Vector,
		TopK:        req.TopK,
		Filter:      req.Filter.toNode(),
		Params:      req.Params,
		WithVectors: req.WithVectors,
		WithPayload: req.WithPayload,
		IndexName:   req.IndexName,
	}
	res, err := s.queryPoints(r.Context(), r.PathValue("name"), qr)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	if r.URL.Query().Get("stream") == "true" {
		s.streamResults(w, res)
		return
	}
	results := make([]jsonScored, len(res.Results))
	for i, sp := range res.Results {
		results[i] = toJSONScored(sp)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results":       results,
		"search_time":   res.SearchTimeMS / 1000,
		"strategy_used": res.Strategy,
	})
}

// streamResults writes the query results as newline-delimited JSON (spec 16 §3),
// one ScoredPoint per line followed by a done sentinel.
func (s *Server) streamResults(w http.ResponseWriter, res queryResult) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	bw := bufio.NewWriter(w)
	defer func() { _ = bw.Flush() }()
	enc := json.NewEncoder(bw)
	for _, sp := range res.Results {
		_ = enc.Encode(toJSONScored(sp))
	}
	_ = enc.Encode(map[string]any{"done": true, "total": len(res.Results)})
}

func (s *Server) handleScroll(w http.ResponseWriter, r *http.Request) {
	// Scroll iterates a collection by filter with a resumable cursor. The cursor
	// rides on the bulk export path from spec 17; until that lands this surface
	// reports the operation as unsupported rather than returning a partial page.
	writeError(w, http.StatusNotImplemented, "UNIMPLEMENTED", errUnsupportedOp("scroll"))
}

func (s *Server) handleReindex(w http.ResponseWriter, r *http.Request) {
	id := s.reindex(r.Context(), r.PathValue("name"))
	writeJSON(w, http.StatusOK, map[string]any{"operation_id": id})
}

func (s *Server) handleVacuum(w http.ResponseWriter, r *http.Request) {
	freed, err := s.vacuum(r.Context())
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bytes_freed": freed})
}

type jsonBackup struct {
	DestPath string `json:"dest_path"`
}

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	var req jsonBackup
	if !decode(w, r, &req) {
		return
	}
	size, err := s.backup(r.Context(), req.DestPath)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"size_bytes": size, "path": req.DestPath})
}

func (s *Server) handleOperation(w http.ResponseWriter, r *http.Request) {
	op, ok := s.ops.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", vec.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"operation_id": op.ID,
		"state":        string(op.State),
		"progress":     op.Progress,
		"error":        op.Err,
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"version":  vec.Version(),
		"uptime_s": int64(time.Since(s.started).Seconds()),
	})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

// jsonFilter is the JSON shape of the filter DSL (spec 16 §3).
type jsonFilter struct {
	Must    []*jsonFilter `json:"must,omitempty"`
	Should  []*jsonFilter `json:"should,omitempty"`
	MustNot []*jsonFilter `json:"must_not,omitempty"`

	Field  string     `json:"field,omitempty"`
	Range  *jsonRange `json:"range,omitempty"`
	Match  *jsonMatch `json:"match,omitempty"`
	IsNull *bool      `json:"is_null,omitempty"`
}

type jsonRange struct {
	Gt  *float64 `json:"gt,omitempty"`
	Gte *float64 `json:"gte,omitempty"`
	Lt  *float64 `json:"lt,omitempty"`
	Lte *float64 `json:"lte,omitempty"`
}

type jsonMatch struct {
	Value any `json:"value"`
}

// toNode converts a JSON filter into the neutral filter tree.
func (f *jsonFilter) toNode() *filterNode {
	if f == nil {
		return nil
	}
	n := &filterNode{Field: f.Field, IsNull: f.IsNull}
	for _, c := range f.Must {
		n.Must = append(n.Must, c.toNode())
	}
	for _, c := range f.Should {
		n.Should = append(n.Should, c.toNode())
	}
	for _, c := range f.MustNot {
		n.MustNot = append(n.MustNot, c.toNode())
	}
	if f.Range != nil {
		n.Range = &rangeTest{Gt: f.Range.Gt, Gte: f.Range.Gte, Lt: f.Range.Lt, Lte: f.Range.Lte}
	}
	if f.Match != nil {
		n.Match = &matchTest{Value: f.Match.Value}
	}
	return n
}

type jsonScored struct {
	ID      uint64         `json:"id"`
	Score   float64        `json:"score"`
	Rank    uint64         `json:"rank"`
	Vector  []float32      `json:"vector,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
}

func toJSONScored(sp scoredPoint) jsonScored {
	return jsonScored(sp)
}

func toJSONPoint(p point) jsonPoint {
	return jsonPoint(p)
}

// decode reads a JSON request body, writing a 400 on a malformed body.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err)
		return false
	}
	return true
}

// writeJSON encodes v as the response body with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a structured error body (spec 16 §3 error shape).
func writeError(w http.ResponseWriter, status int, code string, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "code": code})
}

// writeEngineError maps a library error to an HTTP status and structured body.
func writeEngineError(w http.ResponseWriter, err error) {
	status, code := httpStatusFor(err)
	writeError(w, status, code, err)
}

// httpStatusFor maps a library error to an HTTP status and a code string
// (spec 16 §2.4, §3 response codes).
func httpStatusFor(err error) (int, string) {
	switch {
	case errors.Is(err, vec.ErrNotFound):
		return http.StatusNotFound, "NOT_FOUND"
	case errors.Is(err, vec.ErrAlreadyExists):
		return http.StatusConflict, "ALREADY_EXISTS"
	case errors.Is(err, vec.ErrDimMismatch), errors.Is(err, vec.ErrSchemaViolation):
		return http.StatusBadRequest, "INVALID_ARGUMENT"
	case errors.Is(err, vec.ErrConflict):
		return http.StatusConflict, "ABORTED"
	case errors.Is(err, vec.ErrReadOnly):
		return http.StatusForbidden, "PERMISSION_DENIED"
	case errors.Is(err, vec.ErrBusy):
		return http.StatusServiceUnavailable, "UNAVAILABLE"
	default:
		return http.StatusInternalServerError, "INTERNAL"
	}
}

// identityOf returns the authenticated identity attached to the request.
func identityOf(r *http.Request) Identity {
	if id, ok := r.Context().Value(identityKey{}).(Identity); ok {
		return id
	}
	return anonymous
}

// errUnsupportedOp builds an error for a surface that is not wired in this build.
func errUnsupportedOp(name string) error {
	return fmt.Errorf("%s is not supported by this build", name)
}
