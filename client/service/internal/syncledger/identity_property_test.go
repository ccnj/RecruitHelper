// 身份判新的两条性质测试(战役出口要求):不重复收编、收敛幂等。外加影子
// 靶场:随机世界里新旧引擎并跑,普通顺序增长场景必须零分歧,任意场景下身份
// 引擎的不重复收编不变量必须成立。
package syncledger

import (
	"fmt"
	"math/rand"
	"testing"

	"recruithelper/client/service/internal/store"
)

type propServerMessage struct {
	key       string
	direction string
	text      string
	ours      bool
}

type propWorld struct {
	t       *testing.T
	rng     *rand.Rand
	server  []propServerMessage
	ledger  []store.Message
	nextID  int
	lastLog string
}

func newPropWorld(t *testing.T, seed int64) *propWorld {
	return &propWorld{t: t, rng: rand.New(rand.NewSource(seed))}
}

func (w *propWorld) grow(allowOurs bool, duplicateTextRate float64) {
	count := w.rng.Intn(3)
	for i := 0; i < count; i++ {
		w.nextID++
		text := fmt.Sprintf("消息-%d", w.nextID)
		if w.rng.Float64() < duplicateTextRate {
			text = "同文话术"
		}
		direction := "in"
		if w.rng.Intn(2) == 0 {
			direction = "out"
		}
		message := propServerMessage{
			key: idKey(fmt.Sprintf("prop-%d", w.nextID)), direction: direction, text: text,
		}
		if allowOurs && direction == "out" && w.rng.Intn(2) == 0 {
			// 自家乐观发送:先以无身份行入账,身份等回配。
			message.ours = true
			w.appendLedgerRow(store.MessageDraft{
				Direction: "out", Kind: "text", ContentHash: HashText(text),
				Text: &text, Origin: "self",
			})
		}
		w.server = append(w.server, message)
	}
}

func (w *propWorld) appendLedgerRow(draft store.MessageDraft) {
	seq := int64(1)
	if len(w.ledger) > 0 {
		seq = w.ledger[len(w.ledger)-1].Seq + 1
	}
	row := store.Message{
		Platform: idTestKey.Platform, AccountRef: idTestKey.AccountRef, ConversationRef: idTestKey.ConversationRef,
		Seq: seq, Direction: draft.Direction, Kind: draft.Kind, ContentHash: draft.ContentHash,
		Text: draft.Text, Origin: draft.Origin, SourceKey: draft.SourceKey,
	}
	w.ledger = append(w.ledger, row)
}

func (w *propWorld) snapshotFrom(start int, withCrossPageDup bool) []SnapshotMessage {
	out := make([]SnapshotMessage, 0, len(w.server)-start+1)
	for _, message := range w.server[start:] {
		out = append(out, idSnapText(message.direction, message.text, message.key))
	}
	if withCrossPageDup && len(out) > 1 {
		out = append(out[:1], out...)
	}
	return out
}

// reconcileAndApply 跑引擎并把计划应用到内存账本,返回本轮分歧审计数。
func (w *propWorld) reconcileAndApply(snapshot []SnapshotMessage) int {
	w.t.Helper()
	w.lastLog = ""
	for _, row := range snapshot {
		text := ""
		if row.Text != nil {
			text = *row.Text
		}
		w.lastLog += fmt.Sprintf("[%s %s %s]", row.Direction, text, row.SourceKey[:6])
	}
	in := idInput(w.ledger, snapshot)
	plan, err := Reconcile(in)
	if err != nil {
		w.t.Fatalf("Reconcile 失败: %v", err)
	}
	if plan.NeedsDeep() {
		in.Deep = true
		in.Snapshot = w.snapshotFrom(0, false)
		plan, err = Reconcile(in)
		if err != nil {
			w.t.Fatalf("深读 Reconcile 失败: %v", err)
		}
	}
	divergences := planAuditCount(plan, "identity_shadow_divergence")
	switch {
	case plan.Rebaseline != nil:
		for _, draft := range plan.Rebaseline.Historical {
			w.appendLedgerRow(draft)
		}
		if plan.Rebaseline.AuditDetail != "" {
			// 重建梯把影子分歧并进 detail,粗略计数保持口径一致。
			for i := 0; i+6 <= len(plan.Rebaseline.AuditDetail); i++ {
				if plan.Rebaseline.AuditDetail[i:i+6] == "shadow" {
					divergences++
					break
				}
			}
		}
	case plan.Apply != nil:
		for _, reclaim := range plan.Apply.SourceKeyReclaims {
			found := false
			for i := range w.ledger {
				if w.ledger[i].Seq == reclaim.Seq {
					if w.ledger[i].SourceKey != nil {
						w.t.Fatalf("回配目标已有身份: seq=%d", reclaim.Seq)
					}
					key := reclaim.SourceKey
					w.ledger[i].SourceKey = &key
					found = true
					break
				}
			}
			if !found {
				w.t.Fatalf("回配目标不存在: seq=%d", reclaim.Seq)
			}
		}
		for _, draft := range plan.Apply.NewMessages {
			w.appendLedgerRow(draft)
		}
	}
	w.assertNoDuplicateAdoption()
	return divergences
}

