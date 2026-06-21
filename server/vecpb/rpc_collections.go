package vecpb

// CreateCollectionRequest (spec 16 §2.2).
type CreateCollectionRequest struct {
	Name   string
	Config *CollectionConfig
}

// Marshal encodes the CreateCollectionRequest.
func (m *CreateCollectionRequest) Marshal() []byte {
	var b []byte
	b = appendStringField(b, 1, m.Name)
	if m.Config != nil {
		b = appendMessageField(b, 2, m.Config.Marshal(), true)
	}
	return b
}

// UnmarshalCreateCollectionRequest decodes a CreateCollectionRequest.
func UnmarshalCreateCollectionRequest(data []byte) (*CreateCollectionRequest, error) {
	m := &CreateCollectionRequest{}
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
			m.Name = s
		case 2:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			cfg, err := UnmarshalCollectionConfig(raw)
			if err != nil {
				return nil, err
			}
			m.Config = cfg
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// CreateCollectionResponse (spec 16 §2.2).
type CreateCollectionResponse struct {
	Info *CollectionInfo
}

// Marshal encodes the CreateCollectionResponse.
func (m *CreateCollectionResponse) Marshal() []byte {
	var b []byte
	if m.Info != nil {
		b = appendMessageField(b, 1, m.Info.Marshal(), true)
	}
	return b
}

// UnmarshalCreateCollectionResponse decodes a CreateCollectionResponse.
func UnmarshalCreateCollectionResponse(data []byte) (*CreateCollectionResponse, error) {
	m := &CreateCollectionResponse{}
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
			info, err := UnmarshalCollectionInfo(raw)
			if err != nil {
				return nil, err
			}
			m.Info = info
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// DropCollectionRequest (spec 16 §2.2).
type DropCollectionRequest struct {
	Name string
}

// Marshal encodes the DropCollectionRequest.
func (m *DropCollectionRequest) Marshal() []byte {
	var b []byte
	b = appendStringField(b, 1, m.Name)
	return b
}

// UnmarshalDropCollectionRequest decodes a DropCollectionRequest.
func UnmarshalDropCollectionRequest(data []byte) (*DropCollectionRequest, error) {
	m := &DropCollectionRequest{}
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
			m.Name = s
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// DropCollectionResponse (spec 16 §2.2).
type DropCollectionResponse struct {
	Dropped bool
}

// Marshal encodes the DropCollectionResponse.
func (m *DropCollectionResponse) Marshal() []byte {
	var b []byte
	b = appendBoolField(b, 1, m.Dropped)
	return b
}

// UnmarshalDropCollectionResponse decodes a DropCollectionResponse.
func UnmarshalDropCollectionResponse(data []byte) (*DropCollectionResponse, error) {
	m := &DropCollectionResponse{}
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
			m.Dropped = v != 0
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// GetCollectionRequest (spec 16 §2.2).
type GetCollectionRequest struct {
	Name string
}

// Marshal encodes the GetCollectionRequest.
func (m *GetCollectionRequest) Marshal() []byte {
	var b []byte
	b = appendStringField(b, 1, m.Name)
	return b
}

// UnmarshalGetCollectionRequest decodes a GetCollectionRequest.
func UnmarshalGetCollectionRequest(data []byte) (*GetCollectionRequest, error) {
	m := &GetCollectionRequest{}
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
			m.Name = s
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// GetCollectionResponse (spec 16 §2.2).
type GetCollectionResponse struct {
	Info *CollectionInfo
}

// Marshal encodes the GetCollectionResponse.
func (m *GetCollectionResponse) Marshal() []byte {
	var b []byte
	if m.Info != nil {
		b = appendMessageField(b, 1, m.Info.Marshal(), true)
	}
	return b
}

// UnmarshalGetCollectionResponse decodes a GetCollectionResponse.
func UnmarshalGetCollectionResponse(data []byte) (*GetCollectionResponse, error) {
	m := &GetCollectionResponse{}
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
			info, err := UnmarshalCollectionInfo(raw)
			if err != nil {
				return nil, err
			}
			m.Info = info
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// ListCollectionsRequest is empty (spec 16 §2.2).
type ListCollectionsRequest struct{}

// Marshal encodes the ListCollectionsRequest (always empty).
func (m *ListCollectionsRequest) Marshal() []byte { return nil }

// UnmarshalListCollectionsRequest decodes a ListCollectionsRequest, skipping
// any unknown fields.
func UnmarshalListCollectionsRequest(data []byte) (*ListCollectionsRequest, error) {
	m := &ListCollectionsRequest{}
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

// ListCollectionsResponse (spec 16 §2.2).
type ListCollectionsResponse struct {
	Collections []*CollectionInfo
}

// Marshal encodes the ListCollectionsResponse.
func (m *ListCollectionsResponse) Marshal() []byte {
	var b []byte
	for _, c := range m.Collections {
		if c == nil {
			continue
		}
		b = appendMessageField(b, 1, c.Marshal(), true)
	}
	return b
}

// UnmarshalListCollectionsResponse decodes a ListCollectionsResponse.
func UnmarshalListCollectionsResponse(data []byte) (*ListCollectionsResponse, error) {
	m := &ListCollectionsResponse{}
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
			info, err := UnmarshalCollectionInfo(raw)
			if err != nil {
				return nil, err
			}
			m.Collections = append(m.Collections, info)
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}
