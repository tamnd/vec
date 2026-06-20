// Package query is vec's read-path query engine: the cost-based planner that
// turns a bound logical query into a physical plan (spec 13) and the vectorized
// executor that runs that plan against the index SPI, the storage engine, and the
// distance kernels (spec 10).
//
// The package sits above storage ([04]), index ([07], [08]), distance ([09]), and
// catalog ([02]), and below the VectorSQL frontend ([12], task 14) and the library
// facade ([14], task 15). The frontend produces the BoundQuery this package
// consumes; the planner emits a PhysicalPlan; the executor walks it.
//
// The one defining constraint is latency: the SLO is p50 < 1 ms for 1M x 768-dim
// HNSW recall@10 >= 0.95 on a single node (spec 10 §intro). Late materialization,
// the bounded top-k heap, and the per-query scratch arena are the levers.
package query
