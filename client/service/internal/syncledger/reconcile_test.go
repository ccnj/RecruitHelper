package syncledger

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
)

var testConversationKey = store.ConversationKey{
	Platform: "zhilian", AccountRef: "acc-01", ConversationRef: "conv-01",
}

func TestReconcileCoreCases(t *testing.T) {
	tests := []struct {
		name              string
		ledger            []string
		snapshot          []string
		adopt             bool
		deep              bool
		decision          Decision
		overlap           int
		newMessages       int
		eventProjection   int
		ambiguous         bool
		audits            int
		historicalThrough int64
	}{
		{
			name: "首次整体收编只入账不投影历史", snapshot: []string{"old-a", "old-b"}, adopt: true,
			decision: DecisionFirstAdoption, newMessages: 2, historicalThrough: 2,
		},
		{
			name: "最大重叠只追加尾部新消息", ledger: []string{"a", "b", "c"}, snapshot: []string{"b", "c", "d", "e"},
			decision: DecisionAppend, overlap: 2, newMessages: 2, eventProjection: 2,
		},
		{
			name: "重复快照零投影", ledger: []string{"a", "b", "c"}, snapshot: []string{"b", "c"},
			decision: DecisionNoChange, overlap: 2,
		},
		{
			name: "连续同文取唯一最大重叠", ledger: []string{"x", "same", "same"}, snapshot: []string{"same", "same", "new"},
			decision: DecisionAppend, overlap: 2, newMessages: 1, eventProjection: 1,
		},
		{
			name: "同文边界歧义取最靠后锚并宁可少投影", ledger: []string{"x", "same"}, snapshot: []string{"same", "same", "after"},
			decision: DecisionAppend, overlap: 1, newMessages: 1, eventProjection: 1, ambiguous: true, audits: 2,
		},
		{
			name: "迟到旧快照完全包含于账本时不回退", ledger: []string{"a", "b", "c", "d"}, snapshot: []string{"a", "b", "c"},
			decision: DecisionStaleSnapshot,
		},
		{
			name: "浅读零重叠必须要求 deep", ledger: []string{"a", "b"}, snapshot: []string{"x", "y"},
			decision: DecisionNeedDeep,
		},
		{
			name: "deep 仍零重叠受审计收为新基线且抑制事件", ledger: []string{"a", "b"}, snapshot: []string{"x", "y"}, deep: true,
			decision: DecisionAuditedRebaseline, newMessages: 2, audits: 1, historicalThrough: 4,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := ReconcileInput{
				Key: testConversationKey, RoundID: "round-1", PlatformUserRef: "user-1",
				Ledger: textLedger(t, tc.ledger...), Snapshot: textSnapshot(tc.snapshot...),
				Adopt: tc.adopt, Deep: tc.deep, ReachedTop: tc.deep,
			}
			plan, err := Reconcile(input)
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if plan.Decision != tc.decision || plan.Overlap != tc.overlap || plan.Ambiguous != tc.ambiguous {
				t.Fatalf("决策/重叠错误: %+v", plan)
			}
			if plan.HistoricalThroughSeq != tc.historicalThrough {
				t.Fatalf("historicalThrough=%d, want %d", plan.HistoricalThroughSeq, tc.historicalThrough)
			}
			if len(plan.Audits) != tc.audits {
				t.Fatalf("audits=%d, want %d: %+v", len(plan.Audits), tc.audits, plan.Audits)
			}
			if tc.decision == DecisionNeedDeep {
				if plan.Apply != nil || plan.Rebaseline != nil || !plan.NeedsDeep() {
					t.Fatalf("needDeep 不得生成部分入账请求: %+v", plan)
				}
				return
			}
			if got := len(plan.EventProjection); got != tc.eventProjection {
				t.Fatalf("eventProjection=%d, want %d", got, tc.eventProjection)
			}
			if tc.decision == DecisionAuditedRebaseline {
				if plan.Apply != nil || plan.Rebaseline == nil {
					t.Fatalf("历史基线不得经普通 Apply 写入: %+v", plan)
				}
				if got := len(plan.Rebaseline.Historical); got != tc.newMessages {
					t.Fatalf("historical=%d, want %d", got, tc.newMessages)
				}
				if plan.Rebaseline.ExpectedTailSeq != int64(len(tc.ledger)) {
					t.Fatalf("ExpectedTailSeq=%d, want %d", plan.Rebaseline.ExpectedTailSeq, len(tc.ledger))
				}
				return
			}
			if plan.Apply == nil || plan.Rebaseline != nil {
				t.Fatal("普通可落库决策缺 Apply 请求")
			}
			if got := len(plan.Apply.NewMessages); got != tc.newMessages {
				t.Fatalf("newMessages=%d, want %d", got, tc.newMessages)
			}
			if plan.Apply.ExpectedTailSeq != int64(len(tc.ledger)) {
				t.Fatalf("ExpectedTailSeq=%d, want %d", plan.Apply.ExpectedTailSeq, len(tc.ledger))
			}
			if tc.adopt != plan.Apply.Adopt {
				t.Fatalf("Apply.Adopt=%t, want %t", plan.Apply.Adopt, tc.adopt)
			}
		})
	}
}

