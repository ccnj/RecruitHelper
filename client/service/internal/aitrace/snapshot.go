package aitrace

// SnapshotTo writes a consistent copy of the trace corpus to dst, for the field
// report upload authorized on 2026-07-31 (AGENTS.md「全局约定·现场数据上报」).
//
// VACUUM INTO rather than a file copy, for the same reason as the brain
// database: this store also runs in WAL mode, so a plain copy can miss
// committed-but-uncheckpointed transactions and still open cleanly — a silently
// truncated corpus is worse than none.
//
// The caller is responsible for the boundary: trace rows hold full provider
// request/response bodies, which may leave this machine only through the field
// report channel and only for the packaging step. They must never reach ordinary
// logs, brain.db, the admin API, or any other remote destination.
func (s *Store) SnapshotTo(dst string) error {
	return s.db.Exec("VACUUM INTO ?", dst).Error
}
