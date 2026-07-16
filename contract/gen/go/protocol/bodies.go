// 手写(非生成):各 kind 的 body 结构。
//
// 契约当前只生成"线上字段名清单"(KindBodyFields),body 结构两端手写。
// 一致性由 bodies_test.go 保证:每个结构体的 json tag 必须 ⊆ KindBodyFields[kind],
// 从构造上封杀别名漂移(旧系统 26% 协议事故的根)。
//
// 批次:M1 字段现在就位;标 [S]/[X] 的字段随批次追加,当前用 omitempty 占位或暂缺。
package protocol

import "encoding/json"

// AppInfo:hello.app 版本诊断。
type AppInfo struct {
	ExtVersion string `json:"extVersion"`
	Browser    string `json:"browser"`
}

// HelloBody(手→脑)。HandID/Auth 为首次配对时 nil。
type HelloBody struct {
	HandID         *string  `json:"handId"`
	Auth           *string  `json:"auth"`
	BootID         string   `json:"bootId"`
	ProtoSupported []int    `json:"protoSupported"`
	ContractHash   string   `json:"contractHash"`
	App            AppInfo  `json:"app"`
	Caps           []string `json:"caps"`
	Features       []string `json:"features"`
}

// IssuedCreds:welcome.issued,仅配对签发时出现。
type IssuedCreds struct {
	HandID string `json:"handId"`
	Auth   string `json:"auth"`
}

// HbParams:welcome.hb 心跳参数。
type HbParams struct {
	IntervalMs int64 `json:"intervalMs"`
	GraceMs    int64 `json:"graceMs"`
}

// Limits:welcome.limits 大小纪律。
type Limits struct {
	MaxMsgBytes int64 `json:"maxMsgBytes"`
	InlineBytes int64 `json:"inlineBytes"`
}

// WelcomeBody(脑→手)。sensors [S] / blob [X] 随批次追加。
type WelcomeBody struct {
	Session       string       `json:"session"`
	Proto         int          `json:"proto"`
	Hb            HbParams     `json:"hb"`
	Limits        Limits       `json:"limits"`
	Issued        *IssuedCreds `json:"issued,omitempty"`
	ContractMatch bool         `json:"contractMatch"`
	Now           int64        `json:"now"`
}

// ByeBody(脑→手):拒绝握手/宣告关闭/顶替。
type ByeBody struct {
	Code    ByeCode `json:"code"`
	Message string  `json:"message,omitempty"`
}

// PingContext:ping.contexts 各账号就绪度 [S]。
type PingContext struct {
	Platform   string `json:"platform"`
	AccountRef string `json:"accountRef"`
	Ready      bool   `json:"ready"`
	Reason     string `json:"reason,omitempty"`
}

// PingBody(手→脑)。pre-session 形态只带 QueueDepth/InFlight;contexts/sensors [S]。
type PingBody struct {
	QueueDepth int             `json:"queueDepth"`
	InFlight   *string         `json:"inFlight"`
	Contexts   []PingContext   `json:"contexts,omitempty"`
	Sensors    json.RawMessage `json:"sensors,omitempty"`
}

// PongBody(脑→手)。
type PongBody struct {
	Now int64 `json:"now"`
}

// Encode:body → json.RawMessage,供装入信封。
func Encode(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	return json.RawMessage(b), err
}
