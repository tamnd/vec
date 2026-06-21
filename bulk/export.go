package bulk

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"

	vec "github.com/tamnd/vec"
)

// ExportFormat names a row-export encoding (spec 17 §12.8).
type ExportFormat string

const (
	ExportJSONL ExportFormat = "jsonl"
	ExportCSV   ExportFormat = "csv"
	ExportFvecs ExportFormat = "fvecs"
)

// ExportOptions controls a collection export (spec 14 §4.4, spec 17 §12.8).
type ExportOptions struct {
	// Format is the output encoding (default jsonl).
	Format ExportFormat
	// IncludeVectors writes the vector field. For fvecs it is always written.
	IncludeVectors bool
}

func (o ExportOptions) format() ExportFormat {
	if o.Format == "" {
		return ExportJSONL
	}
	return o.Format
}

// Export streams every live point of a collection to w in the chosen format (spec
// 17 §12.8). It is the data-only counterpart to Dump: JSONL and CSV are the widely
// understood interchange forms, and fvecs is the raw vector form. The scan runs at
// a snapshot pinned when the export begins.
func Export(ctx context.Context, db *vec.DB, collection string, w io.Writer, opts ExportOptions) error {
	coll, err := db.Collection(collection)
	if err != nil {
		return err
	}
	info, err := db.GetCollection(ctx, collection)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(w)
	switch opts.format() {
	case ExportJSONL:
		err = exportJSONL(ctx, coll, info, bw, opts)
	case ExportCSV:
		err = exportCSV(ctx, coll, info, bw, opts)
	case ExportFvecs:
		err = exportFvecs(ctx, coll, info, bw)
	default:
		return fmt.Errorf("bulk: unknown export format %q", opts.Format)
	}
	if err != nil {
		return err
	}
	return bw.Flush()
}

// metaColumnNames returns the metadata column names in schema order and the vector
// column name.
func metaColumnNames(info vec.CollectionInfo) (vectorCol string, meta []string) {
	for _, c := range info.Columns {
		if c.Type == vec.TypeVector {
			vectorCol = c.Name
			continue
		}
		if c.Name == "id" || c.Name == "point_id" {
			continue
		}
		meta = append(meta, c.Name)
	}
	return vectorCol, meta
}

func exportJSONL(ctx context.Context, coll *vec.Collection, info vec.CollectionInfo, w *bufio.Writer, opts ExportOptions) error {
	vectorCol, meta := metaColumnNames(info)
	enc := json.NewEncoder(w)
	return coll.Scan(ctx, func(p vec.Point) error {
		obj := map[string]any{"id": p.ID.N}
		if p.ID.IsBytes {
			obj["id"] = string(p.ID.B)
		}
		if opts.IncludeVectors {
			if dense := denseOf(p, vectorCol); dense != nil {
				obj[vectorCol] = float32sToFloat64s(dense)
			}
		}
		for _, name := range meta {
			if v, ok := p.Meta[name]; ok && !v.IsNull() {
				obj[name] = jsonValue(v)
			}
		}
		return enc.Encode(obj)
	})
}

func exportCSV(ctx context.Context, coll *vec.Collection, info vec.CollectionInfo, w *bufio.Writer, opts ExportOptions) error {
	vectorCol, meta := metaColumnNames(info)
	cw := csv.NewWriter(w)
	header := []string{"id"}
	if opts.IncludeVectors {
		header = append(header, vectorCol)
	}
	header = append(header, meta...)
	if err := cw.Write(header); err != nil {
		return err
	}
	err := coll.Scan(ctx, func(p vec.Point) error {
		rec := make([]string, 0, len(header))
		if p.ID.IsBytes {
			rec = append(rec, string(p.ID.B))
		} else {
			rec = append(rec, strconv.FormatUint(p.ID.N, 10))
		}
		if opts.IncludeVectors {
			rec = append(rec, vectorToBracketString(denseOf(p, vectorCol)))
		}
		for _, name := range meta {
			if v, ok := p.Meta[name]; ok && !v.IsNull() {
				rec = append(rec, v.String())
			} else {
				rec = append(rec, "")
			}
		}
		return cw.Write(rec)
	})
	if err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

func exportFvecs(ctx context.Context, coll *vec.Collection, info vec.CollectionInfo, w *bufio.Writer) error {
	vectorCol, _ := metaColumnNames(info)
	var dimBuf [4]byte
	return coll.Scan(ctx, func(p vec.Point) error {
		dense := denseOf(p, vectorCol)
		binary.LittleEndian.PutUint32(dimBuf[:], uint32(len(dense)))
		if _, err := w.Write(dimBuf[:]); err != nil {
			return err
		}
		var elem [4]byte
		for _, x := range dense {
			binary.LittleEndian.PutUint32(elem[:], math.Float32bits(x))
			if _, err := w.Write(elem[:]); err != nil {
				return err
			}
		}
		return nil
	})
}

// denseOf returns the dense vector of point p for the given column, falling back to
// the sole vector when the column name does not match.
func denseOf(p vec.Point, col string) []float32 {
	if av, ok := p.Vectors[col]; ok && av.Dense != nil {
		return av.Dense
	}
	for _, av := range p.Vectors {
		if av.Dense != nil {
			return av.Dense
		}
	}
	return nil
}

func float32sToFloat64s(in []float32) []float64 {
	out := make([]float64, len(in))
	for i, x := range in {
		out[i] = float64(x)
	}
	return out
}

func vectorToBracketString(v []float32) string {
	var b []byte
	b = append(b, '[')
	for i, x := range v {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendFloat(b, float64(x), 'g', -1, 32)
	}
	b = append(b, ']')
	return string(b)
}

// jsonValue renders a metadata Value as a JSON-encodable Go value.
func jsonValue(v vec.Value) any {
	switch v.Type() {
	case vec.TypeInt64:
		return v.Int()
	case vec.TypeFloat64:
		return v.Float()
	case vec.TypeBool:
		return v.Bool()
	case vec.TypeText, vec.TypeJSON:
		return v.Text()
	case vec.TypeBytes:
		return v.Bytes()
	case vec.TypeTimestamp:
		return v.String()
	default:
		return nil
	}
}
