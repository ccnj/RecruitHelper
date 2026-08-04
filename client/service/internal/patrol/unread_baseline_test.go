package patrol

import (
	"testing"

	"recruithelper/client/service/internal/store"
)

func TestUnreadPassEndBaselineDecision(t *testing.T) {
	h := newHarness(t)
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取账号失败: account=%+v err=%v", account, err)
	}

	h.manager.mu.Lock()
	defer h.manager.mu.Unlock()

	if !h.manager.unreadPassNeeded(account, ptr(3)) {
		t.Fatal("无结束基线时首个正数没有获得未读插队机会")
	}
	if !h.manager.recordUnreadPassEnd(account, ptr(3)) {
		t.Fatal("完整未读子轮结束数没有写入基线")
	}
	if h.manager.unreadPassNeeded(account, ptr(3)) {
		t.Fatal("与结束基线相同的残留气泡仍触发插队")
	}
	if !h.manager.unreadPassNeeded(account, ptr(2)) ||
		!h.manager.unreadPassNeeded(account, ptr(4)) {
		t.Fatal("相对结束基线增加或减少都应重新获得插队机会")
	}

	if h.manager.recordUnreadPassEnd(account, nil) {
		t.Fatal("缺失结束读数不应伪造完成基线")
	}
	if h.manager.unreadPassNeeded(account, ptr(3)) {
		t.Fatal("缺失结束读数错误改写了既有基线")
	}
	if h.manager.recordUnreadPassEnd(account, ptr(-1)) {
		t.Fatal("非法负数不应写入完成基线")
	}
	if h.manager.unreadPassNeeded(account, ptr(3)) {
		t.Fatal("非法负数错误改写了既有基线")
	}
	if h.manager.unreadPassNeeded(account, nil) {
		t.Fatal("缺失当前读数不应授权插队")
	}

	if h.manager.unreadPassNeeded(account, ptr(0)) {
		t.Fatal("稳定零值不应授权插队")
	}
	if !h.manager.unreadPassNeeded(account, ptr(3)) {
		t.Fatal("稳定零值清除基线后，后续正数没有重新获得插队机会")
	}
}

func TestUnreadPassEndBaselineIsScopedByPrincipal(t *testing.T) {
	h := newHarness(t)
	original, err := h.db.AccountByKey(h.key)
	if err != nil || original == nil {
		t.Fatalf("读取账号失败: account=%+v err=%v", original, err)
	}

	h.manager.mu.Lock()
	if !h.manager.recordUnreadPassEnd(original, ptr(5)) {
		h.manager.mu.Unlock()
		t.Fatal("旧主体未读结束基线写入失败")
	}
	h.manager.mu.Unlock()

	if err := h.db.BindAccountPrincipal(
		h.key, "hand-1", "principal-2", "session-1", "boot-1", h.clock.Now(),
	); err != nil {
		t.Fatalf("改绑新主体失败: %v", err)
	}
	rebound, err := h.db.AccountByKey(h.key)
	if err != nil || rebound == nil {
		t.Fatalf("读取改绑账号失败: account=%+v err=%v", rebound, err)
	}

	h.manager.mu.Lock()
	defer h.manager.mu.Unlock()
	if !h.manager.unreadPassNeeded(rebound, ptr(5)) {
		t.Fatal("新主体错误继承旧主体的未读结束基线")
	}
	if h.manager.unreadPassNeeded(original, ptr(5)) {
		t.Fatal("旧主体自己的同值结束基线丢失")
	}
}

func TestUnreadPassEndBaselineIsProcessLocal(t *testing.T) {
	h := newHarness(t)
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取账号失败: account=%+v err=%v", account, err)
	}

	h.manager.mu.Lock()
	if !h.manager.recordUnreadPassEnd(account, ptr(2)) {
		h.manager.mu.Unlock()
		t.Fatal("原 manager 写入结束基线失败")
	}
	h.manager.mu.Unlock()

	restarted, err := NewManager(
		h.db,
		h.runner,
		h.hands,
		h.config,
	)
	if err != nil {
		t.Fatalf("重建 manager: %v", err)
	}
	restarted.mu.Lock()
	defer restarted.mu.Unlock()
	if !restarted.unreadPassNeeded(account, ptr(2)) {
		t.Fatal("新 manager 错误继承了进程内结束基线")
	}
}

