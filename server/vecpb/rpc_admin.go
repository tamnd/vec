package vecpb

// ScrollRequest paginates points by position, not distance (spec 16 §2.2).
type ScrollRequest struct {
	Collection  string
	Filter      *Filter
	Limit       uint32
	OffsetToken []byte
	WithVectors bool
	WithPayload bool
}

// Marshal encodes the ScrollRequest.
func (m *ScrollRequest) Marshal() []byte {
	var b []byte
	b = appendStringField(b, 1, m.Collection)
	if m.Filter != nil {
		b = appendMessageField(b, 2, m.Filter.Marshal(), true)
	}
	b = appendVarintField(b, 3, uint64(m.Limit))
	b = appendBytesField(b, 4, m.OffsetToken)
	b = appendBoolField(b, 5, m.WithVectors)
	b = appendBoolField(b, 6, m.WithPayload)
	return b
}

// UnmarshalScrollRequest decodes a ScrollRequest.
func UnmarshalScrollRequest(data []byte) (*ScrollRequest, error) {
	m := &ScrollRequest{}
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
			f, err := UnmarshalFilter(raw)
			if err != nil {
				return nil, err
			}
			m.Filter = f
		case 3:
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			m.Limit = uint32(v)
		case 4:
			p, err := r.readBytesCopy()
			if err != nil {
				return nil, err
			}
			m.OffsetToken = p
		case 5:
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			m.WithVectors = v != 0
		case 6:
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

// ScrollResponse (spec 16 §2.2).
type ScrollResponse struct {
	Points    []*Point
	NextToken []byte
}

// Marshal encodes the ScrollResponse.
func (m *ScrollResponse) Marshal() []byte {
	var b []byte
	for _, p := range m.Points {
		if p == nil {
			continue
		}
		b = appendMessageField(b, 1, p.Marshal(), true)
	}
	b = appendBytesField(b, 2, m.NextToken)
	return b
}

// UnmarshalScrollResponse decodes a ScrollResponse.
func UnmarshalScrollResponse(data []byte) (*ScrollResponse, error) {
	m := &ScrollResponse{}
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
		case 2:
			p, err := r.readBytesCopy()
			if err != nil {
				return nil, err
			}
			m.NextToken = p
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// ReindexRequest (spec 16 §2.2).
type ReindexRequest struct {
	Collection string
	IndexType  string
	Params     map[string]string
}

// Marshal encodes the ReindexRequest.
func (m *ReindexRequest) Marshal() []byte {
	var b []byte
	b = appendStringField(b, 1, m.Collection)
	b = appendStringField(b, 2, m.IndexType)
	b = appendStringMap(b, 3, m.Params)
	return b
}

// UnmarshalReindexRequest decodes a ReindexRequest.
func UnmarshalReindexRequest(data []byte) (*ReindexRequest, error) {
	m := &ReindexRequest{}
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
			s, err := r.readString()
			if err != nil {
				return nil, err
			}
			m.IndexType = s
		case 3:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			k, v, err := readStringMapEntry(raw)
			if err != nil {
				return nil, err
			}
			if m.Params == nil {
				m.Params = make(map[string]string)
			}
			m.Params[k] = v
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// ReindexResponse (spec 16 §2.2).
type ReindexResponse struct {
	OperationID string
}

// Marshal encodes the ReindexResponse.
func (m *ReindexResponse) Marshal() []byte {
	var b []byte
	b = appendStringField(b, 1, m.OperationID)
	return b
}

// UnmarshalReindexResponse decodes a ReindexResponse.
func UnmarshalReindexResponse(data []byte) (*ReindexResponse, error) {
	m := &ReindexResponse{}
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
			m.OperationID = s
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// VacuumRequest (spec 16 §2.2).
type VacuumRequest struct {
	Collection string
	All        bool
}

// Marshal encodes the VacuumRequest.
func (m *VacuumRequest) Marshal() []byte {
	var b []byte
	b = appendStringField(b, 1, m.Collection)
	b = appendBoolField(b, 2, m.All)
	return b
}

// UnmarshalVacuumRequest decodes a VacuumRequest.
func UnmarshalVacuumRequest(data []byte) (*VacuumRequest, error) {
	m := &VacuumRequest{}
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
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			m.All = v != 0
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// VacuumResponse (spec 16 §2.2).
type VacuumResponse struct {
	BytesFreed uint64
}

// Marshal encodes the VacuumResponse.
func (m *VacuumResponse) Marshal() []byte {
	var b []byte
	b = appendVarintField(b, 1, m.BytesFreed)
	return b
}

// UnmarshalVacuumResponse decodes a VacuumResponse.
func UnmarshalVacuumResponse(data []byte) (*VacuumResponse, error) {
	m := &VacuumResponse{}
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
			m.BytesFreed = v
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// BackupRequest (spec 16 §2.2).
type BackupRequest struct {
	DestPath      string
	WALCheckpoint bool
}

// Marshal encodes the BackupRequest.
func (m *BackupRequest) Marshal() []byte {
	var b []byte
	b = appendStringField(b, 1, m.DestPath)
	b = appendBoolField(b, 2, m.WALCheckpoint)
	return b
}

// UnmarshalBackupRequest decodes a BackupRequest.
func UnmarshalBackupRequest(data []byte) (*BackupRequest, error) {
	m := &BackupRequest{}
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
			m.DestPath = s
		case 2:
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			m.WALCheckpoint = v != 0
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// BackupResponse (spec 16 §2.2).
type BackupResponse struct {
	SizeBytes uint64
	Path      string
}

// Marshal encodes the BackupResponse.
func (m *BackupResponse) Marshal() []byte {
	var b []byte
	b = appendVarintField(b, 1, m.SizeBytes)
	b = appendStringField(b, 2, m.Path)
	return b
}

// UnmarshalBackupResponse decodes a BackupResponse.
func UnmarshalBackupResponse(data []byte) (*BackupResponse, error) {
	m := &BackupResponse{}
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
			m.SizeBytes = v
		case 2:
			s, err := r.readString()
			if err != nil {
				return nil, err
			}
			m.Path = s
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// OperationStatusRequest (spec 16 §2.2).
type OperationStatusRequest struct {
	OperationID string
}

// Marshal encodes the OperationStatusRequest.
func (m *OperationStatusRequest) Marshal() []byte {
	var b []byte
	b = appendStringField(b, 1, m.OperationID)
	return b
}

// UnmarshalOperationStatusRequest decodes an OperationStatusRequest.
func UnmarshalOperationStatusRequest(data []byte) (*OperationStatusRequest, error) {
	m := &OperationStatusRequest{}
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
			m.OperationID = s
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// OperationStatusResponse (spec 16 §2.2).
type OperationStatusResponse struct {
	OperationID string
	State       string
	Progress    float64
	Error       string
}

// Marshal encodes the OperationStatusResponse.
func (m *OperationStatusResponse) Marshal() []byte {
	var b []byte
	b = appendStringField(b, 1, m.OperationID)
	b = appendStringField(b, 2, m.State)
	b = appendDoubleField(b, 3, m.Progress)
	b = appendStringField(b, 4, m.Error)
	return b
}

// UnmarshalOperationStatusResponse decodes an OperationStatusResponse.
func UnmarshalOperationStatusResponse(data []byte) (*OperationStatusResponse, error) {
	m := &OperationStatusResponse{}
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
			m.OperationID = s
		case 2:
			s, err := r.readString()
			if err != nil {
				return nil, err
			}
			m.State = s
		case 3:
			d, err := r.readDouble()
			if err != nil {
				return nil, err
			}
			m.Progress = d
		case 4:
			s, err := r.readString()
			if err != nil {
				return nil, err
			}
			m.Error = s
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// HealthRequest is empty (spec 16 §2.2).
type HealthRequest struct{}

// Marshal encodes the HealthRequest (always empty).
func (m *HealthRequest) Marshal() []byte { return nil }

// UnmarshalHealthRequest decodes a HealthRequest, skipping unknown fields.
func UnmarshalHealthRequest(data []byte) (*HealthRequest, error) {
	m := &HealthRequest{}
	r := reader{buf: data}
	for !r.done() {
		_, wire, err := r.readTag()
		if err != nil {
			return nil, err
		}
		if err := r.skip(wire); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// HealthResponse (spec 16 §2.2).
type HealthResponse struct {
	Status  string
	Version string
	UptimeS uint64
	Details map[string]string
}

// Marshal encodes the HealthResponse.
func (m *HealthResponse) Marshal() []byte {
	var b []byte
	b = appendStringField(b, 1, m.Status)
	b = appendStringField(b, 2, m.Version)
	b = appendVarintField(b, 3, m.UptimeS)
	b = appendStringMap(b, 4, m.Details)
	return b
}

// UnmarshalHealthResponse decodes a HealthResponse.
func UnmarshalHealthResponse(data []byte) (*HealthResponse, error) {
	m := &HealthResponse{}
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
			m.Status = s
		case 2:
			s, err := r.readString()
			if err != nil {
				return nil, err
			}
			m.Version = s
		case 3:
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			m.UptimeS = v
		case 4:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			k, v, err := readStringMapEntry(raw)
			if err != nil {
				return nil, err
			}
			if m.Details == nil {
				m.Details = make(map[string]string)
			}
			m.Details[k] = v
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}
