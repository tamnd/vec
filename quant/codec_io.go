package quant

// MarshalQuantizer serializes a trained quantizer to its self-describing codebook
// page so a reopened database, or an index blob that embeds a residual codec
// (spec 08 §4), can reconstruct the exact codec without retraining. Only the
// codecs that carry a trained codebook are supported; Flat has no codebook and is
// rejected so callers do not silently persist an un-reconstructable codec.
func MarshalQuantizer(q Quantizer) ([]byte, error) {
	switch c := q.(type) {
	case *SQQuantizer:
		return MarshalSQ(c.cb), nil
	case *PQQuantizer:
		return MarshalPQ(c.cb), nil
	case *OPQQuantizer:
		return MarshalOPQ(c.cb), nil
	default:
		return nil, ErrBadCodebook
	}
}

// UnmarshalQuantizer reconstructs a quantizer from a codebook page and its kind,
// the inverse of MarshalQuantizer.
func UnmarshalQuantizer(kind CodecKind, page []byte) (Quantizer, error) {
	switch kind {
	case CodecSQ:
		cb, err := UnmarshalSQ(page)
		if err != nil {
			return nil, err
		}
		return NewSQQuantizer(cb), nil
	case CodecPQ:
		cb, err := UnmarshalPQ(page)
		if err != nil {
			return nil, err
		}
		return NewPQQuantizer(cb), nil
	case CodecOPQ:
		cb, err := UnmarshalOPQ(page)
		if err != nil {
			return nil, err
		}
		return NewOPQQuantizer(cb), nil
	default:
		return nil, ErrBadCodebook
	}
}
