package vecpb

// CollectionConfig describes a collection's vector and schema layout (spec 16
// §2.2).
type CollectionConfig struct {
	Dim         uint32
	Metric      Metric
	IndexType   string
	IndexParams map[string]string
	ColumnTypes map[string]string
}

// Marshal encodes the CollectionConfig.
func (c *CollectionConfig) Marshal() []byte {
	var b []byte
	b = appendVarintField(b, 1, uint64(c.Dim))
	b = appendVarintField(b, 2, uint64(c.Metric))
	b = appendStringField(b, 3, c.IndexType)
	b = appendStringMap(b, 4, c.IndexParams)
	b = appendStringMap(b, 5, c.ColumnTypes)
	return b
}

// UnmarshalCollectionConfig decodes a CollectionConfig.
func UnmarshalCollectionConfig(data []byte) (*CollectionConfig, error) {
	c := &CollectionConfig{}
	r := reader{buf: data}
	for !r.done() {
		field, wire, err := r.readTag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			n, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			c.Dim = uint32(n)
		case 2:
			n, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			c.Metric = Metric(n)
		case 3:
			s, err := r.readString()
			if err != nil {
				return nil, err
			}
			c.IndexType = s
		case 4:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			k, v, err := readStringMapEntry(raw)
			if err != nil {
				return nil, err
			}
			if c.IndexParams == nil {
				c.IndexParams = make(map[string]string)
			}
			c.IndexParams[k] = v
		case 5:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			k, v, err := readStringMapEntry(raw)
			if err != nil {
				return nil, err
			}
			if c.ColumnTypes == nil {
				c.ColumnTypes = make(map[string]string)
			}
			c.ColumnTypes[k] = v
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return c, nil
}

// CollectionInfo reports a collection's config plus live stats (spec 16 §2.2).
type CollectionInfo struct {
	Name       string
	Config     *CollectionConfig
	Count      uint64
	SizeBytes  uint64
	IndexState string
}

// Marshal encodes the CollectionInfo.
func (c *CollectionInfo) Marshal() []byte {
	var b []byte
	b = appendStringField(b, 1, c.Name)
	if c.Config != nil {
		b = appendMessageField(b, 2, c.Config.Marshal(), true)
	}
	b = appendVarintField(b, 3, c.Count)
	b = appendVarintField(b, 4, c.SizeBytes)
	b = appendStringField(b, 5, c.IndexState)
	return b
}

// UnmarshalCollectionInfo decodes a CollectionInfo.
func UnmarshalCollectionInfo(data []byte) (*CollectionInfo, error) {
	c := &CollectionInfo{}
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
			c.Name = s
		case 2:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			cfg, err := UnmarshalCollectionConfig(raw)
			if err != nil {
				return nil, err
			}
			c.Config = cfg
		case 3:
			n, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			c.Count = n
		case 4:
			n, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			c.SizeBytes = n
		case 5:
			s, err := r.readString()
			if err != nil {
				return nil, err
			}
			c.IndexState = s
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return c, nil
}
