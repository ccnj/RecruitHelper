package patrol

import "testing"

// 引擎运行期换代(2026-08-12 甲方裁决):SetAdvice 从 nil 首次装配与非空换代
// 都即时对 currentAdvice 可见;构造期注入的初值同样经 currentAdvice 读取。
func TestSetAdviceSwapsEngineForSubsequentSnapshots(t *testing.T) {
	h := newHarness(t)
	if h.manager.currentAdvice() != nil {
		t.Fatal("未注入引擎时快照应为 nil")
	}
	first := &recordingAdviceExecutor{}
	h.manager.SetAdvice(first)
	if h.manager.currentAdvice() != AdviceExecutor(first) {
		t.Fatal("首次装配未生效")
	}
	second := &recordingAdviceExecutor{}
	h.manager.SetAdvice(second)
	if h.manager.currentAdvice() != AdviceExecutor(second) {
		t.Fatal("运行期换代未生效")
	}
}
