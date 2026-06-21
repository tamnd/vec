package config

import (
	"sort"
	"strings"
)

// registry is the full knob table from spec 22 §13 plus the advanced knobs in
// §15 through §18. It is the single source of truth: PRAGMA, Option, and the
// configuration view all read it. Entries are grouped by category for reading;
// the lookup index is built once in init.
var registry = []Knob{
	// Create-time knobs (spec 22 §2, §13.1).
	{Name: "checksum_algorithm", Category: "create", Tier: TierCreate, Kind: KindEnum, Default: "xxhash64", Enum: []string{"xxhash64", "crc32c", "none"}, Doc: "[03] §4"},
	{Name: "cipher", Category: "create", Tier: TierCreate, Kind: KindEnum, Default: "aes-256-gcm", Enum: []string{"aes-256-gcm", "chacha20-poly1305"}, Doc: "[23] §3"},
	{Name: "encryption", Category: "create", Tier: TierCreate, Kind: KindBool, Default: "off", Doc: "[23] §2"},
	{Name: "page_size", Category: "create", Tier: TierCreate, Kind: KindInt, Default: "4096", Min: 512, Max: 65536, HasMin: true, HasMax: true, PowerOfTwo: true, Doc: "[03] §2"},
	{Name: "reserved_per_page", Category: "create", Tier: TierCreate, Kind: KindInt, Default: "0", Min: 0, Max: 255, HasMin: true, HasMax: true, Doc: "[03] §2.3"},
	{Name: "text_encoding", Category: "create", Tier: TierCreate, Kind: KindEnum, Default: "UTF-8", Enum: []string{"UTF-8", "UTF-16-LE", "UTF-16-BE"}, Doc: "[03] §2.4"},
	{Name: "vector_element", Category: "create", Tier: TierCreate, Kind: KindEnum, Default: "float32", Enum: []string{"float32", "float16", "int8", "binary"}, Doc: "[03] §2.2"},
	{Name: "overflow_chain_limit", Category: "create", Tier: TierCreate, Kind: KindInt, Default: "256", Min: 1, HasMin: true, Doc: "[15] §15.1"},
	{Name: "btree_max_inline_value", Category: "create", Tier: TierCreate, Kind: KindInt, Default: "page_size/4", Computed: true, Doc: "[15] §15.2"},

	// Storage and pager knobs (spec 22 §3, §13.2, §15).
	{Name: "cache_size", Category: "storage", Tier: TierPersistent, Kind: KindInt, Default: "adaptive", Computed: true, Doc: "[05] §6"},
	{Name: "checkpoint_mode", Category: "storage", Tier: TierPersistent, Kind: KindEnum, Default: "PASSIVE", Enum: []string{"PASSIVE", "FULL", "RESTART", "TRUNCATE"}, Doc: "[05] §4"},
	{Name: "graph_pool_fraction", Category: "storage", Tier: TierPersistent, Kind: KindFloat, Default: "0.60", Min: 0, Max: 1, HasMin: true, HasMax: true, Doc: "[05] §6.2"},
	{Name: "mmap_size", Category: "storage", Tier: TierPersistent, Kind: KindInt, Default: "0", Min: 0, HasMin: true, Doc: "[05] §6.3"},
	{Name: "prefetch_pages", Category: "storage", Tier: TierSession, Kind: KindInt, Default: "16", Min: 0, Max: 256, HasMin: true, HasMax: true, Doc: "[05] §6.4"},
	{Name: "read_ahead", Category: "storage", Tier: TierSession, Kind: KindBool, Default: "on", Doc: "[05] §6.4"},
	{Name: "segment_pool_fraction", Category: "storage", Tier: TierPersistent, Kind: KindFloat, Default: "0.30", Min: 0, Max: 1, HasMin: true, HasMax: true, Doc: "[05] §6.2"},
	{Name: "wal_autocheckpoint", Category: "storage", Tier: TierPersistent, Kind: KindInt, Default: "1000", Min: 0, Max: 1000000, HasMin: true, HasMax: true, Doc: "[05] §4"},
	{Name: "segment_max_fill", Category: "storage", Tier: TierPersistent, Kind: KindFloat, Default: "0.85", Min: 0, Max: 1, HasMin: true, HasMax: true, Doc: "[04] §3"},
	{Name: "segment_compaction_threshold", Category: "storage", Tier: TierPersistent, Kind: KindFloat, Default: "0.50", Min: 0, Max: 1, HasMin: true, HasMax: true, Doc: "[04] §6"},
	{Name: "segment_compaction_policy", Category: "storage", Tier: TierPersistent, Kind: KindEnum, Default: "lazy", Enum: []string{"lazy", "eager", "manual"}, Doc: "[04] §6"},
	{Name: "btree_fill_factor", Category: "storage", Tier: TierPersistent, Kind: KindFloat, Default: "0.75", Min: 0, Max: 1, HasMin: true, HasMax: true, Doc: "[04] §4"},
	{Name: "btree_interior_cache_fraction", Category: "storage", Tier: TierPersistent, Kind: KindFloat, Default: "0.10", Min: 0, Max: 1, HasMin: true, HasMax: true, Doc: "[04] §4"},
	{Name: "auto_vacuum", Category: "storage", Tier: TierPersistent, Kind: KindEnum, Default: "NONE", Enum: []string{"NONE", "INCREMENTAL", "FULL"}, Doc: "[04] §6"},
	{Name: "incremental_vacuum_pages", Category: "storage", Tier: TierSession, Kind: KindInt, Default: "100", Min: 0, HasMin: true, Doc: "[04] §6"},
	{Name: "freelist_mode", Category: "storage", Tier: TierPersistent, Kind: KindEnum, Default: "lazy", Enum: []string{"lazy", "zero", "return"}, Doc: "[04] §6"},

	// Durability knobs (spec 22 §4, §13.3).
	{Name: "commit_linger_us", Category: "durability", Tier: TierPersistent, Kind: KindInt, Default: "0", Min: 0, Max: 100000, HasMin: true, HasMax: true, Doc: "[05] §3.4"},
	{Name: "fatal_on_fsync", Category: "durability", Tier: TierPersistent, Kind: KindBool, Default: "on", Doc: "[05] §3.5"},
	{Name: "full_page_writes", Category: "durability", Tier: TierPersistent, Kind: KindBool, Default: "on", Doc: "[05] §3.3"},
	{Name: "journal_mode", Category: "durability", Tier: TierPersistent, Kind: KindEnum, Default: "WAL", Enum: []string{"WAL", "DELETE", "TRUNCATE", "PERSIST"}, Doc: "[05] §3"},
	{Name: "synchronous", Category: "durability", Tier: TierPersistent, Kind: KindEnum, Default: "NORMAL", Enum: []string{"OFF", "NORMAL", "FULL", "EXTRA"}, Doc: "[05] §3.2"},
	{Name: "wal_archive", Category: "durability", Tier: TierPersistent, Kind: KindString, Default: "", Doc: "[17] §5"},

	// HNSW index knobs (spec 22 §5, §13.4).
	{Name: "hnsw_build_threads", Category: "hnsw", Tier: TierSession, Kind: KindInt, Default: "GOMAXPROCS", Computed: true, Min: 1, Max: 256, HasMin: true, HasMax: true, Doc: "[07] §6"},
	{Name: "hnsw_delete_rebuild_threshold", Category: "hnsw", Tier: TierPersistent, Kind: KindFloat, Default: "0.20", Min: 0, Max: 1, HasMin: true, HasMax: true, Doc: "[07] §5.7"},
	{Name: "hnsw_ef_construction", Category: "hnsw", Tier: TierPersistent, Kind: KindInt, Default: "200", Min: 1, HasMin: true, Doc: "[07] §2.1"},
	{Name: "hnsw_ef_search", Aliases: []string{"ef_search"}, Category: "hnsw", Tier: TierSession, Kind: KindInt, Default: "64", Min: 1, HasMin: true, Doc: "[07] §3.1"},
	{Name: "hnsw_m", Category: "hnsw", Tier: TierPersistent, Kind: KindInt, Default: "16", Min: 2, Max: 128, HasMin: true, HasMax: true, Doc: "[07] §2.1"},
	{Name: "hnsw_max_m0", Category: "hnsw", Tier: TierPersistent, Kind: KindInt, Default: "2*m", Computed: true, Min: 2, Max: 256, HasMin: true, HasMax: true, Doc: "[07] §2.1"},
	{Name: "hnsw_ml", Category: "hnsw", Tier: TierPersistent, Kind: KindFloat, Default: "1/ln(m)", Computed: true, Min: 0.1, Max: 1.0, HasMin: true, HasMax: true, Doc: "[07] §2.1"},

	// IVF and PQ knobs (spec 22 §6, §13.5).
	{Name: "ivf_nlist", Category: "ivf", Tier: TierPersistent, Kind: KindInt, Default: "max(100,sqrt(N))", Computed: true, Min: 1, Max: 1000000, HasMin: true, HasMax: true, Doc: "[08] §2"},
	{Name: "ivf_nprobe", Aliases: []string{"nprobe"}, Category: "ivf", Tier: TierSession, Kind: KindInt, Default: "10", Min: 1, HasMin: true, Doc: "[08] §2.3"},
	{Name: "opq_enabled", Category: "ivf", Tier: TierPersistent, Kind: KindBool, Default: "off", Doc: "[09] §5"},
	{Name: "pq_m", Category: "ivf", Tier: TierPersistent, Kind: KindInt, Default: "dim/8", Computed: true, Min: 1, HasMin: true, Doc: "[09] §4"},
	{Name: "pq_nbits", Category: "ivf", Tier: TierPersistent, Kind: KindInt, Default: "8", Min: 4, Max: 12, HasMin: true, HasMax: true, Doc: "[09] §4"},

	// DiskANN knobs (spec 22 §6, §13.6).
	{Name: "diskann_alpha", Category: "diskann", Tier: TierPersistent, Kind: KindFloat, Default: "1.2", Min: 1.0, Max: 2.0, HasMin: true, HasMax: true, Doc: "[08] §4"},
	{Name: "diskann_beam_width", Category: "diskann", Tier: TierSession, Kind: KindInt, Default: "4", Min: 1, Max: 32, HasMin: true, HasMax: true, Doc: "[08] §4.3"},
	{Name: "diskann_degree", Category: "diskann", Tier: TierPersistent, Kind: KindInt, Default: "64", Min: 16, Max: 256, HasMin: true, HasMax: true, Doc: "[08] §4"},
	{Name: "diskann_node_cache_size", Category: "diskann", Tier: TierPersistent, Kind: KindInt, Default: "100000", Min: 0, Max: 10000000, HasMin: true, HasMax: true, Doc: "[08] §4.5"},
	{Name: "diskann_search_list_size", Category: "diskann", Tier: TierSession, Kind: KindInt, Default: "100", Min: 1, HasMin: true, Doc: "[08] §4.3"},

	// Quantization knobs (spec 22 §7, §13.7).
	{Name: "codec", Category: "quantization", Tier: TierPersistent, Kind: KindEnum, Default: "none", Enum: []string{"none", "fp16", "sq8", "pq", "opq", "rabitq", "binary"}, Doc: "[09] §2"},
	{Name: "rerank_r", Category: "quantization", Tier: TierSession, Kind: KindInt, Default: "2*k", Computed: true, Min: 1, HasMin: true, Doc: "[09] §6"},
	{Name: "retrain_interval_hours", Category: "quantization", Tier: TierPersistent, Kind: KindInt, Default: "24", Min: 1, Max: 8760, HasMin: true, HasMax: true, Doc: "[09] §7"},
	{Name: "retrain_policy", Category: "quantization", Tier: TierPersistent, Kind: KindEnum, Default: "manual", Enum: []string{"manual", "on_rebuild", "periodic"}, Doc: "[09] §7"},
	{Name: "training_sample_size", Category: "quantization", Tier: TierPersistent, Kind: KindInt, Default: "adaptive", Computed: true, Min: 0, HasMin: true, Doc: "[09] §7"},

	// Query and execution knobs (spec 22 §8, §13.8).
	{Name: "batch_size", Category: "query", Tier: TierSession, Kind: KindInt, Default: "64", Min: 1, Max: 1024, HasMin: true, HasMax: true, Doc: "[10] §8"},
	{Name: "default_k", Aliases: []string{"k"}, Category: "query", Tier: TierPersistent, Kind: KindInt, Default: "10", Min: 1, HasMin: true, Doc: "[10] §2"},
	{Name: "filter_strategy", Category: "query", Tier: TierSession, Kind: KindEnum, Default: "auto", Enum: []string{"auto", "pre", "in", "post"}, Doc: "[11] §3"},
	{Name: "gomaxprocs", Category: "query", Tier: TierProcess, Kind: KindInt, Default: "NumCPU", Computed: true, Min: 1, Max: 256, HasMin: true, HasMax: true, Doc: "[10] §9"},
	{Name: "max_k", Category: "query", Tier: TierPersistent, Kind: KindInt, Default: "10000", Min: 1, Max: 1000000, HasMin: true, HasMax: true, Doc: "[10] §2"},
	{Name: "max_query_memory", Category: "query", Tier: TierSession, Kind: KindInt, Default: "268435456", Min: 16777216, HasMin: true, Doc: "[10] §8"},
	{Name: "over_fetch_factor", Category: "query", Tier: TierSession, Kind: KindFloat, Default: "1.0", Min: 1.0, Max: 10.0, HasMin: true, HasMax: true, Doc: "[11] §3.3"},
	{Name: "query_timeout", Aliases: []string{"timeout"}, Category: "query", Tier: TierSession, Kind: KindInt, Default: "30000", Min: 0, Max: 3600000, HasMin: true, HasMax: true, Doc: "[10] §8"},
	{Name: "worker_pool_size", Category: "query", Tier: TierPersistent, Kind: KindInt, Default: "GOMAXPROCS", Computed: true, Min: 1, Max: 1024, HasMin: true, HasMax: true, Doc: "[10] §9"},

	// Hybrid and filtering knobs (spec 22 §9, §13.9).
	{Name: "bm25_b", Category: "hybrid", Tier: TierPersistent, Kind: KindFloat, Default: "0.75", Min: 0, Max: 1, HasMin: true, HasMax: true, Doc: "[11] §6"},
	{Name: "bm25_k1", Category: "hybrid", Tier: TierPersistent, Kind: KindFloat, Default: "1.2", Min: 0, Max: 3, HasMin: true, HasMax: true, Doc: "[11] §6"},
	{Name: "ef_filter_inflation", Category: "hybrid", Tier: TierSession, Kind: KindFloat, Default: "2.0", Min: 1.0, Max: 20.0, HasMin: true, HasMax: true, Doc: "[11] §3.4"},
	{Name: "fusion_weights", Category: "hybrid", Tier: TierSession, Kind: KindFloatList, Default: "[1,1]", Doc: "[11] §7"},
	{Name: "rrf_k", Category: "hybrid", Tier: TierSession, Kind: KindInt, Default: "60", Min: 1, Max: 1000, HasMin: true, HasMax: true, Doc: "[11] §7"},

	// Planner knobs (spec 22 §10, §13.10).
	{Name: "adaptive_retry", Category: "planner", Tier: TierPersistent, Kind: KindBool, Default: "on", Doc: "[13] §5"},
	{Name: "auto_analyze", Category: "planner", Tier: TierPersistent, Kind: KindBool, Default: "on", Doc: "[13] §4"},
	{Name: "plan_cache_size", Category: "planner", Tier: TierPersistent, Kind: KindInt, Default: "256", Min: 0, Max: 10000, HasMin: true, HasMax: true, Doc: "[13] §6"},
	{Name: "recall_target", Category: "planner", Tier: TierSession, Kind: KindFloat, Default: "0.95", Min: 0, Max: 1, HasMin: true, HasMax: true, Doc: "[13] §5.2"},
	{Name: "stats_target", Category: "planner", Tier: TierPersistent, Kind: KindInt, Default: "100", Min: 10, Max: 10000, HasMin: true, HasMax: true, Doc: "[13] §4"},

	// Server knobs (spec 22 §11, §13.11, §18).
	{Name: "auth_mode", Category: "server", Tier: TierServer, Kind: KindEnum, Default: "none", Enum: []string{"none", "bearer", "mtls", "jwt"}, Doc: "[16] §5"},
	{Name: "auth_token", Category: "server", Tier: TierServer, Kind: KindString, Default: "", Doc: "[16] §5"},
	{Name: "grpc_addr", Category: "server", Tier: TierServer, Kind: KindString, Default: ":50051", Doc: "[16] §2"},
	{Name: "grpc_enabled", Category: "server", Tier: TierServer, Kind: KindBool, Default: "on", Doc: "[16] §2"},
	{Name: "jwt_pubkey", Category: "server", Tier: TierServer, Kind: KindString, Default: "", Doc: "[16] §5"},
	{Name: "max_connections", Category: "server", Tier: TierServer, Kind: KindInt, Default: "1000", Min: 1, Max: 100000, HasMin: true, HasMax: true, Doc: "[16] §4"},
	{Name: "max_request_size", Category: "server", Tier: TierServer, Kind: KindInt, Default: "16777216", Min: 1, HasMin: true, Doc: "[16] §4"},
	{Name: "pg_wire_addr", Category: "server", Tier: TierServer, Kind: KindString, Default: "", Doc: "[16] §2"},
	{Name: "pg_wire_enabled", Category: "server", Tier: TierServer, Kind: KindBool, Default: "off", Doc: "[16] §2"},
	{Name: "rate_limit_rps", Category: "server", Tier: TierServer, Kind: KindInt, Default: "0", Min: 0, HasMin: true, Doc: "[16] §4"},
	{Name: "rest_addr", Category: "server", Tier: TierServer, Kind: KindString, Default: ":8080", Doc: "[16] §2"},
	{Name: "rest_enabled", Category: "server", Tier: TierServer, Kind: KindBool, Default: "on", Doc: "[16] §2"},
	{Name: "tls_ca", Category: "server", Tier: TierServer, Kind: KindString, Default: "", Doc: "[16] §3"},
	{Name: "tls_cert", Category: "server", Tier: TierServer, Kind: KindString, Default: "", Doc: "[16] §3"},
	{Name: "tls_key", Category: "server", Tier: TierServer, Kind: KindString, Default: "", Doc: "[16] §3"},

	// Observability knobs (spec 22 §12, §13.12).
	{Name: "log_format", Category: "observability", Tier: TierProcess, Kind: KindEnum, Default: "text", Enum: []string{"text", "json"}, Doc: "[18] §2"},
	{Name: "log_level", Category: "observability", Tier: TierProcess, Kind: KindEnum, Default: "info", Enum: []string{"debug", "info", "warn", "error"}, Doc: "[18] §2"},
	{Name: "metrics_addr", Category: "observability", Tier: TierServer, Kind: KindString, Default: "", Doc: "[18] §3"},
	{Name: "metrics_enabled", Category: "observability", Tier: TierServer, Kind: KindBool, Default: "on", Doc: "[18] §3"},
	{Name: "query_log_enabled", Category: "observability", Tier: TierPersistent, Kind: KindBool, Default: "off", Doc: "[18] §4"},
	{Name: "recall_sampling", Category: "observability", Tier: TierPersistent, Kind: KindFloat, Default: "0.0", Min: 0, Max: 1, HasMin: true, HasMax: true, Doc: "[18] §5"},
	{Name: "slow_query_threshold_ms", Category: "observability", Tier: TierPersistent, Kind: KindInt, Default: "0", Min: 0, HasMin: true, Doc: "[18] §4"},
	{Name: "trace_sampling", Category: "observability", Tier: TierPersistent, Kind: KindFloat, Default: "0.0", Min: 0, Max: 1, HasMin: true, HasMax: true, Doc: "[18] §6"},

	// MVCC and transaction knobs (spec 22 §16).
	{Name: "max_snapshot_age", Category: "mvcc", Tier: TierPersistent, Kind: KindInt, Default: "0", Min: 0, Max: 86400, HasMin: true, HasMax: true, Doc: "[06] §7"},
	{Name: "txn_timeout_ms", Category: "mvcc", Tier: TierSession, Kind: KindInt, Default: "0", Min: 0, Max: 3600000, HasMin: true, HasMax: true, Doc: "[06] §5"},
	{Name: "max_write_txn_size", Category: "mvcc", Tier: TierPersistent, Kind: KindInt, Default: "0", Min: 0, Max: 1000000, HasMin: true, HasMax: true, Doc: "[06] §4"},
	{Name: "isolation", Category: "mvcc", Tier: TierSession, Kind: KindEnum, Default: "snapshot", Enum: []string{"snapshot"}, ReadOnly: true, Doc: "[06] §2"},

	// Index lifecycle knobs (spec 22 §17).
	{Name: "index_build_mode", Category: "index", Tier: TierSession, Kind: KindEnum, Default: "inline", Enum: []string{"inline", "background", "offline"}, Doc: "[07] §8"},
	{Name: "index_shadow_query", Category: "index", Tier: TierPersistent, Kind: KindBool, Default: "on", Doc: "[07] §5.7"},
	{Name: "reindex_on_open", Category: "index", Tier: TierPersistent, Kind: KindBool, Default: "off", Doc: "[07] §8"},
	{Name: "index_not_found_policy", Category: "index", Tier: TierPersistent, Kind: KindEnum, Default: "flat_fallback", Enum: []string{"flat_fallback", "error"}, Doc: "[07] §8"},

	// Encryption and key management knobs (spec 22 §18).
	{Name: "key_rotation_policy", Category: "security", Tier: TierPersistent, Kind: KindEnum, Default: "manual", Enum: []string{"manual", "auto_90d"}, Doc: "[23] §3.3"},
	{Name: "key_check_on_open", Category: "security", Tier: TierSession, Kind: KindBool, Default: "on", Doc: "[23] §3.2"},
}

// byName indexes every knob by its canonical name and by each alias, lowercased.
var byName = map[string]*Knob{}

func init() {
	for i := range registry {
		k := &registry[i]
		byName[strings.ToLower(k.Name)] = k
		for _, a := range k.Aliases {
			byName[strings.ToLower(a)] = k
		}
	}
}

// Lookup resolves a knob by canonical name or alias, case-insensitively. The
// returned pointer is into the shared registry and must not be mutated.
func Lookup(name string) (*Knob, bool) {
	k, ok := byName[strings.ToLower(strings.TrimSpace(name))]
	return k, ok
}

// All returns every knob in name order. The slice is a fresh copy, so callers may
// sort or filter it freely.
func All() []Knob {
	out := make([]Knob, len(registry))
	copy(out, registry)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// WithPrefix returns the knobs whose name starts with prefix, in name order.
func WithPrefix(prefix string) []Knob {
	var out []Knob
	for _, k := range All() {
		if strings.HasPrefix(k.Name, prefix) {
			out = append(out, k)
		}
	}
	return out
}
