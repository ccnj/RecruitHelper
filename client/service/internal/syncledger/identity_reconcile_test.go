// 身份判新引擎(2026-08-09 战役 S2 换根)的单元套件。判新唯一机制:快照行的
// sourceKey 不在账本=新,按页面顺序追加尾部;无身份自家行按语义回配;首个身份
// 关联之前的未知行是窗口外历史,跳过不收编。位置影子引擎的测试在 reconcile_test.go。
package syncledger

import (
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
)

var idTestKey = store.ConversationKey{Platform: "zhilian", AccountRef: "acc-id", ConversationRef: "conv-id"}

func idKey(seed string) string {
	return HashText("identity-test|" + seed)
}

func idLedgerText(seq int64, direction, text, sourceKey string) store.Message {
	textCopy := text
	m := store.Message{
		Platform: idTestKey.Platform, AccountRef: idTestKey.AccountRef, ConversationRef: idTestKey.ConversationRef,
		Seq: seq, Direction: direction, Kind: "text", ContentHash: HashText(text), Text: &textCopy,
		Origin: "external",
	}
	if sourceKey != "" {
		key := sourceKey
		m.SourceKey = &key
	}
	return m
}

func idLedgerCard(seq int64, cardType, cardState, sourceKey string) store.Message {
	m := store.Message{
		Platform: idTestKey.Platform, AccountRef: idTestKey.AccountRef, ConversationRef: idTestKey.ConversationRef,
		Seq: seq, Direction: "out", Kind: "card", ContentHash: CardIdentityHash(cardType, "identity-test"),
		CardType: cardType, CardState: cardState, Origin: "self",
	}
	if sourceKey != "" {
		key := sourceKey
		m.SourceKey = &key
	}
	return m
}

func idSnapText(direction, text, sourceKey string) SnapshotMessage {
	textCopy := text
	return SnapshotMessage{
		Direction: direction, Kind: "text", Text: &textCopy, ContentHash: HashText(text),
		Origin: "external", SourceKey: sourceKey,
	}
}

func idSnapCard(cardState, sourceKey string) SnapshotMessage {
	return SnapshotMessage{
		Direction: "out", Kind: "card",
		CardType: "wechatExchange", CardIdentity: "identity-test", CardState: cardState,
		Origin: "external", SourceKey: sourceKey,
	}
}

func idInput(ledger []store.Message, snapshot []SnapshotMessage) ReconcileInput {
	return ReconcileInput{
		Key: idTestKey, RoundID: "round-id", Ledger: ledger, Snapshot: snapshot,
		SyncedAt: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
	}
}

func planAuditCount(plan *Plan, category string) int {
	count := 0
	for _, entry := range plan.Audits {
		if entry.Category == category {
			count++
		}
	}
	return count
}

// 战役核心场景:插话夹在我方气泡中间,身份不在账本即为新,按页面顺序捞回;
// 位置影子引擎在同一输入上会裁弃它,分歧必须留审计。
func TestIdentityReconcileAdoptsInterjectionAndAuditsShadowDivergence(t *testing.T) {
	ledger := []store.Message{
		idLedgerText(1, "in", "你好", idKey("hello")),
		idLedgerText(2, "out", "气泡一", idKey("b1")),
		idLedgerText(3, "out", "气泡二", idKey("b2")),
		idLedgerText(4, "out", "气泡三", idKey("b3")),
	}
	snapshot := []SnapshotMessage{
		idSnapText("in", "你好", idKey("hello")),
		idSnapText("out", "气泡一", idKey("b1")),
		idSnapText("in", "算了不考虑了", idKey("interject")),
		idSnapText("out", "气泡二", idKey("b2")),
		idSnapText("out", "气泡三", idKey("b3")),
	}
	plan, err := Reconcile(idInput(ledger, snapshot))
	if err != nil || plan.Decision != DecisionAppend {
		t.Fatalf("插话应判新收编: plan=%+v err=%v", plan, err)
	}
	if len(plan.Apply.NewMessages) != 1 || *plan.Apply.NewMessages[0].Text != "算了不考虑了" ||
		len(plan.EventProjection) != 1 {
		t.Fatalf("应恰好捞回插话一条并投影: %+v", plan.Apply.NewMessages)
	}
	if len(plan.Apply.SourceKeyReclaims) != 0 {
		t.Fatalf("全键账本不应有回配: %+v", plan.Apply.SourceKeyReclaims)
	}
	if planAuditCount(plan, "identity_shadow_divergence") != 1 {
		t.Fatalf("位置引擎会裁弃插话,分歧必须留审计: %+v", plan.Audits)
	}
	if planAuditCount(plan, "conversation_alignment_context_discarded") != 0 {
		t.Fatal("身份引擎不得产生 context_discarded")
	}
}

