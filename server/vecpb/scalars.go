package vecpb

// Value is a scalar metadata value (spec 16 §2.2). The proto declares a oneof
// over int64/double/bool/string/bytes with null meaning "none set". The Go
// model uses a Kind discriminator plus a field per arm. Marshal emits the field
// named by Kind; ValueNull emits an empty message.
type Value struct {
	Kind     ValueKind
	IntVal   int64
	FloatVal float64
	BoolVal  bool
	StrVal   string
	BytesVal []byte
}

// Marshal encodes the Value. Only the field selected by Kind is written, and it
// is written unconditionally (oneof presence is explicit) so a zero-valued
// member still round-trips.
func (v *Value) Marshal() []byte {
	var b []byte
	switch v.Kind {
	case ValueInt:
		b = appendVarintFieldAlways(b, 1, uint64(v.IntVal))
	case ValueFloat:
		b = appendDoubleFieldAlways(b, 2, v.FloatVal)
	case ValueBool:
		b = appendBoolFieldAlways(b, 3, v.BoolVal)
	case ValueStr:
		b = appendTag(b, 4, wireBytes)
		b = appendVarint(b, uint64(len(v.StrVal)))
		b = append(b, v.StrVal...)
	case ValueBytes:
		b = appendBytesFieldAlways(b, 5, v.BytesVal)
	case ValueNull:
		// empty message
	}
	return b
}

// UnmarshalValue decodes a Value. The last oneof field present wins, matching
// proto3 oneof semantics.
func UnmarshalValue(data []byte) (*Value, error) {
	v := &Value{}
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
			v.Kind, v.IntVal = ValueInt, int64(n)
		case 2:
			d, err := r.readDouble()
			if err != nil {
				return nil, err
			}
			v.Kind, v.FloatVal = ValueFloat, d
		case 3:
			n, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			v.Kind, v.BoolVal = ValueBool, n != 0
		case 4:
			s, err := r.readString()
			if err != nil {
				return nil, err
			}
			v.Kind, v.StrVal = ValueStr, s
		case 5:
			p, err := r.readBytesCopy()
			if err != nil {
				return nil, err
			}
			v.Kind, v.BytesVal = ValueBytes, p
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return v, nil
}

// Vector is a dense float32 vector as little-endian IEEE 754 bytes (spec 16
// §2.2). Use EncodeVector/DecodeVector to convert to and from []float32.
type Vector struct {
	Data []byte
}

// Marshal encodes the Vector.
func (v *Vector) Marshal() []byte {
	var b []byte
	b = appendBytesField(b, 1, v.Data)
	return b
}

// UnmarshalVector decodes a Vector.
func UnmarshalVector(data []byte) (*Vector, error) {
	v := &Vector{}
	r := reader{buf: data}
	for !r.done() {
		field, wire, err := r.readTag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			p, err := r.readBytesCopy()
			if err != nil {
				return nil, err
			}
			v.Data = p
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return v, nil
}
