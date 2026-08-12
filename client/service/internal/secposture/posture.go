// Package secposture 采集本机安全姿态(AGENTS.md「全局约定·工作状态上报」
// 2026-08-12 甲方裁决增补)。
//
// 立案起因:2026-08-12 晨,MSRT v5.144 随 Windows 更新在客户机(杨小七01)上
// 静默杀掉运行中的脑进程并删除 exe。Defender 排除项对 MSRT 无效,保护历史与
// Defender 操作日志均无记录,唯一痕迹在 C:\Windows\debug\mrt.log。每月补丁日
// 所有客户机都会重掷一次这个骰子,而"哪台机器上了免疫键、哪台还裸奔"此前只能
// 靠人挨台远程核对。本包把这份核对固化成只读采集,经工作状态上报上行。
//
// 三条纪律,与裁决逐字对应:
//  1. 只读。不写注册表、不改排除项、不修任何东西 —— 修补永远走人工远程。
//  2. 采不到=unknown。权限不足、文件缺失、powershell 失败,一律如实记未知,
//     不重试、不影响上报主体,更不影响业务。
//  3. 非 Windows 整块不存在。mac 开发机上采集器不启动,载荷里没有 security 块。
package secposture

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// 三态与枚举值。后台 pydantic 与管理前台按这些字面量对齐,改动要三处同步。
const (
	StateUnknown = "unknown"

	MrtPolicySet    = "set"    // 免疫键已设为 1,Windows 更新不再分发 MSRT
	MrtPolicyAbsent = "absent" // 键不存在或不为 1

	DefenderRunning = "running"
	DefenderStopped = "stopped"

	ExclusionsOK      = "ok"      // 两条目录排除都在
	ExclusionsPartial = "partial" // 只有一条
	ExclusionsMissing = "missing" // 可读且确认两条都不在
	// unknown:Get-MpPreference 读不到(服务死/权限不足)且策略键里也没有 ——
	// 此时无法证明"没有",按不确认处理,不冒充 missing。

	MsrtScanned  = "scanned"  // mrt.log 里有运行记录,摘要在其余字段
	MsrtNeverRan = "neverRan" // mrt.log 不存在:这台机器 MSRT 从未运行过
)

// exeMarker 用于把 mrt.log 里的检出与"我们的文件"绑定。别家的真木马被 MSRT
// 检出不是我们的事,不采、不报(隐私原则:不主动多取)。
const exeMarker = "RecruitHelperBrain.exe"

// refreshEvery 是后台刷新周期。姿态变化以"人远程改一次设置"或"每月补丁日"为
// 节奏,每日一采足够;高频采集要反复拉起 powershell,不值。
const refreshEvery = 24 * time.Hour

// Posture 是上行的 security 块本体,JSON 键与旧后台 app/api/client_status.py
// 的 alias、管理前台 types.ts 逐字对齐。全部是布尔/枚举/时间戳/短字符串,
// 构造上装不下候选人身份或业务正文。
type Posture struct {
	CollectedAt time.Time `json:"collectedAt"`
	// MrtPolicy: set / absent / unknown
	MrtPolicy string `json:"mrtPolicy"`
	// DefenderService: running / stopped / unknown
	DefenderService string `json:"defenderService"`
	// DefenderExclusions: ok / partial / missing / unknown
	DefenderExclusions string `json:"defenderExclusions"`
	// AvProducts 是安全中心登记名与服务指纹检出名的并集。登记自愿、会漏
	// (火绒常缺席),所以两层合查;仍可能两层都漏,空列表不等于没装杀软。
	AvProducts []string `json:"avProducts"`
	Msrt       MsrtRun  `json:"msrt"`
}

// MsrtRun 是 mrt.log 最近一次运行的摘要。
type MsrtRun struct {
	// Status: scanned / neverRan / unknown
	Status string `json:"status"`
	// LastRunAt 保留日志原文时刻(如 "Wed Aug 12 08:12:08 2026"),不做时区
	// 换算 —— 原文比一次可能算错的转换更可审计。
	LastRunAt string `json:"lastRunAt,omitempty"`
	Version   string `json:"version,omitempty"`
	// DetectedUs / RemovedUs 只在检出行里出现我们的 exe 名时为真。
	DetectedUs bool `json:"detectedUs"`
	RemovedUs  bool `json:"removedUs"`
}

