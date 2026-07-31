package store

// SnapshotTo 把业务库的一致快照写到 dst(现场数据上报用,AGENTS.md「全局约定·
// 现场数据上报」,2026-07-31 甲方裁决)。
//
// 必须走 VACUUM INTO,不能直接拷 brain.db 文件:库跑在 WAL 模式下,已提交但尚未
// checkpoint 的事务还留在 -wal 里(开发机上实测见过 4MB),直接拷文件会得到一个缺
// 尾巴的库 —— 而它看起来是能打开的,排障时按它下的结论会是错的。VACUUM INTO 由
// SQLite 自己保证目标是完整、一致、已整理的副本。
//
// 走 Store 自己的连接是有意的:业务库按 SetMaxOpenConns(1) 串行化,快照因此排在脑
// 的写入队列里,不构成第二个写入者。代价是 VACUUM 期间业务写要排队 —— 60MB 的库
// 约几秒,所以上报按裁决只由人显式点击触发,不做定时。
func (s *Store) SnapshotTo(dst string) error {
	return s.db.Exec("VACUUM INTO ?", dst).Error
}
