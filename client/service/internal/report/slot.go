package report

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"time"
)

// 凌晨档期(2026-09-04 甲方裁决,立案见 docs/凌晨上传时段打散立案-2026-09-04.md)。
//
// 改造前三条凌晨任务钉死在 00:05 / 00:10 / 00:20,所有客户机同一分钟一起上传。
// 真机实测的代价:近 14 天现场包上传 64 次成功、33 次失败(失败率 34%),33 次失败
// 的时间戳全部落在 00:12:07~00:12:31 —— 那正是 00:10 触发点加 UploadTimeout。
// 客户端请求里 97% 来自同一个出口 IP,几台机器共用一条上行,同一分钟各推 150MB
// 谁都传不完,大包全超时、小包才侥幸传成。
//
// 现在改为:每台机器每天抽一个"档期"起点 T,三条任务在档期内按固定偏移排开,
// 把共享上行从"几台抢"变成"一台独占"。
//
// 用 (本地日期 + 机器标识) 稳定哈希而不是随机抽取,理由有二:脑重启重算必得同值
// (与「当日职位计划」总量抽取同一范式,不会因为半夜重启而一天跑两次);排障时能
// 反算出"这台机器今天该在几点几分传",日志对得上。不同机器因机器标识不同天然错开,
// 同一台机器因日期不同每天不同,不会长期占住同一个坑。
const (
	// 档期窗口 [00:05, 01:35)。起点沿用改造前最早那条任务的 00:05;宽度 90 分钟
	// 是在"撞车概率"与"排障时要等多久才能断定今天没传"之间取的折中。
	slotWindowStart  = 5 * time.Minute
	slotWindowSpread = 90 * time.Minute
)

// 三条凌晨任务相对档期起点的偏移。保持改造前 00:05 / 00:10 / 00:20 的相对间隔,
// 因为那个先后是有意排的:审计清理先跑完,当天的诊断包才是瘦身后的;聊天记录排
// 最后,不跟前两个抢静默窗口,也不跟同机的大包抢上行。
const (
	OffsetCmdRetention = time.Duration(0)
	OffsetFieldReport  = 5 * time.Minute
	OffsetChatReport   = 15 * time.Minute
)

// SlotPicker 返回本机在指定本地日期的档期起点。三条凌晨任务共用同一个 picker,
// 各自用 Offset 落位 —— 共用是必须的,否则同一台机器的大包与聊天记录会各抽各的,
// 有概率撞在一起互相抢上行,正是本次要消掉的那件事。
type SlotPicker func(day time.Time) time.Time

// NewSlotPicker 用机器标识构造 picker。machineID 取闭包而不是值:脑启动时可能
// 还没激活(config 尚未落盘),而调度 goroutine 常驻 —— 每次抽取现取,激活之后
// 自然收敛到该机器自己的档期。
//
// 未激活时机器标识为空,所有未激活机器会算出同一个档期。这不构成问题:出站的两条
// 任务在授权未就绪时本来就不传,剩下的审计清理是纯本地动作,撞车无代价。
func NewSlotPicker(machineID func() string) SlotPicker {
	return func(day time.Time) time.Time {
		midnight := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
		identity := ""
		if machineID != nil {
			identity = strings.TrimSpace(machineID())
		}
		// 秒粒度而不是分钟粒度:抢带宽是秒级的事,两台机器差 40 秒就已经不互相拖累,
		// 而按分钟取模会让 90 分钟窗口只剩 90 个坑,十几台机器撞车概率明显更高。
		sum := sha256.Sum256([]byte(midnight.Format("2006-01-02") + "|" + identity))
		spreadSeconds := uint64(slotWindowSpread / time.Second)
		offset := binary.BigEndian.Uint64(sum[:8]) % spreadSeconds
		return midnight.Add(slotWindowStart).Add(time.Duration(offset) * time.Second)
	}
}
