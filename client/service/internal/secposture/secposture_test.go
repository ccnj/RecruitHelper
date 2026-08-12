package secposture

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf16"
)

// 样本取自 2026-08-12 杨小七01 客户机 mrt.log 原文(路径已泛化):
// 08:12 的 WU 触发运行检出并删除了脑 exe;09:55 的 /Q /N 仅检测运行检出未删。
const mrtLogKilled = `---------------------------------------------------------------------------------------
reboot-mode: 0, quiet-mode: 1, detect-only-mode: 0, full-scan-mode: 0, wu-mode: 1
Microsoft Windows Malicious Software Removal Tool v5.144, (build 5.144.26080.1002)
Started On Wed Aug 12 08:12:08 2026

Engine: 1.1.26060.3008
Signatures: 1.455.230.0

Quick Scan Results:
-------------------
Threat Detected: Trojan:Win64/DCRat.CR!MTB and Removed!
  Action: Remove, Result: 0x00000000
    process://pid:12708,ProcessStart:134309266990404241
    file://C:\Users\1\AppData\Local\Programs\RecruitHelper\resources\brain\RecruitHelperBrain.exe
        SigSeq: 0x00002178F6DB5B40

Results Summary:
----------------
Found Trojan:Win64/DCRat.CR!MTB and Removed!
Microsoft Windows Malicious Software Removal Tool Finished On Wed Aug 12 08:14:20 2026

Return code: 6 (0x6)
`

const mrtLogDetectOnly = mrtLogKilled + `
---------------------------------------------------------------------------------------
reboot-mode: 0, quiet-mode: 1, detect-only-mode: 1, full-scan-mode: 0, wu-mode: 0
Microsoft Windows Malicious Software Removal Tool v5.144, (build 5.144.26080.1002)
Started On Wed Aug 12 09:55:54 2026

Quick Scan Results:
-------------------
Threat Detected: Trojan:Win64/DCRat.CR!MTB, not removed.
  Action: NoAction, Result: 0x00000000
    file://C:\Users\1\AppData\Local\Programs\RecruitHelper\resources\brain\RecruitHelperBrain.exe

Results Summary:
----------------
Found Trojan:Win64/DCRat.CR!MTB, not removed.
Microsoft Windows Malicious Software Removal Tool Finished On Wed Aug 12 09:57:50 2026

Return code: 7 (0x7)
`

// 干净样本:另一台客户机的形态,6/7 月各跑一次、无检出。
const mrtLogClean = `Microsoft Windows Malicious Software Removal Tool v5.143, (build 5.143.26070.2001)
Started On Mon Jul 27 14:19:54 2026

Results Summary:
----------------
No infection found.
Microsoft Windows Malicious Software Removal Tool Finished On Mon Jul 27 14:20:49 2026

Return code: 0 (0x0)
`

func TestParseMrtLogKilledRun(t *testing.T) {
	run := parseMrtLog([]byte(mrtLogKilled))
	if run.Status != MsrtScanned {
		t.Fatalf("status = %q", run.Status)
	}
	if run.LastRunAt != "Wed Aug 12 08:12:08 2026" {
		t.Fatalf("lastRunAt = %q", run.LastRunAt)
	}
	if run.Version != "v5.144" {
		t.Fatalf("version = %q", run.Version)
	}
	if !run.DetectedUs || !run.RemovedUs {
		t.Fatalf("detectedUs=%v removedUs=%v,期望都为真", run.DetectedUs, run.RemovedUs)
	}
}

// 仅检测模式:检出但未删除。只看最后一段 —— 前一段的 "and Removed!" 不得串段。
func TestParseMrtLogDetectOnlyRunDoesNotInheritRemoval(t *testing.T) {
	run := parseMrtLog([]byte(mrtLogDetectOnly))
	if run.LastRunAt != "Wed Aug 12 09:55:54 2026" {
		t.Fatalf("lastRunAt = %q,应取最后一段", run.LastRunAt)
	}
	if !run.DetectedUs {
		t.Fatal("最后一段有检出,DetectedUs 应为真")
	}
	if run.RemovedUs {
		t.Fatal("仅检测运行没有删除,RemovedUs 不得从上一段继承")
	}
}

func TestParseMrtLogCleanRun(t *testing.T) {
	run := parseMrtLog([]byte(mrtLogClean))
	if run.Status != MsrtScanned || run.DetectedUs || run.RemovedUs {
		t.Fatalf("干净样本解析异常: %+v", run)
	}
	if run.Version != "v5.143" {
		t.Fatalf("version = %q", run.Version)
	}
}

func TestParseMrtLogGarbageIsUnknown(t *testing.T) {
	if run := parseMrtLog([]byte("random noise")); run.Status != StateUnknown {
		t.Fatalf("无法解析应为 unknown,得到 %+v", run)
	}
}

// Windows 日志常见 UTF-16LE 带 BOM 落盘,解析结果必须与平文一致。
func TestParseMrtLogUTF16(t *testing.T) {
	units := utf16.Encode([]rune(mrtLogKilled))
	raw := []byte{0xFF, 0xFE}
	for _, unit := range units {
		raw = append(raw, byte(unit), byte(unit>>8))
	}
	run := parseMrtLog(raw)
	if run.LastRunAt != "Wed Aug 12 08:12:08 2026" || !run.RemovedUs {
		t.Fatalf("UTF-16 解析结果异常: %+v", run)
	}
}

