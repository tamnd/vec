package server

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tamnd/vec"
	"github.com/tamnd/vec/server/vecpb"
)

// gRPC status codes used by this server (a subset of the canonical set).
const (
	grpcOK                 = 0
	grpcInvalidArgument    = 3
	grpcNotFound           = 5
	grpcAlreadyExists      = 6
	grpcPermissionDenied   = 7
	grpcResourceExhausted  = 8
	grpcAborted            = 10
	grpcUnimplemented      = 12
	grpcInternal           = 13
	grpcUnavailable        = 14
	grpcUnauthenticated    = 16
	maxGRPCMessageBytesCap = 64 << 20
)

// grpcServer builds the HTTP/2 server that speaks gRPC. The wire framing and the
// proto3 codec are hand-rolled (the project takes no grpc-go or protobuf runtime
// dependency), so the handler reads length-prefixed messages off the request
// body and writes the reply plus a grpc-status trailer. HTTP/2 comes from the
// stdlib server when TLS is configured; without TLS the stdlib server does not
// negotiate HTTP/2, so plaintext gRPC needs a TLS listener.
func (s *Server) grpcServer() *http.Server {
	return &http.Server{
		Handler:           http.HandlerFunc(s.serveGRPC),
		ReadHeaderTimeout: 10 * time.Second,
	}
}

// serveGRPC dispatches one gRPC call. The path is /vec.VecService/{Method}.
func (s *Server) serveGRPC(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
		http.Error(w, "expected application/grpc", http.StatusUnsupportedMediaType)
		return
	}
	w.Header().Set("Content-Type", "application/grpc")
	// Predeclaring the status trailers forces chunked framing so they are sent
	// even when a reply body precedes them.
	w.Header().Set("Trailer", "Grpc-Status, Grpc-Message")

	method := lastSegment(r.URL.Path)

	// Health is reachable without a token so a load balancer can probe it.
	if method == "Health" {
		req, err := readGRPCMessage(r.Body)
		if err != nil {
			finishGRPC(w, nil, grpcInternal, err.Error())
			return
		}
		out, code, msg := s.grpcHealth(req)
		finishGRPC(w, out, code, msg)
		return
	}

	id, err := s.auth.verify(bearer(r.Header.Get("Authorization")))
	if err != nil {
		finishGRPC(w, nil, grpcUnauthenticated, err.Error())
		return
	}

	req, err := readGRPCMessage(r.Body)
	if err != nil {
		finishGRPC(w, nil, grpcInternal, err.Error())
		return
	}

	if method == "StreamQuery" {
		s.grpcStreamQuery(w, r.Context(), id, req)
		return
	}

	out, code, msg := s.dispatchGRPC(r.Context(), id, method, req)
	finishGRPC(w, out, code, msg)
}

// dispatchGRPC runs one unary method and returns its reply bytes plus status.
func (s *Server) dispatchGRPC(ctx context.Context, id Identity, method string, req []byte) ([]byte, int, string) {
	switch method {
	case "CreateCollection":
		return s.grpcCreateCollection(ctx, id, req)
	case "DropCollection":
		return s.grpcDropCollection(ctx, id, req)
	case "GetCollection":
		return s.grpcGetCollection(ctx, id, req)
	case "ListCollections":
		return s.grpcListCollections(ctx, id, req)
	case "Upsert":
		return s.grpcUpsert(ctx, id, req)
	case "Delete":
		return s.grpcDelete(ctx, id, req)
	case "GetPoints":
		return s.grpcGetPoints(ctx, id, req)
	case "Query":
		return s.grpcQuery(ctx, id, req)
	case "BatchQuery":
		return s.grpcBatchQuery(ctx, id, req)
	case "Scroll":
		return nil, grpcUnimplemented, "scroll is not supported by this build"
	case "Reindex":
		return s.grpcReindex(ctx, id, req)
	case "Vacuum":
		return s.grpcVacuum(ctx, id, req)
	case "Backup":
		return s.grpcBackup(ctx, id, req)
	case "OperationStatus":
		return s.grpcOperationStatus(ctx, id, req)
	default:
		return nil, grpcUnimplemented, "unknown method " + method
	}
}

