//go:build windows

package secposture

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/registry"
)

const collectSupported = true

// collectTimeout 罩住单次采集(含拉起 powershell)。超时按 unknown 收场。
const collectTimeout = 60 * time.Second

// psScript 一次拿齐要问 powershell 的四件事,输出单行 JSON。
//
// 全 ASCII:指纹名用英文 token,避免 GBK 控制台的编码歧义;WSC 登记名可能含
// 中文,开头强制 UTF-8 输出兜住。$ErrorActionPreference 兜底 + 每步 try/catch,
// 任何一问失败只塌那一个字段。
const psScript = `$ErrorActionPreference='SilentlyContinue'
try { [Console]::OutputEncoding=[Text.Encoding]::UTF8 } catch {}
$r=@{}
try { $s=Get-Service WinDefend -ErrorAction Stop; $r.winDefend=[string]$s.Status } catch { $r.winDefend='unknown' }
try { $p=Get-MpPreference -ErrorAction Stop; $r.exclusions=@($p.ExclusionPath | Where-Object {$_}); $r.exclusionsReadable=$true } catch { $r.exclusions=@(); $r.exclusionsReadable=$false }
try { $r.wsc=@((Get-CimInstance -Namespace root/SecurityCenter2 -ClassName AntiVirusProduct -ErrorAction Stop).displayName | Where-Object {$_}) } catch { $r.wsc=@() }
$fp=@()
if (Get-Service HipsDaemon -ErrorAction SilentlyContinue) { $fp+='Huorong' }
if (Get-Service ZhuDongFangYu -ErrorAction SilentlyContinue) { $fp+='360SafeGuard' }
if (Get-Process 360sd -ErrorAction SilentlyContinue) { $fp+='360AntiVirus' }
if (Get-Service QQPCRTP -ErrorAction SilentlyContinue) { $fp+='TencentPCMgr' }
if (Get-Service kxescore -ErrorAction SilentlyContinue) { $fp+='Kingsoft' }
if (Get-Service AVP -ErrorAction SilentlyContinue) { $fp+='Kaspersky' }
if (Get-Service ekrn -ErrorAction SilentlyContinue) { $fp+='ESET' }
$r.fingerprint=$fp
$r | ConvertTo-Json -Compress`

func collectOnce(parent context.Context) *Posture {
	ctx, cancel := context.WithTimeout(parent, collectTimeout)
	defer cancel()

	posture := &Posture{
		CollectedAt:        time.Now(),
		MrtPolicy:          probeMrtPolicy(),
		DefenderService:    StateUnknown,
		DefenderExclusions: StateUnknown,
		AvProducts:         []string{},
		Msrt:               probeMrtLog(),
	}

	report, ok := runPSProbe(ctx)
	if ok {
		posture.DefenderService = defenderServiceState(report.WinDefend)
		paths, hidden := sanitizeExclusions(report.Exclusions)
		posture.DefenderExclusions = evaluateExclusions(
			report.ExclusionsReadable,
			hidden,
			paths,
			probePolicyExclusions(),
			wantedExclusionPaths(),
		)
		posture.AvProducts = mergeAvProducts(report.Wsc, report.Fingerprint)
	}
	return posture
}

// probeMrtPolicy 读 KB891716 免疫键。键或值不存在 = absent(没设),
// 其他读取错误 = unknown(读不到不冒充没设)。
func probeMrtPolicy() string {
	key, err := registry.OpenKey(
		registry.LOCAL_MACHINE, `SOFTWARE\Policies\Microsoft\MRT`, registry.QUERY_VALUE)
	if err != nil {
		if os.IsNotExist(err) || err == registry.ErrNotExist {
			return MrtPolicyAbsent
		}
		return StateUnknown
	}
	defer key.Close()
	value, _, err := key.GetIntegerValue("DontOfferThroughWUAU")
	if err != nil {
		if os.IsNotExist(err) || err == registry.ErrNotExist {
			return MrtPolicyAbsent
		}
		return StateUnknown
	}
	if value == 1 {
		return MrtPolicySet
	}
	return MrtPolicyAbsent
}

// probePolicyExclusions 读策略级排除项(值名即路径)。这个键普通权限可读;
// 常规排除键在新系统上非管理员读不到,不碰 —— Get-MpPreference 已经覆盖它。
func probePolicyExclusions() []string {
	key, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Policies\Microsoft\Windows Defender\Exclusions\Paths`,
		registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer key.Close()
	names, err := key.ReadValueNames(0)
	if err != nil {
		return nil
	}
	return names
}

func wantedExclusionPaths() []string {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return nil
	}
	return []string{
		filepath.Join(local, "Programs", "RecruitHelper"),
		filepath.Join(local, "RecruitHelper"),
	}
}

func probeMrtLog() MsrtRun {
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	path := filepath.Join(systemRoot, "debug", "mrt.log")
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return MsrtRun{Status: MsrtNeverRan}
		}
		return MsrtRun{Status: StateUnknown}
	}
	raw, err := readTail(path, info.Size(), 1<<20)
	if err != nil {
		return MsrtRun{Status: StateUnknown}
	}
	return parseMrtLog(raw)
}

// readTail 最多读文件末尾 limit 字节。mrt.log 每月只长几 KB,这里只是给
// 异常膨胀兜底,顺带把截断点之前的半行交给解析器自然忽略。
func readTail(path string, size, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if size > limit {
		if _, err := file.Seek(size-limit, 0); err != nil {
			return nil, err
		}
	}
	raw := make([]byte, 0, min64(size, limit))
	buffer := make([]byte, 64*1024)
	for {
		n, readErr := file.Read(buffer)
		raw = append(raw, buffer[:n]...)
		if readErr != nil {
			break
		}
	}
	return raw, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func runPSProbe(ctx context.Context) (psReport, bool) {
	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", psScript)
	output, err := cmd.Output()
	if err != nil {
		return psReport{}, false
	}
	return parsePSReport(output)
}