// 不重复收编:账本内任何服务端身份至多出现一次。
func (w *propWorld) assertNoDuplicateAdoption() {
	w.t.Helper()
	seen := map[string]int64{}
	for _, row := range w.ledger {
		if row.SourceKey == nil {
			continue
		}
		if firstSeq, ok := seen[*row.SourceKey]; ok {
			dump := ""
			for _, r := range w.ledger {
				key := "NULL"
				if r.SourceKey != nil {
					key = (*r.SourceKey)[:6]
				}
				text := ""
				if r.Text != nil {
					text = *r.Text
				}
				dump += fmt.Sprintf("[%d %s %s %s]", r.Seq, r.Direction, text, key)
			}
			w.t.Fatalf("同一服务端身份被收编两次: seq=%d 与 seq=%d ledger=%s snapshot=%s", firstSeq, row.Seq, dump, w.lastLog)
		}
		seen[*row.SourceKey] = row.Seq
	}
}

// 性质一:任意窗口序列(随机起点、跨页重复、乱插自家 NULL 行)下,绝不重复收编。
func TestIdentityPropertyNoDuplicateAdoption(t *testing.T) {
	for seed := int64(1); seed <= 30; seed++ {
		seed := seed
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			w := newPropWorld(t, seed)
			for round := 0; round < 40; round++ {
				w.grow(true, 0.2)
				if len(w.server) == 0 {
					continue
				}
				start := w.rng.Intn(len(w.server))
				w.reconcileAndApply(w.snapshotFrom(start, w.rng.Intn(4) == 0))
			}
		})
	}
}

// 性质二:收敛幂等——完整窗口读一遍收编后,再读同一窗口必须 NoChange、
// 零新增、零回配;重复多少遍都一样。
func TestIdentityPropertyConvergenceIdempotent(t *testing.T) {
	for seed := int64(31); seed <= 45; seed++ {
		w := newPropWorld(t, seed)
		for round := 0; round < 20; round++ {
			w.grow(true, 0.2)
		}
		if len(w.server) == 0 {
			continue
		}
		w.reconcileAndApply(w.snapshotFrom(0, false))
		for repeat := 0; repeat < 3; repeat++ {
			before := len(w.ledger)
			in := idInput(w.ledger, w.snapshotFrom(0, false))
			plan, err := Reconcile(in)
			if err != nil {
				t.Fatalf("seed=%d 收敛读失败: %v", seed, err)
			}
			if plan.Decision != DecisionNoChange || len(plan.Apply.NewMessages) != 0 ||
				len(plan.Apply.SourceKeyReclaims) != 0 || len(plan.EventProjection) != 0 {
				t.Fatalf("seed=%d 已收敛世界重读必须 NoChange: plan=%+v", seed, plan)
			}
			if len(w.ledger) != before {
				t.Fatalf("seed=%d 收敛后账本不得再变化", seed)
			}
		}
	}
}

// 影子靶场(甲):普通顺序增长、窗口含账本锚尾、无自家 NULL 行——新旧引擎
// 必须零分歧。这是身份引擎不回归常见路径的哨兵。
func TestIdentityShadowRangeQuietOnPlainGrowth(t *testing.T) {
	for seed := int64(101); seed <= 115; seed++ {
		w := newPropWorld(t, seed)
		lastAdopted := 0
		for round := 0; round < 30; round++ {
			w.grow(false, 0)
			if len(w.server) == 0 {
				continue
			}
			// 窗口从上次已收编边界稍前开始:恒包含账本尾部重叠,positional
			// 与 identity 在该形态下判新结果一致。
			start := lastAdopted - w.rng.Intn(3)
			if start < 0 {
				start = 0
			}
			if start > len(w.server)-1 {
				start = len(w.server) - 1
			}
			if divergences := w.reconcileAndApply(w.snapshotFrom(start, false)); divergences != 0 {
				t.Fatalf("seed=%d round=%d 普通增长不应有影子分歧", seed, round)
			}
			lastAdopted = len(w.server)
		}
	}
}

// 影子靶场(乙):制造插话(把新入站插进历史中段)——位置引擎裁弃、身份
// 引擎捞回,分歧必须被审计捕获,且捞回后世界收敛。
func TestIdentityShadowRangeCapturesInterjectionDivergence(t *testing.T) {
	w := newPropWorld(t, 201)
	for i := 0; i < 6; i++ {
		w.grow(false, 0)
	}
	for len(w.server) < 4 {
		w.grow(false, 0)
	}
	w.reconcileAndApply(w.snapshotFrom(0, false))

	// 服务端在"历史中段"插入一条(模拟排序穿插到我方链中间的插话)。
	interject := propServerMessage{
		key: idKey("prop-interject"), direction: "in", text: "算了不考虑了",
	}
	middle := len(w.server) / 2
	w.server = append(w.server[:middle], append([]propServerMessage{interject}, w.server[middle:]...)...)

	divergences := w.reconcileAndApply(w.snapshotFrom(0, false))
	if divergences == 0 {
		t.Fatal("插话场景新旧引擎判新不同,影子分歧必须被审计捕获")
	}
	adopted := false
	for _, row := range w.ledger {
		if row.SourceKey != nil && *row.SourceKey == interject.key {
			adopted = true
		}
	}
	if !adopted {
		t.Fatal("插话必须被身份引擎捞回")
	}
	// 捞回后收敛。
	in := idInput(w.ledger, w.snapshotFrom(0, false))
	plan, err := Reconcile(in)
	if err != nil || plan.Decision != DecisionNoChange {
		t.Fatalf("捞回后必须收敛: plan=%+v err=%v", plan, err)
	}
}
