// Package chatreport 实现聊天记录上报（AGENTS.md「全局约定·聊天记录上报」，
// 2026-08-19 甲方裁决）：每日 00:20 检查点把候选人档案行与消息行游标增量上传
// 到旧后台。载荷含候选人姓名与聊天正文，是获准明文；载荷之外的东西——结构化
// 微信号、简历正文、platformUserRef、API key——靠这里的独立结构体显式拼装挡住，
// 键白名单测试是这道闸的看门狗。
package chatreport

import "time"

// SchemaVersion 是载荷结构版本，字段面变化时递增。
const SchemaVersion = 1

// Payload 是一次 POST 的完整载荷。Profiles 与 Messages 允许各自为空：
// 档案批与消息批分开发送。
type Payload struct {
	MachineID     string    `json:"machineId"`
	LicenseToken  string    `json:"licenseToken"`
	AppVersion    string    `json:"appVersion,omitempty"`
	SchemaVersion int       `json:"schemaVersion"`
	ReportedAt    time.Time `json:"reportedAt"`

	Profiles []ProfileRow `json:"profiles"`
	Messages []MessageRow `json:"messages"`
}

// ProfileRow 是候选人档案行。JobName 是职位维度的分组键（职位以名字区分、
// 与平台强绑定，2026-08-19 甲方裁决），BackendJobID 仅作辅助定位列。
type ProfileRow struct {
	ProfileID       string  `json:"profileId"`
	Platform        string  `json:"platform"`
	AccountRef      string  `json:"accountRef"`
	ConversationRef *string `json:"conversationRef,omitempty"`
	DisplayName     *string `json:"displayName,omitempty"`
	BackendJobID    *string `json:"backendJobId,omitempty"`
	JobName         *string `json:"jobName,omitempty"`
	MainStatus      string  `json:"mainStatus"`
	EndReason       *string `json:"endReason,omitempty"`

	// 四个业务毫秒时刻：招呼、进入沟通（候选人首条真实回复）、约面成功（邀面
	// 卡被接受，不是面试进行时刻）、换微信（权威资产收编时刻，号码不上传）。
	GreetedAtMs       *int64 `json:"greetedAtMs,omitempty"`
	CommunicatingAtMs *int64 `json:"communicatingAtMs,omitempty"`
	InterviewedAtMs   *int64 `json:"interviewedAtMs,omitempty"`
	WechatAtMs        *int64 `json:"wechatAtMs,omitempty"`

	UpcomingInterviewStartsAtMs *int64  `json:"upcomingInterviewStartsAtMs,omitempty"`
	UpcomingInterviewEndsAtMs   *int64  `json:"upcomingInterviewEndsAtMs,omitempty"`
	UpcomingInterviewMethod     *string `json:"upcomingInterviewMethod,omitempty"`
}

// MessageRow 是消息行。Provenance 是脑上传时现算的业务出身（封闭枚举，见
// provenance.go），入站行为空。
type MessageRow struct {
	Platform        string  `json:"platform"`
	AccountRef      string  `json:"accountRef"`
	ConversationRef string  `json:"conversationRef"`
	Seq             int64   `json:"seq"`
	ProfileID       *string `json:"profileId,omitempty"`

	Direction string  `json:"direction"`
	Kind      string  `json:"kind"`
	Text      *string `json:"text,omitempty"`
	CardType  string  `json:"cardType,omitempty"`
	CardState string  `json:"cardState,omitempty"`

	InterviewStartsAtMs *int64  `json:"interviewStartsAtMs,omitempty"`
	InterviewEndsAtMs   *int64  `json:"interviewEndsAtMs,omitempty"`
	InterviewMethod     *string `json:"interviewMethod,omitempty"`
	TsApproxMs          *int64  `json:"tsApproxMs,omitempty"`

	Provenance string `json:"provenance,omitempty"`

	Retracted        bool   `json:"retracted,omitempty"`
	RetractionReason string `json:"retractionReason,omitempty"`
}
