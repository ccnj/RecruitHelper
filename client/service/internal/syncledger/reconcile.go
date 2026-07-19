package syncledger

import (
	"errors"
	"fmt"
	"time"

	"recruithelper/client/service/internal/store"
)

var (
	ErrInvalidConversationKey = errors.New("会话正式键不完整")
	ErrInvalidLedger          = errors.New("持久化会话账本无效")
	ErrAdoptionHasLedger      = errors.New("首次收编时账本必须为空")
	ErrAdoptionSnapshotEmpty  = errors.New("首次收编快照不能为空")
	ErrAnchorContractMismatch = errors.New("手报告 anchorMatched 但快照中找不到完整账本锚尾")
)

type Decision string

const (
	DecisionFirstAdoption     Decision = "firstAdoption"
	DecisionAppend            Decision = "append"
	DecisionNoChange          Decision = "noChange"
	DecisionStaleSnapshot     Decision = "staleSnapshot"
	DecisionNeedDeep          Decision = "needDeep"
	DecisionAuditedRebaseline Decision = "auditedRebaseline"
)

// ReconcileInput contains only brain state and a bounded, chronological hand
// snapshot. Adopt marks the first intentional import of a pending tracked
// conversation. Deep marks the second read after a shallow zero-overlap result.
type ReconcileInput struct {
	Key             store.ConversationKey
	RoundID         string
	PlatformUserRef string
	Ledger          []store.Message
	Snapshot        []SnapshotMessage
	Adopt           bool
	Deep            bool
	ReachedTop      bool
	AnchorMatched   bool
	SyncedAt        time.Time
}

type CardTransition struct {
	Seq         int64
	ContentHash string
	CardType    string
	From        string
	To          string
}

// Plan is directly executable by ApplyPlan. EventProjection is deliberately
// separate: first adoption and audited rebaseline persist context but expose no
// historical message as a new business event. Audits are decision diagnostics;
// the rebaseline audit is already committed by its dedicated store transaction
// and must not be appended a second time.
type Plan struct {
	Decision             Decision
	Apply                *store.ApplyConversationChangesRequest
	Rebaseline           *store.RebuildConversationBaselineRequest
	EventProjection      []store.MessageDraft
	CardTransitions      []CardTransition
	Audits               []store.AuditEntry
	Overlap              int
	Ambiguous            bool
	HistoricalThroughSeq int64
}

func (p *Plan) NeedsDeep() bool { return p != nil && p.Decision == DecisionNeedDeep }

