package m5ai

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
)

// BackendProviderCredentials 是旧后台 job-config 响应里的 provider 配置面
// (AGENTS.md 2026-07-30 甲方裁决)。三字段在旧后台都是客户级配置,该客户下
// 所有职位、所有提示词共用同一值,因此这里不按职位或用途区分,取第一个非空值
// 即可。它不进入不可变职位配置版本,也不参与 revisionHash——只是每次同步顺手
// 刷新的本机凭据。
type BackendProviderCredentials struct {
	BaseURL string
	APIKey  string
	// Model 是已经取过首项的单个模型 ID。旧后台那一列是"模型链"自由文本
	// (实测 "deepseek-v4-pro,deepseek-v4-flash"),甲方裁决只取首项、其余
	// 忽略,不实现失败换模型或任何主备切换。
	Model string
}

func (c BackendProviderCredentials) empty() bool {
	return c.BaseURL == "" && c.APIKey == "" && c.Model == ""
}

// credentialBlock 只解析 provider 三字段,刻意不复用 importer 的
// legacyJobBundle:文档集合的严格校验(重复 doc_type、占位符、结构化区一致性)
// 不该阻断凭据提取,两者是各自独立的失败面。
type credentialBlock struct {
	APIKey  *string `json:"apiKey"`
	Model   *string `json:"model"`
	BaseURL *string `json:"baseUrl"`
}

type credentialBundle struct {
	Communication   credentialBlock `json:"communication"`
	Scoring         credentialBlock `json:"scoring"`
	Greeting        credentialBlock `json:"greeting"`
	Intent          credentialBlock `json:"intent"`
	SilenceFollowup credentialBlock `json:"silenceFollowup"`
}

func (b credentialBundle) blocks() []credentialBlock {
	// 固定优先级只为"某个 block 恰好没配值"留退路,不是为了处理多值分歧——
	// 实测同一客户下这些值本来就相同。
	return []credentialBlock{b.Communication, b.Scoring, b.Greeting, b.Intent, b.SilenceFollowup}
}

type credentialPlural struct {
	Jobs []credentialBundle `json:"jobs"`
}

// ExtractProviderCredentials 接受单职位或多职位形态的 job-config 响应,返回其中
// 的 provider 凭据。解析不出结构时返回 error;结构正常但三字段都没值时返回空
// 凭据且 err 为 nil——那是"后台没配"的正常状态,由调用方按兜底路径处理。
func ExtractProviderCredentials(raw []byte) (BackendProviderCredentials, error) {
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(raw, &shape); err != nil {
		return BackendProviderCredentials{}, errors.New("job-config 响应无法解析 provider 凭据")
	}
	var bundles []credentialBundle
	if _, plural := shape["jobs"]; plural {
		var payload credentialPlural
		if err := json.Unmarshal(raw, &payload); err != nil {
			return BackendProviderCredentials{}, errors.New("job-config 多职位响应无法解析 provider 凭据")
		}
		bundles = payload.Jobs
	} else {
		var payload credentialBundle
		if err := json.Unmarshal(raw, &payload); err != nil {
			return BackendProviderCredentials{}, errors.New("job-config 单职位响应无法解析 provider 凭据")
		}
		bundles = []credentialBundle{payload}
	}
	var out BackendProviderCredentials
	for _, bundle := range bundles {
		for _, block := range bundle.blocks() {
			if out.BaseURL == "" {
				out.BaseURL = trimPointer(block.BaseURL)
			}
			if out.APIKey == "" {
				out.APIKey = trimPointer(block.APIKey)
			}
			if out.Model == "" {
				out.Model = firstModelInChain(trimPointer(block.Model))
			}
			if out.BaseURL != "" && out.APIKey != "" && out.Model != "" {
				return out, nil
			}
		}
	}
	return out, nil
}

