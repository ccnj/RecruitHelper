package m5ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const ProviderConfigFilename = "llm-provider.json"

// ProviderConfig 落盘的只有身份与连接参数。token 预算刻意不在其中:AGENTS.md
// 「输入/输出 token 预算由客户端代码固定」,配置文件里另存一份只会与代码常量
// 漂移——2026-08-01 之前正是这样,Validate 要求两边逐字相等,于是升级客户端改
// 常量就会让老配置校验失败、AI 建议层静默停摆一整个进程周期。现在预算只有
// 代码这一个来源,改常量即刻全局生效,客户机不需要任何配置迁移。
// 请求超时 2026-08-02 起同样只由代码常量固定,理由与预算逐字相同:它此前落盘,
// 于是改默认值对**已经有配置文件的机器毫无作用**——加载时 config = *existing,
// 文件里那个 30000 原样盖回来。客户机正是这种机器。
//
// 老配置文件里残留的 max_*_tokens 与 request_timeout_ms 字段由 json.Unmarshal
// 安静忽略。
type ProviderConfig struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url"`
	APIKey   string `json:"api_key"`
}

// ProviderRequestTimeoutMs 是单次 provider 调用的超时。
//
// 2026-08-02 甲方裁决 30 秒 → 60 秒。30 秒是按"生成一条回复"定的,而全批职位
// 类别分配一次要读十几个职位的完整描述再吐一整张分配表:客户机实测 10 个职位
// 47.9 KB,三次尝试里有一次正好卡在 30000ms 上。输出预算同日抬到 10240 之后
// 模型不再被截断、会写得更完整,耗时只会更长。
//
// 代价:任何一次卡住的调用现在要占 60 秒才放手,巡检里那一轮就多等 30 秒。
// 相对于"本来能成的调用被判超时、整批作废",这个代价是划算的。
const ProviderRequestTimeoutMs = 60000

func DefaultProviderConfig() ProviderConfig {
	return ProviderConfig{Provider: "deepseek", Model: "deepseek-v4-pro"}
}

// Validate 只校验非空与格式合法,不再校验具体厂商与模型名(AGENTS.md
// 2026-07-30 甲方裁决):base_url/model 现在都由旧后台下发,日后换用非 deepseek
// 模型时客户端应当跟着走,不再由本地常量把它钉死。token 预算不再参与校验:
// 它已经不落盘,只有代码常量一个来源,没有可校验的第二方。
func (c ProviderConfig) Validate() error {
	if strings.TrimSpace(c.Provider) == "" || strings.TrimSpace(c.APIKey) == "" ||
		validateModel(c.Model) != nil || validateBaseURL(c.BaseURL) != nil {
		return errors.New("LLM provider 配置不完整")
	}
	return nil
}

// validateModel 要求单个模型 ID:模型链的首项提取在 ExtractProviderCredentials
// 就完成了,落盘的配置里不该再留分隔符或空白。
func validateModel(value string) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 ||
		strings.ContainsAny(value, ",\n\r\t ") {
		return errors.New("model 无效")
	}
	return nil
}

// DeriveProviderLabel 从 base_url 推导 provider 标签。旧后台 job-config 不下发厂商
// 名,而这个标签要进普通日志、AI 调用诊断摘要,以及采集批次的模型一致性校验
// (patrol 侧 provider+model 变化会拒绝复用同批次的评分/招呼语进度),所以它必须
// 随实际端点变化,不能恒定写死 "deepseek"——换了厂商还叫 deepseek 会误导排查。
func DeriveProviderLabel(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return ""
	}
	host = strings.TrimPrefix(host, "api.")
	if index := strings.Index(host, "."); index > 0 {
		host = host[:index]
	}
	return host
}