// Reconcile computes a deterministic store plan. It never reads or writes the
// database; optimistic concurrency is carried by Apply.ExpectedTailSeq.
func Reconcile(in ReconcileInput) (*Plan, error) {
	if in.Key.Platform == "" || in.Key.AccountRef == "" || in.Key.ConversationRef == "" {
		return nil, ErrInvalidConversationKey
	}
	if err := validateLedger(in.Key, in.Ledger); err != nil {
		return nil, err
	}
	if in.Adopt && len(in.Ledger) != 0 {
		return nil, ErrAdoptionHasLedger
	}
	if in.Adopt && in.PlatformUserRef == "" {
		return nil, store.ErrPeerIdentityRequired
	}
	normalized := make([]NormalizedMessage, len(in.Snapshot))
	for i := range in.Snapshot {
		message, err := NormalizeMessage(in.Snapshot[i])
		if err != nil {
			return nil, fmt.Errorf("规范化快照消息[%d]: %w", i, err)
		}
		normalized[i] = message
	}
	if in.Adopt && len(normalized) == 0 {
		// 列表索引已证明该会话有 lastMessage；空 thread 与之矛盾。若把它
		// 收编为 boundary=0，下一轮恢复出的全部历史会被误投影成新增。
		return nil, ErrAdoptionSnapshotEmpty
	}

	ledgerKeys := keysFromLedger(in.Ledger)
	snapshotKeys := keysFromNormalized(normalized)
	if in.AnchorMatched {
		anchorStart := len(ledgerKeys) - 5
		if anchorStart < 0 {
			anchorStart = 0
		}
		// actor 下发的 anchorTail 恒为账本最近 ≤5 条。anchorMatched 是
		// 对这段完整序列的证词，不是“碰巧命中最后一条”的弱提示；先独立
		// 验证完整锚，再允许下面用更长/更短账本后缀做最大重叠裁决。
		if len(ledgerKeys) == 0 || !containsKeys(snapshotKeys, ledgerKeys[anchorStart:]) {
			return nil, ErrAnchorContractMismatch
		}
	}

	tail := ledgerTail(in.Ledger)
	baseApply := func(adopt bool, drafts []store.MessageDraft, changes []store.CardStateChange) *store.ApplyConversationChangesRequest {
		return &store.ApplyConversationChangesRequest{
			Key: in.Key, RoundID: in.RoundID, ExpectedTailSeq: tail,
			PlatformUserRef: in.PlatformUserRef, NewMessages: drafts,
			CardChanges: changes, Adopt: adopt, SyncedAt: in.SyncedAt,
		}
	}

	if len(in.Ledger) == 0 {
		drafts := draftsFrom(normalized)
		decision := DecisionAppend
		projection := append([]store.MessageDraft(nil), drafts...)
		historicalThrough := int64(0)
		if in.Adopt {
			decision = DecisionFirstAdoption
			projection = nil
			historicalThrough = int64(len(drafts))
		}
		return &Plan{
			Decision: decision, Apply: baseApply(in.Adopt, drafts, nil),
			EventProjection: projection, HistoricalThroughSeq: historicalThrough,
		}, nil
	}

	if len(normalized) == 0 {
		return &Plan{Decision: DecisionNoChange, Apply: baseApply(false, nil, nil)}, nil
	}

	matches := suffixSnapshotMatches(ledgerKeys, snapshotKeys)
	containedAt := contiguousMatches(ledgerKeys, snapshotKeys)
	tailContained := false
	for _, start := range containedAt {
		if start+len(snapshotKeys) == len(ledgerKeys) {
			tailContained = true
			break
		}
	}
	// 同文窗口可能同时具有两种解释：它完整存在于旧账本的非尾部，但账本的
	// 一个较短后缀又碰巧出现在窗口前部。后一解释会把窗口余部误投影成新增。
	// 若窗口也完整匹配当前尾部，则当前尾部证据更强，仍交给正常 NoChange /
	// 卡片更新路径；否则按“宁可漏、不可多投”裁成 stale，并响亮审计冲突。
	if len(matches) > 0 && len(containedAt) > 0 && !tailContained {
		ledgerStart := containedAt[len(containedAt)-1]
		changes, transitions, audits := collectCardChanges(in, normalized, ledgerStart, 0, len(normalized))
		plan := &Plan{
			Decision: DecisionStaleSnapshot, Apply: baseApply(false, nil, changes),
			CardTransitions: transitions, Audits: audits, Ambiguous: true,
		}
		plan.Audits = append(plan.Audits, audit(in, "conversation_stale_append_ambiguous",
			fmt.Sprintf("positions=%v suffixMatches=%v selected=%d", containedAt, matches, ledgerStart)))
		return plan, nil
	}
	if len(matches) > 0 {
		// 候选先按 overlap 降序、start 升序生成。最大 overlap 优先。
		// 同样长度时取最靠后的 start，只投影更短尾部。start=0 没有
		// 额外权威：手可以合法携带锚点之前的页面上下文，anchorMatched 也
		// 只是 bool。歧义无法消除时宁可漏掉同文消息，也不能把旧上下文
		// 重复投影成新业务事件。anchorTail 是止损提示，脑不能依赖手裁剪。
		selected := matches[0]
		maxMatches := 1
		for _, candidate := range matches[1:] {
			if candidate.length != selected.length {
				break
			}
			maxMatches++
			selected = candidate
		}
		overlap := selected.length
		ledgerStart := len(in.Ledger) - overlap
		changes, transitions, audits := collectCardChanges(in, normalized, ledgerStart, selected.start, overlap)
		drafts := draftsFrom(normalized[selected.start+overlap:])
		decision := DecisionAppend
		if len(drafts) == 0 && len(changes) == 0 {
			decision = DecisionNoChange
		}
		plan := &Plan{
			Decision: decision, Apply: baseApply(false, drafts, changes),
			EventProjection: append([]store.MessageDraft(nil), drafts...),
			CardTransitions: transitions, Audits: audits, Overlap: overlap,
		}
		if maxMatches > 1 {
			plan.Ambiguous = true
			plan.Audits = append(plan.Audits, audit(in, "conversation_alignment_ambiguous",
				fmt.Sprintf("matches=%v selected={overlap:%d start:%d}", matches, overlap, selected.start)))
		}
		if selected.start > 0 {
			plan.Audits = append(plan.Audits, audit(in, "conversation_alignment_context_discarded",
				fmt.Sprintf("discarded=%d overlap=%d", selected.start, overlap)))
		}
		if tailContained && len(containedAt) > 1 {
			plan.Ambiguous = true
			plan.Audits = append(plan.Audits, audit(in, "conversation_tail_alignment_ambiguous",
				fmt.Sprintf("positions=%v selected=tail", containedAt)))
		}
		return plan, nil
	}
	// anchorMatched 是手对“有界窗口内确实观察到脑下发锚尾”的强声明；手可
	// 合法保留锚前页面上下文或重复候选，所以脑已在上面搜索了任意连续片段。
	// 若仍找不到账本后缀，双方证词矛盾，绝不能降格成普通零重叠并在 deep 后
	// 重建基线，否则会重复追加已知消息并静默吞掉真正的新消息。
	if in.AnchorMatched {
		return nil, ErrAnchorContractMismatch
	}

	// A delayed result can be wholly contained before the current ledger tail.
	// Treat it as stale instead of requesting deep or moving the tail backwards.
	if len(containedAt) > 0 {
		ledgerStart := containedAt[len(containedAt)-1] // closest to tail, conservative for repeats.
		changes, transitions, audits := collectCardChanges(in, normalized, ledgerStart, 0, len(normalized))
		plan := &Plan{
			Decision: DecisionStaleSnapshot, Apply: baseApply(false, nil, changes),
			CardTransitions: transitions, Audits: audits,
		}
		if len(containedAt) > 1 {
			plan.Ambiguous = true
			plan.Audits = append(plan.Audits, audit(in, "conversation_stale_alignment_ambiguous",
				fmt.Sprintf("positions=%v selected=%d", containedAt, ledgerStart)))
		}
		return plan, nil
	}

	if !in.Deep {
		return &Plan{Decision: DecisionNeedDeep}, nil
	}
	if in.RoundID == "" {
		return nil, store.ErrHistoricalBaselineNoRound
	}

	// Deep zero-overlap is imported through a dedicated store transaction: bytes
	// are retained for future matching, EventProjection is empty, the round new
	// count stays unchanged, and the mandatory audit commits with the messages.
	drafts := draftsFrom(normalized)
	historicalThrough := tail + int64(len(drafts))
	auditDetail := fmt.Sprintf("reachedTop=%t anchorMatched=%t", in.ReachedTop, in.AnchorMatched)
	return &Plan{
		Decision: DecisionAuditedRebaseline,
		Rebaseline: &store.RebuildConversationBaselineRequest{
			Key: in.Key, RoundID: in.RoundID, ExpectedTailSeq: tail,
			PlatformUserRef: in.PlatformUserRef, Historical: drafts,
			SyncedAt: in.SyncedAt, AuditDetail: auditDetail,
		},
		Audits: []store.AuditEntry{audit(in, "conversation_zero_overlap_rebaseline",
			fmt.Sprintf("oldTail=%d imported=%d historicalThrough=%d reachedTop=%t anchorMatched=%t",
				tail, len(drafts), historicalThrough, in.ReachedTop, in.AnchorMatched))},
		HistoricalThroughSeq: historicalThrough,
	}, nil
}

