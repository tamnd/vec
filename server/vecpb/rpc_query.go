package vecpb

// QueryRequest is a single ANN search (spec 16 §2.2).
type QueryRequest struct {
	Collection  string
	QueryVector *Vector
	TopK        uint32
	Filter      *Filter
	Strategy    SearchStrategy
	Params      map[string]string
	WithVectors bool
	WithPayload bool
	IndexName   string
}

// Marshal encodes the QueryRequest.
func (m *QueryRequest) Marshal() []byte {
	var b []byte
	b = appendStringField(b, 1, m.Collection)
	if m.QueryVector != nil {
		b = appendMessageField(b, 2, m.QueryVector.Marshal(), true)
	}
	b = appendVarintField(b, 3, uint64(m.TopK))
	if m.Filter != nil {
		b = appendMessageField(b, 4, m.Filter.Marshal(), true)
	}
	b = appendVarintField(b, 5, uint64(m.Strategy))
	b = appendStringMap(b, 6, m.Params)
	b = appendBoolField(b, 7, m.WithVectors)
	b = appendBoolField(b, 8, m.WithPayload)
	b = appendStringField(b, 9, m.IndexName)
	return b
}

// UnmarshalQueryRequest decodes a QueryRequest.
func UnmarshalQueryRequest(data []byte) (*QueryRequest, error) {
	m := &QueryRequest{}
	r := reader{buf: data}
	for !r.done() {
		field, wire, err := r.readTag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			s, err := r.readString()
			if err != nil {
				return nil, err
			}
			m.Collection = s
		case 2:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			v, err := UnmarshalVector(raw)
			if err != nil {
				return nil, err
			}
			m.QueryVector = v
		case 3:
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			m.TopK = uint32(v)
		case 4:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			f, err := UnmarshalFilter(raw)
			if err != nil {
				return nil, err
			}
			m.Filter = f
		case 5:
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			m.Strategy = SearchStrategy(v)
		case 6:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			k, val, err := readStringMapEntry(raw)
			if err != nil {
				return nil, err
			}
			if m.Params == nil {
				m.Params = make(map[string]string)
			}
			m.Params[k] = val
		case 7:
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			m.WithVectors = v != 0
		case 8:
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			m.WithPayload = v != 0
		case 9:
			s, err := r.readString()
			if err != nil {
				return nil, err
			}
			m.IndexName = s
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// QueryResponse (spec 16 §2.2).
type QueryResponse struct {
	Results      []*ScoredPoint
	SearchTime   float64
	StrategyUsed string
}

// Marshal encodes the QueryResponse.
func (m *QueryResponse) Marshal() []byte {
	var b []byte
	for _, s := range m.Results {
		if s == nil {
			continue
		}
		b = appendMessageField(b, 1, s.Marshal(), true)
	}
	b = appendDoubleField(b, 2, m.SearchTime)
	b = appendStringField(b, 3, m.StrategyUsed)
	return b
}

// UnmarshalQueryResponse decodes a QueryResponse.
func UnmarshalQueryResponse(data []byte) (*QueryResponse, error) {
	m := &QueryResponse{}
	r := reader{buf: data}
	for !r.done() {
		field, wire, err := r.readTag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			s, err := UnmarshalScoredPoint(raw)
			if err != nil {
				return nil, err
			}
			m.Results = append(m.Results, s)
		case 2:
			d, err := r.readDouble()
			if err != nil {
				return nil, err
			}
			m.SearchTime = d
		case 3:
			s, err := r.readString()
			if err != nil {
				return nil, err
			}
			m.StrategyUsed = s
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// BatchQueryRequest carries several independent queries (spec 16 §2.2).
type BatchQueryRequest struct {
	Collection string
	Queries    []*QueryRequest
}

// Marshal encodes the BatchQueryRequest.
func (m *BatchQueryRequest) Marshal() []byte {
	var b []byte
	b = appendStringField(b, 1, m.Collection)
	for _, q := range m.Queries {
		if q == nil {
			continue
		}
		b = appendMessageField(b, 2, q.Marshal(), true)
	}
	return b
}

// UnmarshalBatchQueryRequest decodes a BatchQueryRequest.
func UnmarshalBatchQueryRequest(data []byte) (*BatchQueryRequest, error) {
	m := &BatchQueryRequest{}
	r := reader{buf: data}
	for !r.done() {
		field, wire, err := r.readTag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			s, err := r.readString()
			if err != nil {
				return nil, err
			}
			m.Collection = s
		case 2:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			q, err := UnmarshalQueryRequest(raw)
			if err != nil {
				return nil, err
			}
			m.Queries = append(m.Queries, q)
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// BatchQueryResult is one query's result inside a batch (spec 16 §2.2).
type BatchQueryResult struct {
	Results      []*ScoredPoint
	SearchTime   float64
	StrategyUsed string
}

// Marshal encodes the BatchQueryResult.
func (m *BatchQueryResult) Marshal() []byte {
	var b []byte
	for _, s := range m.Results {
		if s == nil {
			continue
		}
		b = appendMessageField(b, 1, s.Marshal(), true)
	}
	b = appendDoubleField(b, 2, m.SearchTime)
	b = appendStringField(b, 3, m.StrategyUsed)
	return b
}

// UnmarshalBatchQueryResult decodes a BatchQueryResult.
func UnmarshalBatchQueryResult(data []byte) (*BatchQueryResult, error) {
	m := &BatchQueryResult{}
	r := reader{buf: data}
	for !r.done() {
		field, wire, err := r.readTag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			s, err := UnmarshalScoredPoint(raw)
			if err != nil {
				return nil, err
			}
			m.Results = append(m.Results, s)
		case 2:
			d, err := r.readDouble()
			if err != nil {
				return nil, err
			}
			m.SearchTime = d
		case 3:
			s, err := r.readString()
			if err != nil {
				return nil, err
			}
			m.StrategyUsed = s
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// BatchQueryResponse (spec 16 §2.2).
type BatchQueryResponse struct {
	Results []*BatchQueryResult
}

// Marshal encodes the BatchQueryResponse.
func (m *BatchQueryResponse) Marshal() []byte {
	var b []byte
	for _, res := range m.Results {
		if res == nil {
			continue
		}
		b = appendMessageField(b, 1, res.Marshal(), true)
	}
	return b
}

// UnmarshalBatchQueryResponse decodes a BatchQueryResponse.
func UnmarshalBatchQueryResponse(data []byte) (*BatchQueryResponse, error) {
	m := &BatchQueryResponse{}
	r := reader{buf: data}
	for !r.done() {
		field, wire, err := r.readTag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			res, err := UnmarshalBatchQueryResult(raw)
			if err != nil {
				return nil, err
			}
			m.Results = append(m.Results, res)
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// StreamQueryRequest wraps a QueryRequest with a chunk size (spec 16 §2.2).
type StreamQueryRequest struct {
	Request  *QueryRequest
	PageSize uint32
}

// Marshal encodes the StreamQueryRequest.
func (m *StreamQueryRequest) Marshal() []byte {
	var b []byte
	if m.Request != nil {
		b = appendMessageField(b, 1, m.Request.Marshal(), true)
	}
	b = appendVarintField(b, 2, uint64(m.PageSize))
	return b
}

// UnmarshalStreamQueryRequest decodes a StreamQueryRequest.
func UnmarshalStreamQueryRequest(data []byte) (*StreamQueryRequest, error) {
	m := &StreamQueryRequest{}
	r := reader{buf: data}
	for !r.done() {
		field, wire, err := r.readTag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			q, err := UnmarshalQueryRequest(raw)
			if err != nil {
				return nil, err
			}
			m.Request = q
		case 2:
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			m.PageSize = uint32(v)
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// StreamQueryResponse is one chunk of a server stream (spec 16 §2.2).
type StreamQueryResponse struct {
	Results   []*ScoredPoint
	Done      bool
	TotalSent uint64
}

// Marshal encodes the StreamQueryResponse.
func (m *StreamQueryResponse) Marshal() []byte {
	var b []byte
	for _, s := range m.Results {
		if s == nil {
			continue
		}
		b = appendMessageField(b, 1, s.Marshal(), true)
	}
	b = appendBoolField(b, 2, m.Done)
	b = appendVarintField(b, 3, m.TotalSent)
	return b
}

// UnmarshalStreamQueryResponse decodes a StreamQueryResponse.
func UnmarshalStreamQueryResponse(data []byte) (*StreamQueryResponse, error) {
	m := &StreamQueryResponse{}
	r := reader{buf: data}
	for !r.done() {
		field, wire, err := r.readTag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			s, err := UnmarshalScoredPoint(raw)
			if err != nil {
				return nil, err
			}
			m.Results = append(m.Results, s)
		case 2:
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			m.Done = v != 0
		case 3:
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			m.TotalSent = v
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}
