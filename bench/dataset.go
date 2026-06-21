// Package bench is the benchmark harness from spec 20: dataset loaders for the
// standard ANN file formats, recall computation against ground truth, a latency
// recorder with coordinated-omission-correct percentiles, the parameter sweep that
// drives a searcher across effort values, the result JSON and TSV writers, and the
// CI regression gate.
//
// The harness is engine-agnostic. The sweep takes a Searcher, the recall step
// takes result and truth id slices, and the loaders take a reader, so the package
// is exercised without the storage engine and the CLI composes it by supplying the
// real index search and the dataset files.
package bench

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

// Dataset is a loaded benchmark dataset: the base vectors that are indexed, the
// query vectors that are searched, and the ground-truth neighbor ids per query
// (spec 20 §3). Dimension is the vector width; all base and query vectors share it.
type Dataset struct {
	Base        [][]float32
	Queries     [][]float32
	GroundTruth [][]uint32
	Dimension   int
}

// ReadFvecs reads the texmex fvecs format (spec 20 §3.7): each vector is a little-
// endian int32 dimension followed by that many float32 elements, with no global
// header. Every vector in a well-formed file has the same dimension; ReadFvecs
// enforces that and returns the common dimension.
func ReadFvecs(r io.Reader) ([][]float32, int, error) {
	br := bufio.NewReader(r)
	var out [][]float32
	dim := -1
	for {
		d, err := readInt32(br)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, err
		}
		if d <= 0 {
			return nil, 0, fmt.Errorf("bench: fvecs: bad dimension %d", d)
		}
		if dim < 0 {
			dim = int(d)
		} else if int(d) != dim {
			return nil, 0, fmt.Errorf("bench: fvecs: ragged dimension %d, expected %d", d, dim)
		}
		v := make([]float32, d)
		if err := readFloat32s(br, v); err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	if dim < 0 {
		dim = 0
	}
	return out, dim, nil
}

// ReadBvecs reads the bvecs format (spec 20 §3.7): like fvecs but uint8 elements,
// widened to float32 so the rest of the harness handles one element type.
func ReadBvecs(r io.Reader) ([][]float32, int, error) {
	br := bufio.NewReader(r)
	var out [][]float32
	dim := -1
	buf := make([]byte, 0, 128)
	for {
		d, err := readInt32(br)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, err
		}
		if d <= 0 {
			return nil, 0, fmt.Errorf("bench: bvecs: bad dimension %d", d)
		}
		if dim < 0 {
			dim = int(d)
		} else if int(d) != dim {
			return nil, 0, fmt.Errorf("bench: bvecs: ragged dimension %d, expected %d", d, dim)
		}
		if cap(buf) < int(d) {
			buf = make([]byte, d)
		}
		buf = buf[:d]
		if _, err := io.ReadFull(br, buf); err != nil {
			return nil, 0, err
		}
		v := make([]float32, d)
		for i, b := range buf {
			v[i] = float32(b)
		}
		out = append(out, v)
	}
	if dim < 0 {
		dim = 0
	}
	return out, dim, nil
}

// ReadIvecs reads the ivecs format (spec 20 §3.7): like fvecs but int32 elements.
// Ground-truth files use it, one "vector" per query holding the neighbor ids; the
// ids are returned as uint32 because they are positions in the base file.
func ReadIvecs(r io.Reader) ([][]uint32, int, error) {
	br := bufio.NewReader(r)
	var out [][]uint32
	dim := -1
	for {
		d, err := readInt32(br)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, err
		}
		if d < 0 {
			return nil, 0, fmt.Errorf("bench: ivecs: bad length %d", d)
		}
		if dim < 0 {
			dim = int(d)
		} else if int(d) != dim {
			return nil, 0, fmt.Errorf("bench: ivecs: ragged length %d, expected %d", d, dim)
		}
		v := make([]uint32, d)
		for i := range v {
			n, err := readInt32(br)
			if err != nil {
				return nil, 0, err
			}
			v[i] = uint32(n)
		}
		out = append(out, v)
	}
	if dim < 0 {
		dim = 0
	}
	return out, dim, nil
}

// FBinHeader is the two-field header of the Big-ANN fbin/ibin/u8bin formats (spec
// 20 §3.7): point count and dimension, then a row-major flat array.
type FBinHeader struct {
	NumPoints uint32
	Dimension uint32
}

