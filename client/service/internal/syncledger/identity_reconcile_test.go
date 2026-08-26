// 身份判新引擎(2026-08-09 战役 S2 换根,2026-08-26 S3 拆除位置影子后成为
// 唯一引擎)的单元套件。判新唯一机制:快照行的 sourceKey 不在账本=新,按页面
// 顺序追加尾部;无身份自家行按语义回配;首个身份关联之前的未知行是窗口外
// 历史,跳过不收编。共享守卫与 store 集成的测试在 reconcile_test.go。
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

// 战役核心场景:插话夹在我方气泡中间,身份不在账本即为新,按页面顺序捞回。
// (旧位置对齐在同一输入上会裁弃它——S2 影子对拍与真机分歧审计均已实证,
// S3 已拆影子。)
func TestIdentityReconcileAdoptsInterjection(t *testing.T) {
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
	if len(plan.Audits) != 0 {
		t.Fatalf("普通插话捞回不应产生任何审计: %+v", plan.Audits)
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

// S2 审查阻断项回归:全 NULL 账本自举必须右端优先对齐整个账本,窗口外同文
// 历史抢不走身份,也解锁不了"把陈年历史当新消息"的门;对齐绑不满则不豁免。
func TestIdentityReconcileBootstrapRightmostAlignmentDefeatsHistoryTheft(t *testing.T) {
	ledger := []store.Message{
		idLedgerText(1, "out", "招呼语", ""),
		idLedgerText(2, "in", "好的", ""),
	}
	snapshot := []SnapshotMessage{
		idSnapText("in", "好的", idKey("hist-same")),
		idSnapText("in", "陈年历史", idKey("hist-old")),
		idSnapText("out", "招呼语", idKey("greet")),
		idSnapText("in", "好的", idKey("real")),
	}
	plan, err := Reconcile(idInput(ledger, snapshot))
	if err != nil || plan.Decision != DecisionNoChange {
		t.Fatalf("自举对齐应全额回配且不投影: plan=%+v err=%v", plan, err)
	}
	if len(plan.Apply.SourceKeyReclaims) != 2 ||
		plan.Apply.SourceKeyReclaims[0].Seq != 1 || plan.Apply.SourceKeyReclaims[0].SourceKey != idKey("greet") ||
		plan.Apply.SourceKeyReclaims[1].Seq != 2 || plan.Apply.SourceKeyReclaims[1].SourceKey != idKey("real") {
		t.Fatalf("右端优先必须绑真身而非历史同文: %+v", plan.Apply.SourceKeyReclaims)
	}
	if len(plan.Apply.NewMessages) != 0 || len(plan.EventProjection) != 0 {
		t.Fatalf("窗口外历史不得被收编或投影: %+v", plan.Apply.NewMessages)
	}

	// 自举窗口内的插话(对齐起点之后)仍要捞回。
	withInterject := []SnapshotMessage{
		idSnapText("out", "招呼语", idKey("greet")),
		idSnapText("in", "插话一句", idKey("interject")),
		idSnapText("in", "好的", idKey("real")),
	}
	plan, err = Reconcile(idInput(ledger, withInterject))
	if err != nil || len(plan.Apply.NewMessages) != 1 || *plan.Apply.NewMessages[0].Text != "插话一句" ||
		len(plan.Apply.SourceKeyReclaims) != 2 {
		t.Fatalf("自举窗口内插话应捞回: plan=%+v err=%v", plan, err)
	}

	// 对齐绑不满整个账本→不豁免,走浅读→深读梯。
	partial := []SnapshotMessage{idSnapText("in", "好的", idKey("real"))}
	plan, err = Reconcile(idInput(ledger, partial))
	if err != nil || plan.Decision != DecisionNeedDeep {
		t.Fatalf("不完整自举对齐必须走深读梯: plan=%+v err=%v", plan, err)
	}
}

// S2 审查优化项回归:账本锚尾全是无身份自家行时,anchorMatched 与零身份
// 关联不构成证词矛盾——按普通零关联走深读梯,深读触及更早带身份行即收敛。
func TestIdentityReconcileNullAnchorTailFallsToDeepLadder(t *testing.T) {
	ledger := []store.Message{idLedgerText(1, "in", "早先消息", idKey("early"))}
	bubbles := []string{"气泡一", "气泡二", "气泡三", "催一", "催二"}
	for i, text := range bubbles {
		ledger = append(ledger, idLedgerText(int64(i+2), "out", text, ""))
	}

	shallow := make([]SnapshotMessage, 0, len(bubbles)+1)
	for _, text := range bubbles {
		shallow = append(shallow, idSnapText("out", text, idKey("server-"+text)))
	}
	shallow = append(shallow, idSnapText("in", "新回复", idKey("fresh")))
	in := idInput(ledger, shallow)
	in.AnchorMatched = true
	plan, err := Reconcile(in)
	if err != nil || plan.Decision != DecisionNeedDeep {
		t.Fatalf("全 NULL 锚尾不得判证词矛盾,应走深读梯: plan=%+v err=%v", plan, err)
	}

	deep := append([]SnapshotMessage{idSnapText("in", "早先消息", idKey("early"))}, shallow...)
	deepIn := idInput(ledger, deep)
	deepIn.AnchorMatched = true
	deepIn.Deep = true
	plan, err = Reconcile(deepIn)
	if err != nil || len(plan.Apply.SourceKeyReclaims) != len(bubbles) ||
		len(plan.Apply.NewMessages) != 1 || *plan.Apply.NewMessages[0].Text != "新回复" {
		t.Fatalf("深读触及带身份行后应回配 NULL 尾并收编新回复: plan=%+v err=%v", plan, err)
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

// system 行不作语义证词(§4.5,2026-08-11 甲方裁决)的回归套件。夹具直造
// system 账本行/快照行:兜底文本随解析实现演进属预期,不得触发语义冲突。
func idLedgerSystem(seq int64, text, sourceKey string) store.Message {
	textCopy := text
	m := store.Message{
		Platform: idTestKey.Platform, AccountRef: idTestKey.AccountRef, ConversationRef: idTestKey.ConversationRef,
		Seq: seq, Direction: "system", Kind: "system", ContentHash: HashText(text), Text: &textCopy,
		Origin: "external",
	}
	if sourceKey != "" {
		key := sourceKey
		m.SourceKey = &key
	}
	return m
}

func idSnapSystem(text, sourceKey string) SnapshotMessage {
	textCopy := text
	return SnapshotMessage{
		Direction: "system", Kind: "system", Text: &textCopy, ContentHash: HashText(text),
		Origin: "external", SourceKey: sourceKey,
	}
}

// 真机形态复现(2026-08-10 会话 c1532d3c…):插件换代把同一条系统消息的兜底
// 文本从"抄邻行"改为"[系统消息:99]",同 key 异 hash。修复前整读报
// ErrSourceKeySemanticConflict,新入站行永不入账;修复后 system 行豁免,
// 化石原样保留,仅新行追加。
func TestIdentityReconcileSystemRowFallbackDriftIsNotConflict(t *testing.T) {
	ledger := []store.Message{
		idLedgerText(1, "in", "我想找养老加科技的行业", idKey("in-1")),
		idLedgerSystem(2, "我想找养老加科技的行业", idKey("sys")),
		idLedgerText(3, "out", "明天上午10点方便吗", idKey("out-1")),
	}
	snapshot := []SnapshotMessage{
		idSnapText("in", "我想找养老加科技的行业", idKey("in-1")),
		idSnapSystem("[系统消息:99]", idKey("sys")),
		idSnapText("out", "明天上午10点方便吗", idKey("out-1")),
		idSnapText("in", "不好意思谢谢", idKey("in-2")),
	}
	plan, err := Reconcile(idInput(ledger, snapshot))
	if err != nil {
		t.Fatalf("system 行兜底口径漂移不得判语义冲突: %v", err)
	}
	if plan.Decision != DecisionAppend || len(plan.EventProjection) != 1 {
		t.Fatalf("应只追加新入站行: plan=%+v", plan)
	}
	if got := plan.EventProjection[0].Text; got == nil || *got != "不好意思谢谢" {
		t.Fatalf("追加的应是新入站行: %+v", plan.EventProjection[0])
	}
	if len(plan.Apply.CardChanges) != 0 || len(plan.Apply.SourceKeyReclaims) != 0 {
		t.Fatalf("化石行不得被改写或回配: %+v", plan.Apply)
	}
}

// 前瞻:拒收换微信形态实现后,账本里的 system 化石对上同 key 的 card 观察。
// 闸豁免放行;卡片配对的防御守卫忽略该行对(账本侧不是 card),既不追加、
// 不跃迁,也不报错——存量化石的历史拒绝事实保持不可见(甲方已豁免)。
func TestIdentityReconcileSystemRowLaterParsedAsCardIsNotConflict(t *testing.T) {
	ledger := []store.Message{
		idLedgerCard(1, "wechatExchange", "pending", idKey("card")),
		idLedgerSystem(2, "[系统消息:99]", idKey("sys")),
	}
	snapshot := []SnapshotMessage{
		idSnapCard("pending", idKey("card")),
		{
			Direction: "in", Kind: "card", CardType: "wechatExchange",
			CardIdentity: "identity-test", CardState: "rejected",
			Origin: "external", SourceKey: idKey("sys"),
		},
		idSnapText("in", "换个方式聊吧", idKey("in-1")),
	}
	plan, err := Reconcile(idInput(ledger, snapshot))
	if err != nil {
		t.Fatalf("system 化石对 card 观察不得判语义冲突: %v", err)
	}
	if len(plan.EventProjection) != 1 || len(plan.Apply.CardChanges) != 0 {
		t.Fatalf("应只追加文本行、不产生卡片跃迁: plan=%+v", plan)
	}
}

// 反向:解析回归把既有 card 行读成 system 兜底。闸同样豁免,身份已知不追加,
// 卡片事实原样保留——最坏丢一次观察,业务惰性。
func TestIdentityReconcileCardRowRegressedToSystemIsNotConflict(t *testing.T) {
	ledger := []store.Message{
		idLedgerCard(1, "wechatExchange", "pending", idKey("card")),
	}
	snapshot := []SnapshotMessage{
		idSnapSystem("[系统消息:99]", idKey("card")),
	}
	plan, err := Reconcile(idInput(ledger, snapshot))
	if err != nil {
		t.Fatalf("card 行被回归解析成 system 不得判语义冲突: %v", err)
	}
	if len(plan.EventProjection) != 0 || len(plan.Apply.CardChanges) != 0 {
		t.Fatalf("身份已知不追加、不跃迁: plan=%+v", plan)
	}
}
