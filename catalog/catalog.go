package catalog

import (
	"sync"
	"time"

	"github.com/tamnd/vector/storage"
)

// defaultSegmentCapacity is the per-segment point capacity passed to the engine
// when a collection does not pin one; 0 lets the engine derive it from the 256 MB
// segment target over the stride (spec 04 §3.5).
const defaultSegmentCapacity = 0

// Collection is a live, registered collection: its schema, the identity state
// (sequence and deleted-id set), and the storage addressing the engine needs
// (collection id and the metadata column-id map). It is the handle the db layer
// ([14]) drives for reads and writes.
type Collection struct {
	Schema    *Schema
	ID        uint64
	CreatedAt time.Time

	id     *identity
	def    storage.CollectionDef
	colIDs map[string]storage.ColID // metadata column name to engine ColID
	count  uint64                   // live point count, catalog-maintained estimate
}

// StorageDef returns the engine collection definition lowered from the schema
// (spec 02 §11.2). The db layer passes it to storage.Engine.CreateCollection.
func (c *Collection) StorageDef() storage.CollectionDef { return c.def }

// ColID returns the engine column id for a metadata column name, or false if the
// name is not a metadata column of this collection.
func (c *Collection) ColID(name string) (storage.ColID, bool) {
	id, ok := c.colIDs[name]
	return id, ok
}

// NextID returns the next auto-assigned point id (spec 02 §3.5), or ErrIDRequired
// if the collection does not auto-assign.
func (c *Collection) NextID() (PointID, error) { return c.id.nextAuto() }

// LowerMeta resolves and validates the supplied metadata into the engine row,
// keyed by engine ColID (spec 02 §9.6, §10.5). now is the transaction start time
// for DEFAULT NOW(). It returns the storage MetaRow ready for an insert.
func (c *Collection) LowerMeta(supplied map[string]Value, now time.Time) (storage.MetaRow, error) {
	resolved, err := c.Schema.resolveMeta(supplied, Timestamp(now))
	if err != nil {
		return nil, err
	}
	row := make(storage.MetaRow, len(resolved))
	for name, v := range resolved {
		id, ok := c.colIDs[name]
		if !ok {
			continue // schema-optional extra column, not yet lowered to a segment
		}
		row[id] = v.lower()
	}
	return row, nil
}

// ValidateVector checks the vector for the named vector column (spec 02 §4.5).
func (c *Collection) ValidateVector(column string, vec []float32) error {
	col := c.Schema.Column(column)
	if col == nil || col.Kind != ColumnVector {
		return ErrCollectionNotFound
	}
	return validateVector(col, vec)
}

// PrepareID validates or assigns the point id for a write (spec 02 §3.5, §3.6,
// §13.2, §13.17). A nil id auto-assigns; a supplied id is form-checked, rejected
// if it was deleted (non-reuse), and advances the sequence. It returns the
// resolved id and its engine-space fold.
func (c *Collection) PrepareID(supplied *PointID) (PointID, storage.PointID, error) {
	var pid PointID
	if supplied == nil {
		var err error
		if pid, err = c.id.nextAuto(); err != nil {
			return PointID{}, 0, err
		}
	} else {
		pid = *supplied
		if err := validateForm(c.Schema.IDKind, pid); err != nil {
			return PointID{}, 0, err
		}
		if c.id.wasDeleted(pid) {
			return PointID{}, 0, ErrDuplicateKey
		}
		c.id.reserveAuto(pid)
	}
	return pid, pid.engineID(), nil
}

// NoteDeleted records a deleted point id so it is never reused (spec 02 §3.6).
func (c *Collection) NoteDeleted(pid PointID) { c.id.markDeleted(pid) }

// Catalog is the registry of collections in one database (spec 02 §2.6). It owns
// the schema authority and assigns the 32-bit-range collection ids the engine
// keys on (spec 02 §2.6). It is safe for concurrent use.
type Catalog struct {
	mu      sync.RWMutex
	byName  map[string]*Collection
	nextCID uint64
	now     func() time.Time
}

// New creates an empty catalog (spec 02 §2.5: the three system collections are
// virtual and computed on demand, not stored as rows).
func New() *Catalog {
	return &Catalog{
		byName:  make(map[string]*Collection),
		nextCID: 1,
		now:     time.Now,
	}
}

// CreateCollection registers a new collection from a schema (spec 02 §9.1). The
// schema is normalized and validated in place. ifNotExists makes a name clash a
// no-op returning the existing collection (spec 02 §2.2). It returns the live
// collection handle and whether a new collection was created.
func (cat *Catalog) CreateCollection(s *Schema, ifNotExists bool) (*Collection, bool, error) {
	if err := s.normalize(); err != nil {
		return nil, false, err
	}
	cat.mu.Lock()
	defer cat.mu.Unlock()
	if existing, ok := cat.byName[s.Name]; ok {
		if ifNotExists {
			return existing, false, nil
		}
		return nil, false, ErrCollectionExists
	}
	cid := cat.nextCID
	cat.nextCID++
	def, colIDs := s.lower(cid, defaultSegmentCapacity)
	c := &Collection{
		Schema:    s,
		ID:        cid,
		CreatedAt: cat.now(),
		id:        newIdentity(s.IDKind, s.AutoIncrement),
		def:       def,
		colIDs:    colIDs,
	}
	cat.byName[s.Name] = c
	return c, true, nil
}

// Get returns the live collection by name (spec 02 §13.16).
func (cat *Catalog) Get(name string) (*Collection, error) {
	cat.mu.RLock()
	c, ok := cat.byName[name]
	cat.mu.RUnlock()
	if !ok {
		return nil, ErrCollectionNotFound
	}
	return c, nil
}

// DropCollection removes a collection from the catalog (spec 02 §13.15). The
// deleted-id records and all schema state are discarded; ifExists makes an absent
// collection a no-op (spec 02 §13.16). It returns the dropped collection so the
// db layer can drop the engine collection and free pages.
func (cat *Catalog) DropCollection(name string, ifExists bool) (*Collection, error) {
	cat.mu.Lock()
	defer cat.mu.Unlock()
	c, ok := cat.byName[name]
	if !ok {
		if ifExists {
			return nil, nil
		}
		return nil, ErrCollectionNotFound
	}
	delete(cat.byName, name)
	return c, nil
}

// List returns every collection name sorted (deterministic catalog order).
func (cat *Catalog) List() []string {
	cat.mu.RLock()
	names := make([]string, 0, len(cat.byName))
	for n := range cat.byName {
		names = append(names, n)
	}
	cat.mu.RUnlock()
	sortStrings(names)
	return names
}

// SetClock overrides the creation-time source, for tests that need a fixed clock.
func (cat *Catalog) SetClock(now func() time.Time) { cat.now = now }

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
