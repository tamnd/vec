package vecpb

// Point is an id + embedding + metadata payload (spec 16 §2.2).
type Point struct {
	ID      uint64
	Vector  *Vector
	Payload map[string]*Value
}

// Marshal encodes the Point.
func (p *Point) Marshal() []byte {
	var b []byte
	b = appendVarintField(b, 1, p.ID)
	if p.Vector != nil {
		b = appendMessageField(b, 2, p.Vector.Marshal(), true)
	}
	for k, v := range p.Payload {
		var entry []byte
		entry = appendTag(entry, 1, wireBytes)
		entry = appendVarint(entry, uint64(len(k)))
		entry = append(entry, k...)
		if v != nil {
			entry = appendMessageField(entry, 2, v.Marshal(), true)
		}
		b = appendMessageField(b, 3, entry, true)
	}
	return b
}

// UnmarshalPoint decodes a Point.
func UnmarshalPoint(data []byte) (*Point, error) {
	p := &Point{}
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
			p.ID = n
		case 2:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			vec, err := UnmarshalVector(raw)
			if err != nil {
				return nil, err
			}
			p.Vector = vec
		case 3:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			k, v, err := readValueMapEntry(raw)
			if err != nil {
				return nil, err
			}
			if p.Payload == nil {
				p.Payload = make(map[string]*Value)
			}
			p.Payload[k] = v
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return p, nil
}

// ScoredPoint is a Point with its distance score and rank (spec 16 §2.2).
type ScoredPoint struct {
	Point *Point
	Score float64
	Rank  uint64
}

// Marshal encodes the ScoredPoint.
func (s *ScoredPoint) Marshal() []byte {
	var b []byte
	if s.Point != nil {
		b = appendMessageField(b, 1, s.Point.Marshal(), true)
	}
	b = appendDoubleField(b, 2, s.Score)
	b = appendVarintField(b, 3, s.Rank)
	return b
}

// UnmarshalScoredPoint decodes a ScoredPoint.
func UnmarshalScoredPoint(data []byte) (*ScoredPoint, error) {
	s := &ScoredPoint{}
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
			pt, err := UnmarshalPoint(raw)
			if err != nil {
				return nil, err
			}
			s.Point = pt
		case 2:
			d, err := r.readDouble()
			if err != nil {
				return nil, err
			}
			s.Score = d
		case 3:
			n, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			s.Rank = n
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}
