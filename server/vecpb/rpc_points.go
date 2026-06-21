package vecpb

// UpsertRequest inserts or overwrites points by id (spec 16 §2.2).
type UpsertRequest struct {
	Collection string
	Points     []*Point
	Wait       bool
}

// Marshal encodes the UpsertRequest.
func (m *UpsertRequest) Marshal() []byte {
	var b []byte
	b = appendStringField(b, 1, m.Collection)
	for _, p := range m.Points {
		if p == nil {
			continue
		}
		b = appendMessageField(b, 2, p.Marshal(), true)
	}
	b = appendBoolField(b, 3, m.Wait)
	return b
}

// UnmarshalUpsertRequest decodes an UpsertRequest.
func UnmarshalUpsertRequest(data []byte) (*UpsertRequest, error) {
	m := &UpsertRequest{}
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
			p, err := UnmarshalPoint(raw)
			if err != nil {
				return nil, err
			}
			m.Points = append(m.Points, p)
		case 3:
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			m.Wait = v != 0
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// UpsertResponse (spec 16 §2.2).
type UpsertResponse struct {
	Upserted    uint64
	Updated     uint64
	OperationID string
}

// Marshal encodes the UpsertResponse.
func (m *UpsertResponse) Marshal() []byte {
	var b []byte
	b = appendVarintField(b, 1, m.Upserted)
	b = appendVarintField(b, 2, m.Updated)
	b = appendStringField(b, 3, m.OperationID)
	return b
}

// UnmarshalUpsertResponse decodes an UpsertResponse.
func UnmarshalUpsertResponse(data []byte) (*UpsertResponse, error) {
	m := &UpsertResponse{}
	r := reader{buf: data}
	for !r.done() {
		field, wire, err := r.readTag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			m.Upserted = v
		case 2:
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			m.Updated = v
		case 3:
			s, err := r.readString()
			if err != nil {
				return nil, err
			}
			m.OperationID = s
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// DeleteRequest deletes points by id (spec 16 §2.2).
type DeleteRequest struct {
	Collection string
	IDs        []uint64
}

// Marshal encodes the DeleteRequest.
func (m *DeleteRequest) Marshal() []byte {
	var b []byte
	b = appendStringField(b, 1, m.Collection)
	b = appendRepeatedUint64(b, 2, m.IDs)
	return b
}

// UnmarshalDeleteRequest decodes a DeleteRequest.
func UnmarshalDeleteRequest(data []byte) (*DeleteRequest, error) {
	m := &DeleteRequest{}
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
			ids, err := r.readRepeatedUint64(wire, m.IDs)
			if err != nil {
				return nil, err
			}
			m.IDs = ids
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// DeleteResponse (spec 16 §2.2).
type DeleteResponse struct {
	Deleted uint64
}

// Marshal encodes the DeleteResponse.
func (m *DeleteResponse) Marshal() []byte {
	var b []byte
	b = appendVarintField(b, 1, m.Deleted)
	return b
}

// UnmarshalDeleteResponse decodes a DeleteResponse.
func UnmarshalDeleteResponse(data []byte) (*DeleteResponse, error) {
	m := &DeleteResponse{}
	r := reader{buf: data}
	for !r.done() {
		field, wire, err := r.readTag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			m.Deleted = v
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// GetPointsRequest fetches points by id (spec 16 §2.2).
type GetPointsRequest struct {
	Collection  string
	IDs         []uint64
	WithVectors bool
	WithPayload bool
}

// Marshal encodes the GetPointsRequest.
func (m *GetPointsRequest) Marshal() []byte {
	var b []byte
	b = appendStringField(b, 1, m.Collection)
	b = appendRepeatedUint64(b, 2, m.IDs)
	b = appendBoolField(b, 3, m.WithVectors)
	b = appendBoolField(b, 4, m.WithPayload)
	return b
}

// UnmarshalGetPointsRequest decodes a GetPointsRequest.
func UnmarshalGetPointsRequest(data []byte) (*GetPointsRequest, error) {
	m := &GetPointsRequest{}
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
			ids, err := r.readRepeatedUint64(wire, m.IDs)
			if err != nil {
				return nil, err
			}
			m.IDs = ids
		case 3:
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			m.WithVectors = v != 0
		case 4:
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			m.WithPayload = v != 0
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// GetPointsResponse (spec 16 §2.2).
type GetPointsResponse struct {
	Points []*Point
}

// Marshal encodes the GetPointsResponse.
func (m *GetPointsResponse) Marshal() []byte {
	var b []byte
	for _, p := range m.Points {
		if p == nil {
			continue
		}
		b = appendMessageField(b, 1, p.Marshal(), true)
	}
	return b
}

// UnmarshalGetPointsResponse decodes a GetPointsResponse.
func UnmarshalGetPointsResponse(data []byte) (*GetPointsResponse, error) {
	m := &GetPointsResponse{}
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
			p, err := UnmarshalPoint(raw)
			if err != nil {
				return nil, err
			}
			m.Points = append(m.Points, p)
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}
