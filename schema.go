package vec

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tamnd/vec/catalog"
)

// toCatalogSchema translates a public CollectionSchema into a catalog.Schema
// (spec 14 §4.2). The collection always uses an auto-increment BIGINT primary key
// named "id"; a column literally named "id" or "point_id" is treated as that
// implicit key and dropped from the column list, matching the catalog's reserved
// primary-key names.
func toCatalogSchema(s CollectionSchema) (*catalog.Schema, error) {
	if s.Name == "" {
		return nil, &SchemaError{Reason: "collection name is empty"}
	}
	cs := &catalog.Schema{
		Name:          s.Name,
		IDName:        "id",
		IDKind:        catalog.IDBigInt,
		AutoIncrement: true,
		Mode:          catalog.SchemaFixed,
	}
	vectorSeen := false
	for _, col := range s.Columns {
		if col.Name == "id" || col.Name == "point_id" {
			continue // the implicit primary key
		}
		cd, err := toCatalogColumn(col)
		if err != nil {
			return nil, err
		}
		if cd.Kind == catalog.ColumnVector {
			vectorSeen = true
		}
		cs.Columns = append(cs.Columns, cd)
	}
	if !vectorSeen {
		return nil, &SchemaError{Reason: "schema has no vector column"}
	}
	return cs, nil
}

// toCatalogColumn lowers one public column definition.
func toCatalogColumn(col ColumnDef) (catalog.ColumnDef, error) {
	out := catalog.ColumnDef{Name: col.Name, Nullable: !col.NotNull}
	if col.Type == TypeVector {
		if col.Dim <= 0 {
			return out, &SchemaError{Column: col.Name, Reason: "vector column needs a positive dimension"}
		}
		out.Kind = catalog.ColumnVector
		out.Dim = uint32(col.Dim)
		out.ElemType = catalog.ElemFP32
		out.VecMetric = col.Metric.catalogMetric()
		return out, nil
	}
	kind, ok := metadataKind(col.Type)
	if !ok {
		return out, &SchemaError{Column: col.Name, Reason: "unsupported column type " + col.Type.String()}
	}
	out.Kind = catalog.ColumnMetadata
	out.DataType = kind
	if col.Default != nil {
		d := col.Default.catalogValue()
		out.Default = &d
	}
	return out, nil
}

// metadataKind maps a public scalar column type to a catalog kind.
func metadataKind(t ColumnType) (catalog.Kind, bool) {
	switch t {
	case TypeInt64:
		return catalog.KindBigInt, true
	case TypeFloat64:
		return catalog.KindDouble, true
	case TypeBool:
		return catalog.KindBool, true
	case TypeText:
		return catalog.KindText, true
	case TypeBytes:
		return catalog.KindBlob, true
	case TypeJSON:
		return catalog.KindJSON, true
	case TypeTimestamp:
		return catalog.KindTimestamp, true
	default:
		return catalog.KindNull, false
	}
}

// columnDefFromCatalog renders a catalog column back into the public form for
// CollectionInfo and Collection.Schema.
func columnDefFromCatalog(c catalog.ColumnDef) ColumnDef {
	out := ColumnDef{Name: c.Name, NotNull: !c.Nullable}
	if c.Kind == catalog.ColumnVector {
		out.Type = TypeVector
		out.Dim = int(c.Dim)
		out.Metric = metricFromCatalog(c.VecMetric)
		return out
	}
	out.Type = columnTypeFromKind(c.DataType)
	return out
}

// metricFromCatalog maps a catalog metric back to the public enum.
func metricFromCatalog(m catalog.Metric) Metric {
	switch m {
	case catalog.MetricCosine:
		return MetricCosine
	case catalog.MetricInnerProduct:
		return MetricDot
	case catalog.MetricHamming:
		return MetricHamming
	case catalog.MetricJaccard:
		return MetricJaccard
	default:
		return MetricL2
	}
}

// columnTypeFromKind maps a catalog metadata kind back to the public column type.
func columnTypeFromKind(k catalog.Kind) ColumnType {
	switch k {
	case catalog.KindBigInt, catalog.KindInt:
		return TypeInt64
	case catalog.KindDouble, catalog.KindReal:
		return TypeFloat64
	case catalog.KindBool:
		return TypeBool
	case catalog.KindText:
		return TypeText
	case catalog.KindBlob:
		return TypeBytes
	case catalog.KindJSON:
		return TypeJSON
	case catalog.KindTimestamp:
		return TypeTimestamp
	default:
		return TypeText
	}
}

// mapCatalogErr maps a catalog error to the library sentinel vocabulary.
func mapCatalogErr(name string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, catalog.ErrCollectionExists):
		return fmt.Errorf("vec: collection %q: %w", name, ErrAlreadyExists)
	case errors.Is(err, catalog.ErrCollectionNotFound):
		return fmt.Errorf("vec: collection %q: %w", name, ErrNotFound)
	case errors.Is(err, catalog.ErrInvalidSchema), errors.Is(err, catalog.ErrReservedName), errors.Is(err, catalog.ErrMetricUnsupported):
		return fmt.Errorf("vec: collection %q: %w: %v", name, ErrSchemaViolation, err)
	default:
		return fmt.Errorf("vec: collection %q: %w", name, err)
	}
}

// lockCtx acquires mu, honoring ctx cancellation and a busy timeout. It returns
// ErrBusy if the timeout fires and ErrCanceled if ctx is done first (spec 14
// §11.1, §11.6).
func lockCtx(ctx context.Context, mu *sync.Mutex, timeout time.Duration) error {
	got := make(chan struct{})
	go func() {
		mu.Lock()
		close(got)
	}()
	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}
	select {
	case <-got:
		return nil
	case <-ctx.Done():
		// Release the lock once the pending goroutine acquires it, to avoid a leak.
		go func() { <-got; mu.Unlock() }()
		return ctxErr(ctx.Err())
	case <-timer:
		go func() { <-got; mu.Unlock() }()
		return ErrBusy
	}
}
