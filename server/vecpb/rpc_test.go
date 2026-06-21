package vecpb

import (
	"reflect"
	"testing"
)

func samplePoint(id uint64) *Point {
	return &Point{
		ID:     id,
		Vector: &Vector{Data: EncodeVector([]float32{0.1, 0.2, 0.3})},
		Payload: map[string]*Value{
			"title": {Kind: ValueStr, StrVal: "doc"},
		},
	}
}

func sampleFilter() *Filter {
	return &Filter{Kind: ClauseCond, Cond: &Condition{
		Field: "ts",
		Kind:  TestRange,
		Range: &RangeTest{Gte: f64(1000)},
	}}
}

// TestRPCRoundTrip marshals then unmarshals every request/response type with all
// fields populated and asserts deep equality.
func TestRPCRoundTrip(t *testing.T) {
	cfg := &CollectionConfig{
		Dim: 3, Metric: MetricDot, IndexType: "hnsw",
		IndexParams: map[string]string{"m": "16"},
		ColumnTypes: map[string]string{"ts": "int"},
	}
	info := &CollectionInfo{Name: "docs", Config: cfg, Count: 2, SizeBytes: 99, IndexState: "ready"}
	qr := &QueryRequest{
		Collection:  "docs",
		QueryVector: &Vector{Data: EncodeVector([]float32{1, 2, 3})},
		TopK:        5,
		Filter:      sampleFilter(),
		Strategy:    SearchStrategyANN,
		Params:      map[string]string{"ef_search": "128"},
		WithVectors: true,
		WithPayload: true,
		IndexName:   "idx",
	}
	sp := &ScoredPoint{Point: samplePoint(1), Score: 0.5, Rank: 1}

	cases := []struct {
		name      string
		marshal   func() []byte
		unmarshal func([]byte) (any, error)
		want      any
	}{
		{"CreateCollectionRequest",
			func() []byte { return (&CreateCollectionRequest{Name: "docs", Config: cfg}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalCreateCollectionRequest(b) },
			&CreateCollectionRequest{Name: "docs", Config: cfg}},
		{"CreateCollectionResponse",
			func() []byte { return (&CreateCollectionResponse{Info: info}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalCreateCollectionResponse(b) },
			&CreateCollectionResponse{Info: info}},
		{"DropCollectionRequest",
			func() []byte { return (&DropCollectionRequest{Name: "docs"}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalDropCollectionRequest(b) },
			&DropCollectionRequest{Name: "docs"}},
		{"DropCollectionResponse",
			func() []byte { return (&DropCollectionResponse{Dropped: true}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalDropCollectionResponse(b) },
			&DropCollectionResponse{Dropped: true}},
		{"GetCollectionRequest",
			func() []byte { return (&GetCollectionRequest{Name: "docs"}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalGetCollectionRequest(b) },
			&GetCollectionRequest{Name: "docs"}},
		{"GetCollectionResponse",
			func() []byte { return (&GetCollectionResponse{Info: info}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalGetCollectionResponse(b) },
			&GetCollectionResponse{Info: info}},
		{"ListCollectionsResponse",
			func() []byte { return (&ListCollectionsResponse{Collections: []*CollectionInfo{info}}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalListCollectionsResponse(b) },
			&ListCollectionsResponse{Collections: []*CollectionInfo{info}}},
		{"UpsertRequest",
			func() []byte {
				return (&UpsertRequest{Collection: "docs", Points: []*Point{samplePoint(1), samplePoint(2)}, Wait: true}).Marshal()
			},
			func(b []byte) (any, error) { return UnmarshalUpsertRequest(b) },
			&UpsertRequest{Collection: "docs", Points: []*Point{samplePoint(1), samplePoint(2)}, Wait: true}},
		{"UpsertResponse",
			func() []byte { return (&UpsertResponse{Upserted: 1, Updated: 1, OperationID: "op1"}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalUpsertResponse(b) },
			&UpsertResponse{Upserted: 1, Updated: 1, OperationID: "op1"}},
		{"DeleteRequest",
			func() []byte { return (&DeleteRequest{Collection: "docs", IDs: []uint64{1, 2, 3}}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalDeleteRequest(b) },
			&DeleteRequest{Collection: "docs", IDs: []uint64{1, 2, 3}}},
		{"DeleteResponse",
			func() []byte { return (&DeleteResponse{Deleted: 3}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalDeleteResponse(b) },
			&DeleteResponse{Deleted: 3}},
		{"GetPointsRequest",
			func() []byte {
				return (&GetPointsRequest{Collection: "docs", IDs: []uint64{9, 8}, WithVectors: true, WithPayload: true}).Marshal()
			},
			func(b []byte) (any, error) { return UnmarshalGetPointsRequest(b) },
			&GetPointsRequest{Collection: "docs", IDs: []uint64{9, 8}, WithVectors: true, WithPayload: true}},
		{"GetPointsResponse",
			func() []byte { return (&GetPointsResponse{Points: []*Point{samplePoint(1)}}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalGetPointsResponse(b) },
			&GetPointsResponse{Points: []*Point{samplePoint(1)}}},
		{"QueryRequest",
			func() []byte { return qr.Marshal() },
			func(b []byte) (any, error) { return UnmarshalQueryRequest(b) },
			qr},
		{"QueryResponse",
			func() []byte {
				return (&QueryResponse{Results: []*ScoredPoint{sp}, SearchTime: 0.001, StrategyUsed: "ann:hnsw"}).Marshal()
			},
			func(b []byte) (any, error) { return UnmarshalQueryResponse(b) },
			&QueryResponse{Results: []*ScoredPoint{sp}, SearchTime: 0.001, StrategyUsed: "ann:hnsw"}},
		{"BatchQueryRequest",
			func() []byte { return (&BatchQueryRequest{Collection: "docs", Queries: []*QueryRequest{qr}}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalBatchQueryRequest(b) },
			&BatchQueryRequest{Collection: "docs", Queries: []*QueryRequest{qr}}},
		{"BatchQueryResponse",
			func() []byte {
				return (&BatchQueryResponse{Results: []*BatchQueryResult{{Results: []*ScoredPoint{sp}, SearchTime: 0.002, StrategyUsed: "flat"}}}).Marshal()
			},
			func(b []byte) (any, error) { return UnmarshalBatchQueryResponse(b) },
			&BatchQueryResponse{Results: []*BatchQueryResult{{Results: []*ScoredPoint{sp}, SearchTime: 0.002, StrategyUsed: "flat"}}}},
		{"StreamQueryRequest",
			func() []byte { return (&StreamQueryRequest{Request: qr, PageSize: 100}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalStreamQueryRequest(b) },
			&StreamQueryRequest{Request: qr, PageSize: 100}},
		{"StreamQueryResponse",
			func() []byte {
				return (&StreamQueryResponse{Results: []*ScoredPoint{sp}, Done: true, TotalSent: 1}).Marshal()
			},
			func(b []byte) (any, error) { return UnmarshalStreamQueryResponse(b) },
			&StreamQueryResponse{Results: []*ScoredPoint{sp}, Done: true, TotalSent: 1}},
		{"ScrollRequest",
			func() []byte {
				return (&ScrollRequest{Collection: "docs", Filter: sampleFilter(), Limit: 100, OffsetToken: []byte{1, 2}, WithVectors: true, WithPayload: true}).Marshal()
			},
			func(b []byte) (any, error) { return UnmarshalScrollRequest(b) },
			&ScrollRequest{Collection: "docs", Filter: sampleFilter(), Limit: 100, OffsetToken: []byte{1, 2}, WithVectors: true, WithPayload: true}},
		{"ScrollResponse",
			func() []byte {
				return (&ScrollResponse{Points: []*Point{samplePoint(1)}, NextToken: []byte{9}}).Marshal()
			},
			func(b []byte) (any, error) { return UnmarshalScrollResponse(b) },
			&ScrollResponse{Points: []*Point{samplePoint(1)}, NextToken: []byte{9}}},
		{"ReindexRequest",
			func() []byte {
				return (&ReindexRequest{Collection: "docs", IndexType: "ivf", Params: map[string]string{"nlist": "100"}}).Marshal()
			},
			func(b []byte) (any, error) { return UnmarshalReindexRequest(b) },
			&ReindexRequest{Collection: "docs", IndexType: "ivf", Params: map[string]string{"nlist": "100"}}},
		{"ReindexResponse",
			func() []byte { return (&ReindexResponse{OperationID: "op2"}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalReindexResponse(b) },
			&ReindexResponse{OperationID: "op2"}},
		{"VacuumRequest",
			func() []byte { return (&VacuumRequest{Collection: "docs", All: true}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalVacuumRequest(b) },
			&VacuumRequest{Collection: "docs", All: true}},
		{"VacuumResponse",
			func() []byte { return (&VacuumResponse{BytesFreed: 4096}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalVacuumResponse(b) },
			&VacuumResponse{BytesFreed: 4096}},
		{"BackupRequest",
			func() []byte { return (&BackupRequest{DestPath: "/tmp/b", WALCheckpoint: true}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalBackupRequest(b) },
			&BackupRequest{DestPath: "/tmp/b", WALCheckpoint: true}},
		{"BackupResponse",
			func() []byte { return (&BackupResponse{SizeBytes: 12345, Path: "/tmp/b"}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalBackupResponse(b) },
			&BackupResponse{SizeBytes: 12345, Path: "/tmp/b"}},
		{"OperationStatusRequest",
			func() []byte { return (&OperationStatusRequest{OperationID: "op3"}).Marshal() },
			func(b []byte) (any, error) { return UnmarshalOperationStatusRequest(b) },
			&OperationStatusRequest{OperationID: "op3"}},
		{"OperationStatusResponse",
			func() []byte {
				return (&OperationStatusResponse{OperationID: "op3", State: "running", Progress: 0.5, Error: ""}).Marshal()
			},
			func(b []byte) (any, error) { return UnmarshalOperationStatusResponse(b) },
			&OperationStatusResponse{OperationID: "op3", State: "running", Progress: 0.5}},
		{"HealthResponse",
			func() []byte {
				return (&HealthResponse{Status: "ok", Version: "1.0.0", UptimeS: 42, Details: map[string]string{"db": "open"}}).Marshal()
			},
			func(b []byte) (any, error) { return UnmarshalHealthResponse(b) },
			&HealthResponse{Status: "ok", Version: "1.0.0", UptimeS: 42, Details: map[string]string{"db": "open"}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.unmarshal(c.marshal())
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("mismatch:\n got %+v\nwant %+v", got, c.want)
			}
		})
	}
}

// TestEmptyRequests checks the empty request messages decode cleanly and skip
// unknown fields.
func TestEmptyRequests(t *testing.T) {
	if b := (&ListCollectionsRequest{}).Marshal(); len(b) != 0 {
		t.Fatalf("ListCollectionsRequest should marshal empty, got % x", b)
	}
	if b := (&HealthRequest{}).Marshal(); len(b) != 0 {
		t.Fatalf("HealthRequest should marshal empty, got % x", b)
	}
	// unknown field 3 on an empty request must be skipped.
	junk := appendVarintFieldAlways(nil, 3, 7)
	if _, err := UnmarshalListCollectionsRequest(junk); err != nil {
		t.Fatalf("ListCollections skip unknown: %v", err)
	}
	if _, err := UnmarshalHealthRequest(junk); err != nil {
		t.Fatalf("Health skip unknown: %v", err)
	}
}

// TestPackedRepeatedUint64 checks that the decoder accepts the packed form a
// conforming proto3 encoder might send.
func TestPackedRepeatedUint64(t *testing.T) {
	// Build field 2 as a packed varint block, the form a conforming proto3
	// encoder may emit for repeated uint64.
	var body []byte
	body = appendVarint(body, 1)
	body = appendVarint(body, 2)
	body = appendVarint(body, 300)
	var b []byte
	b = appendStringField(b, 1, "docs")
	b = appendTag(b, 2, wireBytes)
	b = appendVarint(b, uint64(len(body)))
	b = append(b, body...)

	got, err := UnmarshalDeleteRequest(b)
	if err != nil {
		t.Fatal(err)
	}
	want := &DeleteRequest{Collection: "docs", IDs: []uint64{1, 2, 300}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("packed decode: %+v != %+v", got, want)
	}
}
