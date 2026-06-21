package bulk

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// BackupDestination is where streaming and incremental backups land (spec 17 §5.1).
// The names are object keys, slash-separated, relative to the destination root. A
// destination is the seam the streaming backup writes to and PITR reads from; the
// local DirDestination is the stdlib-only implementation, and an object-store
// destination (S3, GCS) would satisfy the same four methods.
type BackupDestination interface {
	// Put writes the whole object at key, replacing any existing object.
	Put(key string, r io.Reader) error
	// Get opens the object at key for reading. The caller closes it.
	Get(key string) (io.ReadCloser, error)
	// List returns the keys under prefix, sorted ascending.
	List(prefix string) ([]string, error)
	// Delete removes the object at key. Deleting a missing key is not an error.
	Delete(key string) error
}

// ErrNotFound is returned by a destination Get for a missing key.
var ErrNotFound = errors.New("bulk: backup object not found")

// DirDestination is a BackupDestination backed by a local directory tree. Keys map
// to paths under Root with the same slash structure, so the on-disk layout matches
// the object-store layout in spec 17 §5.3 (generations/<id>/segments/...).
type DirDestination struct {
	Root string
}

// NewDirDestination returns a destination rooted at dir, creating the directory if
// it does not exist.
func NewDirDestination(dir string) (*DirDestination, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &DirDestination{Root: dir}, nil
}

func (d *DirDestination) resolve(key string) (string, error) {
	clean := path.Clean("/" + strings.TrimPrefix(key, "/"))
	if clean == "/" {
		return "", fmt.Errorf("bulk: empty backup key")
	}
	rel := filepath.FromSlash(strings.TrimPrefix(clean, "/"))
	full := filepath.Join(d.Root, rel)
	// Guard against keys that escape the root via .. segments.
	if !strings.HasPrefix(full, filepath.Clean(d.Root)+string(os.PathSeparator)) && full != filepath.Clean(d.Root) {
		return "", fmt.Errorf("bulk: backup key %q escapes destination root", key)
	}
	return full, nil
}

// Put writes the object at key. It writes to a temp file in the same directory and
// renames into place so a reader never sees a half-written object.
func (d *DirDestination) Put(key string, r io.Reader) error {
	full, err := d.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, full)
}

func (d *DirDestination) Get(key string) (io.ReadCloser, error) {
	full, err := d.resolve(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return f, err
}

func (d *DirDestination) List(prefix string) ([]string, error) {
	var keys []string
	root := filepath.Clean(d.Root)
	walkErr := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasPrefix(filepath.Base(p), ".tmp-") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Strings(keys)
	return keys, nil
}

func (d *DirDestination) Delete(key string) error {
	full, err := d.resolve(key)
	if err != nil {
		return err
	}
	err = os.Remove(full)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Generation is one backup generation: an unbroken chain of WAL segments rooted at
// a base snapshot (spec 17 §5.3). A new generation starts whenever the WAL chain is
// broken, for example after a non-WAL write or a restore.
type Generation struct {
	// ID is the generation identifier, a sortable string.
	ID string `json:"id"`
	// BaseVersion is the snapshot version the generation builds on.
	BaseVersion uint64 `json:"base_version"`
	// CreatedUnixNano is when the generation was opened.
	CreatedUnixNano int64 `json:"created_unix_nano"`
	// Segments lists the segment keys in version order.
	Segments []SegmentRef `json:"segments"`
}

// SegmentRef points at one stored segment and records its version span so PITR can
// pick the segments that cover a target version without opening every object.
type SegmentRef struct {
	Key         string `json:"key"`
	BaseVersion uint64 `json:"base_version"`
	EndVersion  uint64 `json:"end_version"`
	Compressed  bool   `json:"compressed"`
}

// Manifest is the top-level index of generations at a destination, stored at
// generations.json (spec 17 §5.3).
type Manifest struct {
	FormatVersion int          `json:"format_version"`
	Generations   []Generation `json:"generations"`
}

const manifestKey = "generations.json"
const manifestFormatVersion = 1

// ReadManifest loads the manifest from dst, returning an empty manifest when none
// exists yet.
func ReadManifest(dst BackupDestination) (Manifest, error) {
	rc, err := dst.Get(manifestKey)
	if errors.Is(err, ErrNotFound) {
		return Manifest{FormatVersion: manifestFormatVersion}, nil
	}
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = rc.Close() }()
	var m Manifest
	if err := json.NewDecoder(rc).Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("bulk: decode manifest: %w", err)
	}
	return m, nil
}

// WriteManifest stores the manifest at dst, stamping the format version.
func WriteManifest(dst BackupDestination, m Manifest) error {
	m.FormatVersion = manifestFormatVersion
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return dst.Put(manifestKey, strings.NewReader(string(b)))
}

// Generation returns the generation with the given id, or false when absent.
func (m Manifest) Generation(id string) (Generation, bool) {
	for _, g := range m.Generations {
		if g.ID == id {
			return g, true
		}
	}
	return Generation{}, false
}

// Latest returns the generation with the highest id, or false when the manifest is
// empty. Generation ids are sortable strings, so the lexicographic maximum is the
// most recent.
func (m Manifest) Latest() (Generation, bool) {
	if len(m.Generations) == 0 {
		return Generation{}, false
	}
	latest := m.Generations[0]
	for _, g := range m.Generations[1:] {
		if g.ID > latest.ID {
			latest = g
		}
	}
	return latest, true
}

// SegmentsCovering returns the segments of generation g whose version span overlaps
// [g.BaseVersion, target], in version order. This is the set PITR replays to reach
// target (spec 17 §6.2). Segments are assumed contiguous and pre-sorted.
func (g Generation) SegmentsCovering(target uint64) []SegmentRef {
	var out []SegmentRef
	for _, s := range g.Segments {
		if s.BaseVersion > target {
			break
		}
		out = append(out, s)
	}
	return out
}

// SegmentKey builds the object key for a segment in a generation (spec 17 §5.3).
func SegmentKey(generationID string, endVersion uint64, compressed bool) string {
	ext := "seg"
	if compressed {
		ext = "seg.gz"
	}
	return fmt.Sprintf("generations/%s/segments/%020d.%s", generationID, endVersion, ext)
}