// ReadFBin reads the fbin format (spec 20 §3.7): a NumPoints/Dimension header then
// NumPoints*Dimension float32 in row-major order, with no per-vector size prefix.
func ReadFBin(r io.Reader) ([][]float32, int, error) {
	br := bufio.NewReader(r)
	h, err := readFBinHeader(br)
	if err != nil {
		return nil, 0, err
	}
	out := make([][]float32, h.NumPoints)
	for i := range out {
		v := make([]float32, h.Dimension)
		if err := readFloat32s(br, v); err != nil {
			return nil, 0, err
		}
		out[i] = v
	}
	return out, int(h.Dimension), nil
}

// ReadU8Bin reads the u8bin format: an fbin header then row-major uint8 elements,
// widened to float32.
func ReadU8Bin(r io.Reader) ([][]float32, int, error) {
	br := bufio.NewReader(r)
	h, err := readFBinHeader(br)
	if err != nil {
		return nil, 0, err
	}
	row := make([]byte, h.Dimension)
	out := make([][]float32, h.NumPoints)
	for i := range out {
		if _, err := io.ReadFull(br, row); err != nil {
			return nil, 0, err
		}
		v := make([]float32, h.Dimension)
		for j, b := range row {
			v[j] = float32(b)
		}
		out[i] = v
	}
	return out, int(h.Dimension), nil
}

// ReadIBin reads the ibin ground-truth format: an fbin header then row-major int32
// neighbor ids, returned as uint32.
func ReadIBin(r io.Reader) ([][]uint32, int, error) {
	br := bufio.NewReader(r)
	h, err := readFBinHeader(br)
	if err != nil {
		return nil, 0, err
	}
	out := make([][]uint32, h.NumPoints)
	for i := range out {
		v := make([]uint32, h.Dimension)
		for j := range v {
			n, err := readInt32(br)
			if err != nil {
				return nil, 0, err
			}
			v[j] = uint32(n)
		}
		out[i] = v
	}
	return out, int(h.Dimension), nil
}

// DetectFormat picks a loader by file extension (spec 20 §3.7 format negotiation).
// It returns one of "fvecs", "bvecs", "ivecs", "fbin", "u8bin", "ibin", or "" for
// an unrecognized extension, in which case the caller must pass the format
// explicitly.
func DetectFormat(path string) string {
	switch strings.ToLower(extOf(path)) {
	case ".fvecs":
		return "fvecs"
	case ".bvecs":
		return "bvecs"
	case ".ivecs":
		return "ivecs"
	case ".fbin":
		return "fbin"
	case ".u8bin":
		return "u8bin"
	case ".ibin":
		return "ibin"
	default:
		return ""
	}
}

// LoadVectors opens path, detects or uses the given format, and returns the
// vectors and their dimension. An empty format triggers extension detection.
func LoadVectors(path, format string) ([][]float32, int, error) {
	if format == "" {
		format = DetectFormat(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()
	switch format {
	case "fvecs":
		return ReadFvecs(f)
	case "bvecs":
		return ReadBvecs(f)
	case "fbin":
		return ReadFBin(f)
	case "u8bin":
		return ReadU8Bin(f)
	default:
		return nil, 0, fmt.Errorf("bench: %q is not a vector format for %s", format, path)
	}
}

// LoadGroundTruth opens path and returns the per-query neighbor id lists.
func LoadGroundTruth(path, format string) ([][]uint32, int, error) {
	if format == "" {
		format = DetectFormat(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()
	switch format {
	case "ivecs":
		return ReadIvecs(f)
	case "ibin":
		return ReadIBin(f)
	default:
		return nil, 0, fmt.Errorf("bench: %q is not a ground-truth format for %s", format, path)
	}
}

func readFBinHeader(r io.Reader) (FBinHeader, error) {
	var h FBinHeader
	np, err := readUint32(r)
	if err != nil {
		return h, err
	}
	dim, err := readUint32(r)
	if err != nil {
		return h, err
	}
	if dim == 0 {
		return h, fmt.Errorf("bench: fbin header has zero dimension")
	}
	h.NumPoints, h.Dimension = np, dim
	return h, nil
}

func readInt32(r io.Reader) (int32, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return int32(binary.LittleEndian.Uint32(b[:])), nil
}

func readUint32(r io.Reader) (uint32, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

func readFloat32s(r io.Reader, dst []float32) error {
	b := make([]byte, len(dst)*4)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	for i := range dst {
		dst[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return nil
}

// extOf returns the file extension including the dot, or "" if none.
func extOf(path string) string {
	for i := len(path) - 1; i >= 0 && path[i] != '/'; i-- {
		if path[i] == '.' {
			return path[i:]
		}
	}
	return ""
}