type messageKey struct {
	direction string
	hash      string
}

func keysFromLedger(messages []store.Message) []messageKey {
	out := make([]messageKey, len(messages))
	for i := range messages {
		out[i] = messageKey{direction: messages[i].Direction, hash: messages[i].ContentHash}
	}
	return out
}

func keysFromNormalized(messages []NormalizedMessage) []messageKey {
	out := make([]messageKey, len(messages))
	for i := range messages {
		out[i] = messageKey{direction: messages[i].Direction, hash: messages[i].ContentHash}
	}
	return out
}

type overlapMatch struct {
	length int
	start  int
}

// suffixSnapshotMatches 枚举“账本后缀 == 快照任意连续片段”。快照可能
// 合法携带锚点之前的页面上下文；anchorTail 只是让手尽早停止滚动的提示，
// 不能成为脑侧对齐的正确性前提。
func suffixSnapshotMatches(ledger, snapshot []messageKey) []overlapMatch {
	max := len(ledger)
	if len(snapshot) < max {
		max = len(snapshot)
	}
	var out []overlapMatch
	for length := max; length >= 1; length-- {
		for start := 0; start+length <= len(snapshot); start++ {
			if equalKeys(ledger[len(ledger)-length:], snapshot[start:start+length]) {
				out = append(out, overlapMatch{length: length, start: start})
			}
		}
	}
	return out
}

