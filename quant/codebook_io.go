package quant

import (
	"encoding/binary"
	"math"
)

// Codebook page magics (spec 09 §2.6, §3.8, §4.4). Each trained codec serializes
// to a self-describing page so a reopened database can reconstruct the exact
// quantizer without retraining; the magic guards against loading a page of the
// wrong codec.
const (
	magicSQ  uint32 = 0x53514342 // "SQCB"
	magicPQ  uint32 = 0x50514342 // "PQCB"
	magicOPQ uint32 = 0x4F504342 // "OPCB"
)

// putFloat32 writes v little-endian into b[:4].
func putFloat32(b []byte, v float32) {
	binary.LittleEndian.PutUint32(b, math.Float32bits(v))
}

// getFloat32 reads a little-endian float32 from b[:4].
func getFloat32(b []byte) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(b))
}

// MarshalSQ serializes an SQ codebook to its on-disk page (spec 09 §2.6): magic,
// dimension, bit width, then the per-dimension Min and Max arrays.
func MarshalSQ(cb *SQCodebook) []byte {
	out := make([]byte, 0, 16+cb.D*8)
	out = appendU32(out, magicSQ)
	out = appendU32(out, uint32(cb.D))
	out = appendU32(out, uint32(cb.Bits))
	out = appendU32(out, 0) // reserved
	for _, v := range cb.Min {
		out = appendF32(out, v)
	}
	for _, v := range cb.Max {
		out = appendF32(out, v)
	}
	return out
}

// UnmarshalSQ reconstructs an SQ codebook from a page written by MarshalSQ.
func UnmarshalSQ(b []byte) (*SQCodebook, error) {
	if len(b) < 16 || readU32(b, 0) != magicSQ {
		return nil, ErrBadCodebook
	}
	d := int(readU32(b, 4))
	bits := int(readU32(b, 8))
	if d < 0 || len(b) < 16+d*8 {
		return nil, ErrBadCodebook
	}
	cb := &SQCodebook{D: d, Bits: bits, Min: make([]float32, d), Max: make([]float32, d)}
	off := 16
	for i := 0; i < d; i++ {
		cb.Min[i] = getFloat32(b[off:])
		off += 4
	}
	for i := 0; i < d; i++ {
		cb.Max[i] = getFloat32(b[off:])
		off += 4
	}
	return cb, nil
}

// MarshalPQ serializes a PQ codebook to its on-disk page (spec 09 §3.8): magic,
// D, M, Ksub, Nbits, then the M centroid tables concatenated row-major.
func MarshalPQ(cb *PQCodebook) []byte {
	out := make([]byte, 0, 24+cb.M*cb.Ksub*cb.Ds*4)
	out = appendU32(out, magicPQ)
	out = appendU32(out, uint32(cb.D))
	out = appendU32(out, uint32(cb.M))
	out = appendU32(out, uint32(cb.Ksub))
	out = appendU32(out, uint32(cb.Nbits))
	out = appendU32(out, uint32(cb.Ds))
	for j := 0; j < cb.M; j++ {
		for _, v := range cb.Centroids[j] {
			out = appendF32(out, v)
		}
	}
	return out
}

// UnmarshalPQ reconstructs a PQ codebook from a page written by MarshalPQ.
func UnmarshalPQ(b []byte) (*PQCodebook, error) {
	if len(b) < 24 || readU32(b, 0) != magicPQ {
		return nil, ErrBadCodebook
	}
	cb := &PQCodebook{
		D:     int(readU32(b, 4)),
		M:     int(readU32(b, 8)),
		Ksub:  int(readU32(b, 12)),
		Nbits: int(readU32(b, 16)),
		Ds:    int(readU32(b, 20)),
	}
	if cb.M <= 0 || cb.Ksub <= 0 || cb.Ds <= 0 {
		return nil, ErrBadCodebook
	}
	per := cb.Ksub * cb.Ds
	if len(b) < 24+cb.M*per*4 {
		return nil, ErrBadCodebook
	}
	cb.Centroids = make([][]float32, cb.M)
	off := 24
	for j := 0; j < cb.M; j++ {
		row := make([]float32, per)
		for i := 0; i < per; i++ {
			row[i] = getFloat32(b[off:])
			off += 4
		}
		cb.Centroids[j] = row
	}
	return cb, nil
}

// MarshalOPQ serializes an OPQ codebook (spec 09 §4.4): magic, D, the D*D
// rotation, then the embedded PQ page.
func MarshalOPQ(cb *OPQCodebook) []byte {
	pq := MarshalPQ(cb.PQ)
	out := make([]byte, 0, 8+cb.D*cb.D*4+len(pq))
	out = appendU32(out, magicOPQ)
	out = appendU32(out, uint32(cb.D))
	for _, v := range cb.R {
		out = appendF32(out, v)
	}
	out = append(out, pq...)
	return out
}

// UnmarshalOPQ reconstructs an OPQ codebook from a page written by MarshalOPQ.
func UnmarshalOPQ(b []byte) (*OPQCodebook, error) {
	if len(b) < 8 || readU32(b, 0) != magicOPQ {
		return nil, ErrBadCodebook
	}
	d := int(readU32(b, 4))
	if d <= 0 || len(b) < 8+d*d*4 {
		return nil, ErrBadCodebook
	}
	r := make([]float32, d*d)
	off := 8
	for i := range r {
		r[i] = getFloat32(b[off:])
		off += 4
	}
	pq, err := UnmarshalPQ(b[off:])
	if err != nil {
		return nil, err
	}
	return &OPQCodebook{PQ: pq, R: r, D: d}, nil
}

func appendU32(b []byte, v uint32) []byte {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], v)
	return append(b, tmp[:]...)
}

func appendF32(b []byte, v float32) []byte {
	return appendU32(b, math.Float32bits(v))
}

func readU32(b []byte, off int) uint32 {
	return binary.LittleEndian.Uint32(b[off:])
}
