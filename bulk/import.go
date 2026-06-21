package bulk

import (
	"context"
	"fmt"
	"io"
	"strings"

	vec "github.com/tamnd/vec"
)

// Format names an import source encoding (spec 17 §2).
type Format string

const (
	FormatCSV   Format = "csv"
	FormatJSON  Format = "json"
	FormatJSONL Format = "jsonl"
	FormatFbin  Format = "fbin"
	FormatIbin  Format = "ibin"
	FormatNpy   Format = "npy"
	FormatFvecs Format = "fvecs"
	FormatBvecs Format = "bvecs"
	FormatIvecs Format = "ivecs"
)

// FormatFromExt guesses a Format from a file extension or path. It returns "" when
// the extension is not recognized.
func FormatFromExt(path string) Format {
	p := strings.ToLower(path)
	for _, suf := range []string{".gz", ".zst", ".zstd"} {
		p = strings.TrimSuffix(p, suf)
	}
	switch {
	case strings.HasSuffix(p, ".csv"), strings.HasSuffix(p, ".tsv"):
		return FormatCSV
	case strings.HasSuffix(p, ".jsonl"), strings.HasSuffix(p, ".ndjson"):
		return FormatJSONL
	case strings.HasSuffix(p, ".json"):
		return FormatJSON
	case strings.HasSuffix(p, ".fbin"):
		return FormatFbin
	case strings.HasSuffix(p, ".ibin"):
		return FormatIbin
	case strings.HasSuffix(p, ".npy"):
		return FormatNpy
	case strings.HasSuffix(p, ".fvecs"):
		return FormatFvecs
	case strings.HasSuffix(p, ".bvecs"):
		return FormatBvecs
	case strings.HasSuffix(p, ".ivecs"):
		return FormatIvecs
	default:
		return ""
	}
}

// Importer holds the resolved source and options for a single import run.
type Importer struct {
	coll *vec.Collection
	src  RowSource
	opts ImportOptions
	ct   columnTypes
}

// NewImporter binds a RowSource to a collection. The collection schema is read
// once up front so the dimension and metadata types are known before any row is
// read; a schema mismatch surfaces here rather than mid-stream.
func NewImporter(ctx context.Context, db *vec.DB, collection string, src RowSource, opts ImportOptions) (*Importer, error) {
	coll, err := db.Collection(collection)
	if err != nil {
		return nil, err
	}
	info, err := db.GetCollection(ctx, collection)
	if err != nil {
		return nil, err
	}
	ct := schemaForImport(info)
	if ct.vectorColumn == "" {
		return nil, fmt.Errorf("bulk: collection %q has no vector column", collection)
	}
	// If the source declares a dimension, validate it against the schema before
	// streaming so a wrong file fails fast.
	if d := src.Dim(); d != 0 && d != ct.dim {
		return nil, fmt.Errorf("bulk: source dimension %d does not match collection dimension %d", d, ct.dim)
	}
	return &Importer{coll: coll, src: src, opts: opts, ct: ct}, nil
}

// Run streams every row from the source into the collection through batched
// upserts (spec 17 §2.8). It returns the import statistics, including the per-row
// errors retained up to ImportOptions.MaxErrorsKept. Run honors ctx cancellation
// between batches.
func (im *Importer) Run(ctx context.Context) (ImportStats, error) {
	var stats ImportStats
	batchSize := im.opts.batchSize()
	batch := make([]vec.Point, 0, batchSize)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, err := im.coll.UpsertBatch(ctx, batch); err != nil {
			return err
		}
		stats.RowsImported += int64(len(batch))
		batch = batch[:0]
		return nil
	}

	for {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		row, err := im.src.Next()
		if err == errStop {
			break
		}
		if err != nil {
			// A decode error is row-level: record and either skip or abort.
			stats.RowsRead++
			stats.RowsErrored++
			im.recordError(&stats, ImportError{Row: stats.RowsRead - 1, Err: err.Error()})
			if im.opts.OnError == Abort {
				return stats, err
			}
			continue
		}
		stats.RowsRead++
		p, err := mapRow(row, im.opts.Mapping, im.ct, im.opts.AllowNonFinite)
		if err != nil {
			stats.RowsErrored++
			im.recordError(&stats, ImportError{Row: stats.RowsRead - 1, Err: err.Error()})
			if im.opts.OnError == Abort {
				return stats, fmt.Errorf("bulk: import aborted at row %d: %w", stats.RowsRead-1, err)
			}
			continue
		}
		batch = append(batch, p)
		if len(batch) >= batchSize {
			if err := flush(); err != nil {
				return stats, err
			}
		}
	}
	if err := flush(); err != nil {
		return stats, err
	}
	return stats, nil
}

func (im *Importer) recordError(stats *ImportStats, e ImportError) {
	if len(stats.Errors) < im.opts.maxErrorsKept() {
		stats.Errors = append(stats.Errors, e)
	}
}

// Import is the one-call convenience wrapper: open a source for the format over r,
// then run it into the collection.
func Import(ctx context.Context, db *vec.DB, collection string, format Format, r io.Reader, opts ImportOptions) (ImportStats, error) {
	// Default the vector field to the collection's vector column so a CSV or JSON
	// file whose vector column is named like the schema imports without a mapping.
	if opts.Mapping.VectorColumn == "" {
		if info, err := db.GetCollection(ctx, collection); err == nil {
			for _, c := range info.Columns {
				if c.Type == vec.TypeVector {
					opts.Mapping.VectorColumn = c.Name
					break
				}
			}
		}
	}
	src, err := openSource(format, r, opts)
	if err != nil {
		return ImportStats{}, err
	}
	defer func() { _ = src.Close() }()
	im, err := NewImporter(ctx, db, collection, src, opts)
	if err != nil {
		return ImportStats{}, err
	}
	return im.Run(ctx)
}

// openSource builds the RowSource for a format over r, threading the vector-field
// hint from the import mapping.
func openSource(format Format, r io.Reader, opts ImportOptions) (RowSource, error) {
	vecField := opts.Mapping.VectorColumn
	switch format {
	case FormatCSV:
		return NewCSVSource(r, CSVOptions{VectorColumn: vecField})
	case FormatJSON:
		return NewJSONSource(r, JSONOptions{VectorField: vecField})
	case FormatJSONL:
		return NewJSONLSource(r, JSONOptions{VectorField: vecField})
	case FormatFbin:
		return NewFbinSource(r)
	case FormatIbin:
		return NewIbinSource(r)
	case FormatNpy:
		return NewNpySource(r)
	case FormatFvecs:
		return NewFvecsSource(r)
	case FormatBvecs:
		return NewBvecsSource(r)
	case FormatIvecs:
		return NewIvecsSource(r)
	default:
		return nil, fmt.Errorf("bulk: unknown import format %q", format)
	}
}
