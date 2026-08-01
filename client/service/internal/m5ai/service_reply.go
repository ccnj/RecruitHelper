package m5ai

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// 服务补句是已约面轮唯一的 AI 出口(规格 v4 §七,2026-07-31 修订):提示词
// 固定在客户端代码内,不经旧后台职位配置下发,也不携带简历、攻略与可约
// 时段——已约面轮的唯一任务是"判断要不要回,要回就一句话引导微信",输出
// 没有任何动作枚举。
const serviceReplyPromptHeader = `你是招聘方,候选人已经接受了面试邀请,进入服务阶段。`

const serviceReplyPromptRules = `你只做一件事:判断候选人这轮消息是否需要回应,按格式输出 JSON。
- 需要回应(问题、请求、顾虑、改期、取消等):写一句简短话术,把话题引导到微信继续聊,例如「关于您提到的时间安排,咱们微信上细聊吧～」。只写一句,不罗列,不解释。
- 无需回应(「嗯嗯」「好的」「收到」「到时见」这类纯确认、客套):输出空字符串。
硬规则:不代表我方做任何承诺(不得出现"帮您反馈""我去问下"这类话);不提出、确认或更改任何面试时间;不发起任何操作;只输出 JSON,不要任何解释。
【输出】{"回复": "一句话,或空字符串"}`

// RenderServiceReplyPrompt binds the only two approved inputs: the fixed
// bubbles this turn already sent (so the model knows what was just said and
// that a wechat invite card may already be in front of the candidate) and the
// candidate's own texts. Anything else — resume, playbook, slots — stays out.
func RenderServiceReplyPrompt(
	fixedBubbles []string,
	wechatInviteSent bool,
	candidateTexts []string,
) (string, error) {
	trimmedCandidate := make([]string, 0, len(candidateTexts))
	for _, text := range candidateTexts {
		if value := strings.TrimSpace(text); value != "" {
			trimmedCandidate = append(trimmedCandidate, value)
		}
	}
	if len(trimmedCandidate) == 0 {
		return "", errors.New("missingServiceReplyCandidateText")
	}
	var builder strings.Builder
	builder.WriteString(serviceReplyPromptHeader)
	builder.WriteString("\n")
	sentAny := false
	for _, bubble := range fixedBubbles {
		if value := strings.TrimSpace(bubble); value != "" {
			if !sentAny {
				builder.WriteString("系统刚刚已代表你发出:\n")
				sentAny = true
			}
			builder.WriteString("我(消息):")
			builder.WriteString(value)
			builder.WriteString("\n")
		}
	}
	if wechatInviteSent {
		if !sentAny {
			builder.WriteString("系统刚刚已代表你发出:\n")
			sentAny = true
		}
		builder.WriteString("我(卡片):已发起微信交换邀请\n")
	}
	builder.WriteString("候选人本轮消息:\n")
	for _, text := range trimmedCandidate {
		builder.WriteString("候选人(消息):")
		builder.WriteString(text)
		builder.WriteString("\n")
	}
	builder.WriteString(serviceReplyPromptRules)
	return builder.String(), nil
}

// ParseServiceReplySuggestion accepts exactly one key. An empty string is the
// explicit silence verdict — it is a valid terminal, not a parse failure.
func ParseServiceReplySuggestion(raw string) (ServiceReplySuggestion, error) {
	if !utf8.ValidString(raw) {
		return ServiceReplySuggestion{}, errors.New("invalidJSON")
	}
	object, err := decodeUniqueObject(raw)
	if err != nil {
		return ServiceReplySuggestion{}, err
	}
	for key := range object {
		if key != "回复" {
			return ServiceReplySuggestion{}, errors.New("unknownOutputKey")
		}
	}
	textRaw, exists := object["回复"]
	if !exists {
		return ServiceReplySuggestion{}, errors.New("missingServiceReplyText")
	}
	var text string
	if err := json.Unmarshal(textRaw, &text); err != nil {
		return ServiceReplySuggestion{}, errors.New("invalidServiceReplyText")
	}
	text = norm.NFC.String(strings.TrimSpace(text))
	if text == "" {
		return ServiceReplySuggestion{Reply: ""}, nil
	}
	if err := ValidateSendText(text); err != nil {
		return ServiceReplySuggestion{}, err
	}
	return ServiceReplySuggestion{Reply: text}, nil
}