func TestReconcileAcceptsSparseStrictlyIncreasingActiveSequence(t *testing.T) {
	ledger := textLedger(t, "old", "tail")
	ledger[1].Seq = 3 // seq=2 是已保留但不进入活动视图的撤回事实。
	plan, err := Reconcile(ReconcileInput{
		Key: testConversationKey, PlatformUserRef: "user-1", Ledger: ledger,
		Snapshot: textSnapshot("tail", "new"),
	})
	if err != nil {
		t.Fatalf("稀疏活动 seq 不应被判为账本损坏: %v", err)
	}
	if plan.Decision != DecisionAppend || plan.Overlap != 1 || plan.Apply == nil ||
		plan.Apply.ExpectedTailSeq != 3 || len(plan.Apply.NewMessages) != 1 {
		t.Fatalf("稀疏账本对齐错误: %+v", plan)
	}
}

func TestReconcileSourceKeyIdentityAndLegacyCompatibility(t *testing.T) {
	keyA := strings.Repeat("a", 64)
	keyB := strings.Repeat("b", 64)
	old := textSnapshot("same")[0]
	old.SourceKey = keyA
	ledger := snapshotLedger(t, old)

	t.Run("双方有键时不得把同文不同消息误当重叠", func(t *testing.T) {
		newSameText := textSnapshot("same")[0]
		newSameText.SourceKey = keyB
		plan, err := Reconcile(ReconcileInput{
			Key: testConversationKey, Ledger: ledger,
			Snapshot: []SnapshotMessage{old, newSameText},
		})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Decision != DecisionAppend || plan.Overlap != 1 || len(plan.EventProjection) != 1 ||
			plan.EventProjection[0].SourceKey == nil || *plan.EventProjection[0].SourceKey != keyB {
			t.Fatalf("同文不同 key 必须作为新消息追加: %+v", plan)
		}
	})

	t.Run("任一侧无键时保持旧账本兼容", func(t *testing.T) {
		legacySnapshot := textSnapshot("same")
		plan, err := Reconcile(ReconcileInput{
			Key: testConversationKey, Ledger: ledger, Snapshot: legacySnapshot,
		})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Decision != DecisionNoChange || plan.Overlap != 1 {
			t.Fatalf("无键快照应以 direction+hash 兼容旧账本: %+v", plan)
		}
		legacyLedger := textLedger(t, "same")
		plan, err = Reconcile(ReconcileInput{
			Key: testConversationKey, Ledger: legacyLedger, Snapshot: []SnapshotMessage{old},
		})
		if err != nil || plan.Decision != DecisionNoChange || plan.Overlap != 1 {
			t.Fatalf("无键账本应兼容有键快照: plan=%+v err=%v", plan, err)
		}
	})
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
				t.Fatalf("同 key 语义冲突必须在对齐前响亮失败: %v", err)
			}
			if strings.Contains(err.Error(), sourceKey) {
				t.Fatal("错误不得泄露 sourceKey")
			}
		})
	}
}