func contiguousMatches(ledger, snapshot []messageKey) []int {
	if len(snapshot) == 0 || len(snapshot) > len(ledger) {
		return nil
	}
	var out []int
	for start := 0; start+len(snapshot) <= len(ledger); start++ {
		if equalKeys(ledger[start:start+len(snapshot)], snapshot) {
			out = append(out, start)
		}
	}
	return out
}

func equalKeys(a, b []messageKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsKeys(haystack, needle []messageKey) bool {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for start := 0; start+len(needle) <= len(haystack); start++ {
		if equalKeys(haystack[start:start+len(needle)], needle) {
			return true
		}
	}
	return false
}

func collectCardChanges(in ReconcileInput, snapshot []NormalizedMessage, ledgerStart, snapshotStart, count int) ([]store.CardStateChange, []CardTransition, []store.AuditEntry) {
	var changes []store.CardStateChange
	var transitions []CardTransition
	var audits []store.AuditEntry
	for offset := 0; offset < count; offset++ {
		old := in.Ledger[ledgerStart+offset]
		observed := snapshot[snapshotStart+offset]
		if old.Kind != "card" || observed.Kind != "card" || old.ContentHash != observed.ContentHash {
			continue
		}
		from := normalizedCardState(old.CardState)
		to := normalizedCardState(observed.CardState)
		if from == to {
			continue
		}
		if !forwardCardTransition(from, to) {
			audits = append(audits, audit(in, "card_state_regression_ignored",
				fmt.Sprintf("seq=%d hash=%s from=%s to=%s", old.Seq, old.ContentHash, from, to)))
			continue
		}
		changes = append(changes, store.CardStateChange{
			Seq: old.Seq, ContentHash: old.ContentHash, FromState: from, CardState: to,
		})
		transitions = append(transitions, CardTransition{
			Seq: old.Seq, ContentHash: old.ContentHash, CardType: old.CardType, From: from, To: to,
		})
	}
	return changes, transitions, audits
}

func normalizedCardState(state string) string {
	if state == "" {
		return "unknown"
	}
	return state
}

func forwardCardTransition(from, to string) bool {
	if from == to || to == "" || to == "unknown" {
		return false
	}
	switch from {
	case "", "unknown":
		return true
	case "pending":
		return to == "accepted" || to == "rejected" || to == "expired"
	default:
		// accepted/rejected/expired are terminal observations. A delayed
		// pending render or a conflicting terminal render never rolls them back.
		return false
	}
}

func draftsFrom(messages []NormalizedMessage) []store.MessageDraft {
	if len(messages) == 0 {
		return nil
	}
	out := make([]store.MessageDraft, len(messages))
	for i := range messages {
		out[i] = messages[i].draft()
	}
	return out
}

func ledgerTail(messages []store.Message) int64 {
	if len(messages) == 0 {
		return 0
	}
	return messages[len(messages)-1].Seq
}

func validateLedger(key store.ConversationKey, messages []store.Message) error {
	var previous int64
	for i := range messages {
		message := messages[i]
		if message.Platform != key.Platform || message.AccountRef != key.AccountRef || message.ConversationRef != key.ConversationRef {
			return fmt.Errorf("%w: seq=%d 身份键错配", ErrInvalidLedger, message.Seq)
		}
		if message.Seq <= 0 || message.ContentHash == "" || !validDirection(message.Direction) || !validKind(message.Kind) {
			return fmt.Errorf("%w: seq=%d 消息字段非法", ErrInvalidLedger, message.Seq)
		}
		if i > 0 && message.Seq != previous+1 {
			return fmt.Errorf("%w: seq=%d 不连续", ErrInvalidLedger, message.Seq)
		}
		previous = message.Seq
	}
	return nil
}

func audit(in ReconcileInput, category, detail string) store.AuditEntry {
	return store.AuditEntry{
		At: in.SyncedAt, Category: category, Platform: in.Key.Platform,
		AccountRef: in.Key.AccountRef, ConversationRef: in.Key.ConversationRef,
		RoundID: in.RoundID, Detail: detail,
	}
}

type Anchor struct {
	Direction   string
	ContentHash string
}

// AnchorTail derives the hand hint from the durable ledger. It carries no
// timestamp and never exceeds five entries.
func AnchorTail(messages []store.Message) []Anchor {
	start := len(messages) - 5
	if start < 0 {
		start = 0
	}
	out := make([]Anchor, len(messages)-start)
	for i := start; i < len(messages); i++ {
		out[i-start] = Anchor{Direction: messages[i].Direction, ContentHash: messages[i].ContentHash}
	}
	return out
}
