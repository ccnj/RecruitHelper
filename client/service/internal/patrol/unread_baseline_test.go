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