// RefreshBackendProviderConfig 用一次 job-config 响应刷新本机 provider 配置。
//
// 它刻意不返回错误:provider 凭据与职位配置导入是两条独立的失败面。凭据取不到
// 不该挡住职位配置同步,更不该阻断脑启动或任何业务裁决;取不到就沿用本机既有
// 配置,由既有的"模型连接尚未配齐 + 手工配置入口"兜底。
//
// 日志只出现 model、刷新了哪些字段与错误分类。base_url 原值与 API key 一概不记
// (AGENTS.md「AI provider 数据边界」)。实际生效的 provider/model 由引擎装配方
// (脑启动的"M5 建议层已就绪"或换代回调的"模型引擎已换代生效")负责记录。
//
// onApplied 在配置实际落盘后被调用(2026-08-12 甲方裁决"落盘即生效"):由 main
// 装配为"重建引擎并换代"。传 nil 则退回只落盘的旧行为。
func RefreshBackendProviderConfig(store *ProviderConfigStore, raw []byte, onApplied func()) {
	if store == nil {
		return
	}
	credentials, err := ExtractProviderCredentials(raw)
	if err != nil {
		slog.Warn("旧后台 provider 凭据无法解析，沿用本机模型配置",
			"errorCode", "providerCredentialsUnparsable", "err", err)
		return
	}
	if credentials.empty() {
		return
	}
	applied, err := store.ApplyBackendCredentials(credentials)
	if err != nil {
		slog.Warn("旧后台 provider 凭据未能落盘，沿用本机模型配置",
			"errorCode", "providerCredentialsNotStored", "err", err)
		return
	}
	if applied {
		slog.Info("已按旧后台下发刷新模型配置",
			"model", credentials.Model,
			"baseUrlRefreshed", credentials.BaseURL != "",
			"keyRefreshed", credentials.APIKey != "")
		if onApplied != nil {
			onApplied()
		}
	}
}

// ExtractSmartProviderCredentials 提取发布专用「聪明ai」凭据(AGENTS.md「LLM
// provider 直连」2026-08-24 增补)。它只认响应顶层的 smartAi 块——单职位与多职位
// 形态后台都放在顶层,所以这里不需要区分两种形状。块缺失不是错误:那是"后台
// 尚未配置聪明ai"的正常状态,返回空凭据由调用方按兜底停用路径处理。
func ExtractSmartProviderCredentials(raw []byte) (BackendProviderCredentials, error) {
	var payload struct {
		SmartAi *credentialBlock `json:"smartAi"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return BackendProviderCredentials{}, errors.New("job-config 响应无法解析聪明ai凭据")
	}
	if payload.SmartAi == nil {
		return BackendProviderCredentials{}, nil
	}
	return BackendProviderCredentials{
		BaseURL: trimPointer(payload.SmartAi.BaseURL),
		APIKey:  trimPointer(payload.SmartAi.APIKey),
		Model:   firstModelInChain(trimPointer(payload.SmartAi.Model)),
	}, nil
}

// RefreshSmartProviderConfig 与 RefreshBackendProviderConfig 同构:不返回错误、
// 取不到只记日志沿用本机既有文件、落盘成功才触发 onApplied 换代。差异只有两处:
// 数据源是顶层 smartAi 块,以及日志措辞指明是发布模型——排查"发布为什么还在用
// 旧凭据"时要能与客户级配置的刷新日志一眼区分。
func RefreshSmartProviderConfig(store *ProviderConfigStore, raw []byte, onApplied func()) {
	if store == nil {
		return
	}
	credentials, err := ExtractSmartProviderCredentials(raw)
	if err != nil {
		slog.Warn("旧后台聪明ai凭据无法解析，沿用本机发布模型配置",
			"errorCode", "smartProviderCredentialsUnparsable", "err", err)
		return
	}
	if credentials.empty() {
		return
	}
	applied, err := store.ApplyBackendCredentials(credentials)
	if err != nil {
		slog.Warn("旧后台聪明ai凭据未能落盘，沿用本机发布模型配置",
			"errorCode", "smartProviderCredentialsNotStored", "err", err)
		return
	}
	if applied {
		slog.Info("已按旧后台下发刷新发布模型配置(聪明ai)",
			"model", credentials.Model,
			"baseUrlRefreshed", credentials.BaseURL != "",
			"keyRefreshed", credentials.APIKey != "")
		if onApplied != nil {
			onApplied()
		}
	}
}

func trimPointer(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

// firstModelInChain 取模型链首项。旧后台那一列按逗号分隔,旧客户端另外还容忍
// 换行,这里保持同样的宽容度,但只取首项。
func firstModelInChain(raw string) string {
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	}) {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