func TestSourceKeySemanticConflictIncludesKindCardTypeAndInterview(t *testing.T) {
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
		{name: "interview", mutate: func(key *messageKey) { key.interview = "1000\x1f3000\x1fwechatVideo" }},
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
	}{
		{name: "未读到顶部", mutate: func(in *ReconcileInput) { in.ReachedTop = false }},
		{name: "缺少巡检轮", mutate: func(in *ReconcileInput) { in.RoundID = "" }},
		{name: "修正行缺等值键", mutate: func(in *ReconcileInput) {
			in.Snapshot[len(in.Snapshot)-1].SourceKey = ""
		}},
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
			plan, err := Reconcile(input)
			if !errors.Is(err, ErrUnsafeMessageClassificationCorrection) || plan != nil {
				t.Fatalf("候选修正证据不全必须专用失败且不得进入 deep: plan=%+v err=%v", plan, err)
			}
		})
	}
}

func TestReconcileDoesNotMisclassifyNormalMessageAfterSystemTail(t *testing.T) {
	ledger, snapshot := classificationCorrectionWithLeadingHistoryFixture(t)
	legacy := snapshot[len(snapshot)-1]
	legacy.Direction = "system"
	legacy.Kind = "system"
	legacy.SourceKey = ""
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
	prefix := "不同的前缀"
	snapshot[0] = textSnapshot(prefix)[0]
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

func TestAuditedRebaselineBecomesIdempotent(t *testing.T) {
	ledger := textLedger(t, "a", "b", "x", "y")
	plan, err := Reconcile(ReconcileInput{
		Key: testConversationKey, Ledger: ledger, Snapshot: textSnapshot("x", "y"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Decision != DecisionNoChange || plan.Overlap != 2 || len(plan.Apply.NewMessages) != 0 {
		t.Fatalf("新基线落库后相同快照必须幂等: %+v", plan)
	}
}

func TestOlderContextAroundAnchorIsIgnoredWithoutLosingNewTail(t *testing.T) {
	ledger := textLedger(t, "a", "b")
	for _, deep := range []bool{false, true} {
		plan, err := Reconcile(ReconcileInput{
			Key: testConversationKey, RoundID: "round-anchor-contract", PlatformUserRef: "user-1",
			Ledger: ledger, Snapshot: textSnapshot("older", "a", "b", "new"),
			Deep: deep, AnchorMatched: true,
		})
		if err != nil {
			t.Fatalf("deep=%t 的锚前上下文应由脑忽略: %v", deep, err)
		}
		if plan.Decision != DecisionAppend || plan.Overlap != 2 || len(plan.EventProjection) != 1 ||
			plan.EventProjection[0].ContentHash != HashText("new") || plan.Rebaseline != nil {
			t.Fatalf("deep=%t 的中部锚对齐错误: %+v", deep, plan)
		}
	}
}

func TestAnchorMatchedWithoutAnyLedgerSuffixFailsLoudly(t *testing.T) {
	_, err := Reconcile(ReconcileInput{
		Key: testConversationKey, RoundID: "round-anchor-mismatch", PlatformUserRef: "user-1",
		Ledger: textLedger(t, "a", "b"), Snapshot: textSnapshot("older", "x", "y"),
		Deep: true, AnchorMatched: true,
	})
	if !errors.Is(err, ErrAnchorContractMismatch) {
		t.Fatalf("伪 anchorMatched 不得降级成重基线，得到 %v", err)
	}
}

func TestAnchorMatchedRequiresTheWholeDerivedAnchorTail(t *testing.T) {
	_, err := Reconcile(ReconcileInput{
		Key: testConversationKey, RoundID: "round-partial-anchor", PlatformUserRef: "user-1",
		Ledger: textLedger(t, "a", "b"), Snapshot: textSnapshot("a", "between", "b", "new"),
		AnchorMatched: true,
	})
	if !errors.Is(err, ErrAnchorContractMismatch) {
		t.Fatalf("只碰巧命中较短账本后缀不得伪装完整锚尾，得到 %v", err)
	}
}

func TestAnchorMatchedEmptySnapshotFailsLoudly(t *testing.T) {
	_, err := Reconcile(ReconcileInput{
		Key: testConversationKey, RoundID: "round-empty-anchor", PlatformUserRef: "user-1",
		Ledger: textLedger(t, "a"), Snapshot: nil, AnchorMatched: true,
	})
	if !errors.Is(err, ErrAnchorContractMismatch) {
		t.Fatalf("空快照不得绕过 anchorMatched 完整证词校验，得到 %v", err)
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

func TestRepeatedFullAnchorWithOlderContextChoosesMinimumProjection(t *testing.T) {
	plan, err := Reconcile(ReconcileInput{
		Key: testConversationKey, RoundID: "round-repeated-anchor", PlatformUserRef: "user-1",
		Ledger: textLedger(t, "a"), Snapshot: textSnapshot("a", "x", "a", "new"),
		AnchorMatched: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Decision != DecisionAppend || plan.Overlap != 1 || !plan.Ambiguous ||
		len(plan.EventProjection) != 1 || plan.EventProjection[0].ContentHash != HashText("new") {
		t.Fatalf("重复完整锚必须取最靠后位置并只投最短尾部: %+v", plan)
	}
}

func TestContainedStaleSnapshotWinsOverRepeatedSuffixAppend(t *testing.T) {
	plan, err := Reconcile(ReconcileInput{
		Key: testConversationKey, RoundID: "round-stale-repeat", PlatformUserRef: "user-1",
		Ledger: textLedger(t, "你好", "收到", "你好"), Snapshot: textSnapshot("你好", "收到"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Decision != DecisionStaleSnapshot || len(plan.EventProjection) != 0 ||
		plan.Apply == nil || len(plan.Apply.NewMessages) != 0 || !plan.Ambiguous ||
		countAuditCategory(plan.Audits, "conversation_stale_append_ambiguous") != 1 {
		t.Fatalf("完整旧窗口与短后缀冲突时必须保守判 stale 并审计: %+v", plan)
	}
}

func TestRepeatedSnapshotAtCurrentTailRemainsNoChange(t *testing.T) {
	plan, err := Reconcile(ReconcileInput{
		Key: testConversationKey, RoundID: "round-tail-repeat", PlatformUserRef: "user-1",
		Ledger: textLedger(t, "你好", "收到", "你好", "收到"), Snapshot: textSnapshot("你好", "收到"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Decision != DecisionNoChange || len(plan.EventProjection) != 0 ||
		plan.Apply == nil || len(plan.Apply.NewMessages) != 0 || !plan.Ambiguous ||
		countAuditCategory(plan.Audits, "conversation_tail_alignment_ambiguous") != 1 {
		t.Fatalf("完整当前尾部即使也见于历史位置仍应正常 NoChange 并审计: %+v", plan)
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
		Snapshot: textSnapshot("unrelated"), Deep: true,
	})
	if !errors.Is(err, store.ErrHistoricalBaselineNoRound) {
		t.Fatalf("历史基线必须归属巡检轮,得到 %v", err)
	}
}

func TestCardTransitionUsesIdentityAndOnlyAdvancesOnce(t *testing.T) {
	before := textSnapshot("before")[0]
	after := textSnapshot("after")[0]
	pending := cardSnapshot("pending")
	accepted := cardSnapshot("accepted")

	ledger := snapshotLedger(t, before, pending, after)
	plan, err := Reconcile(ReconcileInput{
		Key: testConversationKey, Ledger: ledger, Snapshot: []SnapshotMessage{accepted, after},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Overlap != 2 || len(plan.Apply.CardChanges) != 1 || len(plan.CardTransitions) != 1 {
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
		Snapshot: []SnapshotMessage{cardSnapshot("pending")},
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
		Snapshot: []SnapshotMessage{cardSnapshot("accepted")},
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
		Snapshot: []SnapshotMessage{cardSnapshot("accepted")},
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
		Snapshot: textSnapshot("a"),
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
		Key: key, RoundID: "round-1", Ledger: ledger, Snapshot: textSnapshot("a", "b"),
	})
	if err != nil {
		t.Fatal(err)
	}
	staleConcurrent, err := Reconcile(ReconcileInput{
		Key: key, RoundID: "round-1", Ledger: ledger, Snapshot: textSnapshot("a", "c"),
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
		Snapshot: textSnapshot("old-context"),
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
		Snapshot: textSnapshot("deep-x", "deep-y"), Deep: true, ReachedTop: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Decision != DecisionAuditedRebaseline || baseline.Apply != nil || baseline.Rebaseline == nil || len(baseline.EventProjection) != 0 {
		t.Fatalf("deep 零重叠必须走专用空投影计划: %+v", baseline)
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
		Snapshot: textSnapshot("deep-x", "deep-y", "real-new"),
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
		Snapshot: textSnapshot("unrelated-p", "unrelated-q"), Deep: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	competitor, err := Reconcile(ReconcileInput{
		Key: key, RoundID: "round-baseline", Ledger: ledger,
		Snapshot: textSnapshot("real-new", "competitor"),
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

// TestReconcileRepeatedTextBoundarySoak exercises the production reconciler
// against many bounded windows cut from conversations whose identities repeat
// heavily. The fixed seed makes every failure exactly reproducible.
func TestReconcileRepeatedTextBoundarySoak(t *testing.T) {
	const (
		seed      int64 = 0x5eedc0de
		caseCount       = 20_000
	)

	rng := rand.New(rand.NewSource(seed))
	decisionCounts := make(map[Decision]int)
	var generatedMessages, primaryRepeats, ambiguous, ambiguityAudits, replayChecks int

	for caseN := 0; caseN < caseCount; caseN++ {
		ledgerLen := 4 + rng.Intn(17)
		generatedAppend := rng.Intn(7)
		truth := make([]SnapshotMessage, ledgerLen+generatedAppend)
		for i := range truth {
			truth[i] = repeatedSoakMessage(rng)
			if truth[i].Text != nil && *truth[i].Text == "你好" {
				primaryRepeats++
			}
		}
		generatedMessages += len(truth)
		ledger := snapshotLedger(t, truth[:ledgerLen]...)

		var snapshot []SnapshotMessage
		var anchorMatched, deep bool
		observableAppend := 0
		switch caseN % 23 {
		case 0:
			// Empty bounded reads are an explicit no-change result.
			snapshot = nil
		case 1, 2:
			// A deterministic zero-overlap read exercises both the shallow
			// needDeep gate and the audited deep rebaseline path.
			foreign := fmt.Sprintf("zero-overlap-%d", caseN)
			snapshot = []SnapshotMessage{{Direction: "system", Kind: "text", Text: &foreign, Origin: "external"}}
			deep = caseN%23 == 2
		default:
			start, end := soakWindow(rng, ledgerLen, len(truth))
			snapshot = append([]SnapshotMessage(nil), truth[start:end]...)
			if end > ledgerLen {
				newStart := start
				if newStart < ledgerLen {
					newStart = ledgerLen
				}
				observableAppend = end - newStart
			}
			deep = rng.Intn(4) == 0
			anchorStart := ledgerLen - 5
			if anchorStart < 0 {
				anchorStart = 0
			}
			// Set the strong hand claim only when this concrete window really
			// contains the complete anchor tail derived from the ledger.
			anchorMatched = start <= anchorStart && end >= ledgerLen && rng.Intn(2) == 0
		}

		input := ReconcileInput{
			Key: testConversationKey, RoundID: "round-soak", PlatformUserRef: "user-soak",
			Ledger: ledger, Snapshot: snapshot, Deep: deep, ReachedTop: deep,
			AnchorMatched: anchorMatched,
		}
		plan, err := Reconcile(input)
		if err != nil {
			t.Fatalf("seed=%d case=%d Reconcile: %v", seed, caseN, err)
		}
		decisionCounts[plan.Decision]++

		projected := len(plan.EventProjection)
		if projected > observableAppend {
			t.Fatalf("seed=%d case=%d decision=%s projected=%d exceeds real append=%d ledger=%d snapshot=%d",
				seed, caseN, plan.Decision, projected, observableAppend, len(ledger), len(snapshot))
		}
		switch plan.Decision {
		case DecisionNeedDeep, DecisionStaleSnapshot, DecisionNoChange:
			if projected != 0 {
				t.Fatalf("seed=%d case=%d decision=%s must not project, got=%d", seed, caseN, plan.Decision, projected)
			}
		case DecisionAppend:
			if plan.Apply == nil || len(plan.Apply.NewMessages) != projected {
				t.Fatalf("seed=%d case=%d append plan/projection diverged: %+v", seed, caseN, plan)
			}
			// Model the successful append using the production plan, then feed
			// the exact same observation back through production Reconcile.
			// No already-projected suffix may be projected a second time.
			advanced := appendSoakDrafts(ledger, plan.Apply.NewMessages)
			replayInput := input
			replayInput.Ledger = advanced
			replayInput.AnchorMatched = false // derive a fresh claim on the next command
			replay, replayErr := Reconcile(replayInput)
			if replayErr != nil {
				t.Fatalf("seed=%d case=%d replay Reconcile: %v", seed, caseN, replayErr)
			}
			if len(replay.EventProjection) != 0 {
				t.Fatalf("seed=%d case=%d same snapshot projected twice: first=%d replay=%d replayDecision=%s",
					seed, caseN, projected, len(replay.EventProjection), replay.Decision)
			}
			replayChecks++
		}

		if plan.Ambiguous {
			ambiguous++
			hasAmbiguityAudit := false
			for _, entry := range plan.Audits {
				if strings.Contains(entry.Category, "ambiguous") {
					hasAmbiguityAudit = true
					break
				}
			}
			if !hasAmbiguityAudit {
				t.Fatalf("seed=%d case=%d ambiguous decision has no ambiguity audit: %+v", seed, caseN, plan)
			}
			ambiguityAudits++
		}
	}

	if generatedMessages < 20_000 || primaryRepeats*2 < generatedMessages || ambiguous == 0 || replayChecks == 0 {
		t.Fatalf("soak coverage too weak: messages=%d primaryRepeats=%d ambiguous=%d replayChecks=%d",
			generatedMessages, primaryRepeats, ambiguous, replayChecks)
	}
	t.Logf("seed=%d cases=%d messages=%d primaryRepeats=%d append=%d noChange=%d stale=%d needDeep=%d rebaseline=%d ambiguous=%d ambiguityAudits=%d replayChecks=%d",
		seed, caseCount, generatedMessages, primaryRepeats,
		decisionCounts[DecisionAppend], decisionCounts[DecisionNoChange], decisionCounts[DecisionStaleSnapshot],
		decisionCounts[DecisionNeedDeep], decisionCounts[DecisionAuditedRebaseline], ambiguous, ambiguityAudits, replayChecks)
}

func repeatedSoakMessage(rng *rand.Rand) SnapshotMessage {
	roll := rng.Intn(100)
	text, direction := "你好", "in"
	switch {
	case roll < 62:
	case roll < 76:
		direction = "out"
	case roll < 87:
		text = "收到"
	case roll < 95:
		text = "好的"
	default:
		text = "谢谢"
	}
	return SnapshotMessage{Direction: direction, Kind: "text", Text: &text, Origin: "external"}
}

func soakWindow(rng *rand.Rand, ledgerLen, truthLen int) (int, int) {
	switch rng.Intn(4) {
	case 0:
		// Current suffix, with enough old context to exercise the boundary.
		width := 1 + rng.Intn(min(ledgerLen, 10))
		return ledgerLen - width, truthLen
	case 1:
		// Delayed window wholly contained in the durable ledger.
		end := 1 + rng.Intn(ledgerLen)
		return rng.Intn(end), end
	case 2:
		end := 1 + rng.Intn(truthLen)
		return rng.Intn(end), end
	default:
		if truthLen > ledgerLen {
			// A window whose observed portion starts after the old boundary.
			return ledgerLen + rng.Intn(truthLen-ledgerLen), truthLen
		}
		return ledgerLen - 1, ledgerLen
	}
}

func appendSoakDrafts(ledger []store.Message, drafts []store.MessageDraft) []store.Message {
	out := append([]store.Message(nil), ledger...)
	for _, draft := range drafts {
		out = append(out, store.Message{
			Platform: testConversationKey.Platform, AccountRef: testConversationKey.AccountRef,
			ConversationRef: testConversationKey.ConversationRef, Seq: int64(len(out) + 1),
			Direction: draft.Direction, Kind: draft.Kind, ContentHash: draft.ContentHash,
			Text: draft.Text, BlobRef: draft.BlobRef, CardType: draft.CardType,
			CardState: draft.CardState, TsApproxMs: draft.TsApproxMs, Origin: draft.Origin,
			SourceKey: draft.SourceKey,
		})
	}
	return out
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

func cardSnapshot(state string) SnapshotMessage {
	return SnapshotMessage{
		Direction: "out", Kind: "card", CardType: "interviewInvite",
		CardIdentity: "2026-07-20 15:00", CardState: state, Origin: "external",
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
