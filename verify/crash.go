package verify

import (
	"fmt"

	"github.com/tamnd/vec/vfs"
)

// CrashDB is the minimal handle the crash harness needs from a database opened
// on a fault FS (spec 21 §8.2). The real DB satisfies it; a test can supply a
// toy log-structured store to exercise the harness itself.
type CrashDB interface {
	Close() error
}

// CrashHarness drives the exhaustive crash enumeration (spec 21 §8.2). The
// caller supplies how to make a fresh persistent backing store, how to open a DB
// on a given FS, the workload to run, and how to verify a recovered DB. The
// harness owns the fault injection and the crash-point loop.
type CrashHarness struct {
	// NewBacking returns a fresh, empty persistent store. The same instance is
	// reused across a crash and the reopen within one crash point, so a write
	// made durable before the crash survives into recovery.
	NewBacking func() vfs.FS
	// Open opens a database on fs. It is called once before the crash to run the
	// workload and once after to recover.
	Open func(fs vfs.FS) (CrashDB, error)
	// Workload runs the mutations. It may return ErrCrashed (or a wrapped form)
	// when the FS crashes mid-write; the harness treats that as expected.
	Workload func(db CrashDB) error
	// Verify checks a recovered DB against the durable-prefix property for the
	// given crash point: no committed state lost, no uncommitted state visible,
	// no broken invariant. It returns an error describing any violation.
	Verify func(db CrashDB, crashAt int) error
}

// CrashReport is the outcome of one crash point.
type CrashReport struct {
	CrashAt int
	Err     error
}

// RunExhaustiveCrash counts the syncs in the workload, then crashes at each sync
// boundary and verifies recovery (spec 21 §8.2). It returns one report per crash
// point that failed verification; an empty slice means every crash point
// recovered to a valid durable prefix.
func RunExhaustiveCrash(h CrashHarness) ([]CrashReport, error) {
	nSyncs, err := h.countSyncs()
	if err != nil {
		return nil, fmt.Errorf("crash harness: count syncs: %w", err)
	}

	var failures []CrashReport
	for crashAt := 0; crashAt < nSyncs; crashAt++ {
		backing := h.NewBacking()

		// Run the workload until it crashes at this sync boundary.
		crashFS := NewFaultFS(backing, FaultConfig{Mode: FaultCrash, CrashAfterSync: crashAt})
		if err := h.runWorkload(crashFS); err != nil {
			return failures, fmt.Errorf("crash harness: workload at crashAt=%d: %w", crashAt, err)
		}

		// Reopen on the same backing through a transparent FS and verify.
		recoverFS := NewFaultFS(backing, FaultConfig{Mode: FaultNone})
		db, err := h.Open(recoverFS)
		if err != nil {
			failures = append(failures, CrashReport{CrashAt: crashAt, Err: fmt.Errorf("reopen: %w", err)})
			continue
		}
		verr := h.Verify(db, crashAt)
		_ = db.Close()
		if verr != nil {
			failures = append(failures, CrashReport{CrashAt: crashAt, Err: verr})
		}
	}
	return failures, nil
}

// countSyncs runs the workload once on a transparent FS and reports how many
// syncs it issued, which bounds the crash-point loop.
func (h CrashHarness) countSyncs() (int, error) {
	fs := NewFaultFS(h.NewBacking(), FaultConfig{Mode: FaultNone})
	db, err := h.Open(fs)
	if err != nil {
		return 0, err
	}
	if err := h.Workload(db); err != nil {
		return 0, err
	}
	if err := db.Close(); err != nil {
		return 0, err
	}
	return fs.SyncCount(), nil
}

// runWorkload opens a DB on the crashing FS and runs the workload, swallowing
// the crash. A panic or an ErrCrashed return is the expected way a workload ends
// when the FS dies mid-write; any other error is propagated.
func (h CrashHarness) runWorkload(fs *FaultFS) (err error) {
	defer func() {
		if r := recover(); r != nil {
			// A crash surfacing as a panic is expected; only an unexpected panic
			// would carry a non-crash value, which we still swallow here because
			// the engine may panic on a failed write by design.
			err = nil
		}
	}()
	db, oerr := h.Open(fs)
	if oerr != nil {
		// Opening may itself crash if the FS is configured to crash at sync 0.
		if fs.Crashed() {
			return nil
		}
		return oerr
	}
	defer func() { _ = db.Close() }()
	werr := h.Workload(db)
	if werr != nil && !fs.Crashed() {
		return werr
	}
	return nil
}
