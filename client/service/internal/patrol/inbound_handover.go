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
