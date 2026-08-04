// Package m5ai contains the platform-independent M5 AI seam. It owns prompt
// rendering and provider wire semantics, but never decides or performs a
// browser side effect.
package m5ai

import (
	"context"
	"time"
)

const (
	MappingVersion                    = "m5-communication-v1"
	InboundConversationNoGreetingText = "（候选人主动发起会话，我方尚未发送招呼语）"
	HistoryFormatVersion              = 1
	ScheduleFormatVersion             = 1
	ProviderAssemblyVersion           = 1
	DialogueRenderFormatVersion       = "m5-history1-schedule1-assembly1"
	SilenceFollowupRenderVersion      = "m5-silence-followup-v1"
	SendTextMaxUTF8Bytes              = 2048
	ReplyPhraseMaxItems               = 5
	HistoryLimit                      = 20
	IntentInputTokenLimit             = 8000
	// 2026-08-01 甲方裁决:输入 16000 → 32000。实测单次回复输入最大 10943
	// (戴相成客户机 68 次调用),已经吃掉 68%,而对话历史随轮次单调增长,聊到
	// 十轮必然撞线。这两个上限都是"超了就拒绝"的闸而非预付额度,调大本身不
	// 花钱,只在真用到时才计费。
	ReplyInputTokenLimit           = 32000
	SilenceFollowupInputTokenLimit = ReplyInputTokenLimit
	GreetingInputTokenLimit        = ReplyInputTokenLimit
	// 2026-08-01 甲方裁决:所有输出预算统一 10240。原值(intent 64、reply 512、
	// 补句 128、评分 512、职位类别/关键词 256)按"实测用量 + 一点余量"设定,
	// 但 reply 那档 512 已经装不下业务自己允许的最长话术——规格上限 2048 UTF-8
	// bytes 约 680 汉字,加 JSON 结构后很可能越过 512 token,那类合法话术会被
	// 截断成非法 JSON、整轮作废。统一给足余量,也为日后开思考模式留出空间
	// (实测 512 会被 reasoning 全部吃光,content 直接返回空字符串)。
	// 话痨风险由业务侧既有校验兜住:话术 1~5 项、每项与整组均不得超 2048 bytes。
	IntentOutputTokenLimit          = 10240
	ReplyOutputTokenLimit           = 10240
	ServiceReplyOutputTokenLimit    = 10240
	SilenceFollowupOutputTokenLimit = ReplyOutputTokenLimit
	ScoringOutputTokenLimit         = 10240
	GreetingOutputTokenLimit        = ReplyOutputTokenLimit
	// 职位类别全批分配一次要带上全部职位的完整描述,远超按单次回复设的
	// ReplyInputTokenLimit。甲方 2026-08-01 裁决按用途放宽到 64000:按中文约
	// 0.6 token/字估,12 个职位约 1.4 万、20 个约 2.2 万、40 个约 4.4 万 token;
	// 再往上会先撞 maxProviderRequestBytes(256 KB,约 45 个职位)那道硬闸,
	// 那道不动。成本上按冻结价 cache-miss $0.435/M token,一次 2.2 万 token
	// 约 $0.01,一批发布只调一次。
	//
	// 为什么给完整描述而不是摘要:类别选错会把职位长期推给错误的人群,代价
	// 远高于这点 token;描述又正是平台自己判定候选的输入,截断会让模型看到的
	// 和平台看到的不是同一份。
	JobClassInputTokenLimit = 64000
	// 职位类别与关键词的实际输出仍是类别名、置信度和一句理由的量级,但按
	// 2026-08-01 甲方裁决与其余输出预算统一到 10240,不再各档单独掐。上限只
	// 在模型真吐这么多时才计费,写废话的风险由输出契约校验兜住。
	JobClassOutputTokenLimit    = 10240
	JobKeywordsOutputTokenLimit = 10240
)

type JobConfigDocument struct {
	DocType string `json:"docType"`
	Content string `json:"content"`
}

type JobConfigDocumentPackage struct {
	Documents []JobConfigDocument `json:"documents"`
}

type CommunicationView struct {
	ReplyPrompt    string `json:"replyPrompt"`
	IntentPrompt   string `json:"intentPrompt"`
	CustomerFacts  string `json:"customerFacts"`
	MappingVersion string `json:"mappingVersion"`
}

// ContextRevision is the repository-owned, immutable representation of one
// imported legacy job configuration revision. Provider credentials are
// deliberately absent.
type ContextRevision struct {
	ContextID     string                   `json:"contextId"`
	RevisionHash  string                   `json:"revisionHash"`
	SourceKind    string                   `json:"sourceKind"`
	SourceJobRef  string                   `json:"sourceJobRef"`
	DisplayName   string                   `json:"displayName"`
	Environment   string                   `json:"environment"`
	SourcePackage JobConfigDocumentPackage `json:"sourcePackage"`
	Communication CommunicationView        `json:"communication"`
	CreatedAt     time.Time                `json:"createdAt"`
}