type ProviderConfigView struct {
	Provider              string `json:"provider"`
	Model                 string `json:"model"`
	BaseURLConfigured     bool   `json:"baseUrlConfigured"`
	KeyConfigured         bool   `json:"keyConfigured"`
	RequestTimeoutMs      int64  `json:"request_timeout_ms"`
	MaxInputTokens        int    `json:"max_input_tokens"`
	MaxIntentOutputTokens int    `json:"max_intent_output_tokens"`
	MaxReplyOutputTokens  int    `json:"max_reply_output_tokens"`
}

// View 里的 token 预算直接来自代码常量:配置已经不存它们,诊断台看到的就是
// 本次进程真正生效的值。
func (c ProviderConfig) View() ProviderConfigView {
	return ProviderConfigView{
		Provider: c.Provider, Model: c.Model, BaseURLConfigured: strings.TrimSpace(c.BaseURL) != "",
		KeyConfigured: strings.TrimSpace(c.APIKey) != "", RequestTimeoutMs: ProviderRequestTimeoutMs,
		MaxInputTokens: ReplyInputTokenLimit, MaxIntentOutputTokens: IntentOutputTokenLimit,
		MaxReplyOutputTokens: ReplyOutputTokenLimit,
	}
}

type ProviderConfigStore struct {
	path string
}

func NewProviderConfigStore(dataDir string) (*ProviderConfigStore, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("provider 配置缺少 data 目录")
	}
	return &ProviderConfigStore{path: filepath.Join(dataDir, ProviderConfigFilename)}, nil
}

func (s *ProviderConfigStore) Load() (*ProviderConfig, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 provider 配置失败")
	}
	var config ProviderConfig
	if json.Unmarshal(raw, &config) != nil || config.Validate() != nil {
		return nil, errors.New("provider 配置文件无效")
	}
	return &config, nil
}

// Save intentionally uses a small direct private-file write. Configuration
// loss is recoverable by a person in the attended development profile, so M5
// does not introduce a second journal/recovery protocol for this one file.
func (s *ProviderConfigStore) Save(config ProviderConfig) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return errors.New("provider 配置目录不可写")
	}
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return errors.New("provider 配置编码失败")
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(s.path, raw, 0o600); err != nil {
		return errors.New("provider 配置写入失败")
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return errors.New("provider 配置权限设置失败")
	}
	return nil
}

// ApplyBackendCredentials 把旧后台下发的 provider 凭据合并进本机配置(AGENTS.md
// 2026-07-30 甲方裁决),返回是否实际写盘。
//
// 合并规则:后台某项非空即覆盖本机同项,后台该项为空则保留本机原值——后台清空
// 一个字段不该把已经能用的本机配置打掉。token 预算永不来自后台。
//
// 写盘本身只管持久化;运行期生效由调用方的 onApplied 回调负责(2026-08-12 甲方
// 裁决"落盘即生效",撤销此前"生效于下次启动"的取舍):patrol.Manager 的引擎经
// SetAdvice 原子换代,在途调用拿旧引擎收尾,混模型批次甲方明示接受。
func (s *ProviderConfigStore) ApplyBackendCredentials(
	credentials BackendProviderCredentials,
) (bool, error) {
	if credentials.empty() {
		return false, nil
	}
	existing, loadErr := s.Load()
	if loadErr != nil {
		// 本机文件损坏或不再合法,不该连带把后台凭据也挡在外面:后台是这三个
		// 字段的单一事实源,按默认预算重建。真正不可写的情况下面 Save 会报。
		existing = nil
	}
	config := DefaultProviderConfig()
	if existing != nil {
		config = *existing
	}
	if credentials.BaseURL != "" {
		config.BaseURL = credentials.BaseURL
	}
	if credentials.APIKey != "" {
		config.APIKey = credentials.APIKey
	}
	if credentials.Model != "" {
		config.Model = credentials.Model
	}
	config.Provider = DeriveProviderLabel(config.BaseURL)
	if existing != nil && config == *existing {
		return false, nil
	}
	if err := s.Save(config); err != nil {
		return false, err
	}
	return true, nil
}