func TestIdentityReconcileKnownWindowIsNoChange(t *testing.T) {
	ledger := []store.Message{
		idLedgerText(1, "in", "你好", idKey("hello")),
		idLedgerText(2, "out", "回复", idKey("reply")),
	}
	snapshot := []SnapshotMessage{
		idSnapText("in", "你好", idKey("hello")),
		idSnapText("out", "回复", idKey("reply")),
	}
	plan, err := Reconcile(idInput(ledger, snapshot))
	if err != nil || plan.Decision != DecisionNoChange || len(plan.Apply.NewMessages) != 0 ||
		len(plan.EventProjection) != 0 {
		t.Fatalf("全部已知应 NoChange: plan=%+v err=%v", plan, err)
	}
	// 迟到的部分窗口(只含更老的已知行)同样安全:不再有 stale 概念,效果同 NoChange。
	stale := []SnapshotMessage{idSnapText("in", "你好", idKey("hello"))}
	plan, err = Reconcile(idInput(ledger, stale))
	if err != nil || plan.Decision != DecisionNoChange || len(plan.Apply.NewMessages) != 0 {
		t.Fatalf("迟到已知窗口应 NoChange: plan=%+v err=%v", plan, err)
	}
}

func TestIdentityReconcileReclaimsNullSelfRow(t *testing.T) {
	ledger := []store.Message{
		idLedgerText(1, "in", "在吗", idKey("q")),
		idLedgerText(2, "out", "在的", ""),
	}
	snapshot := []SnapshotMessage{
		idSnapText("in", "在吗", idKey("q")),
		idSnapText("out", "在的", idKey("reply-server")),
	}
	plan, err := Reconcile(idInput(ledger, snapshot))
	if err != nil || plan.Decision != DecisionNoChange {
		t.Fatalf("回配不是业务变化: plan=%+v err=%v", plan, err)
	}
	if len(plan.Apply.SourceKeyReclaims) != 1 ||
		plan.Apply.SourceKeyReclaims[0].Seq != 2 ||
		plan.Apply.SourceKeyReclaims[0].SourceKey != idKey("reply-server") {
		t.Fatalf("NULL 自家行应按语义回配身份: %+v", plan.Apply.SourceKeyReclaims)
	}
	if len(plan.Apply.NewMessages) != 0 || len(plan.EventProjection) != 0 {
		t.Fatalf("回配不得重复收编或投影: %+v", plan.Apply.NewMessages)
	}
	if planAuditCount(plan, "message_source_key_reclaimed") != 1 {
		t.Fatalf("回配必须留审计: %+v", plan.Audits)
	}
}

