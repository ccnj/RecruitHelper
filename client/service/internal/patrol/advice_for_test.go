package patrol

import (
	"context"
	"testing"

	"recruithelper/client/service/internal/m5ai"
)

type namedFakeAdvice struct{ name string }

func (f *namedFakeAdvice) ProviderName() string { return f.name }
func (f *namedFakeAdvice) ModelName() string    { return f.name + "-model" }
func (f *namedFakeAdvice) CompleteJSON(context.Context, m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
	return m5ai.CompletionResponse{}, nil
}

// 路由表是次聪明的封闭用途清单(AGENTS.md「LLM provider 直连」次聪明段):
// 回复族三用途走次聪明,其余一律客户级。评分/招呼/意向/发布被误切会让
// 账本与实际引擎错位,这里逐用途锁死。
func TestAdviceForRoutesReplyFamilyToReplyEngine(t *testing.T) {
	base := &namedFakeAdvice{name: "base"}
	reply := &namedFakeAdvice{name: "reply"}
	m := &Manager{adviceEngine: base, replyAdviceEngine: reply}

	replyFamily := []m5ai.CompletionPurpose{
		m5ai.PurposeReply, m5ai.PurposeServiceReply, m5ai.PurposeSilenceFollowup,
	}
	for _, purpose := range replyFamily {
		if got := m.adviceFor(purpose); got != AdviceExecutor(reply) {
			t.Fatalf("回复族用途 %s 未走次聪明引擎: %v", purpose, got)
		}
	}
	baseFamily := []m5ai.CompletionPurpose{
		m5ai.PurposeIntent, m5ai.PurposeScoring, m5ai.PurposeGreeting,
		m5ai.PurposeJobClass, m5ai.PurposeJobKeywords,
	}
	for _, purpose := range baseFamily {
		if got := m.adviceFor(purpose); got != AdviceExecutor(base) {
			t.Fatalf("非回复族用途 %s 被误切到次聪明: %v", purpose, got)
		}
	}
}

// 次聪明未配置=特性关闭:全部用途(含回复族)回到客户级引擎,行为与
// 本特性引入前逐字相同。
func TestAdviceForWithoutReplyEngineFallsBackToBase(t *testing.T) {
	base := &namedFakeAdvice{name: "base"}
	m := &Manager{adviceEngine: base}
	for _, purpose := range []m5ai.CompletionPurpose{
		m5ai.PurposeReply, m5ai.PurposeServiceReply, m5ai.PurposeSilenceFollowup,
		m5ai.PurposeIntent, m5ai.PurposeScoring,
	} {
		if got := m.adviceFor(purpose); got != AdviceExecutor(base) {
			t.Fatalf("次聪明未配置时用途 %s 未回落客户级: %v", purpose, got)
		}
	}
}

// 客户级未配置而次聪明已配置:回复族仍可用(批B等业务事件轮的意向不经 AI,
// 回复引擎在就该答),非回复族保持 nil 停用。
func TestAdviceForReplyEngineWorksWithoutBase(t *testing.T) {
	reply := &namedFakeAdvice{name: "reply"}
	m := &Manager{replyAdviceEngine: reply}
	if got := m.adviceFor(m5ai.PurposeReply); got != AdviceExecutor(reply) {
		t.Fatalf("客户级缺席不该拖垮次聪明回复引擎: %v", got)
	}
	if got := m.adviceFor(m5ai.PurposeIntent); got != nil {
		t.Fatalf("客户级缺席时意向用途应为未装配: %v", got)
	}
}

// SetReplyAdvice 运行期换代与 SetAdvice 同款语义。
func TestSetReplyAdviceHotSwap(t *testing.T) {
	base := &namedFakeAdvice{name: "base"}
	m := &Manager{adviceEngine: base}
	if got := m.adviceFor(m5ai.PurposeReply); got != AdviceExecutor(base) {
		t.Fatalf("装配前回复族应走客户级: %v", got)
	}
	reply := &namedFakeAdvice{name: "reply"}
	m.SetReplyAdvice(reply)
	if got := m.adviceFor(m5ai.PurposeReply); got != AdviceExecutor(reply) {
		t.Fatalf("装配后回复族未换代: %v", got)
	}
	if got := m.adviceFor(m5ai.PurposeScoring); got != AdviceExecutor(base) {
		t.Fatalf("换代不该波及非回复族: %v", got)
	}
}
