package vecpb

// Metric is the distance metric enum (spec 16 §2.2). Numbers match the proto.
type Metric int32

const (
	MetricUnspecified Metric = 0
	MetricL2          Metric = 1
	MetricCosine      Metric = 2
	MetricDot         Metric = 3
	MetricHamming     Metric = 4
	MetricJaccard     Metric = 5
)

func (m Metric) String() string {
	switch m {
	case MetricUnspecified:
		return "METRIC_UNSPECIFIED"
	case MetricL2:
		return "METRIC_L2"
	case MetricCosine:
		return "METRIC_COSINE"
	case MetricDot:
		return "METRIC_DOT"
	case MetricHamming:
		return "METRIC_HAMMING"
	case MetricJaccard:
		return "METRIC_JACCARD"
	default:
		return "METRIC_UNKNOWN"
	}
}

// SearchStrategy selects the query execution path (spec 16 §2.2).
type SearchStrategy int32

const (
	SearchStrategyUnspecified SearchStrategy = 0
	SearchStrategyANN         SearchStrategy = 1
	SearchStrategyFlat        SearchStrategy = 2
	SearchStrategyAuto        SearchStrategy = 3
)

func (s SearchStrategy) String() string {
	switch s {
	case SearchStrategyUnspecified:
		return "SEARCH_STRATEGY_UNSPECIFIED"
	case SearchStrategyANN:
		return "SEARCH_STRATEGY_ANN"
	case SearchStrategyFlat:
		return "SEARCH_STRATEGY_FLAT"
	case SearchStrategyAuto:
		return "SEARCH_STRATEGY_AUTO"
	default:
		return "SEARCH_STRATEGY_UNKNOWN"
	}
}

// ValueKind discriminates the Value oneof (spec 16 §2.2). The proto oneof has
// no explicit "null" tag; ValueNull means no field was set, which decodes from
// an empty Value message.
type ValueKind int32

const (
	ValueNull  ValueKind = 0 // none of the oneof fields set
	ValueInt   ValueKind = 1 // int_val,   field 1
	ValueFloat ValueKind = 2 // float_val, field 2
	ValueBool  ValueKind = 3 // bool_val,  field 3
	ValueStr   ValueKind = 4 // str_val,   field 4
	ValueBytes ValueKind = 5 // bytes_val, field 5
)

func (k ValueKind) String() string {
	switch k {
	case ValueNull:
		return "null"
	case ValueInt:
		return "int"
	case ValueFloat:
		return "float"
	case ValueBool:
		return "bool"
	case ValueStr:
		return "str"
	case ValueBytes:
		return "bytes"
	default:
		return "unknown"
	}
}

// ClauseKind discriminates the Filter oneof (spec 16 §2.2).
type ClauseKind int32

const (
	ClauseNone    ClauseKind = 0 // no clause set (empty filter)
	ClauseMust    ClauseKind = 1 // must,     field 1 (AND)
	ClauseShould  ClauseKind = 2 // should,   field 2 (OR)
	ClauseMustNot ClauseKind = 3 // must_not, field 3 (NOT)
	ClauseCond    ClauseKind = 4 // cond,     field 4 (leaf predicate)
)

func (k ClauseKind) String() string {
	switch k {
	case ClauseNone:
		return "none"
	case ClauseMust:
		return "must"
	case ClauseShould:
		return "should"
	case ClauseMustNot:
		return "must_not"
	case ClauseCond:
		return "cond"
	default:
		return "unknown"
	}
}

// TestKind discriminates the Condition oneof (spec 16 §2.2).
type TestKind int32

const (
	TestNone   TestKind = 0 // no test set
	TestRange  TestKind = 2 // range,   field 2
	TestMatch  TestKind = 3 // match,   field 3
	TestIsNull TestKind = 4 // is_null, field 4
)

func (k TestKind) String() string {
	switch k {
	case TestNone:
		return "none"
	case TestRange:
		return "range"
	case TestMatch:
		return "match"
	case TestIsNull:
		return "is_null"
	default:
		return "unknown"
	}
}
