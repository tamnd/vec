package vec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/tamnd/vec/catalog"
	"github.com/tamnd/vec/index"
	"github.com/tamnd/vec/query"
	"github.com/tamnd/vec/storage"
)

// DB is the long-lived, goroutine-safe handle to one vec database (spec 14 §2).
// It owns the storage engine and catalog and assembles the query stack on demand.
// In this build the engine is process-resident: a file path names the database
// for diagnostics and future on-disk durability, while :memory: is explicitly
// ephemeral. All collection metadata and points live for the life of the *DB.
type DB struct {
	cfg  openConfig
	path string

	engine *storage.Engine
	cat    *catalog.Catalog

	mu     sync.RWMutex
	colls  map[string]*collState
	closed bool

	// pragmaMu guards the runtime knob store. persistent holds catalog-tier
	// overrides (process-resident in this build, the catalog page once the pager
	// is wired); session holds per-connection overrides. Reads layer session over
	// persistent over the open-time config over the compiled-in default (spec 22
	// §1.4).
	pragmaMu   sync.Mutex
	persistent map[string]string
	session    map[string]string

	// writeMu enforces the single-writer model (spec 14 §11.1): one writable Txn
	// at a time across the whole database; readers never take it.
	writeMu sync.Mutex
}

// collState is the per-collection runtime state the db layer threads between the
// catalog, the engine, and the ANN index the query path uses.
type collState struct {
	cc *catalog.Collection

	idxName   string
	idxType   IndexType
	idxColumn string
	idxParams IndexParams

	index   index.Index
	idxKind query.PathKind
	m       int
	efc     int
	nlist   int
}

// Open opens or creates a database at path (spec 14 §2.1). A path of ":memory:"
// (or a DSN naming mode=memory) creates an ephemeral database.
func Open(path string, opts ...Option) (*DB, error) {
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	return openWith(path, cfg)
}

// OpenReadOnly opens path for reading only (spec 14 §2.1).
func OpenReadOnly(path string, opts ...Option) (*DB, error) {
	cfg := defaultConfig()
	cfg.readOnly = true
	for _, o := range opts {
		o(&cfg)
	}
	cfg.readOnly = true
	return openWith(path, cfg)
}

// OpenDSN opens using a DSN/URI with options in the query string (spec 14 §2.1),
// for example "file:data.vec?mode=ro&cache=256mb".
func OpenDSN(dsn string) (*DB, error) {
	path, cfg, err := parseDSN(dsn)
	if err != nil {
		return nil, err
	}
	return openWith(path, cfg)
}

// openWith builds the engine and catalog and registers any nothing (collections
// are created explicitly). The path is recorded for diagnostics.
func openWith(path string, cfg openConfig) (*DB, error) {
	if cfg.logger == nil {
		cfg.logger = DefaultLogger
	}
	db := &DB{
		cfg:        cfg,
		path:       path,
		engine:     storage.NewEngine(),
		cat:        catalog.New(),
		colls:      make(map[string]*collState),
		persistent: make(map[string]string),
		session:    make(map[string]string),
	}
	// Options that named knobs through ParseOptions/WithPragma are applied as
	// persistent-runtime PRAGMAs at open time (spec 22 §22.3 step 5).
	for name, value := range cfg.pragmas {
		db.persistent[name] = value
	}
	if err := db.validateConfig(); err != nil {
		return nil, err
	}
	return db, nil
}

// parseDSN extracts the file path and option overrides from a DSN (spec 14 §2.5).
func parseDSN(dsn string) (string, openConfig, error) {
	cfg := defaultConfig()
	s := strings.TrimPrefix(dsn, "file:")
	path := s
	if i := strings.IndexByte(s, '?'); i >= 0 {
		path = s[:i]
		for _, kv := range strings.Split(s[i+1:], "&") {
			if kv == "" {
				continue
			}
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "mode":
				if v == "ro" || v == "memory" {
					cfg.readOnly = v == "ro"
				}
			}
			if k == "mode" && v == "memory" {
				path = ":memory:"
			}
		}
	}
	if path == "" {
		return "", cfg, fmt.Errorf("vec: empty DSN path: %w", ErrSchemaViolation)
	}
	return path, cfg, nil
}

