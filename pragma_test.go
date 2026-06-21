package vec

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func openMem(t *testing.T) *DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPragmaReadDefault(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()
	if v, err := db.Pragma(ctx, "hnsw_ef_search", ""); err != nil || v != "64" {
		t.Fatalf("ef_search default: got %q err %v, want 64", v, err)
	}
	if v, err := db.Pragma(ctx, "synchronous", ""); err != nil || v != "NORMAL" {
		t.Fatalf("synchronous default: got %q err %v, want NORMAL", v, err)
	}
}

func TestPragmaAlias(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()
	if _, err := db.Pragma(ctx, "ef_search", "128"); err != nil {
		t.Fatalf("set ef_search: %v", err)
	}
	long, err := db.Pragma(ctx, "hnsw_ef_search", "")
	if err != nil || long != "128" {
		t.Fatalf("hnsw_ef_search after ef_search=128: got %q err %v", long, err)
	}
	short, err := db.Pragma(ctx, "ef_search", "")
	if err != nil || short != "128" {
		t.Fatalf("ef_search read: got %q err %v", short, err)
	}
}

func TestPragmaSetSessionAndPersistent(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()
	if v, err := db.Pragma(ctx, "ivf_nprobe", "20"); err != nil || v != "20" {
		t.Fatalf("set nprobe: got %q err %v", v, err)
	}
	if v, err := db.Pragma(ctx, "wal_autocheckpoint", "500"); err != nil || v != "500" {
		t.Fatalf("set wal_autocheckpoint: got %q err %v", v, err)
	}
	if v, _ := db.Pragma(ctx, "wal_autocheckpoint", ""); v != "500" {
		t.Fatalf("persistent read back: got %q", v)
	}
}

func TestPragmaImmutable(t *testing.T) {
	db := openMem(t)
	_, err := db.Pragma(context.Background(), "page_size", "8192")
	var imm *ErrPragmaImmutable
	if !errors.As(err, &imm) {
		t.Fatalf("page_size set: want ErrPragmaImmutable, got %v", err)
	}
}

func TestPragmaReadOnly(t *testing.T) {
	db := openMem(t)
	_, err := db.Pragma(context.Background(), "isolation", "serializable")
	var ro *ErrPragmaReadOnly
	if !errors.As(err, &ro) {
		t.Fatalf("isolation set: want ErrPragmaReadOnly, got %v", err)
	}
}

func TestPragmaUnknown(t *testing.T) {
	db := openMem(t)
	_, err := db.Pragma(context.Background(), "no_such_knob", "")
	var un *ErrUnknownPragma
	if !errors.As(err, &un) {
		t.Fatalf("want ErrUnknownPragma, got %v", err)
	}
}

func TestPragmaInvalidValue(t *testing.T) {
	db := openMem(t)
	_, err := db.Pragma(context.Background(), "recall_target", "2.0")
	var ic *ErrInvalidConfig
	if !errors.As(err, &ic) {
		t.Fatalf("recall_target 2.0: want ErrInvalidConfig, got %v", err)
	}
	if ic.Knob != "recall_target" {
		t.Errorf("error knob = %q, want recall_target", ic.Knob)
	}
}

func TestPragmaSQLiteCompatNoop(t *testing.T) {
	db := openMem(t)
	v, err := db.Pragma(context.Background(), "foreign_keys", "")
	if err != nil || v != "" {
		t.Fatalf("foreign_keys: got %q err %v, want empty/no error", v, err)
	}
}

func TestPragmaConfigurationView(t *testing.T) {
	db := openMem(t)
	view, err := db.Pragma(context.Background(), "configuration", "hnsw")
	if err != nil {
		t.Fatalf("configuration: %v", err)
	}
	if !strings.Contains(view, "hnsw_ef_search") {
		t.Errorf("configuration view missing hnsw_ef_search:\n%s", view)
	}
	if strings.Contains(view, "ivf_nprobe") {
		t.Errorf("hnsw-filtered view should not include ivf_nprobe")
	}
}

func TestPragmaVersion(t *testing.T) {
	db := openMem(t)
	v, err := db.Pragma(context.Background(), "vec_version", "")
	if err != nil || !strings.HasPrefix(v, "vec ") {
		t.Fatalf("vec_version: got %q err %v", v, err)
	}
}

