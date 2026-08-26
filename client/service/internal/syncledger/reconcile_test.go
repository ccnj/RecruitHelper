// 本文件测的是判新引擎的共享守卫与 store 集成:账本校验、sourceKey 语义闸、
// 尾行分类修正、空读矛盾、收编前置、卡片跃迁事实与乐观并发。判新主体
// (身份集合、回配、窗口定界、深读梯)的单元套件见 identity_reconcile_test.go,
// 性质测试见 identity_property_test.go。旧位置对齐引擎及其测试矩阵已于
// 2026-08-26 S3 拆除。
package syncledger

import (
	"errors"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
)

var testConversationKey = store.ConversationKey{
	Platform: "zhilian", AccountRef: "acc-01", ConversationRef: "conv-01",
}

func TestReconcileAcceptsSparseStrictlyIncreasingActiveSequence(t *testing.T) {
	ledger := snapshotLedger(t, keyedTextSnapshot("old", "tail")...)
	ledger[1].Seq = 3 // seq=2 是已保留但不进入活动视图的撤回事实。
	plan, err := Reconcile(ReconcileInput{
		Key: testConversationKey, PlatformUserRef: "user-1", Ledger: ledger,
		Snapshot: keyedTextSnapshot("tail", "new"),
	})
	if err != nil {
		t.Fatalf("稀疏活动 seq 不应被判为账本损坏: %v", err)
	}
	if plan.Decision != DecisionAppend || plan.Apply == nil ||
		plan.Apply.ExpectedTailSeq != 3 || len(plan.Apply.NewMessages) != 1 {
		t.Fatalf("稀疏账本判新错误: %+v", plan)
	}
}

func TestReconcileRejectsSourceKeySemanticConflict(t *testing.T) {
	sourceKey := strings.Repeat("c", 64)
	base := textSnapshot("first")[0]
	base.SourceKey = sourceKey
	ledger := snapshotLedger(t, base)

	for _, test := range []struct {
		name      string
		direction string
		text      string
	}{
		{name: "direction", direction: "out", text: "first"},
		{name: "contentHash", direction: "in", text: "changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			conflicting := textSnapshot(test.text)[0]
			conflicting.Direction = test.direction
			conflicting.SourceKey = sourceKey
			_, err := Reconcile(ReconcileInput{
				Key: testConversationKey, Ledger: ledger, Snapshot: []SnapshotMessage{conflicting},
			})
			if !errors.Is(err, ErrSourceKeySemanticConflict) {
				t.Fatalf("同 key 语义冲突必须在判新前响亮失败: %v", err)
			}
			if strings.Contains(err.Error(), sourceKey) {
				t.Fatal("错误不得泄露 sourceKey")
			}
		})
	}
}

func TestSourceKeySemanticConflictIncludesKindAndCardType(t *testing.T) {
	sourceKey := strings.Repeat("d", 64)
	base := messageKey{
		direction: "out", kind: "card", hash: strings.Repeat("a", 64),
		cardType: "interviewInvite", interview: "1000\x1f2000\x1fwechatVideo",
		sourceKey: sourceKey,
	}
	for _, test := range []struct {
		name   string
		mutate func(*messageKey)
	}{
		{name: "kind", mutate: func(key *messageKey) { key.kind = "text" }},
		{name: "cardType", mutate: func(key *messageKey) { key.cardType = "wechatExchange" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			conflicting := base
			test.mutate(&conflicting)
			err := validateSourceKeySemantics([]messageKey{base}, []messageKey{conflicting})
			if !errors.Is(err, ErrSourceKeySemanticConflict) {
				t.Fatalf("同 sourceKey 的 %s 冲突必须被拒绝: %v", test.name, err)
			}
			if strings.Contains(err.Error(), sourceKey) {
				t.Fatal("错误不得泄露 sourceKey")
			}
		})
	}
}

