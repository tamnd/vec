package vecpb

// Filter is the recursive metadata predicate tree (spec 16 §2.2). The proto
// oneof "clause" selects one of must / should / must_not / cond. The Go model
// uses a Kind discriminator; exactly one of the matching arms is read by
// Marshal. MustAll, ShouldAny and MustNotAll wrap a list of child Filters, so
// they are modeled inline as Children plus the Kind tag rather than as separate
// wrapper structs (the wire form is identical: a length-delimited message
// holding repeated field-1 Filters).
type Filter struct {
	Kind     ClauseKind
	Children []*Filter  // for ClauseMust / ClauseShould / ClauseMustNot
	Cond     *Condition // for ClauseCond
}

// marshalChildList encodes a MustAll/ShouldAny/MustNotAll body: repeated
// Filter filters = 1.
func marshalChildList(children []*Filter) []byte {
	var b []byte
	for _, c := range children {
		if c == nil {
			continue
		}
		b = appendMessageField(b, 1, c.Marshal(), true)
	}
	return b
}

// Marshal encodes the Filter.
func (f *Filter) Marshal() []byte {
	var b []byte
	switch f.Kind {
	case ClauseMust:
		b = appendMessageField(b, 1, marshalChildList(f.Children), true)
	case ClauseShould:
		b = appendMessageField(b, 2, marshalChildList(f.Children), true)
	case ClauseMustNot:
		b = appendMessageField(b, 3, marshalChildList(f.Children), true)
	case ClauseCond:
		if f.Cond != nil {
			b = appendMessageField(b, 4, f.Cond.Marshal(), true)
		}
	case ClauseNone:
		// empty
	}
	return b
}

// unmarshalChildList decodes a MustAll/ShouldAny/MustNotAll body.
func unmarshalChildList(data []byte) ([]*Filter, error) {
	var out []*Filter
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
			child, err := UnmarshalFilter(raw)
			if err != nil {
				return nil, err
			}
			out = append(out, child)
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// UnmarshalFilter decodes a Filter.
func UnmarshalFilter(data []byte) (*Filter, error) {
	f := &Filter{}
	r := reader{buf: data}
	for !r.done() {
		field, wire, err := r.readTag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1, 2, 3:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			children, err := unmarshalChildList(raw)
			if err != nil {
				return nil, err
			}
			f.Children = children
			switch field {
			case 1:
				f.Kind = ClauseMust
			case 2:
				f.Kind = ClauseShould
			case 3:
				f.Kind = ClauseMustNot
			}
		case 4:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			cond, err := UnmarshalCondition(raw)
			if err != nil {
				return nil, err
			}
			f.Kind, f.Cond = ClauseCond, cond
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return f, nil
}

// Condition is a leaf predicate on one field (spec 16 §2.2). The proto oneof
// "test" selects range / match / is_null. The Go model carries a Kind plus the
// three arms.
type Condition struct {
	Field  string
	Kind   TestKind
	Range  *RangeTest
	Match  *MatchTest
	IsNull *NullTest
}

// Marshal encodes the Condition.
func (c *Condition) Marshal() []byte {
	var b []byte
	b = appendStringField(b, 1, c.Field)
	switch c.Kind {
	case TestRange:
		if c.Range != nil {
			b = appendMessageField(b, 2, c.Range.Marshal(), true)
		}
	case TestMatch:
		if c.Match != nil {
			b = appendMessageField(b, 3, c.Match.Marshal(), true)
		}
	case TestIsNull:
		if c.IsNull != nil {
			b = appendMessageField(b, 4, c.IsNull.Marshal(), true)
		}
	case TestNone:
		// empty
	}
	return b
}

// UnmarshalCondition decodes a Condition.
func UnmarshalCondition(data []byte) (*Condition, error) {
	c := &Condition{}
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
			c.Field = s
		case 2:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			rt, err := UnmarshalRangeTest(raw)
			if err != nil {
				return nil, err
			}
			c.Kind, c.Range = TestRange, rt
		case 3:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			mt, err := UnmarshalMatchTest(raw)
			if err != nil {
				return nil, err
			}
			c.Kind, c.Match = TestMatch, mt
		case 4:
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			nt, err := UnmarshalNullTest(raw)
			if err != nil {
				return nil, err
			}
			c.Kind, c.IsNull = TestIsNull, nt
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return c, nil
}

// RangeTest is a numeric comparison with proto3 optional bounds (spec 16 §2.2).
// Each bound is a *float64; nil means the bound is absent. proto3 "optional"
// fields are explicitly present even when zero, so a present bound is always
// emitted.
type RangeTest struct {
	Gt  *float64
	Gte *float64
	Lt  *float64
	Lte *float64
}

// Marshal encodes the RangeTest. Present (non-nil) bounds are always written,
// even when the value is 0, matching proto3 optional presence.
func (rt *RangeTest) Marshal() []byte {
	var b []byte
	if rt.Gt != nil {
		b = appendDoubleFieldAlways(b, 1, *rt.Gt)
	}
	if rt.Gte != nil {
		b = appendDoubleFieldAlways(b, 2, *rt.Gte)
	}
	if rt.Lt != nil {
		b = appendDoubleFieldAlways(b, 3, *rt.Lt)
	}
	if rt.Lte != nil {
		b = appendDoubleFieldAlways(b, 4, *rt.Lte)
	}
	return b
}

// UnmarshalRangeTest decodes a RangeTest.
func UnmarshalRangeTest(data []byte) (*RangeTest, error) {
	rt := &RangeTest{}
	r := reader{buf: data}
	for !r.done() {
		field, wire, err := r.readTag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1, 2, 3, 4:
			d, err := r.readDouble()
			if err != nil {
				return nil, err
			}
			v := d
			switch field {
			case 1:
				rt.Gt = &v
			case 2:
				rt.Gte = &v
			case 3:
				rt.Lt = &v
			case 4:
				rt.Lte = &v
			}
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return rt, nil
}

// MatchTest is an equality test against a Value (spec 16 §2.2).
type MatchTest struct {
	Value *Value
}

// Marshal encodes the MatchTest.
func (m *MatchTest) Marshal() []byte {
	var b []byte
	if m.Value != nil {
		b = appendMessageField(b, 1, m.Value.Marshal(), true)
	}
	return b
}

// UnmarshalMatchTest decodes a MatchTest.
func UnmarshalMatchTest(data []byte) (*MatchTest, error) {
	m := &MatchTest{}
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
			v, err := UnmarshalValue(raw)
			if err != nil {
				return nil, err
			}
			m.Value = v
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// NullTest checks IS NULL / IS NOT NULL (spec 16 §2.2).
type NullTest struct {
	IsNull bool
}

// Marshal encodes the NullTest.
func (n *NullTest) Marshal() []byte {
	var b []byte
	b = appendBoolField(b, 1, n.IsNull)
	return b
}

// UnmarshalNullTest decodes a NullTest.
func UnmarshalNullTest(data []byte) (*NullTest, error) {
	n := &NullTest{}
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
			n.IsNull = v != 0
		default:
			if err := r.skip(wire); err != nil {
				return nil, err
			}
		}
	}
	return n, nil
}
