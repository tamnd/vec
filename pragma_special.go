package vec

import (
	"fmt"
	"strings"

	"github.com/tamnd/vec/config"
)

// specialPragma handles the read-only diagnostic PRAGMAs (spec 22 §19.2), the
// action PRAGMAs (§19.3), and the configuration view (§1.5). It returns
// handled=false when the name is an ordinary knob, so the caller falls through to
// the registry path.
func (db *DB) specialPragma(name, value string) (result string, handled bool, err error) {
	switch name {
	case "configuration":
		return db.configurationView(value), true, nil
	case "vec_version":
		return "vec " + version, true, nil
	case "file_format":
		return "1", true, nil

	// File and WAL diagnostics. The process-resident engine has no on-disk file
	// or WAL yet, so these report zero honestly rather than a fabricated size.
	case "page_count", "freelist_count", "file_size", "wal_size", "wal_frame_count":
		if value != "" {
			return "", true, &ErrPragmaReadOnly{Pragma: name}
		}
		return "0", true, nil
	case "wal_checkpoint_threshold":
		if value != "" {
			return "", true, &ErrPragmaReadOnly{Pragma: name}
		}
		k, _ := config.Lookup("wal_autocheckpoint")
		return db.pragmaRead(k), true, nil

	// Integrity checks pass trivially on the in-memory engine: there is no file to
	// corrupt. They become substantive once the pager is wired.
	case "integrity_check", "quick_check":
		if value != "" {
			return "", true, &ErrPragmaReadOnly{Pragma: name}
		}
		return "ok", true, nil

	// Action PRAGMAs that are safe no-ops on the in-memory engine.
	case "wal_checkpoint", "analyze", "optimize", "incremental_vacuum", "vacuum", "compact_segments":
		return "ok", true, nil

	// Action PRAGMAs that need a subsystem this build does not expose through the
	// public surface; report unsupported rather than pretend they ran.
	case "rebuild_index", "rekey", "estimate_recall", "index_build_status", "index_integrity_check", "collection_info", "index_info", "collection_size":
		return "", true, errUnsupported
	}
	return "", false, nil
}

// configurationView renders the effective configuration as a text table (spec 22
// §1.5). A non-empty arg filters knobs by name prefix. The columns are name,
// value, tier, default, and the doc cross-reference.
func (db *DB) configurationView(prefix string) string {
	knobs := config.All()
	var b strings.Builder
	fmt.Fprintf(&b, "%-32s %-22s %-11s %-18s %s\n", "name", "value", "tier", "default", "doc")
	for i := range knobs {
		k := &knobs[i]
		if prefix != "" && !strings.HasPrefix(k.Name, prefix) {
			continue
		}
		def := k.Default
		fmt.Fprintf(&b, "%-32s %-22s %-11s %-18s %s\n", k.Name, db.pragmaRead(k), k.Tier, def, k.Doc)
	}
	return strings.TrimRight(b.String(), "\n")
}