func TestSourceKeySemanticConflictAllowsLaterOptionalInterviewDetail(t *testing.T) {
	sourceKey := strings.Repeat("e", 64)
	legacy := messageKey{
		direction: "out", kind: "card", hash: strings.Repeat("a", 64),
		cardType: "interviewInvite", sourceKey: sourceKey,
	}
	enriched := legacy
	enriched.interview = "1000\x1f2000\x1fwechatVideo"

	if err := validateSourceKeySemantics(
		[]messageKey{legacy},
		[]messageKey{enriched},
	); err != nil {
		t.Fatalf("optional interview 后补不得制造 sourceKey 语义冲突: %v", err)
	}
	if equalMessageKey(legacy, enriched) {
		t.Fatal("后补 interview 仍应参与精确消息对齐，不得被静默视为同一快照形态")
	}
}

func TestReconcileRejectsInvalidPersistedSourceKey(t *testing.T) {
	ledger := textLedger(t, "old")
	invalid := strings.Repeat("A", 64)
	ledger[0].SourceKey = &invalid
	_, err := Reconcile(ReconcileInput{
		Key: testConversationKey, Ledger: ledger, Snapshot: textSnapshot("old"),
	})
	if !errors.Is(err, ErrInvalidLedger) {
		t.Fatalf("非法持久化 sourceKey 必须按账本损坏拒绝: %v", err)
	}
}

func TestReconcilePlansUniqueTailClassificationCorrection(t *testing.T) {
	ledger, snapshot := classificationCorrectionWithLeadingHistoryFixture(t)
	plan, err := Reconcile(ReconcileInput{
		Key: testConversationKey, RoundID: "round-correction", Ledger: ledger,
		Snapshot: snapshot, ReachedTop: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Decision != DecisionClassificationCorrection || plan.Correction == nil ||
		plan.Apply != nil || plan.Rebaseline != nil || len(plan.EventProjection) != 0 ||
		len(plan.CardTransitions) != 0 || len(plan.Audits) != 0 {
		t.Fatalf("唯一尾分类修正必须生成专用零投影计划: %+v", plan)
	}
	request := plan.Correction
	if request.ExpectedTailSeq != ledger[len(ledger)-1].Seq || request.OldSeq != ledger[len(ledger)-1].Seq ||
		request.RoundID != "round-correction" || request.Corrected.Direction != "in" ||
		request.Corrected.Kind != "text" || request.Corrected.SourceKey == nil ||
		*request.Corrected.SourceKey != snapshot[len(snapshot)-1].SourceKey {
		t.Fatalf("分类修正事务参数错误: %+v", request)
	}
}

func TestReconcileRejectsAmbiguousClassificationCorrectionCandidates(t *testing.T) {
	ledger, aligned := classificationCorrectionFixture(t)
	secondPrefix := aligned[0]
	secondPrefix.SourceKey = strings.Repeat("c", 64)
	secondCorrected := aligned[1]
	secondCorrected.SourceKey = strings.Repeat("d", 64)
	snapshot := append(append([]SnapshotMessage(nil), aligned...), secondPrefix, secondCorrected)

	plan, err := Reconcile(ReconcileInput{
		Key: testConversationKey, RoundID: "round-correction-ambiguous", Ledger: ledger,
		Snapshot: snapshot, ReachedTop: true,
	})
	if !errors.Is(err, ErrUnsafeMessageClassificationCorrection) || plan != nil {
		t.Fatalf("多个严格候选必须停止且不得选择尾部一个: plan=%+v err=%v", plan, err)
	}
}

func TestReconcileRejectsMissingClassificationCorrectionTail(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]SnapshotMessage) []SnapshotMessage
	}{
		{
			name: "预期尾缺失",
			mutate: func(snapshot []SnapshotMessage) []SnapshotMessage {
				return snapshot[:len(snapshot)-1]
			},
		},
		{
			name: "预期尾被无关消息替换",
			mutate: func(snapshot []SnapshotMessage) []SnapshotMessage {
				replacement := textSnapshot("这是另一条入站消息")[0]
				replacement.SourceKey = strings.Repeat("e", 64)
				snapshot[len(snapshot)-1] = replacement
				return snapshot
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger, snapshot := classificationCorrectionWithLeadingHistoryFixture(t)
			plan, err := Reconcile(ReconcileInput{
				Key: testConversationKey, RoundID: "round-correction-missing-tail", Ledger: ledger,
				Snapshot: test.mutate(snapshot), ReachedTop: true,
			})
			if !errors.Is(err, ErrUnsafeMessageClassificationCorrection) || plan != nil {
				t.Fatalf("前缀已定位但预期尾缺失/替换必须停止: plan=%+v err=%v", plan, err)
			}
		})
	}
}