func (s *Server) grpcCreateCollection(ctx context.Context, id Identity, req []byte) ([]byte, int, string) {
	m, err := vecpb.UnmarshalCreateCollectionRequest(req)
	if err != nil {
		return nil, grpcInvalidArgument, err.Error()
	}
	if id.Role < RoleAdmin {
		return nil, grpcPermissionDenied, errPermDenied.Error()
	}
	if !id.CanAccess(m.Name) {
		return nil, grpcPermissionDenied, errNoCollAcces.Error()
	}
	cfg := collectionConfig{}
	if m.Config != nil {
		cfg = pbToConfig(m.Config)
	}
	info, err := s.createCollection(ctx, m.Name, cfg)
	if err != nil {
		return grpcFail(err)
	}
	return marshal(&vecpb.CreateCollectionResponse{Info: infoToPB(info)})
}

func (s *Server) grpcDropCollection(ctx context.Context, id Identity, req []byte) ([]byte, int, string) {
	m, err := vecpb.UnmarshalDropCollectionRequest(req)
	if err != nil {
		return nil, grpcInvalidArgument, err.Error()
	}
	if id.Role < RoleAdmin {
		return nil, grpcPermissionDenied, errPermDenied.Error()
	}
	if !id.CanAccess(m.Name) {
		return nil, grpcPermissionDenied, errNoCollAcces.Error()
	}
	if err := s.dropCollection(ctx, m.Name); err != nil {
		return grpcFail(err)
	}
	return marshal(&vecpb.DropCollectionResponse{Dropped: true})
}

func (s *Server) grpcGetCollection(ctx context.Context, id Identity, req []byte) ([]byte, int, string) {
	m, err := vecpb.UnmarshalGetCollectionRequest(req)
	if err != nil {
		return nil, grpcInvalidArgument, err.Error()
	}
	if !id.CanAccess(m.Name) {
		return nil, grpcPermissionDenied, errNoCollAcces.Error()
	}
	info, err := s.getCollection(ctx, m.Name)
	if err != nil {
		return grpcFail(err)
	}
	return marshal(&vecpb.GetCollectionResponse{Info: infoToPB(info)})
}

func (s *Server) grpcListCollections(ctx context.Context, id Identity, req []byte) ([]byte, int, string) {
	if _, err := vecpb.UnmarshalListCollectionsRequest(req); err != nil {
		return nil, grpcInvalidArgument, err.Error()
	}
	infos, err := s.listCollections(ctx)
	if err != nil {
		return grpcFail(err)
	}
	resp := &vecpb.ListCollectionsResponse{}
	for _, ci := range infos {
		if id.CanAccess(ci.Name) {
			resp.Collections = append(resp.Collections, infoToPB(ci))
		}
	}
	return marshal(resp)
}

func (s *Server) grpcUpsert(ctx context.Context, id Identity, req []byte) ([]byte, int, string) {
	m, err := vecpb.UnmarshalUpsertRequest(req)
	if err != nil {
		return nil, grpcInvalidArgument, err.Error()
	}
	if id.Role < RoleReadWrite {
		return nil, grpcPermissionDenied, errPermDenied.Error()
	}
	if !id.CanAccess(m.Collection) {
		return nil, grpcPermissionDenied, errNoCollAcces.Error()
	}
	if len(m.Points) > s.cfg.MaxUpsertBatch {
		return nil, grpcInvalidArgument, "batch exceeds max upsert size"
	}
	pts := make([]point, len(m.Points))
	for i, p := range m.Points {
		pts[i] = pbToPoint(p)
	}
	n, err := s.upsertPoints(ctx, m.Collection, pts)
	if err != nil {
		return grpcFail(err)
	}
	return marshal(&vecpb.UpsertResponse{Upserted: uint64(n)})
}

func (s *Server) grpcDelete(ctx context.Context, id Identity, req []byte) ([]byte, int, string) {
	m, err := vecpb.UnmarshalDeleteRequest(req)
	if err != nil {
		return nil, grpcInvalidArgument, err.Error()
	}
	if id.Role < RoleReadWrite {
		return nil, grpcPermissionDenied, errPermDenied.Error()
	}
	if !id.CanAccess(m.Collection) {
		return nil, grpcPermissionDenied, errNoCollAcces.Error()
	}
	n, err := s.deletePoints(ctx, m.Collection, m.IDs)
	if err != nil {
		return grpcFail(err)
	}
	return marshal(&vecpb.DeleteResponse{Deleted: uint64(n)})
}