func TestIdentityReconcileContextBeforeFirstLinkIsSkipped(t *testing.T) {
	ledger := []store.Message{idLedgerText(1, "in", "已知", idKey("known"))}
	snapshot := []SnapshotMessage{
		idSnapText("in", "更老的历史", idKey("older")),
		idSnapText("in", "已知", idKey("known")),
		idSnapText("in", "新消息", idKey("fresh")),
	}
	plan, err := Reconcile(idInput(ledger, snapshot))
	if err != nil || plan.Decision != DecisionAppend {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if len(plan.Apply.NewMessages) != 1 || *plan.Apply.NewMessages[0].Text != "新消息" {
		t.Fatalf("首个身份关联之前的未知行是窗口外历史,不得收编: %+v", plan.Apply.NewMessages)
	}
}

// 窗口外历史里与自家 NULL 行同文的旧消息,不得抢走 NULL 行的身份;
// 真正的自家行(首个关联之后)才回配。
func TestIdentityReconcileContextCannotStealNullIdentity(t *testing.T) {
	ledger := []store.Message{
		idLedgerText(1, "out", "同文话术", ""),
		idLedgerText(2, "in", "已知", idKey("known")),
	}
	snapshot := []SnapshotMessage{
		idSnapText("out", "同文话术", idKey("historic")),
		idSnapText("in", "已知", idKey("known")),
		idSnapText("out", "同文话术", idKey("ours")),
	}
	plan, err := Reconcile(idInput(ledger, snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Apply.SourceKeyReclaims) != 1 ||
		plan.Apply.SourceKeyReclaims[0].Seq != 1 ||
		plan.Apply.SourceKeyReclaims[0].SourceKey != idKey("ours") {
		t.Fatalf("回配必须取首个关联之后的行: %+v", plan.Apply.SourceKeyReclaims)
	}
	if len(plan.Apply.NewMessages) != 0 {
		t.Fatalf("窗口外历史与回配行都不得当新消息: %+v", plan.Apply.NewMessages)
	}
}

// 账本只有无身份自家行(如仅招呼)时,回配不受"首个关联之后"限制,
// 否则这类会话永远无法建立身份关联。
func TestIdentityReconcileAllNullLedgerAllowsLeadingReclaim(t *testing.T) {
	ledger := []store.Message{idLedgerText(1, "out", "招呼语", "")}
	snapshot := []SnapshotMessage{
		idSnapText("out", "招呼语", idKey("greet-server")),
		idSnapText("in", "有兴趣", idKey("interest")),
	}
	plan, err := Reconcile(idInput(ledger, snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Apply.SourceKeyReclaims) != 1 || plan.Apply.SourceKeyReclaims[0].Seq != 1 {
		t.Fatalf("全 NULL 账本应允许首行回配: %+v", plan.Apply.SourceKeyReclaims)
	}
	if len(plan.Apply.NewMessages) != 1 || *plan.Apply.NewMessages[0].Text != "有兴趣" {
		t.Fatalf("候选人回复应收编: %+v", plan.Apply.NewMessages)
	}
}

func TestIdentityReconcileZeroLinkageLadder(t *testing.T) {
	ledger := []store.Message{idLedgerText(1, "in", "已知", idKey("known"))}
	snapshot := []SnapshotMessage{idSnapText("in", "全新窗口", idKey("unrelated"))}

	shallow := idInput(ledger, snapshot)
	plan, err := Reconcile(shallow)
	if err != nil || plan.Decision != DecisionNeedDeep {
		t.Fatalf("零身份关联浅读应 NeedDeep: plan=%+v err=%v", plan, err)
	}

	anchored := shallow
	anchored.AnchorMatched = true
	if _, err := Reconcile(anchored); !errors.Is(err, ErrAnchorContractMismatch) {
		t.Fatalf("anchorMatched 与零关联矛盾必须报错: err=%v", err)
	}

	deep := shallow
	deep.Deep = true
	plan, err = Reconcile(deep)
	if err != nil || plan.Decision != DecisionAuditedRebaseline ||
		len(plan.Rebaseline.Historical) != 1 || len(plan.EventProjection) != 0 {
		t.Fatalf("深读零关联应走审计重建梯且不投影: plan=%+v err=%v", plan, err)
	}
}

func TestIdentityReconcileMissingIdentityFailsWholeRead(t *testing.T) {
	ledger := []store.Message{idLedgerText(1, "in", "已知", idKey("known"))}
	snapshot := []SnapshotMessage{
		idSnapText("in", "已知", idKey("known")),
		idSnapText("in", "无身份行", ""),
	}
	if _, err := Reconcile(idInput(ledger, snapshot)); !errors.Is(err, ErrSnapshotIdentityMissing) {
		t.Fatalf("能力门槛:任何一行缺身份必须整读失败: err=%v", err)
	}
}

func TestIdentityReconcileSameKeyDifferentSemanticsFails(t *testing.T) {
	ledger := []store.Message{idLedgerText(1, "in", "原话", idKey("clash"))}
	snapshot := []SnapshotMessage{idSnapText("in", "被改写的话", idKey("clash"))}
	if _, err := Reconcile(idInput(ledger, snapshot)); !errors.Is(err, ErrSourceKeySemanticConflict) {
		t.Fatalf("同 key 异语义必须整读失败: err=%v", err)
	}
}

// 卡片状态跃迁按身份配对(拍板 4),与窗口位置无关:快照顺序打乱也能配对;
// 终态回退被忽略并留审计。
func TestIdentityReconcileCardTransitionPairsBySourceKey(t *testing.T) {
	ledger := []store.Message{
		idLedgerCard(1, "wechatExchange", "pending", idKey("card")),
		idLedgerText(2, "in", "好的", idKey("ok")),
	}
	snapshot := []SnapshotMessage{
		idSnapText("in", "好的", idKey("ok")),
		idSnapCard("accepted", idKey("card")),
	}
	plan, err := Reconcile(idInput(ledger, snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Apply.CardChanges) != 1 || plan.Apply.CardChanges[0].Seq != 1 ||
		plan.Apply.CardChanges[0].FromState != "pending" || plan.Apply.CardChanges[0].CardState != "accepted" ||
		len(plan.CardTransitions) != 1 {
		t.Fatalf("卡片跃迁应按身份配对: changes=%+v", plan.Apply.CardChanges)
	}

	regressed := []store.Message{idLedgerCard(1, "wechatExchange", "accepted", idKey("card"))}
	back := []SnapshotMessage{idSnapCard("pending", idKey("card"))}
	plan, err = Reconcile(idInput(regressed, back))
	if err != nil || len(plan.Apply.CardChanges) != 0 ||
		planAuditCount(plan, "card_state_regression_ignored") != 1 {
		t.Fatalf("终态回退必须忽略并留审计: plan=%+v err=%v", plan, err)
	}
}

func TestIdentityReconcileDuplicateSnapshotIdentityProcessedOnce(t *testing.T) {
	ledger := []store.Message{idLedgerText(1, "in", "已知", idKey("known"))}
	snapshot := []SnapshotMessage{
		idSnapText("in", "已知", idKey("known")),
		idSnapText("in", "新消息", idKey("fresh")),
		idSnapText("in", "新消息", idKey("fresh")),
	}
	plan, err := Reconcile(idInput(ledger, snapshot))
	if err != nil || len(plan.Apply.NewMessages) != 1 {
		t.Fatalf("跨页重复同身份行只收编一次: plan=%+v err=%v", plan, err)
	}
}

func TestIdentityReconcileEmptyLedgerAndAdoption(t *testing.T) {
	snapshot := []SnapshotMessage{
		idSnapText("in", "第一句", idKey("first")),
		idSnapText("in", "第二句", idKey("second")),
	}
	adopt := idInput(nil, snapshot)
	adopt.Adopt = true
	adopt.PlatformUserRef = "peer-id"
	plan, err := Reconcile(adopt)
	if err != nil || plan.Decision != DecisionFirstAdoption || len(plan.EventProjection) != 0 ||
		plan.HistoricalThroughSeq != 2 {
		t.Fatalf("首次收编为历史不投影: plan=%+v err=%v", plan, err)
	}

	appendCase := idInput(nil, snapshot)
	plan, err = Reconcile(appendCase)
	if err != nil || plan.Decision != DecisionAppend || len(plan.EventProjection) != 2 {
		t.Fatalf("空账本非收编读取应全量判新: plan=%+v err=%v", plan, err)
	}
}