func TestReconcileRejectsUniqueClassificationCorrectionCandidateBeforeSnapshotTail(t *testing.T) {
	ledger, snapshot := classificationCorrectionWithLeadingHistoryFixture(t)
	newMessage := textSnapshot("修正候选之后的另一条消息")[0]
	newMessage.SourceKey = strings.Repeat("f", 64)
	snapshot = append(snapshot, newMessage)

	plan, err := Reconcile(ReconcileInput{
		Key: testConversationKey, RoundID: "round-correction-not-tail", Ledger: ledger,
		Snapshot: snapshot, ReachedTop: true,
	})
	if !errors.Is(err, ErrUnsafeMessageClassificationCorrection) || plan != nil {
		t.Fatalf("唯一候选不在快照尾部时不得修正中间历史行: plan=%+v err=%v", plan, err)
	}
}

func TestReconcileClassificationCorrectionCandidateFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ReconcileInput)
		want   error
	}{
		{name: "未读到顶部", mutate: func(in *ReconcileInput) { in.ReachedTop = false }},
		{name: "缺少巡检轮", mutate: func(in *ReconcileInput) { in.RoundID = "" }},
		// 修正行缺等值键:身份能力门槛先于修正判定,整读失败(方向相同,失败更早)。
		{name: "修正行缺等值键", mutate: func(in *ReconcileInput) {
			in.Snapshot[len(in.Snapshot)-1].SourceKey = ""
		}, want: ErrSnapshotIdentityMissing},
		{name: "旧时间缺失", mutate: func(in *ReconcileInput) {
			in.Ledger[len(in.Ledger)-1].TsApproxMs = nil
		}},
		{name: "修正时间缺失", mutate: func(in *ReconcileInput) {
			in.Snapshot[len(in.Snapshot)-1].TsApproxMs = nil
		}},
		{name: "时间不同", mutate: func(in *ReconcileInput) {
			value := *in.Snapshot[len(in.Snapshot)-1].TsApproxMs + 1
			in.Snapshot[len(in.Snapshot)-1].TsApproxMs = &value
		}},
		{name: "旧正文与哈希自相矛盾", mutate: func(in *ReconcileInput) {
			value := "另一条旧正文"
			in.Ledger[len(in.Ledger)-1].Text = &value
		}},
		{name: "旧行残留卡片元数据", mutate: func(in *ReconcileInput) {
			in.Ledger[len(in.Ledger)-1].CardType = "unknownCard"
		}},
		{name: "修正行残留 blob", mutate: func(in *ReconcileInput) {
			in.Snapshot[len(in.Snapshot)-1].BlobRef = "blob-should-not-exist"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger, snapshot := classificationCorrectionFixture(t)
			input := ReconcileInput{
				Key: testConversationKey, RoundID: "round-correction", Ledger: ledger,
				Snapshot: snapshot, ReachedTop: true,
			}
			test.mutate(&input)
			want := test.want
			if want == nil {
				want = ErrUnsafeMessageClassificationCorrection
			}
			plan, err := Reconcile(input)
			if !errors.Is(err, want) || plan != nil {
				t.Fatalf("候选修正证据不全必须响亮失败且不得进入 deep: plan=%+v err=%v", plan, err)
			}
		})
	}
}