func TestPragmaThroughExec(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()
	rows, err := db.Exec(ctx, "PRAGMA ef_search = 200")
	if err != nil {
		t.Fatalf("exec set: %v", err)
	}
	if !rows.Next() {
		t.Fatal("exec set: no row")
	}
	v, _ := rows.Result().Column("value")
	if v.Text() != "200" {
		t.Fatalf("exec set echoed %q, want 200", v.Text())
	}
	_ = rows.Close()

	rows, err = db.Exec(ctx, "PRAGMA ef_search")
	if err != nil {
		t.Fatalf("exec read: %v", err)
	}
	rows.Next()
	v, _ = rows.Result().Column("value")
	if v.Text() != "200" {
		t.Fatalf("exec read %q, want 200", v.Text())
	}
	_ = rows.Close()
}

func TestPragmaParenForm(t *testing.T) {
	db := openMem(t)
	rows, err := db.Exec(context.Background(), "PRAGMA nprobe(15)")
	if err != nil {
		t.Fatalf("paren form: %v", err)
	}
	rows.Next()
	v, _ := rows.Result().Column("value")
	if v.Text() != "15" {
		t.Fatalf("nprobe(15) -> %q, want 15", v.Text())
	}
	_ = rows.Close()
}

func TestParseOptions(t *testing.T) {
	opts, err := ParseOptions(map[string]string{
		"cache_size":  "-536870912",
		"synchronous": "FULL",
		"ef_search":   "256",
	})
	if err != nil {
		t.Fatalf("ParseOptions: %v", err)
	}
	db, err := Open(":memory:", opts...)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if v, _ := db.Pragma(context.Background(), "synchronous", ""); v != "FULL" {
		t.Errorf("synchronous = %q, want FULL", v)
	}
	if v, _ := db.Pragma(context.Background(), "hnsw_ef_search", ""); v != "256" {
		t.Errorf("ef_search = %q, want 256", v)
	}
	if db.cfg.cacheBytes != 536870912 {
		t.Errorf("cacheBytes = %d, want 536870912", db.cfg.cacheBytes)
	}
}

func TestParseOptionsRejectsUnknown(t *testing.T) {
	if _, err := ParseOptions(map[string]string{"bogus": "1"}); err == nil {
		t.Fatal("ParseOptions should reject an unknown knob")
	}
	if _, err := ParseOptions(map[string]string{"hnsw_m": "1000"}); err == nil {
		t.Fatal("ParseOptions should reject an out-of-range value")
	}
}

func TestWithPragmaInvalidFailsOpen(t *testing.T) {
	_, err := Open(":memory:", WithPragma("graph_pool_fraction", "0.9"), WithPragma("segment_pool_fraction", "0.6"))
	var ic *ErrInvalidConfig
	if !errors.As(err, &ic) {
		t.Fatalf("pool fractions over 1.0: want ErrInvalidConfig, got %v", err)
	}
}

func TestOptionsFromEnv(t *testing.T) {
	opts, err := OptionsFromEnv([]string{
		"VEC_EF_SEARCH=300",
		"VEC_DATABASE_SYNCHRONOUS=FULL",
		"PATH=/usr/bin",
		"VEC_BOGUS=1",
	})
	if err != nil {
		t.Fatalf("OptionsFromEnv: %v", err)
	}
	db, err := Open(":memory:", opts...)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if v, _ := db.Pragma(context.Background(), "ef_search", ""); v != "300" {
		t.Errorf("ef_search from env = %q, want 300", v)
	}
	if v, _ := db.Pragma(context.Background(), "synchronous", ""); v != "FULL" {
		t.Errorf("synchronous from env = %q, want FULL", v)
	}
}

func TestConfigSnapshot(t *testing.T) {
	db, err := Open(":memory:", WithPragma("ef_search", "111"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	cfg := db.Config()
	if cfg.Session.EfSearch != 111 {
		t.Errorf("snapshot ef_search = %d, want 111", cfg.Session.EfSearch)
	}
	if cfg.PageSize != db.cfg.pageSize {
		t.Errorf("snapshot page_size = %d, want %d", cfg.PageSize, db.cfg.pageSize)
	}
	if cfg.CacheSizeBytes() != db.cfg.cacheBytes {
		t.Errorf("snapshot cache bytes = %d, want %d", cfg.CacheSizeBytes(), db.cfg.cacheBytes)
	}
}

func TestPragmaIntHelper(t *testing.T) {
	db := openMem(t)
	if _, err := db.Pragma(context.Background(), "max_k", "5000"); err != nil {
		t.Fatalf("set max_k: %v", err)
	}
	n, err := PragmaInt(db, "max_k")
	if err != nil || n != 5000 {
		t.Fatalf("PragmaInt max_k: got %d err %v, want 5000", n, err)
	}
	s, err := PragmaString(db, "synchronous")
	if err != nil || s != "NORMAL" {
		t.Fatalf("PragmaString synchronous: got %q err %v", s, err)
	}
}
