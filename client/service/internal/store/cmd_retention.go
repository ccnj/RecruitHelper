package store

import "time"

// 命令审计留存(AGENTS.md「全局约定·命令审计留存」,2026-08-07 甲方裁决)。
//
// cmd_records.result_body 只保留最近 48 小时。**置空不是删行** —— msg_id、
// 原语名、idem_key、args、guards、时间、状态与错误码原样留着,「业务事实行禁止
// 物理 DELETE」不因此放宽。
//
// 立案依据是真机数据:客户机 10 天积到 354MB,其中 cmd_records 占 79%,而只读
// 原语 chat.readList 一个就占 143MB —— 它每 7～15 秒读一次会话列表、单条返回
// 9.6KB 全文落库,而同期真正的业务事实(全部聊天消息)只有 2.2MB。
const CmdResultRetention = 48 * time.Hour

// PurgeCmdResultBodies 把 cutoff 之前的 result_body 置空,返回受影响行数。
//
// 用 UpdateColumn 而不是 Update:后者会顺手刷新 updated_at,把"这条命令最后一次
// 状态变更发生在何时"这个审计事实覆盖成"清理跑过的时刻"。清理是维护动作,不该
// 在审计行上留下业务痕迹。
//
// result_body <> '' 的条件让重复执行是幂等的,也让"本次清了多少条"这个数字诚实
// —— 否则每天都会把同一批早已清空的行再数一遍。
func (s *Store) PurgeCmdResultBodies(cutoff time.Time) (int64, error) {
	res := s.db.Model(&CmdRecord{}).
		Where("created_at < ? AND result_body <> ''", cutoff).
		UpdateColumn("result_body", "")
	return res.RowsAffected, res.Error
}

// VacuumDB 重写整个库,把置空腾出的页真正还给文件系统。
//
// 不 VACUUM 的话 SQLite 只把那些页标成空闲留着复用,brain.db 的**文件大小纹丝
// 不动** —— 诊断包照样每天传三百多兆,服务器那边一分没省。所以它不是可选的收尾。
//
// VACUUM 全程持有写锁并重写整个库,因此只能在静默窗口里跑;调用方负责确认没有
// 活跃工作流与未收束命令。
//
// 真机规模的合成库(255MB、2.6 万行)在开发机 SSD 上实测:置空 2 万行 0.56 秒、
// VACUUM 0.45 秒,文件收到 54MB。同一次实测也确认了置空之后不 VACUUM 的话文件
// 停在 255MB 一字节不少。客户机是机械盘或负载高时会更慢,但 00:05 到 02:00 的
// 顺延窗口留得足够宽,不必为它另设超时。
func (s *Store) VacuumDB() error {
	return s.db.Exec("VACUUM").Error
}