type AdviceMessage struct {
	Seq       int64  `json:"seq"`
	Direction string `json:"direction"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	Retracted bool   `json:"retracted,omitempty"`
}

type IntentLabel string

const (
	IntentInterested IntentLabel = "interested"
	IntentRejected   IntentLabel = "rejected"
	IntentNeutral    IntentLabel = "neutral"
)

type IntentSuggestion struct {
	Label IntentLabel
}

type ReplyAction string

const (
	ReplyActionNone                 ReplyAction = ""
	ReplyActionStartOnlineMeeting   ReplyAction = "startOnlineMeeting"
	ReplyActionStartOnsiteInterview ReplyAction = "startOnsiteInterview"
	ReplyActionInviteWechat         ReplyAction = "inviteWechat"
)

// IsInterviewInvite 把两种邀面动作收成一个谓词。它们共用同一道业务前置、
// 同一份冻结推荐时段和同一个 MeetingTime 承载位，只在派生的 method 与
// endsAt 上分叉。
func (action ReplyAction) IsInterviewInvite() bool {
	return action == ReplyActionStartOnlineMeeting ||
		action == ReplyActionStartOnsiteInterview
}

type ReplySuggestion struct {
	// Phrases preserves the provider's 话术_序列 item boundaries. Text remains
	// the canonical newline-joined compatibility summary; dispatch planners
	// must use Phrases instead of splitting Text.
	Phrases []string
	Text    string
	Action  ReplyAction
	// MeetingTime 承载"本次动作绑定的那个时间字段"的原文,两种邀面动作共用
	// 这一个位置:`发起线上会议` 取自输出里的 `会议时间`,`发起线下面试` 取自
	// `面试时间`。非邀面动作恒为空——不是"模型没填",是解析器按 2026-08-04
	// 甲方裁决把与本次动作无关的时间字段整个忽略掉了。
	MeetingTime string
}

// ReplyMenuWechatLine 只服务【本轮可选动作】块的措辞。同样是"本轮不能再
// 邀请",已经发出邀请与已经换到号该说的话不一样;说错就是拿假事实喂模型。
type ReplyMenuWechatLine string

const (
	ReplyMenuWechatNotInvited ReplyMenuWechatLine = "notInvited"
	ReplyMenuWechatInvited    ReplyMenuWechatLine = "invited"
	ReplyMenuWechatExchanged  ReplyMenuWechatLine = "exchanged"
)

// ReplyActionMenu 是脑在调用 provider 之前算出的、本轮 `动作` 字段的合法
// 取值范围,由 communication 侧按已冻结状态与本轮事实计算。
//
// 它必须与事后业务前置裁决共用同一份判据(沟通逻辑规格 v4 §五「客户端渲染
// 期追加块」的同源要求):提示词侧另抄一份规则省事,但两边总有一天走岔,
// 那时症状是"模型照块填了、脑还是拒",极难查。
//
// 它不构成授权:模型照它给的建议仍要过完整的业务前置裁决。
type ReplyActionMenu struct {
	AllowStartMeeting bool
	AllowInviteWechat bool
	WechatLine        ReplyMenuWechatLine
}

type ScoringSuggestion struct {
	Score int
}

type GreetingSuggestion struct {
	Text string
}

type SilenceFollowupSuggestion struct {
	Text string
}

// ServiceReplySuggestion is the two-way verdict of the post-interview service
// turn: a single guidance sentence, or the explicit silence verdict (empty
// Reply). It deliberately has no action vocabulary.
type ServiceReplySuggestion struct {
	Reply string
}

type CompletionPurpose string

const (
	PurposeIntent          CompletionPurpose = "intent"
	PurposeReply           CompletionPurpose = "reply"
	PurposeServiceReply    CompletionPurpose = "serviceReply"
	PurposeSilenceFollowup CompletionPurpose = "silenceFollowup"
	PurposeScoring         CompletionPurpose = "scoring"
	PurposeGreeting        CompletionPurpose = "greeting"
	PurposeJobClass        CompletionPurpose = "jobClass"
	PurposeJobKeywords     CompletionPurpose = "jobKeywords"
)

type CompletionRequest struct {
	InvocationID        string
	Purpose             CompletionPurpose
	ContextRevisionHash string
	PromptRevision      string
	UserContent         string
	MaxOutputTokens     int
}

type CompletionUsage struct {
	InputTokens       int
	CachedInputTokens int
	OutputTokens      int
	ReasoningTokens   *int
}

type CompletionResponse struct {
	JSONText              string
	Usage                 CompletionUsage
	ReasoningContentEmpty bool
	Diagnostics           CompletionDiagnostics
}

type TraceStatus string

const (
	TraceStatusComplete            TraceStatus = "complete"
	TraceStatusUnavailable         TraceStatus = "unavailable"
	TraceStatusResponseUnavailable TraceStatus = "responseUnavailable"
)

const (
	FailureStageRequestBuild   = "requestBuild"
	FailureStageTransport      = "transport"
	FailureStageProviderHTTP   = "providerHTTP"
	FailureStageResponseDecode = "responseDecode"
	FailureStageBusinessParse  = "businessParse"
	FailureStageReducer        = "reducer"
	FailureStagePersistence    = "persistence"
)

// CompletionDiagnostics is the PII-free bridge from the provider adapter to
// brain.db and stdout. Raw request/response bodies remain exclusively in the
// standalone ai-traces.db.
type CompletionDiagnostics struct {
	ProviderHTTPStatus *int
	RequestBytes       int
	ResponseBytes      int
	TraceStatus        TraceStatus
	TraceErrorCode     string
}

type LLMProvider interface {
	CompleteJSON(ctx context.Context, request CompletionRequest) (CompletionResponse, error)
}