func TestReconcileDoesNotMisclassifyNormalMessageAfterSystemTail(t *testing.T) {
	ledger, snapshot := classificationCorrectionWithLeadingHistoryFixture(t)
	legacy := snapshot[len(snapshot)-1]
	legacy.Direction = "system"
	legacy.Kind = "system"
	legacy.SourceKey = strings.Repeat("9", 64)
	newMessage := textSnapshot("这是一条正常的新入站消息")[0]
	newMessage.SourceKey = strings.Repeat("e", 64)
	snapshot[len(snapshot)-1] = legacy
	snapshot = append(snapshot, newMessage)
	plan, err := Reconcile(ReconcileInput{
		Key: testConversationKey, RoundID: "round-normal-append", Ledger: ledger,
		Snapshot: snapshot, ReachedTop: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Decision != DecisionAppend || plan.Apply == nil || len(plan.Apply.NewMessages) != 1 ||
		plan.Correction != nil || len(plan.EventProjection) != 1 {
		t.Fatalf("system 尾后普通 in/text 必须仍走正常追加: %+v", plan)
	}
}

func TestReconcileTreatsNonmatchingCorrectionSkeletonAsOrdinaryAlignment(t *testing.T) {
	ledger, snapshot := classificationCorrectionFixture(t)
	replacement := textSnapshot("不同的前缀")[0]
	replacement.SourceKey = strings.Repeat("8", 64)
	snapshot[0] = replacement
	plan, err := Reconcile(ReconcileInput{
		Key: testConversationKey, RoundID: "round-not-correction", Ledger: ledger,
		Snapshot: snapshot, ReachedTop: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Decision != DecisionNeedDeep || plan.Correction != nil {
		t.Fatalf("前缀不一致不是候选修正，应走普通对账: %+v", plan)
	}
}

func TestReconcileStillRejectsDuplicateOrDescendingSequence(t *testing.T) {
	for _, test := range []struct {
		name string
		seqs []int64
	}{
		{name: "duplicate", seqs: []int64{2, 2}},
		{name: "descending", seqs: []int64{3, 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger := textLedger(t, "a", "b")
			ledger[0].Seq, ledger[1].Seq = test.seqs[0], test.seqs[1]
			_, err := Reconcile(ReconcileInput{
				Key: testConversationKey, Ledger: ledger, Snapshot: textSnapshot("b"),
			})
			if !errors.Is(err, ErrInvalidLedger) {
				t.Fatalf("非严格递增 seq 必须拒绝: %v", err)
			}
		})
	}
}

func TestFirstAdoptionRejectsEmptySnapshot(t *testing.T) {
	_, err := Reconcile(ReconcileInput{
		Key: testConversationKey, RoundID: "round-empty-adoption", PlatformUserRef: "user-1",
		Adopt: true, Snapshot: nil,
	})
	if !errors.Is(err, ErrAdoptionSnapshotEmpty) {
		t.Fatalf("空快照不得把 pending 收编成 boundary=0，得到 %v", err)
	}
}

func TestTrackedSnapshotEmptyContradictsLedger(t *testing.T) {
	_, err := Reconcile(ReconcileInput{
		Key: testConversationKey, RoundID: "round-empty-contradiction", PlatformUserRef: "user-1",
		Ledger: textLedger(t, "你好", "收到"),
	})
	if !errors.Is(err, ErrTrackedSnapshotEmpty) {
		t.Fatalf("账本非空而快照整窗为空必须判矛盾而非 NoChange,得到 %v", err)
	}
}

func TestTrackedSnapshotEmptyDoesNotFireOnEmptyLedger(t *testing.T) {
	plan, err := Reconcile(ReconcileInput{
		Key: testConversationKey, RoundID: "round-both-empty", PlatformUserRef: "user-1",
	})
	if err != nil || plan.Decision != DecisionAppend || len(plan.Apply.NewMessages) != 0 {
		t.Fatalf("账本与快照同空的未收编读取不属于矛盾: plan=%+v err=%v", plan, err)
	}
}

func TestFirstAdoptionRequiresPlatformUserRef(t *testing.T) {
	_, err := Reconcile(ReconcileInput{
		Key: testConversationKey, Adopt: true, Snapshot: textSnapshot("history"),
	})
	if !errors.Is(err, store.ErrPeerIdentityRequired) {
		t.Fatalf("首次收编不得脱离 platformUserRef 候选人锚,得到 %v", err)
	}
}

func TestDeepRebaselineRequiresPatrolRound(t *testing.T) {
	_, err := Reconcile(ReconcileInput{
		Key: testConversationKey, Ledger: textLedger(t, "old"),
		Snapshot: keyedTextSnapshot("unrelated"), Deep: true,
	})
	if !errors.Is(err, store.ErrHistoricalBaselineNoRound) {
		t.Fatalf("历史基线必须归属巡检轮,得到 %v", err)
	}
}

func TestCardTransitionUsesIdentityAndOnlyAdvancesOnce(t *testing.T) {
	before := keyedTextSnapshot("before")[0]
	after := keyedTextSnapshot("after")[0]
	pending := cardSnapshotKeyed("pending", "invite")
	accepted := cardSnapshotKeyed("accepted", "invite")

	ledger := snapshotLedger(t, before, pending, after)
	plan, err := Reconcile(ReconcileInput{
		Key: testConversationKey, Ledger: ledger, Snapshot: []SnapshotMessage{accepted, after},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Apply.CardChanges) != 1 || len(plan.CardTransitions) != 1 {
		t.Fatalf("卡片身份应对齐而状态独立跃迁: %+v", plan)
	}
	change := plan.Apply.CardChanges[0]
	if change.Seq != 2 || change.FromState != "pending" || change.CardState != "accepted" || plan.CardTransitions[0].From != "pending" {
		t.Fatalf("卡片跃迁错误: %+v / %+v", change, plan.CardTransitions[0])
	}

	ledger[1].CardState = "accepted" // 模拟上一计划已事务落库。
	repeated, err := Reconcile(ReconcileInput{
		Key: testConversationKey, Ledger: ledger, Snapshot: []SnapshotMessage{accepted, after},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(repeated.Apply.CardChanges) != 0 || len(repeated.CardTransitions) != 0 {
		t.Fatalf("相同 accepted 快照不得二次触发: %+v", repeated)
	}

	stale, err := Reconcile(ReconcileInput{
		Key: testConversationKey, Ledger: ledger, Snapshot: []SnapshotMessage{pending, after},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale.Apply.CardChanges) != 0 || len(stale.Audits) != 1 || stale.Audits[0].Category != "card_state_regression_ignored" {
		t.Fatalf("迟到 pending 不得把 accepted 回退: %+v", stale)
	}
}

func TestRepeatedCardReconciliationDoesNotDuplicateTransitionFact(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := store.ConversationKey{
		Platform: "zhilian", AccountRef: "acc-card-reconcile", ConversationRef: "conv-card-reconcile",
	}
	const roundID = "round-card-reconcile"
	if err := s.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePatrolRound(&store.PatrolRound{Platform: key.Platform, AccountRef: key.AccountRef, RoundID: roundID}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveConversationList(store.SaveConversationListRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, RoundID: roundID,
		Entries: []store.ListIndexEntry{{ConversationRef: key.ConversationRef, PlatformUserRef: "user-card-reconcile"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrackConversation(key, "test", time.Now()); err != nil {
		t.Fatal(err)
	}

	initial, err := Reconcile(ReconcileInput{
		Key: key, RoundID: roundID, PlatformUserRef: "user-card-reconcile", Adopt: true,
		Snapshot: []SnapshotMessage{cardSnapshotKeyed("pending", "card-fact")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPlan(s, initial); err != nil {
		t.Fatal(err)
	}
	ledger, err := s.MessagesForConversation(key)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Reconcile(ReconcileInput{
		Key: key, RoundID: roundID, Ledger: ledger,
		Snapshot: []SnapshotMessage{cardSnapshotKeyed("accepted", "card-fact")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPlan(s, first); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingCardTransitions(10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("首次对账应产生一条跃迁事实: %+v err=%v", pending, err)
	}

	ledger, err = s.MessagesForConversation(key)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := Reconcile(ReconcileInput{
		Key: key, RoundID: roundID, Ledger: ledger,
		Snapshot: []SnapshotMessage{cardSnapshotKeyed("accepted", "card-fact")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Apply == nil || len(repeated.Apply.CardChanges) != 0 || len(repeated.CardTransitions) != 0 {
		t.Fatalf("重复对账不得再产生跃迁计划: %+v", repeated)
	}
	if _, err := ApplyPlan(s, repeated); err != nil {
		t.Fatal(err)
	}
	pending, err = s.PendingCardTransitions(10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("重复对账不得增生第二条事实: %+v err=%v", pending, err)
	}
}

func TestPlanDrivesStoreAndOptimisticConflict(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := testConversationKey
	if err := s.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePatrolRound(&store.PatrolRound{Platform: key.Platform, AccountRef: key.AccountRef, RoundID: "round-1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveConversationList(store.SaveConversationListRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, RoundID: "round-1", Complete: true,
		Entries: []store.ListIndexEntry{{ConversationRef: key.ConversationRef, PlatformUserRef: "user-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrackConversation(key, "user", time.Now()); err != nil {
		t.Fatal(err)
	}

	initial, err := Reconcile(ReconcileInput{
		Key: key, RoundID: "round-1", PlatformUserRef: "user-1", Adopt: true,
		Snapshot: keyedTextSnapshot("a"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPlan(s, initial); err != nil {
		t.Fatalf("算法输出无法直接驱动 store: %v", err)
	}
	ledger, err := s.MessagesForConversation(key)
	if err != nil {
		t.Fatal(err)
	}

	first, err := Reconcile(ReconcileInput{
		Key: key, RoundID: "round-1", Ledger: ledger, Snapshot: keyedTextSnapshot("a", "b"),
	})
	if err != nil {
		t.Fatal(err)
	}
	staleConcurrent, err := Reconcile(ReconcileInput{
		Key: key, RoundID: "round-1", Ledger: ledger, Snapshot: keyedTextSnapshot("a", "c"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPlan(s, first); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPlan(s, staleConcurrent); !errors.Is(err, store.ErrConversationVersionConflict) {
		t.Fatalf("过时计划必须由 store 乐观版本闸拒绝,得到 %v", err)
	}
	ledger, err = s.MessagesForConversation(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger) != 2 || ledger[1].ContentHash != HashText("b") {
		t.Fatalf("版本冲突后不得混入部分写: %+v", ledger)
	}
}

func TestRebaselineAdapterKeepsProjectionAndDBCountConsistent(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := testConversationKey
	if err := s.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePatrolRound(&store.PatrolRound{Platform: key.Platform, AccountRef: key.AccountRef, RoundID: "round-baseline"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveConversationList(store.SaveConversationListRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, RoundID: "round-baseline", Complete: true,
		Entries: []store.ListIndexEntry{{ConversationRef: key.ConversationRef, PlatformUserRef: "user-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrackConversation(key, "user", time.Now()); err != nil {
		t.Fatal(err)
	}
	initial, err := Reconcile(ReconcileInput{
		Key: key, RoundID: "round-baseline", PlatformUserRef: "user-1", Adopt: true,
		Snapshot: keyedTextSnapshot("old-context"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPlan(s, initial); err != nil {
		t.Fatal(err)
	}
	ledger, _ := s.MessagesForConversation(key)

	baseline, err := Reconcile(ReconcileInput{
		Key: key, RoundID: "round-baseline", Ledger: ledger,
		Snapshot: keyedTextSnapshot("deep-x", "deep-y"), Deep: true, ReachedTop: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Decision != DecisionAuditedRebaseline || baseline.Apply != nil || baseline.Rebaseline == nil || len(baseline.EventProjection) != 0 {
		t.Fatalf("deep 零关联必须走专用空投影计划: %+v", baseline)
	}
	if _, err := ApplyPlan(s, baseline); err != nil {
		t.Fatalf("历史基线 adapter 落库: %v", err)
	}
	ledger, _ = s.MessagesForConversation(key)
	round, _ := s.PatrolRoundByKey(key.Platform, key.AccountRef, "round-baseline")
	conv, _ := s.ConversationByKey(key)
	intent, _ := s.TrackedIntentByConversation(key)
	if len(ledger) != 3 || round.NewMessageCount != len(baseline.EventProjection) {
		t.Fatalf("基线落库后 DB 新增计数必须与空事件投影一致: messages=%d count=%d projection=%d",
			len(ledger), round.NewMessageCount, len(baseline.EventProjection))
	}
	if conv.TrackingState != store.TrackingAdopted || intent.Status != store.TrackingAdopted || conv.AdoptedBoundarySeq != 1 || conv.LastMessageSeq != 3 {
		t.Fatalf("基线重建不得重走 adopt/pending 状态机: conv=%+v intent=%+v", conv, intent)
	}
	for _, message := range ledger[1:] {
		if message.FirstSeenRoundID != "round-baseline" {
			t.Fatalf("历史基线消息仍须保留 firstSeenRound: %+v", message)
		}
	}
	audits, _ := s.AuditEntries(20)
	if countAuditCategory(audits, "conversation_zero_overlap_rebaseline") != 1 {
		t.Fatalf("历史基线必备审计未与消息同时提交: %+v", audits)
	}

	// 基线后的真正新消息恢复普通投影/计数。
	next, err := Reconcile(ReconcileInput{
		Key: key, RoundID: "round-baseline", Ledger: ledger,
		Snapshot: keyedTextSnapshot("deep-x", "deep-y", "real-new"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPlan(s, next); err != nil {
		t.Fatal(err)
	}
	round, _ = s.PatrolRoundByKey(key.Platform, key.AccountRef, "round-baseline")
	if len(next.EventProjection) != 1 || round.NewMessageCount != 1 {
		t.Fatalf("普通新消息投影与 DB 计数不一致: projection=%d count=%d", len(next.EventProjection), round.NewMessageCount)
	}

	// 过时基线计划被版本闸拒绝时,消息、审计和计数都不得部分落库。
	ledger, _ = s.MessagesForConversation(key)
	staleBaseline, err := Reconcile(ReconcileInput{
		Key: key, RoundID: "round-baseline", Ledger: ledger,
		Snapshot: keyedTextSnapshot("unrelated-p", "unrelated-q"), Deep: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	competitor, err := Reconcile(ReconcileInput{
		Key: key, RoundID: "round-baseline", Ledger: ledger,
		Snapshot: keyedTextSnapshot("real-new", "competitor"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPlan(s, competitor); err != nil {
		t.Fatal(err)
	}
	beforeMessages, _ := s.MessagesForConversation(key)
	beforeRound, _ := s.PatrolRoundByKey(key.Platform, key.AccountRef, "round-baseline")
	beforeAudits, _ := s.AuditEntries(50)
	if _, err := ApplyPlan(s, staleBaseline); !errors.Is(err, store.ErrConversationVersionConflict) {
		t.Fatalf("过时基线计划应失败,得到 %v", err)
	}
	afterMessages, _ := s.MessagesForConversation(key)
	afterRound, _ := s.PatrolRoundByKey(key.Platform, key.AccountRef, "round-baseline")
	afterAudits, _ := s.AuditEntries(50)
	if len(afterMessages) != len(beforeMessages) || afterRound.NewMessageCount != beforeRound.NewMessageCount ||
		countAuditCategory(afterAudits, "conversation_zero_overlap_rebaseline") != countAuditCategory(beforeAudits, "conversation_zero_overlap_rebaseline") {
		t.Fatalf("基线失败发生部分写: messages %d->%d count %d->%d audits %d->%d",
			len(beforeMessages), len(afterMessages), beforeRound.NewMessageCount, afterRound.NewMessageCount,
			countAuditCategory(beforeAudits, "conversation_zero_overlap_rebaseline"),
			countAuditCategory(afterAudits, "conversation_zero_overlap_rebaseline"))
	}
}

func countAuditCategory(entries []store.AuditEntry, category string) int {
	count := 0
	for _, entry := range entries {
		if entry.Category == category {
			count++
		}
	}
	return count
}

func TestAnchorTailUsesLastFive(t *testing.T) {
	ledger := textLedger(t, "1", "2", "3", "4", "5", "6", "7")
	anchors := AnchorTail(ledger)
	if len(anchors) != 5 || anchors[0].ContentHash != HashText("3") || anchors[4].ContentHash != HashText("7") {
		t.Fatalf("anchorTail 必须只由账本末 5 条派生: %+v", anchors)
	}
}

func textSnapshot(values ...string) []SnapshotMessage {
	out := make([]SnapshotMessage, len(values))
	for i := range values {
		value := values[i]
		out[i] = SnapshotMessage{Direction: "in", Kind: "text", Text: &value, Origin: "external"}
	}
	return out
}

// keyedTextSnapshot 按正文派生确定性服务端身份(同文即同 key),满足身份
// 能力门槛;需要同文异 key 的场景请显式设 SourceKey。
func keyedTextSnapshot(values ...string) []SnapshotMessage {
	out := textSnapshot(values...)
	for i := range out {
		out[i].SourceKey = HashText("reconcile-test|" + values[i])
	}
	return out
}

func cardSnapshotKeyed(state, keySeed string) SnapshotMessage {
	return SnapshotMessage{
		Direction: "out", Kind: "card", CardType: "interviewInvite",
		CardIdentity: "2026-07-20 15:00", CardState: state, Origin: "external",
		SourceKey: HashText("reconcile-test-card|" + keySeed),
	}
}

func classificationCorrectionFixture(t *testing.T) ([]store.Message, []SnapshotMessage) {
	t.Helper()
	prefixKey := strings.Repeat("a", 64)
	correctedKey := strings.Repeat("b", 64)
	prefix := textSnapshot("你好")[0]
	prefix.Direction = "out"
	prefix.Origin = "self"
	prefix.SourceKey = prefixKey
	timestamp := int64(1_753_146_000_000)
	text := "我暂时不考虑，祝你早日找到合适的人"
	legacy := SnapshotMessage{
		Direction: "system", Kind: "system", Text: &text, TsApproxMs: &timestamp, Origin: "external",
	}
	corrected := legacy
	corrected.Direction = "in"
	corrected.Kind = "text"
	corrected.SourceKey = correctedKey
	ledger := snapshotLedger(t, prefix, legacy)
	// 旧招呼行没有 sourceKey，页面快照可以带后来可见的稳定身份；
	// 这沿用既有 equalMessageKey 对单边缺键的对齐语义。
	ledger[0].SourceKey = nil
	return ledger, []SnapshotMessage{prefix, corrected}
}

func classificationCorrectionWithLeadingHistoryFixture(t *testing.T) ([]store.Message, []SnapshotMessage) {
	t.Helper()
	ledger, aligned := classificationCorrectionFixture(t)
	firstText := "平台历史系统消息一"
	secondText := "平台历史系统消息二"
	leading := []SnapshotMessage{
		{
			Direction: "system", Kind: "system", Text: &firstText, Origin: "external",
			SourceKey: strings.Repeat("c", 64),
		},
		{
			Direction: "system", Kind: "system", Text: &secondText, Origin: "external",
			SourceKey: strings.Repeat("d", 64),
		},
	}
	return ledger, append(leading, aligned...)
}

func textLedger(t *testing.T, values ...string) []store.Message {
	t.Helper()
	return snapshotLedger(t, textSnapshot(values...)...)
}

func snapshotLedger(t *testing.T, snapshot ...SnapshotMessage) []store.Message {
	t.Helper()
	out := make([]store.Message, len(snapshot))
	for i := range snapshot {
		normalized, err := NormalizeMessage(snapshot[i])
		if err != nil {
			t.Fatalf("NormalizeMessage[%d]: %v", i, err)
		}
		out[i] = store.Message{
			Platform: testConversationKey.Platform, AccountRef: testConversationKey.AccountRef,
			ConversationRef: testConversationKey.ConversationRef, Seq: int64(i + 1),
			Direction: normalized.Direction, Kind: normalized.Kind, ContentHash: normalized.ContentHash,
			Text: normalized.Text, BlobRef: normalized.BlobRef, CardType: normalized.CardType,
			CardState: normalized.CardState, TsApproxMs: normalized.TsApproxMs, Origin: normalized.Origin,
		}
		if normalized.SourceKey != "" {
			sourceKey := normalized.SourceKey
			out[i].SourceKey = &sourceKey
		}
	}
	return out
}