func TestUnreadPassEndBaselineIsScopedByAccount(t *testing.T) {
	h := newHarness(t)
	original, err := h.db.AccountByKey(h.key)
	if err != nil || original == nil {
		t.Fatalf("读取账号失败: account=%+v err=%v", original, err)
	}
	secondKey := store.AccountKey{
		Platform:   "second-platform",
		AccountRef: "account-2",
	}
	if err := h.db.CreateAccount(&store.Account{
		Platform: secondKey.Platform, AccountRef: secondKey.AccountRef,
	}); err != nil {
		t.Fatalf("创建第二账号: %v", err)
	}
	if err := h.db.BindAccountPrincipal(
		secondKey,
		"hand-1",
		"principal-1",
		"session-1",
		"boot-1",
		h.clock.Now(),
	); err != nil {
		t.Fatalf("绑定第二账号: %v", err)
	}
	second, err := h.db.AccountByKey(secondKey)
	if err != nil || second == nil {
		t.Fatalf("读取第二账号失败: account=%+v err=%v", second, err)
	}

	h.manager.mu.Lock()
	defer h.manager.mu.Unlock()
	if !h.manager.recordUnreadPassEnd(original, ptr(4)) {
		t.Fatal("原账号写入结束基线失败")
	}
	if !h.manager.unreadPassNeeded(second, ptr(4)) {
		t.Fatal("第二账号错误继承原账号的未读结束基线")
	}
}

// 四种"不插队"在外部表现完全一样，但要修的地方全然不同：读不到是传感通道断了、
// 与基线同值是子轮没收尾、零未读是本来就没有、身份未就绪是账号没绑好。诊断日志
// 靠这个 reason 区分它们，所以合并或改写任一分支都必须先撞红这条测试。
func TestUnreadPassDecisionSeparatesEveryRefusalCause(t *testing.T) {
	h := newHarness(t)
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取账号失败: account=%+v err=%v", account, err)
	}

	h.manager.mu.Lock()
	defer h.manager.mu.Unlock()

	if needed, reason, baseline := h.manager.unreadPassDecision(account, ptr(3)); !needed ||
		reason != "进入未读子轮" || baseline != nil {
		t.Fatalf("无基线正数应进入子轮且基线为空: needed=%v reason=%q baseline=%v",
			needed, reason, baseline)
	}
	if !h.manager.recordUnreadPassEnd(account, ptr(3)) {
		t.Fatal("完整子轮结束数没有写入基线")
	}

	needed, reason, baseline := h.manager.unreadPassDecision(account, ptr(3))
	if needed || reason != "与基线同值" || baseline == nil || *baseline != 3 {
		t.Fatalf("同值应被拒且交出基线供诊断: needed=%v reason=%q baseline=%v",
			needed, reason, baseline)
	}

	// 读不到与零未读必须分开：前者不得改动基线（通道断了不代表未读清空），
	// 后者是有效清空信号、要删基线。
	if needed, reason, baseline = h.manager.unreadPassDecision(account, nil); needed ||
		reason != "读不到" || baseline == nil || *baseline != 3 {
		t.Fatalf("缺失读数应单列成因且保留基线: needed=%v reason=%q baseline=%v",
			needed, reason, baseline)
	}
	if needed, reason, _ = h.manager.unreadPassDecision(account, ptr(-1)); needed ||
		reason != "读数非法" {
		t.Fatalf("非法读数应单列成因: needed=%v reason=%q", needed, reason)
	}
	if needed, reason, baseline = h.manager.unreadPassDecision(account, ptr(0)); needed ||
		reason != "零未读" || baseline == nil || *baseline != 3 {
		t.Fatalf("零未读应单列成因并交出被清除前的基线: needed=%v reason=%q baseline=%v",
			needed, reason, baseline)
	}
	if needed, reason, baseline = h.manager.unreadPassDecision(account, ptr(3)); !needed ||
		reason != "进入未读子轮" || baseline != nil {
		t.Fatalf("零未读必须已清除基线: needed=%v reason=%q baseline=%v",
			needed, reason, baseline)
	}

	unbound := *account
	unbound.PrincipalFingerprint = nil
	if needed, reason, _ = h.manager.unreadPassDecision(&unbound, ptr(3)); needed ||
		reason != "身份未就绪" {
		t.Fatalf("身份缺失应单列成因: needed=%v reason=%q", needed, reason)
	}
}
