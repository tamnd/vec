package bulk

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// fbinSource streams the Big-ANN-Benchmarks .fbin / .ibin format (spec 17 §2.5):
// a uint32 count, a uint32 dimension, then count*dim little-endian elements.
type fbinSource struct {
	r       *bufio.Reader
	dim     int
	count   uint32
	isInt   bool // .ibin: int32 elements rather than float32
	rowIdx  uint64
	scratch []byte
}

// NewFbinSource builds a RowSource over a .fbin file (float32 elements).
func NewFbinSource(r io.Reader) (RowSource, error) { return newFbin(r, false) }

// NewIbinSource builds a RowSource over a .ibin file (int32 elements, read as
// float32 so ground-truth and integer datasets share one path).
func NewIbinSource(r io.Reader) (RowSource, error) { return newFbin(r, true) }

func newFbin(r io.Reader, isInt bool) (RowSource, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	var hdr [8]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return nil, fmt.Errorf("bulk: read fbin header: %w", err)
	}
	count := binary.LittleEndian.Uint32(hdr[0:4])
	dim := binary.LittleEndian.Uint32(hdr[4:8])
	if dim == 0 {
		return nil, fmt.Errorf("bulk: fbin header reports zero dimension")
	}
	return &fbinSource{
		r:       br,
		dim:     int(dim),
		count:   count,
		isInt:   isInt,
		scratch: make([]byte, int(dim)*4),
	}, nil
}

func (s *fbinSource) Dim() int { return s.dim }

func (s *fbinSource) Next() (Row, error) {
	if s.rowIdx >= uint64(s.count) {
		return Row{}, errStop
	}
	if _, err := io.ReadFull(s.r, s.scratch); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return Row{}, errStop
		}
		return Row{}, err
	}
	vecf := make([]float32, s.dim)
	for i := 0; i < s.dim; i++ {
		bits := binary.LittleEndian.Uint32(s.scratch[i*4:])
		if s.isInt {
			vecf[i] = float32(int32(bits))
		} else {
			vecf[i] = math.Float32frombits(bits)
		}
	}
	row := Row{Vector: vecf, AutoIndex: s.rowIdx}
	s.rowIdx++
	return row, nil
}

func (s *fbinSource) Close() error { return nil }

// xvecsSource streams the INRIA .fvecs / .bvecs / .ivecs format (spec 17 §2.6):
// each vector is a uint32 dimension prefix followed by dim elements. Element width
// is 1 byte for bvecs, 4 for fvecs and ivecs.
type xvecsSource struct {
	r       *bufio.Reader
	kind    xvecsKind
	dim     int // 0 until the first vector fixes it
	rowIdx  uint64
	scratch []byte
}

type xvecsKind uint8

const (
	fvecsKind xvecsKind = iota // float32
	bvecsKind                  // uint8
	ivecsKind                  // int32
)

// NewFvecsSource builds a RowSource over a .fvecs file (float32 elements).
func NewFvecsSource(r io.Reader) (RowSource, error) { return &xvecsSource{r: bufio.NewReaderSize(r, 1<<20), kind: fvecsKind}, nil }

// NewBvecsSource builds a RowSource over a .bvecs file (uint8 elements).
func NewBvecsSource(r io.Reader) (RowSource, error) { return &xvecsSource{r: bufio.NewReaderSize(r, 1<<20), kind: bvecsKind}, nil }

// NewIvecsSource builds a RowSource over a .ivecs file (int32 elements).
func NewIvecsSource(r io.Reader) (RowSource, error) { return &xvecsSource{r: bufio.NewReaderSize(r, 1<<20), kind: ivecsKind}, nil }

func (s *xvecsSource) Dim() int { return s.dim }

func (s *xvecsSource) elemWidth() int {
	if s.kind == bvecsKind {
		return 1
	}
	return 4
}