func (s *Server) grpcGetPoints(ctx context.Context, id Identity, req []byte) ([]byte, int, string) {
	m, err := vecpb.UnmarshalGetPointsRequest(req)
	if err != nil {
		return nil, grpcInvalidArgument, err.Error()
	}
	if !id.CanAccess(m.Collection) {
		return nil, grpcPermissionDenied, errNoCollAcces.Error()
	}
	pts, err := s.getPoints(ctx, m.Collection, m.IDs, m.WithVectors, m.WithPayload)
	if err != nil {
		return grpcFail(err)
	}
	resp := &vecpb.GetPointsResponse{}
	for _, p := range pts {
		resp.Points = append(resp.Points, pointToPB(p))
	}
	return marshal(resp)
}

func (s *Server) grpcQuery(ctx context.Context, id Identity, req []byte) ([]byte, int, string) {
	m, err := vecpb.UnmarshalQueryRequest(req)
	if err != nil {
		return nil, grpcInvalidArgument, err.Error()
	}
	if !id.CanAccess(m.Collection) {
		return nil, grpcPermissionDenied, errNoCollAcces.Error()
	}
	res, err := s.queryPoints(ctx, m.Collection, pbToQuery(m))
	if err != nil {
		return grpcFail(err)
	}
	resp := &vecpb.QueryResponse{SearchTime: res.SearchTimeMS / 1000, StrategyUsed: res.Strategy}
	for _, sp := range res.Results {
		resp.Results = append(resp.Results, scoredToPB(sp))
	}
	return marshal(resp)
}

func (s *Server) grpcBatchQuery(ctx context.Context, id Identity, req []byte) ([]byte, int, string) {
	m, err := vecpb.UnmarshalBatchQueryRequest(req)
	if err != nil {
		return nil, grpcInvalidArgument, err.Error()
	}
	if !id.CanAccess(m.Collection) {
		return nil, grpcPermissionDenied, errNoCollAcces.Error()
	}
	resp := &vecpb.BatchQueryResponse{}
	for _, q := range m.Queries {
		coll := q.Collection
		if coll == "" {
			coll = m.Collection
		}
		res, err := s.queryPoints(ctx, coll, pbToQuery(q))
		if err != nil {
			return grpcFail(err)
		}
		one := &vecpb.BatchQueryResult{SearchTime: res.SearchTimeMS / 1000, StrategyUsed: res.Strategy}
		for _, sp := range res.Results {
			one.Results = append(one.Results, scoredToPB(sp))
		}
		resp.Results = append(resp.Results, one)
	}
	return marshal(resp)
}

func (s *Server) grpcReindex(ctx context.Context, id Identity, req []byte) ([]byte, int, string) {
	m, err := vecpb.UnmarshalReindexRequest(req)
	if err != nil {
		return nil, grpcInvalidArgument, err.Error()
	}
	if id.Role < RoleAdmin {
		return nil, grpcPermissionDenied, errPermDenied.Error()
	}
	if !id.CanAccess(m.Collection) {
		return nil, grpcPermissionDenied, errNoCollAcces.Error()
	}
	op := s.reindex(ctx, m.Collection)
	return marshal(&vecpb.ReindexResponse{OperationID: op})
}

func (s *Server) grpcVacuum(ctx context.Context, id Identity, req []byte) ([]byte, int, string) {
	if _, err := vecpb.UnmarshalVacuumRequest(req); err != nil {
		return nil, grpcInvalidArgument, err.Error()
	}
	if id.Role < RoleAdmin {
		return nil, grpcPermissionDenied, errPermDenied.Error()
	}
	freed, err := s.vacuum(ctx)
	if err != nil {
		return grpcFail(err)
	}
	return marshal(&vecpb.VacuumResponse{BytesFreed: uint64(freed)})
}

func (s *Server) grpcBackup(ctx context.Context, id Identity, req []byte) ([]byte, int, string) {
	m, err := vecpb.UnmarshalBackupRequest(req)
	if err != nil {
		return nil, grpcInvalidArgument, err.Error()
	}
	if id.Role < RoleAdmin {
		return nil, grpcPermissionDenied, errPermDenied.Error()
	}
	size, err := s.backup(ctx, m.DestPath)
	if err != nil {
		return grpcFail(err)
	}
	return marshal(&vecpb.BackupResponse{SizeBytes: uint64(size), Path: m.DestPath})
}

