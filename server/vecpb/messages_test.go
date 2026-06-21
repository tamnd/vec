package vecpb

import (
	"reflect"
	"testing"
)

func f64(v float64) *float64 { return &v }

// TestValueOneofRoundTrip exercises every Value arm.
func TestValueOneofRoundTrip(t *testing.T) {
	cases := []*Value{
		{Kind: ValueNull},
		{Kind: ValueInt, IntVal: -42},
		{Kind: ValueInt, IntVal: 0}, // zero must still decode as Int
		{Kind: ValueFloat, FloatVal: 3.14},
		{Kind: ValueFloat, FloatVal: 0}, // zero float still Float
		{Kind: ValueBool, BoolVal: true},
		{Kind: ValueBool, BoolVal: false}, // false still Bool
		{Kind: ValueStr, StrVal: "hello"},
		{Kind: ValueStr, StrVal: ""}, // empty string still Str
		{Kind: ValueBytes, BytesVal: []byte{0, 1, 2, 255}},
	}
	for i, c := range cases {
		got, err := UnmarshalValue(c.Marshal())
		if err != nil {
			t.Fatalf("case %d unmarshal: %v", i, err)
		}
		if got.Kind != c.Kind {
			t.Fatalf("case %d: kind %v want %v", i, got.Kind, c.Kind)
		}
		if !reflect.DeepEqual(got, c) {
			t.Fatalf("case %d: %+v != %+v", i, got, c)
		}
	}
}

// TestValueNullIsEmpty confirms a null Value marshals to zero bytes and decodes
// back to ValueNull.
func TestValueNullIsEmpty(t *testing.T) {
	v := &Value{Kind: ValueNull}
	if len(v.Marshal()) != 0 {
		t.Fatalf("null value should be empty, got % x", v.Marshal())
	}
	got, err := UnmarshalValue(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ValueNull {
		t.Fatalf("empty decode kind = %v", got.Kind)
	}
}

// TestPointRoundTrip covers a Point with vector and payload.
func TestPointRoundTrip(t *testing.T) {
	p := &Point{
		ID:     1001,
		Vector: &Vector{Data: EncodeVector([]float32{0.1, 0.2, 0.3})},
		Payload: map[string]*Value{
			"title": {Kind: ValueStr, StrVal: "Hello World"},
			"ts":    {Kind: ValueInt, IntVal: 1718000000},
			"score": {Kind: ValueFloat, FloatVal: 0.95},
			"ok":    {Kind: ValueBool, BoolVal: true},
		},
	}
	got, err := UnmarshalPoint(p.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, p) {
		t.Fatalf("point mismatch:\n got %+v\nwant %+v", got, p)
	}
}

// TestScoredPointRoundTrip covers a ScoredPoint.
func TestScoredPointRoundTrip(t *testing.T) {
	s := &ScoredPoint{
		Point: &Point{ID: 7, Vector: &Vector{Data: EncodeVector([]float32{1, 2})}},
		Score: 0.9823,
		Rank:  1,
	}
	got, err := UnmarshalScoredPoint(s.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, s) {
		t.Fatalf("scored point mismatch: %+v != %+v", got, s)
	}
}

// TestCollectionConfigRoundTrip covers maps and the metric enum.
func TestCollectionConfigRoundTrip(t *testing.T) {
	c := &CollectionConfig{
		Dim:         768,
		Metric:      MetricCosine,
		IndexType:   "hnsw",
		IndexParams: map[string]string{"m": "16", "ef_construction": "200"},
		ColumnTypes: map[string]string{"title": "text", "ts": "int"},
	}
	got, err := UnmarshalCollectionConfig(c.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("config mismatch: %+v != %+v", got, c)
	}
}

// TestCollectionInfoRoundTrip covers nested config.
func TestCollectionInfoRoundTrip(t *testing.T) {
	c := &CollectionInfo{
		Name:       "docs",
		Config:     &CollectionConfig{Dim: 3, Metric: MetricL2, IndexType: "flat"},
		Count:      10,
		SizeBytes:  4096,
		IndexState: "ready",
	}
	got, err := UnmarshalCollectionInfo(c.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("info mismatch: %+v != %+v", got, c)
	}
}

// TestFilterTreeRoundTrip builds a nested must/should/cond tree.
func TestFilterTreeRoundTrip(t *testing.T) {
	f := &Filter{
		Kind: ClauseMust,
		Children: []*Filter{
			{Kind: ClauseCond, Cond: &Condition{
				Field: "ts",
				Kind:  TestRange,
				Range: &RangeTest{Gte: f64(1718000000), Lt: f64(1719000000)},
			}},
			{Kind: ClauseCond, Cond: &Condition{
				Field: "title",
				Kind:  TestMatch,
				Match: &MatchTest{Value: &Value{Kind: ValueStr, StrVal: "Hello"}},
			}},
			{Kind: ClauseMustNot, Children: []*Filter{
				{Kind: ClauseCond, Cond: &Condition{
					Field:  "deleted",
					Kind:   TestIsNull,
					IsNull: &NullTest{IsNull: true},
				}},
			}},
			{Kind: ClauseShould, Children: []*Filter{
				{Kind: ClauseCond, Cond: &Condition{
					Field: "x",
					Kind:  TestRange,
					Range: &RangeTest{Gt: f64(0)}, // present zero bound
				}},
			}},
		},
	}
	got, err := UnmarshalFilter(f.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, f) {
		t.Fatalf("filter mismatch:\n got %+v\nwant %+v", got, f)
	}
}

// TestRangeTestOptionalPresence checks that a present zero bound survives and an
// absent bound stays nil.
func TestRangeTestOptionalPresence(t *testing.T) {
	rt := &RangeTest{Gte: f64(0)} // present zero, others absent
	got, err := UnmarshalRangeTest(rt.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if got.Gte == nil || *got.Gte != 0 {
		t.Fatalf("Gte should be present zero, got %v", got.Gte)
	}
	if got.Gt != nil || got.Lt != nil || got.Lte != nil {
		t.Fatalf("absent bounds should stay nil: %+v", got)
	}
}