// PowerShell 5.1 会把单元素数组塌成裸字符串,flexList 必须两种都吃。
func TestParsePSReportSingleElementCollapse(t *testing.T) {
	raw := []byte(`{"winDefend":"Running","exclusions":"C:\\Users\\1\\AppData\\Local\\RecruitHelper","exclusionsReadable":true,"wsc":"Windows Defender","fingerprint":["Huorong"]}`)
	report, ok := parsePSReport(raw)
	if !ok {
		t.Fatal("解析失败")
	}
	if len(report.Exclusions) != 1 || len(report.Wsc) != 1 || len(report.Fingerprint) != 1 {
		t.Fatalf("塌缩数组未展开: %+v", report)
	}
}

func TestParsePSReportGarbage(t *testing.T) {
	if _, ok := parsePSReport([]byte("not json")); ok {
		t.Fatal("垃圾输入不应解析成功")
	}
}

// 三台真值机(2026-08-12 人工核对)构成的判定矩阵,前台验收按同一口径对照。
// 注意"杨小七01(提权视角两条都在)"只在提权进程里成立;脑是普通权限进程,
// 真机上它拿到的是哨兵文本 —— 见下一条测试。
func TestEvaluateExclusionsGroundTruth(t *testing.T) {
	wanted := []string{
		`C:\Users\1\AppData\Local\Programs\RecruitHelper`,
		`C:\Users\1\AppData\Local\RecruitHelper`,
	}
	cases := []struct {
		name       string
		psReadable bool
		psHidden   bool
		psPaths    []string
		policy     []string
		want       string
	}{
		{"杨小七01(提权视角):两条都在", true, false, wanted, nil, ExclusionsOK},
		{"只加了一条", true, false, wanted[:1], nil, ExclusionsPartial},
		{"可读且确认没有", true, false, nil, nil, ExclusionsMissing},
		{"非提权:被隐藏不得判缺失", true, true, nil, nil, ExclusionsHidden},
		{"被隐藏但策略键预埋齐全", true, true, nil, wanted, ExclusionsOK},
		{"火绒机:服务死且无策略键", false, false, nil, nil, StateUnknown},
		{"火绒机+预埋策略键", false, false, nil, wanted, ExclusionsOK},
		{"大小写与尾反斜杠差异", true, false,
			[]string{`c:\users\1\appdata\local\programs\recruithelper\`,
				`C:\USERS\1\AppData\Local\RecruitHelper`}, nil, ExclusionsOK},
	}
	for _, c := range cases {
		got := evaluateExclusions(c.psReadable, c.psHidden, c.psPaths, c.policy, wanted)
		if got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// 3.0.1 首上真机的事故复盘用例:杨小七01 排除项明明齐全,脑(非提权)拿到的
// 却是哨兵文本,当时被当成"可读且没找到"误判成 missing 亮了红灯。
func TestSanitizeExclusionsRecognizesAdminOnlySentinel(t *testing.T) {
	paths, hidden := sanitizeExclusions([]string{
		"N/A: Must be an administrator to view exclusions",
	})
	if !hidden {
		t.Fatal("哨兵文本必须识别为 hidden")
	}
	if len(paths) != 0 {
		t.Fatalf("哨兵文本不得混入路径: %v", paths)
	}

	paths, hidden = sanitizeExclusions([]string{
		`C:\Users\1\AppData\Local\RecruitHelper`, " ",
	})
	if hidden || len(paths) != 1 {
		t.Fatalf("真路径不受影响: hidden=%v paths=%v", hidden, paths)
	}
}

func TestDefenderServiceState(t *testing.T) {
	for input, want := range map[string]string{
		"Running": DefenderRunning, "Stopped": DefenderStopped,
		"StopPending": DefenderStopped, "unknown": StateUnknown, "": StateUnknown,
	} {
		if got := defenderServiceState(input); got != want {
			t.Fatalf("%q: got %q want %q", input, got, want)
		}
	}
}

func TestMergeAvProductsDedupes(t *testing.T) {
	merged := mergeAvProducts(
		[]string{"Windows Defender", " ", "Windows Defender"},
		[]string{"Huorong", "Windows Defender"})
	if len(merged) != 2 || merged[0] != "Windows Defender" || merged[1] != "Huorong" {
		t.Fatalf("合并结果异常: %v", merged)
	}
}

// 载荷侧契约:security 块序列化后的键必须落在工作状态上报白名单里,
// 且构造上不可能承载候选人信息 —— 这里锚定键集合,statusreport 的白名单
// 测试对整份载荷再验一次。
func TestPostureJSONKeys(t *testing.T) {
	posture := Posture{
		MrtPolicy: MrtPolicySet, DefenderService: DefenderRunning,
		DefenderExclusions: ExclusionsOK, AvProducts: []string{"Windows Defender"},
		Msrt: MsrtRun{Status: MsrtScanned, LastRunAt: "x", Version: "v5.144",
			DetectedUs: true, RemovedUs: true},
	}
	encoded, err := json.Marshal(posture)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"collectedAt"`, `"mrtPolicy"`, `"defenderService"`, `"defenderExclusions"`,
		`"avProducts"`, `"msrt"`, `"status"`, `"lastRunAt"`, `"version"`,
		`"detectedUs"`, `"removedUs"`,
	} {
		if !strings.Contains(string(encoded), key) {
			t.Fatalf("序列化缺少键 %s: %s", key, encoded)
		}
	}
}

// 非 Windows(本仓库所有开发与 CI 环境)上 Run 必须立即返回、缓存恒空 ——
// mac 启动闪退这个失效方向在构造上不存在。
func TestCollectorNoopOffWindows(t *testing.T) {
	if collectSupported {
		t.Skip("仅在非 Windows 上有意义")
	}
	collector := NewCollector()
	done := make(chan struct{})
	go func() { collector.Run(context.Background()); close(done) }()
	<-done
	if collector.Cached() != nil {
		t.Fatal("非 Windows 上不应有采集结果")
	}
}