func (s *xvecsSource) Next() (Row, error) {
	var dimBuf [4]byte
	if _, err := io.ReadFull(s.r, dimBuf[:]); err != nil {
		if err == io.EOF {
			return Row{}, errStop
		}
		if err == io.ErrUnexpectedEOF {
			return Row{}, errStop
		}
		return Row{}, err
	}
	d := int(binary.LittleEndian.Uint32(dimBuf[:]))
	if d <= 0 {
		return Row{}, fmt.Errorf("bulk: xvecs row %d: invalid dimension %d", s.rowIdx, d)
	}
	if s.dim == 0 {
		s.dim = d
		s.scratch = make([]byte, d*s.elemWidth())
	} else if d != s.dim {
		return Row{}, fmt.Errorf("bulk: xvecs row %d: dimension %d differs from %d", s.rowIdx, d, s.dim)
	}
	if _, err := io.ReadFull(s.r, s.scratch); err != nil {
		return Row{}, err
	}
	vecf := make([]float32, s.dim)
	switch s.kind {
	case bvecsKind:
		for i := 0; i < s.dim; i++ {
			vecf[i] = float32(s.scratch[i])
		}
	case ivecsKind:
		for i := 0; i < s.dim; i++ {
			vecf[i] = float32(int32(binary.LittleEndian.Uint32(s.scratch[i*4:])))
		}
	default:
		for i := 0; i < s.dim; i++ {
			vecf[i] = math.Float32frombits(binary.LittleEndian.Uint32(s.scratch[i*4:]))
		}
	}
	row := Row{Vector: vecf, AutoIndex: s.rowIdx}
	s.rowIdx++
	return row, nil
}

func (s *xvecsSource) Close() error { return nil }

// npySource streams a 2-D NumPy .npy array of float32 in C order (spec 17 §2.5).
// Only the subset the importer needs is parsed: a little-endian float32 [N, D]
// array. Other dtypes or Fortran order are a clear error.
type npySource struct {
	r       *bufio.Reader
	rows    int
	dim     int
	rowIdx  uint64
	scratch []byte
}

// NewNpySource builds a RowSource over a .npy file. It parses the header, then
// streams rows.
func NewNpySource(r io.Reader) (RowSource, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	var magic [6]byte
	if _, err := io.ReadFull(br, magic[:]); err != nil {
		return nil, fmt.Errorf("bulk: read npy magic: %w", err)
	}
	if string(magic[:]) != "\x93NUMPY" {
		return nil, fmt.Errorf("bulk: not a npy file")
	}
	var ver [2]byte
	if _, err := io.ReadFull(br, ver[:]); err != nil {
		return nil, err
	}
	var headerLen int
	if ver[0] >= 2 {
		var l [4]byte
		if _, err := io.ReadFull(br, l[:]); err != nil {
			return nil, err
		}
		headerLen = int(binary.LittleEndian.Uint32(l[:]))
	} else {
		var l [2]byte
		if _, err := io.ReadFull(br, l[:]); err != nil {
			return nil, err
		}
		headerLen = int(binary.LittleEndian.Uint16(l[:]))
	}
	hdr := make([]byte, headerLen)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return nil, err
	}
	rows, dim, err := parseNpyHeader(string(hdr))
	if err != nil {
		return nil, err
	}
	return &npySource{r: br, rows: rows, dim: dim, scratch: make([]byte, dim*4)}, nil
}

// parseNpyHeader reads shape and validates dtype from the npy header dict string.
func parseNpyHeader(h string) (rows, dim int, err error) {
	if !strings.Contains(h, "'<f4'") && !strings.Contains(h, "'|f4'") && !strings.Contains(h, "float32") {
		return 0, 0, fmt.Errorf("bulk: npy dtype is not little-endian float32: %q", strings.TrimSpace(h))
	}
	if strings.Contains(h, "'fortran_order': True") {
		return 0, 0, fmt.Errorf("bulk: npy fortran_order is not supported")
	}
	openIdx := strings.Index(h, "(")
	closeIdx := strings.Index(h, ")")
	if openIdx < 0 || closeIdx < 0 || closeIdx < openIdx {
		return 0, 0, fmt.Errorf("bulk: npy header has no shape tuple")
	}
	shape := h[openIdx+1 : closeIdx]
	parts := strings.Split(shape, ",")
	var dims []int
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, 0, fmt.Errorf("bulk: npy shape element %q: %w", p, err)
		}
		dims = append(dims, n)
	}
	if len(dims) != 2 {
		return 0, 0, fmt.Errorf("bulk: npy array must be 2-D, got shape %v", dims)
	}
	return dims[0], dims[1], nil
}

func (s *npySource) Dim() int { return s.dim }

func (s *npySource) Next() (Row, error) {
	if s.rowIdx >= uint64(s.rows) {
		return Row{}, errStop
	}
	if _, err := io.ReadFull(s.r, s.scratch); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return Row{}, errStop
		}
		return Row{}, err
	}
	vecf := make([]float32, s.dim)
	for i := 0; i < s.dim; i++ {
		vecf[i] = math.Float32frombits(binary.LittleEndian.Uint32(s.scratch[i*4:]))
	}
	row := Row{Vector: vecf, AutoIndex: s.rowIdx}
	s.rowIdx++
	return row, nil
}

func (s *npySource) Close() error { return nil }