// Path returns the path or DSN the database was opened with.
func (db *DB) Path() string { return db.path }

// Close closes the database; it must be called exactly once (spec 14 §2.6).
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return ErrClosed
	}
	db.closed = true
	for _, cs := range db.colls {
		if cs.index != nil {
			_ = cs.index.Close()
		}
	}
	return db.engine.Close()
}

// state returns the per-collection runtime state by name.
func (db *DB) state(name string) (*collState, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return nil, ErrClosed
	}
	cs, ok := db.colls[name]
	if !ok {
		return nil, fmt.Errorf("vec: collection %q: %w", name, ErrNotFound)
	}
	return cs, nil
}

// CreateCollection creates a new collection (spec 14 §4.1). It is an error if the
// collection already exists.
func (db *DB) CreateCollection(ctx context.Context, schema CollectionSchema) error {
	if err := ctx.Err(); err != nil {
		return ctxErr(err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return ErrClosed
	}
	if db.cfg.readOnly {
		return ErrReadOnly
	}
	if _, ok := db.colls[schema.Name]; ok {
		return fmt.Errorf("vec: collection %q: %w", schema.Name, ErrAlreadyExists)
	}
	cs, err := toCatalogSchema(schema)
	if err != nil {
		return err
	}
	cc, _, err := db.cat.CreateCollection(cs, false)
	if err != nil {
		return mapCatalogErr(schema.Name, err)
	}
	if err := db.engine.CreateCollection(cc.StorageDef()); err != nil {
		return fmt.Errorf("vec: collection %q: %w", schema.Name, err)
	}
	db.colls[schema.Name] = &collState{cc: cc, idxKind: query.PathFlat}
	return nil
}

// Collection returns a handle to the named collection (spec 14 §4.5).
func (db *DB) Collection(name string) (*Collection, error) {
	if _, err := db.state(name); err != nil {
		return nil, err
	}
	return &Collection{db: db, name: name}, nil
}

// Collections returns handles to all collections (spec 14 §4.5).
func (db *DB) Collections() ([]*Collection, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return nil, ErrClosed
	}
	out := make([]*Collection, 0, len(db.colls))
	for name := range db.colls {
		out = append(out, &Collection{db: db, name: name})
	}
	return out, nil
}

// DropCollection drops a collection and all its indexes (spec 14 §4.7).
func (db *DB) DropCollection(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return ctxErr(err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return ErrClosed
	}
	if db.cfg.readOnly {
		return ErrReadOnly
	}
	cs, ok := db.colls[name]
	if !ok {
		return fmt.Errorf("vec: collection %q: %w", name, ErrNotFound)
	}
	if cs.index != nil {
		_ = cs.index.Close()
	}
	if _, err := db.cat.DropCollection(name, false); err != nil {
		return mapCatalogErr(name, err)
	}
	delete(db.colls, name)
	return nil
}

// RenameCollection renames a collection (spec 14 §4.8). The engine keeps the
// collection by id, so the rename updates the catalog name binding.
func (db *DB) RenameCollection(ctx context.Context, oldName, newName string) error {
	if err := ctx.Err(); err != nil {
		return ctxErr(err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return ErrClosed
	}
	if db.cfg.readOnly {
		return ErrReadOnly
	}
	cs, ok := db.colls[oldName]
	if !ok {
		return fmt.Errorf("vec: collection %q: %w", oldName, ErrNotFound)
	}
	if _, exists := db.colls[newName]; exists {
		return fmt.Errorf("vec: collection %q: %w", newName, ErrAlreadyExists)
	}
	cs.cc.Schema.Name = newName
	delete(db.colls, oldName)
	db.colls[newName] = cs
	return nil
}

// ListCollections lists all collections (spec 14 §4.6).
func (db *DB) ListCollections(ctx context.Context) ([]CollectionInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxErr(err)
	}
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return nil, ErrClosed
	}
	out := make([]CollectionInfo, 0, len(db.colls))
	for _, name := range db.cat.List() {
		cs, ok := db.colls[name]
		if !ok {
			continue
		}
		out = append(out, collectionInfo(db, cs))
	}
	return out, nil
}