// Collector 持有最近一次采集结果。采集在后台进行,读方永远拿缓存,零阻塞。
type Collector struct {
	mu  sync.Mutex
	cur *Posture
}

func NewCollector() *Collector { return &Collector{} }

// Cached 返回最近一次采集结果;从未采集(含非 Windows)返回 nil,
// 载荷侧 omitempty 会让 security 块整体消失。
func (c *Collector) Cached() *Posture {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur
}

func (c *Collector) set(p *Posture) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur = p
}

// Run 阻塞运行采集循环直到 ctx 结束。非 Windows 上立即返回。
func (c *Collector) Run(ctx context.Context) {
	if !collectSupported {
		return
	}
	c.set(collectOnce(ctx))
	ticker := time.NewTicker(refreshEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.set(collectOnce(ctx))
		}
	}
}

// ---- powershell 汇总报告的解析与求值(纯逻辑,跨平台测试) ----

// psReport 对应探针脚本 ConvertTo-Json 的输出。字段用 flexList 兜 PowerShell
// 5.1 的单元素数组塌缩:管道展开会把 @("x") 序列化成 "x"。
type psReport struct {
	WinDefend          string   `json:"winDefend"`
	Exclusions         flexList `json:"exclusions"`
	ExclusionsReadable bool     `json:"exclusionsReadable"`
	Wsc                flexList `json:"wsc"`
	Fingerprint        flexList `json:"fingerprint"`
}

type flexList []string

func (f *flexList) UnmarshalJSON(data []byte) error {
	var many []string
	if err := json.Unmarshal(data, &many); err == nil {
		*f = many
		return nil
	}
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		if one == "" {
			*f = nil
		} else {
			*f = []string{one}
		}
		return nil
	}
	// null 或意外形态:当空处理,不让一个字段坏掉整份报告。
	*f = nil
	return nil
}

func parsePSReport(raw []byte) (psReport, bool) {
	var report psReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return psReport{}, false
	}
	return report, true
}

func defenderServiceState(winDefend string) string {
	switch strings.ToLower(strings.TrimSpace(winDefend)) {
	case "running":
		return DefenderRunning
	case "", "unknown":
		return StateUnknown
	default:
		// Stopped / StopPending / Paused …… 对运营都是"没在防护"。
		return DefenderStopped
	}
}

// evaluateExclusions 判定两条目录排除是否齐全。
//
// psReadable 为真时 Get-MpPreference 是全量权威,可以下 missing 的结论;
// 只有策略键可看时,它只是排除项的一个子集 —— 在里面找到算数,找不到不能
// 证明整体没有,只能 unknown(火绒机就是这个形态:服务死、策略键空,而 8/10
// 经界面加的排除写在读不到的常规位置)。
func evaluateExclusions(psReadable bool, psPaths, policyPaths, wanted []string) string {
	found := 0
	for _, want := range wanted {
		if containsPath(psPaths, want) || containsPath(policyPaths, want) {
			found++
		}
	}
	switch {
	case found == len(wanted) && found > 0:
		return ExclusionsOK
	case found > 0:
		return ExclusionsPartial
	case psReadable:
		return ExclusionsMissing
	default:
		return StateUnknown
	}
}

func containsPath(haystack []string, want string) bool {
	normalized := normalizePath(want)
	for _, candidate := range haystack {
		if normalizePath(candidate) == normalized {
			return true
		}
	}
	return false
}

func normalizePath(p string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(p), `\`))
}

// mergeAvProducts 合并安全中心登记名与服务指纹检出名,去重、去空、保序。
func mergeAvProducts(wsc, fingerprint []string) []string {
	seen := map[string]bool{}
	merged := []string{}
	for _, name := range append(append([]string{}, wsc...), fingerprint...) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		merged = append(merged, name)
	}
	return merged
}
