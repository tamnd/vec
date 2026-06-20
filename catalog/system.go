package catalog

// The three system collections of spec 02 §2.5. They are virtual: the catalog
// computes their rows on demand from the live schema rather than storing them as
// points. VectorSQL exposes them as read-only tables (spec 02 §2.5). An index
// registry is not yet populated (indexes are created by the db/index layer); the
// vec_indexes producer returns the rows the catalog knows about, which is none in
// this slice, and the db layer extends it when it owns index creation.

// SystemRow is one row of a system collection, a column-name to string map.
// Every cell renders as text, matching the read-only virtual-table surface of
// spec 02 §2.5 where every system column is TEXT/INT.
type SystemRow map[string]string

// VecCollections returns the rows of the vec_collections system collection
// (spec 02 §2.5): one row per user collection with its mode, live point count,
// and creation time.
func (cat *Catalog) VecCollections() []SystemRow {
	cat.mu.RLock()
	defer cat.mu.RUnlock()
	rows := make([]SystemRow, 0, len(cat.byName))
	for _, name := range cat.listLocked() {
		c := cat.byName[name]
		rows = append(rows, SystemRow{
			"name":        c.Schema.Name,
			"schema_mode": c.Schema.Mode.String(),
			"point_count": utoa(c.count),
			"created_at":  c.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return rows
}

// VecColumns returns the rows of the vec_columns system collection (spec 02
// §2.5): one row per column of every collection, in (collection, ordinal) order.
func (cat *Catalog) VecColumns() []SystemRow {
	cat.mu.RLock()
	defer cat.mu.RUnlock()
	var rows []SystemRow
	for _, name := range cat.listLocked() {
		c := cat.byName[name]
		for i := range c.Schema.Columns {
			col := &c.Schema.Columns[i]
			row := SystemRow{
				"collection": c.Schema.Name,
				"name":       col.Name,
				"ordinal":    itoa(int64(col.Ordinal)),
				"nullable":   btoa(col.Nullable),
			}
			if col.Kind == ColumnVector {
				row["kind"] = "vector"
				row["dim"] = utoa(uint64(col.Dim))
				row["element_type"] = col.ElemType.String()
				row["metric"] = col.VecMetric.String()
				row["data_type"] = ""
			} else {
				row["kind"] = "metadata"
				row["dim"] = ""
				row["element_type"] = ""
				row["metric"] = ""
				row["data_type"] = col.DataType.String()
			}
			row["default_value"] = defaultText(col)
			rows = append(rows, row)
		}
	}
	return rows
}

// VecIndexes returns the rows of the vec_indexes system collection (spec 02
// §2.5). Index registration is owned by the db/index layer ([14], [07], [08]);
// the catalog holds none in this slice, so the result is empty and grows when
// the db layer records index creation against the catalog.
func (cat *Catalog) VecIndexes() []SystemRow { return nil }

// listLocked returns collection names sorted; the caller holds cat.mu.
func (cat *Catalog) listLocked() []string {
	names := make([]string, 0, len(cat.byName))
	for n := range cat.byName {
		names = append(names, n)
	}
	sortStrings(names)
	return names
}

func defaultText(col *ColumnDef) string {
	switch {
	case col.DefaultNow:
		return "NOW()"
	case col.Default != nil && !col.Default.IsNull():
		return renderValue(*col.Default)
	default:
		return ""
	}
}

// renderValue renders a scalar default value as text for vec_columns.default_value.
func renderValue(v Value) string {
	switch v.Kind() {
	case KindBigInt, KindInt:
		return itoa(v.BigInt())
	case KindBool:
		return btoa(v.Bool())
	case KindText, KindJSON:
		return v.Text()
	default:
		return ""
	}
}

func utoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func itoa(n int64) string {
	if n < 0 {
		return "-" + utoa(uint64(-n))
	}
	return utoa(uint64(n))
}

func btoa(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