// GetCollection returns info for one collection (spec 14 §4.6).
func (db *DB) GetCollection(ctx context.Context, name string) (CollectionInfo, error) {
	cs, err := db.state(name)
	if err != nil {
		return CollectionInfo{}, err
	}
	return collectionInfo(db, cs), nil
}

// collectionInfo materializes a CollectionInfo snapshot from runtime state.
func collectionInfo(db *DB, cs *collState) CollectionInfo {
	info := CollectionInfo{Name: cs.cc.Schema.Name}
	for _, c := range cs.cc.Schema.Columns {
		info.Columns = append(info.Columns, columnDefFromCatalog(c))
	}
	if st, err := db.engine.CollectionStats(cs.cc.ID); err == nil {
		info.PointCount = int64(st.LivePoints)
	}
	return info
}

// Begin starts a transaction (spec 14 §11.1). A writable transaction blocks until
// the previous writer commits or rolls back, or until ctx is canceled.
func (db *DB) Begin(ctx context.Context, writable bool) (*Txn, error) {
	db.mu.RLock()
	closed := db.closed
	ro := db.cfg.readOnly
	db.mu.RUnlock()
	if closed {
		return nil, ErrClosed
	}
	if writable && ro {
		return nil, ErrReadOnly
	}
	if writable {
		if err := lockCtx(ctx, &db.writeMu, db.cfg.busyTimeout); err != nil {
			return nil, err
		}
	}
	stx := db.engine.Begin(writable)
	return &Txn{db: db, stx: stx, writable: writable, snap: db.engine.Snapshot()}, nil
}

// View runs fn in a read-only snapshot transaction (spec 14 §11).
func (db *DB) View(ctx context.Context, fn func(txn *Txn) error) error {
	txn, err := db.Begin(ctx, false)
	if err != nil {
		return err
	}
	defer func() { _ = txn.Rollback() }()
	return fn(txn)
}

// Update runs fn in a read-write transaction, committing on success and rolling
// back on error (spec 14 §11). Write conflicts are retried up to the configured
// maximum.
func (db *DB) Update(ctx context.Context, fn func(txn *Txn) error) error {
	retries := db.cfg.maxRetries
	if retries < 1 {
		retries = 1
	}
	var lastErr error
	for attempt := 0; attempt < retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return ctxErr(err)
		}
		txn, err := db.Begin(ctx, true)
		if err != nil {
			return err
		}
		err = fn(txn)
		if err != nil {
			_ = txn.Rollback()
			return err
		}
		if err := txn.Commit(); err != nil {
			lastErr = err
			if errors.Is(err, ErrConflict) {
				continue
			}
			return err
		}
		return nil
	}
	if lastErr == nil {
		lastErr = ErrConflict
	}
	return lastErr
}

// queryCollection assembles the executor-facing collection view for cs, binding
// the current ANN index and its tuning so the planner can choose the index path.
func (db *DB) queryCollection(cs *collState) *query.Collection {
	def := cs.cc.StorageDef()
	return &query.Collection{
		Engine:         db.engine,
		CollID:         cs.cc.ID,
		Dims:           int(def.Dims),
		Metric:         def.Metric,
		Index:          cs.index,
		IndexKind:      cs.idxKind,
		M:              cs.m,
		EfConstruction: cs.efc,
		NList:          cs.nlist,
		MetaCols:       metaColMap(cs.cc),
	}
}

// metaColMap builds the metadata column name to engine id map the executor needs.
func metaColMap(cc *catalog.Collection) map[string]storage.ColID {
	out := make(map[string]storage.ColID)
	for _, c := range cc.Schema.MetadataColumns() {
		if id, ok := cc.ColID(c.Name); ok {
			out[c.Name] = id
		}
	}
	return out
}

// ctxErr maps a context error to the library sentinel.
func ctxErr(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrCanceled
	}
	return err
}
