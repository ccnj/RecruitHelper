package patrol

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// 交接日入站建档闸（2026-07-29 甲方裁决，见 docs/交接日入站闸裁决-2026-07-29.md）。
//
// 旧产品时代留在平台账号里的会话与"陌生候选人主动来聊"在列表上无法区分：同样
// 有 platformUserRef、displayName 和可匹配的职位标题。缺这道闸时，一个旧会话
// 只要进入列表窗口就会被建档，随后自动走完采简历、建 V4 根、AI 生成回复的整条
// 链，对旧产品已经聊过、可能已约面或已谈崩的候选人重走一遍沟通流程。
//
// 闸只作用于尚无档案的会话，判据取列表行已有的 lastActivityTs，不新增页面读取，
// 也不落任何持久拒绝状态：被跳过的会话下一轮巡检重新评估。lastActivityTs 会被
// 新消息刷新，因此本闸拦不住"旧关系在交接日之后复活"，该缺口由甲方明确接受。
const (
	InboundHandoverDateEnv     = "RECRUITHELPER_INBOUND_HANDOVER_DATE"
	DefaultInboundHandoverDate = "2026-07-29"

	inboundHandoverDateLayout = "2006-01-02"
	// 与既有主动来聊收编审计的 status=skipped 通道共用，不新增审计类别。
	inboundHandoverSkipReason = "beforeHandover"
)

var ErrInboundHandoverDateInvalid = errors.New("交接日必须是 YYYY-MM-DD")

// ParseInboundHandoverCutoff 把 YYYY-MM-DD 解析成客户端本地时区当日 00:00。
// 空值取默认交接日；任何其他非法值返回错误，由调用方拒绝启动。格式打错一位
// （例如 2026-8-1）若静默回落到默认，会放进一批本应挡住的旧会话，正是本闸要
// 阻断的方向，所以这里不做任何宽容解析。
func ParseInboundHandoverCutoff(value string, location *time.Location) (time.Time, error) {
	if location == nil {
		location = time.Local
	}
	date := strings.TrimSpace(value)
	if date == "" {
		date = DefaultInboundHandoverDate
	}
	cutoff, err := time.ParseInLocation(inboundHandoverDateLayout, date, location)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q", ErrInboundHandoverDateInvalid, value)
	}
	return cutoff, nil
}

// inboundHandoverBlocked 判定一个尚无档案的会话是否属于交接前存量。
// 时间戳缺失时同样拦截：读不到会话年龄不该默认接管，方向保守。
func inboundHandoverBlocked(lastActivityTs *int64, cutoff time.Time) bool {
	if lastActivityTs == nil {
		return true
	}
	return *lastActivityTs < cutoff.UnixMilli()
}

const (
	// listStopOlderThanDaysMax 是滚动窗口上界，取值依据是沉默跟进的七天兜底
	// 归档周期加一天余量：超过它的会话已经没有自动化动作等待触发，继续向下
	// 翻只会消耗窗口预算。它同时是本函数唯一的无界防线——交接日是固定日历
	// 日，单独用它推导会让遍历范围随时间单调增长。
	listStopOlderThanDaysMax = 8
	listStopOlderThanDaysMin = 1
)

// listStopOlderThanDays 推导 all 面列表遍历的年龄截止。取滚动上界与"距交接日
// 天数"的较小值：交接日之前的会话一律不建档（inboundHandoverBlocked），继续
// 向下翻只会读到永远不会被处理的存量，而平台列表按最后活动时间倒序排列，
// 因此撞上第一个交接前会话即可安全收束本轮。
//
// 两条不变式，改动此函数时必须同时保住：
//
//  1. 手端把参数解释为滚动的 days×24h（cutoffMs = Date.now() - days*86400000），
//     而交接日是本地日历日 00:00。"+1" 正是两种时间语义的转换余量：设 now =
//     今日00:00 + t（0 ≤ t < 24h）、今日00:00 - 交接日 = D 天，则手端截止
//     = 交接日 + t - 24h < 交接日。**手端截止永远早于交接日**，所以本函数
//     只会多读、不会少读。
//
//  2. 已建档且仍在自动化沟通中的会话不会掉出范围：V4 聚合以我方招呼为根，
//     招呼不早于交接日且发送本身会刷新 lastActivityTs；即使候选人始终不回，
//     七天兜底归档也会在上界之内终局它。掉出范围的只可能是尚无 V4 根的会话，
//     它们的推进走漏斗的推荐页扫描，不经过 IM 列表遍历。
//
// 交接日被配置到未来时不缩小范围，直接退回上界：异常配置的方向必须是多读。
func listStopOlderThanDays(now, cutoff time.Time, location *time.Location) int {
	if location == nil {
		location = time.Local
	}
	year, month, day := now.In(location).Date()
	today := time.Date(year, month, day, 0, 0, 0, 0, location)
	days := int(today.Sub(cutoff)/(24*time.Hour)) + 1
	if days < listStopOlderThanDaysMin {
		return listStopOlderThanDaysMax
	}
	if days > listStopOlderThanDaysMax {
		return listStopOlderThanDaysMax
	}
	return days
}