func (s *Server) grpcOperationStatus(_ context.Context, _ Identity, req []byte) ([]byte, int, string) {
	m, err := vecpb.UnmarshalOperationStatusRequest(req)
	if err != nil {
		return nil, grpcInvalidArgument, err.Error()
	}
	op, ok := s.ops.get(m.OperationID)
	if !ok {
		return nil, grpcNotFound, "no such operation"
	}
	return marshal(&vecpb.OperationStatusResponse{
		OperationID: op.ID,
		State:       string(op.State),
		Progress:    op.Progress,
		Error:       op.Err,
	})
}

func (s *Server) grpcHealth(req []byte) ([]byte, int, string) {
	if _, err := vecpb.UnmarshalHealthRequest(req); err != nil {
		return nil, grpcInvalidArgument, err.Error()
	}
	return marshal(&vecpb.HealthResponse{
		Status:  "ok",
		Version: vec.Version(),
		UptimeS: uint64(time.Since(s.started).Seconds()),
	})
}

// grpcStreamQuery runs one query and writes its results as a server stream, one
// StreamQueryResponse frame per page followed by a done frame.
func (s *Server) grpcStreamQuery(w http.ResponseWriter, ctx context.Context, id Identity, req []byte) {
	m, err := vecpb.UnmarshalStreamQueryRequest(req)
	if err != nil {
		finishGRPC(w, nil, grpcInvalidArgument, err.Error())
		return
	}
	if m.Request == nil {
		finishGRPC(w, nil, grpcInvalidArgument, "missing query request")
		return
	}
	if !id.CanAccess(m.Request.Collection) {
		finishGRPC(w, nil, grpcPermissionDenied, errNoCollAcces.Error())
		return
	}
	res, err := s.queryPoints(ctx, m.Request.Collection, pbToQuery(m.Request))
	if err != nil {
		out, code, msg := grpcFail(err)
		finishGRPC(w, out, code, msg)
		return
	}
	page := int(m.PageSize)
	if page <= 0 {
		page = 100
	}
	flusher, _ := w.(http.Flusher)
	var sent uint64
	for i := 0; i < len(res.Results); i += page {
		end := min(i+page, len(res.Results))
		chunk := &vecpb.StreamQueryResponse{}
		for _, sp := range res.Results[i:end] {
			chunk.Results = append(chunk.Results, scoredToPB(sp))
		}
		sent += uint64(end - i)
		chunk.TotalSent = sent
		if err := writeGRPCMessage(w, chunk.Marshal()); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	done := &vecpb.StreamQueryResponse{Done: true, TotalSent: sent}
	_ = writeGRPCMessage(w, done.Marshal())
	setGRPCTrailer(w, grpcOK, "")
}

// pbToQuery converts a proto QueryRequest into the neutral query type.
func pbToQuery(m *vecpb.QueryRequest) queryRequest {
	q := queryRequest{
		TopK:        int(m.TopK),
		Params:      m.Params,
		WithVectors: m.WithVectors,
		WithPayload: m.WithPayload,
		IndexName:   m.IndexName,
		Filter:      pbFilterToNode(m.Filter),
	}
	if m.QueryVector != nil {
		q.Vector = vecpb.DecodeVector(m.QueryVector.Data)
	}
	return q
}

func pbFilterToNode(f *vecpb.Filter) *filterNode {
	if f == nil {
		return nil
	}
	switch f.Kind {
	case vecpb.ClauseMust:
		n := &filterNode{}
		for _, c := range f.Children {
			n.Must = append(n.Must, pbFilterToNode(c))
		}
		return n
	case vecpb.ClauseShould:
		n := &filterNode{}
		for _, c := range f.Children {
			n.Should = append(n.Should, pbFilterToNode(c))
		}
		return n
	case vecpb.ClauseMustNot:
		n := &filterNode{}
		for _, c := range f.Children {
			n.MustNot = append(n.MustNot, pbFilterToNode(c))
		}
		return n
	case vecpb.ClauseCond:
		return pbCondToNode(f.Cond)
	default:
		return nil
	}
}

func pbCondToNode(c *vecpb.Condition) *filterNode {
	if c == nil {
		return nil
	}
	n := &filterNode{Field: c.Field}
	switch c.Kind {
	case vecpb.TestRange:
		if c.Range != nil {
			n.Range = &rangeTest{Gt: c.Range.Gt, Gte: c.Range.Gte, Lt: c.Range.Lt, Lte: c.Range.Lte}
		}
	case vecpb.TestMatch:
		if c.Match != nil {
			n.Match = &matchTest{Value: pbValueToAny(c.Match.Value)}
		}
	case vecpb.TestIsNull:
		if c.IsNull != nil {
			b := c.IsNull.IsNull
			n.IsNull = &b
		}
	}
	return n
}

func pbToPoint(p *vecpb.Point) point {
	out := point{ID: p.ID}
	if p.Vector != nil {
		out.Vector = vecpb.DecodeVector(p.Vector.Data)
	}
	if p.Payload != nil {
		out.Payload = make(map[string]any, len(p.Payload))
		for k, v := range p.Payload {
			out.Payload[k] = pbValueToAny(v)
		}
	}
	return out
}

func pointToPB(p point) *vecpb.Point {
	pb := &vecpb.Point{ID: p.ID}
	if p.Vector != nil {
		pb.Vector = &vecpb.Vector{Data: vecpb.EncodeVector(p.Vector)}
	}
	if p.Payload != nil {
		pb.Payload = make(map[string]*vecpb.Value, len(p.Payload))
		for k, v := range p.Payload {
			pb.Payload[k] = anyToPBValue(v)
		}
	}
	return pb
}

func scoredToPB(sp scoredPoint) *vecpb.ScoredPoint {
	return &vecpb.ScoredPoint{
		Point: pointToPB(point{ID: sp.ID, Vector: sp.Vector, Payload: sp.Payload}),
		Score: sp.Score,
		Rank:  sp.Rank,
	}
}

func pbValueToAny(v *vecpb.Value) any {
	if v == nil {
		return nil
	}
	switch v.Kind {
	case vecpb.ValueInt:
		return v.IntVal
	case vecpb.ValueFloat:
		return v.FloatVal
	case vecpb.ValueBool:
		return v.BoolVal
	case vecpb.ValueStr:
		return v.StrVal
	case vecpb.ValueBytes:
		return v.BytesVal
	default:
		return nil
	}
}

func anyToPBValue(a any) *vecpb.Value {
	switch x := a.(type) {
	case nil:
		return &vecpb.Value{Kind: vecpb.ValueNull}
	case int64:
		return &vecpb.Value{Kind: vecpb.ValueInt, IntVal: x}
	case int:
		return &vecpb.Value{Kind: vecpb.ValueInt, IntVal: int64(x)}
	case float64:
		return &vecpb.Value{Kind: vecpb.ValueFloat, FloatVal: x}
	case bool:
		return &vecpb.Value{Kind: vecpb.ValueBool, BoolVal: x}
	case string:
		return &vecpb.Value{Kind: vecpb.ValueStr, StrVal: x}
	case []byte:
		return &vecpb.Value{Kind: vecpb.ValueBytes, BytesVal: x}
	default:
		return &vecpb.Value{Kind: vecpb.ValueNull}
	}
}

func pbToConfig(c *vecpb.CollectionConfig) collectionConfig {
	return collectionConfig{
		Dim:         int(c.Dim),
		Metric:      pbMetricName(c.Metric),
		IndexType:   c.IndexType,
		IndexParams: c.IndexParams,
		ColumnTypes: c.ColumnTypes,
	}
}

func infoToPB(ci collectionInfo) *vecpb.CollectionInfo {
	return &vecpb.CollectionInfo{
		Name:       ci.Name,
		Count:      uint64(ci.Count),
		IndexState: ci.IndexState,
		Config: &vecpb.CollectionConfig{
			Dim:         uint32(ci.Config.Dim),
			Metric:      nameToPBMetric(ci.Config.Metric),
			IndexType:   ci.Config.IndexType,
			IndexParams: ci.Config.IndexParams,
			ColumnTypes: ci.Config.ColumnTypes,
		},
	}
}

func pbMetricName(m vecpb.Metric) string {
	switch m {
	case vecpb.MetricL2:
		return "l2"
	case vecpb.MetricCosine:
		return "cosine"
	case vecpb.MetricDot:
		return "dot"
	case vecpb.MetricHamming:
		return "hamming"
	case vecpb.MetricJaccard:
		return "jaccard"
	default:
		return ""
	}
}

func nameToPBMetric(s string) vecpb.Metric {
	switch strings.ToLower(s) {
	case "l2", "euclidean":
		return vecpb.MetricL2
	case "cosine":
		return vecpb.MetricCosine
	case "dot", "ip":
		return vecpb.MetricDot
	case "hamming":
		return vecpb.MetricHamming
	case "jaccard":
		return vecpb.MetricJaccard
	default:
		return vecpb.MetricUnspecified
	}
}

// marshaler is implemented by every vecpb message.
type marshaler interface{ Marshal() []byte }

// marshal renders a reply message with an OK status.
func marshal(m marshaler) ([]byte, int, string) {
	return m.Marshal(), grpcOK, ""
}

// grpcFail maps a library error onto a gRPC status code and message.
func grpcFail(err error) ([]byte, int, string) {
	code, _ := grpcStatusFor(err)
	return nil, code, err.Error()
}

// grpcStatusFor maps a library error to a gRPC status code.
func grpcStatusFor(err error) (int, string) {
	switch {
	case errors.Is(err, vec.ErrNotFound):
		return grpcNotFound, "NOT_FOUND"
	case errors.Is(err, vec.ErrAlreadyExists):
		return grpcAlreadyExists, "ALREADY_EXISTS"
	case errors.Is(err, vec.ErrDimMismatch), errors.Is(err, vec.ErrSchemaViolation):
		return grpcInvalidArgument, "INVALID_ARGUMENT"
	case errors.Is(err, vec.ErrConflict):
		return grpcAborted, "ABORTED"
	case errors.Is(err, vec.ErrReadOnly):
		return grpcPermissionDenied, "PERMISSION_DENIED"
	case errors.Is(err, vec.ErrBusy):
		return grpcUnavailable, "UNAVAILABLE"
	default:
		return grpcInternal, "INTERNAL"
	}
}

// readGRPCMessage reads one length-prefixed gRPC frame: a one-byte compression
// flag, a four-byte big-endian length, then the message body.
func readGRPCMessage(r io.Reader) ([]byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if err == io.EOF {
			return nil, nil
		}
		return nil, err
	}
	if hdr[0] != 0 {
		return nil, errors.New("grpc: compressed messages are not supported")
	}
	n := binary.BigEndian.Uint32(hdr[1:5])
	if n > maxGRPCMessageBytesCap {
		return nil, errors.New("grpc: message exceeds size cap")
	}
	msg := make([]byte, n)
	if _, err := io.ReadFull(r, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// writeGRPCMessage writes one length-prefixed gRPC frame.
func writeGRPCMessage(w io.Writer, msg []byte) error {
	var hdr [5]byte
	binary.BigEndian.PutUint32(hdr[1:5], uint32(len(msg)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(msg)
	return err
}

// finishGRPC writes the reply frame for an OK status, then the status trailer.
// On a non-OK status no body is written, which yields a trailers-only response.
func finishGRPC(w http.ResponseWriter, out []byte, code int, msg string) {
	if code == grpcOK {
		_ = writeGRPCMessage(w, out)
	}
	setGRPCTrailer(w, code, msg)
}

// setGRPCTrailer sets the grpc-status and grpc-message trailers. serveGRPC
// predeclares these in the Trailer header, so they are written after the body.
func setGRPCTrailer(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Grpc-Status", strconv.Itoa(code))
	if msg != "" {
		w.Header().Set("Grpc-Message", grpcEncodeMessage(msg))
	}
}

// grpcEncodeMessage percent-encodes a status message per the gRPC spec: bytes
// outside the printable ASCII range, and the percent sign, are escaped.
func grpcEncodeMessage(msg string) string {
	needs := false
	for i := 0; i < len(msg); i++ {
		if c := msg[i]; c < 0x20 || c > 0x7e || c == '%' {
			needs = true
			break
		}
	}
	if !needs {
		return msg
	}
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(msg); i++ {
		c := msg[i]
		if c < 0x20 || c > 0x7e || c == '%' {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// lastSegment returns the final path element, the gRPC method name.
func lastSegment(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
