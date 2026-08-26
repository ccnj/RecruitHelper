package syncledger

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"recruithelper/client/service/internal/store"
)

var (
	ErrInvalidConversationKey                = errors.New("会话正式键不完整")
	ErrInvalidLedger                         = errors.New("持久化会话账本无效")
	ErrAdoptionHasLedger                     = errors.New("首次收编时账本必须为空")
	ErrAdoptionSnapshotEmpty                 = errors.New("首次收编快照不能为空")
	ErrTrackedSnapshotEmpty                  = errors.New("已收编会话快照为空,与非空账本矛盾")
	ErrAnchorContractMismatch                = errors.New("手报告 anchorMatched 但快照中找不到完整账本锚尾")
	ErrSourceKeySemanticConflict             = errors.New("相同 sourceKey 的消息语义冲突")
	ErrUnsafeMessageClassificationCorrection = errors.New("发现可能的消息分类修正，但缺少完整唯一证据")
	ErrSnapshotIdentityMissing               = errors.New("快照消息缺少服务端稳定身份")
)

type Decision string

const (
	DecisionFirstAdoption            Decision = "firstAdoption"
	DecisionAppend                   Decision = "append"
	DecisionNoChange                 Decision = "noChange"
	DecisionNeedDeep                 Decision = "needDeep"
	DecisionAuditedRebaseline        Decision = "auditedRebaseline"
	DecisionClassificationCorrection Decision = "classificationCorrection"
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
	Correction           *store.CorrectMessageClassificationRequest
	EventProjection      []store.MessageDraft
	CardTransitions      []CardTransition
	Audits               []store.AuditEntry
	HistoricalThroughSeq int64
}

func (p *Plan) NeedsDeep() bool { return p != nil && p.Decision == DecisionNeedDeep }

// Reconcile computes a deterministic store plan. It never reads or writes the
// database; optimistic concurrency is carried by Apply.ExpectedTailSeq.
//
// 2026-08-09 身份判新换根(战役 S2):判新的唯一机制是服务端消息身份集合——
// 快照行的 sourceKey 不在账本即为新,按页面顺序追加尾部;无身份的自家账本行
// (乐观判定、人工裁决产物)按语义相符、发生顺序回配身份,不重复收编。
// 2026-08-26 S3 落日:旧位置对齐机器与影子对拍已拆除(16 天 6 客户 615 条
// 影子分歧全部为旧引擎错误或多余,新引擎错误方向零条,甲方裁决);规格见
// 《协议规格-v1》§12.4。
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
	// 能力门槛(2026-08-09 甲方确认):每行必须携带服务端消息身份,取不到即
	// 整读失败。失效方向是"读不到→不确认→转人工",不是退回位置猜测。
	for i := range normalized {
		if normalized[i].SourceKey == "" {
			return nil, fmt.Errorf("%w: 快照第 %d 行", ErrSnapshotIdentityMissing, i)
		}
	}

	ledgerKeys := keysFromLedger(in.Ledger)
	snapshotKeys := keysFromNormalized(normalized)
	if err := validateSourceKeySemantics(ledgerKeys, snapshotKeys); err != nil {
		return nil, err
	}
	if correction, detected, err := classificationCorrectionPlan(in, normalized, ledgerKeys, snapshotKeys); err != nil {
		return nil, err
	} else if detected {
		return correction, nil
	}

	tail := ledgerTail(in.Ledger)
	baseApply := func(adopt bool, drafts []store.MessageDraft, changes []store.CardStateChange, reclaims []store.SourceKeyReclaim) *store.ApplyConversationChangesRequest {
		return &store.ApplyConversationChangesRequest{
			Key: in.Key, RoundID: in.RoundID, ExpectedTailSeq: tail,
			PlatformUserRef: in.PlatformUserRef, NewMessages: drafts,
			CardChanges: changes, SourceKeyReclaims: reclaims, Adopt: adopt, SyncedAt: in.SyncedAt,
		}
	}

	if len(in.Ledger) == 0 {
		drafts := draftsFrom(dedupeByIdentity(normalized, snapshotKeys))
		decision := DecisionAppend
		projection := append([]store.MessageDraft(nil), drafts...)
		historicalThrough := int64(0)
		if in.Adopt {
			decision = DecisionFirstAdoption
			projection = nil
			historicalThrough = int64(len(drafts))
		}
		return &Plan{
			Decision: decision, Apply: baseApply(in.Adopt, drafts, nil, nil),
			EventProjection: projection, HistoricalThroughSeq: historicalThrough,
		}, nil
	}

	if len(normalized) == 0 {
		// 消息不会从平台消失:账本非空的已收编会话读回整窗空快照,只能是
		// 感知通道退化(真机 2026-07-28:IM 页刚导航出来的同步窗口内,平台
		// 历史接口对明明有消息的会话返回空成功)。健康的"无新增"读取至少
		// 包含锚点前缀,不会为空;消化成 NoChange 会把陈旧基线放行给排程。
		return nil, ErrTrackedSnapshotEmpty
	}

	// 身份集合:账本已知身份的索引 + 无身份行按语义分桶的回配池(发生顺序)。
	ledgerBySource := make(map[string]int, len(in.Ledger))
	nullPool := make(map[messageKey][]int)
	for i := range in.Ledger {
		if in.Ledger[i].SourceKey != nil {
			ledgerBySource[*in.Ledger[i].SourceKey] = i
			continue
		}
		semantic := ledgerKeys[i]
		semantic.sourceKey = ""
		nullPool[semantic] = append(nullPool[semantic], i)
	}

	// 同窗身份去重,保序取首现。
	order := make([]int, 0, len(normalized))
	seen := make(map[string]struct{}, len(normalized))
	for i := range normalized {
		if _, duplicate := seen[snapshotKeys[i].sourceKey]; duplicate {
			// 跨页聚合可能重复呈现同一条服务端消息;同一身份只处理一次。
			continue
		}
		seen[snapshotKeys[i].sourceKey] = struct{}{}
		order = append(order, i)
	}

	// 全 NULL 账本(存量升级、仅招呼的会话)的身份自举:把整个账本按语义
	// 右端优先对齐到窗口——末行绑窗口中最靠后的同语义行,依次向前;绑不满
	// 整个账本就不豁免,走浅读→深读梯。右端优先防止窗口带出的更老同文历史
	// 抢走 NULL 行身份、再把陈年历史解锁成"新消息"(S2 审查实证的阻断路径:
	// 一旦历史行抢先入桶,linked>0 会解除窗口定界,已收编消息的真身随后被
	// 二次收编并投影)。
	var bootstrapBinding map[int]int
	bootstrapFirst := -1
	if len(ledgerBySource) == 0 && len(in.Ledger) > 0 {
		binding := make(map[int]int, len(in.Ledger))
		ledgerIdx := len(in.Ledger) - 1
		for pos := len(order) - 1; pos >= 0 && ledgerIdx >= 0; pos-- {
			semantic := snapshotKeys[order[pos]]
			semantic.sourceKey = ""
			ledgerSemantic := ledgerKeys[ledgerIdx]
			ledgerSemantic.sourceKey = ""
			if semantic == ledgerSemantic {
				binding[pos] = ledgerIdx
				ledgerIdx--
			}
		}
		if ledgerIdx < 0 {
			bootstrapBinding = binding
			for pos := range binding {
				if bootstrapFirst == -1 || pos < bootstrapFirst {
					bootstrapFirst = pos
				}
			}
		}
	}

	var drafts []store.MessageDraft
	var reclaims []store.SourceKeyReclaim
	var changes []store.CardStateChange
	var transitions []CardTransition
	var audits []store.AuditEntry
	linked := 0
	for pos, i := range order {
		key := snapshotKeys[i]
		if ledgerIdx, ok := ledgerBySource[key.sourceKey]; ok {
			linked++
			collectCardChangeByIdentity(in, in.Ledger[ledgerIdx], normalized[i], &changes, &transitions, &audits)
			continue
		}
		if bootstrapBinding != nil {
			if ledgerIdx, ok := bootstrapBinding[pos]; ok {
				reclaims = append(reclaims, store.SourceKeyReclaim{
					Seq: in.Ledger[ledgerIdx].Seq, SourceKey: key.sourceKey,
				})
				linked++
				collectCardChangeByIdentity(in, in.Ledger[ledgerIdx], normalized[i], &changes, &transitions, &audits)
				continue
			}
			if pos < bootstrapFirst {
				// 自举对齐起点之前的未知行是窗口外历史,与混合账本的窗口
				// 定界同款,不收编、不投影。
				continue
			}
			drafts = append(drafts, normalized[i].draft())
			continue
		}
		semantic := key
		semantic.sourceKey = ""
		// 回配限定在首个身份关联之后:窗口外的更老历史里可能有与自家行
		// 同文的旧消息,不设此限会让它抢走 NULL 行的身份、再把真正的自家行
		// 错判为新。
		if pool := nullPool[semantic]; len(pool) > 0 && linked > 0 {
			ledgerIdx := pool[0]
			nullPool[semantic] = pool[1:]
			reclaims = append(reclaims, store.SourceKeyReclaim{
				Seq: in.Ledger[ledgerIdx].Seq, SourceKey: key.sourceKey,
			})
			linked++
			collectCardChangeByIdentity(in, in.Ledger[ledgerIdx], normalized[i], &changes, &transitions, &audits)
			continue
		}
		if linked == 0 {
			// 窗口定界(保留项):首个身份关联之前的未知行,是手合法携带的
			// 锚点之前更老历史,不是"自上次观察以来的新消息"——不收编、
			// 不投影,每轮原样跳过。导入更老历史的唯一通道是审计重建梯。
			continue
		}
		drafts = append(drafts, normalized[i].draft())
	}

	if linked == 0 {
		// 窗口与账本零身份关联。anchorMatched 声称手在窗口内看到了脑下发的
		// 锚尾——但锚尾只有 direction+hash,账本锚尾若全是无身份自家行,
		// 手按同文命中并不与零身份关联矛盾(NULL 行的真身本就配不上身份),
		// 此时按普通零关联走浅读→深读梯,深读窗口触及更早带身份行即收敛。
		// 只有锚尾至少一行带身份时,零关联才是真正的证词冲突,必须响亮停住。
		if in.AnchorMatched && anchorTailCarriesIdentity(in.Ledger) {
			return nil, ErrAnchorContractMismatch
		}
		if !in.Deep {
			return &Plan{Decision: DecisionNeedDeep}, nil
		}
		if in.RoundID == "" {
			return nil, store.ErrHistoricalBaselineNoRound
		}
		historicalDrafts := draftsFrom(dedupeByIdentity(normalized, snapshotKeys))
		historicalThrough := tail + int64(len(historicalDrafts))
		auditDetail := fmt.Sprintf("reachedTop=%t anchorMatched=%t", in.ReachedTop, in.AnchorMatched)
		plan := &Plan{
			Decision: DecisionAuditedRebaseline,
			Rebaseline: &store.RebuildConversationBaselineRequest{
				Key: in.Key, RoundID: in.RoundID, ExpectedTailSeq: tail,
				PlatformUserRef: in.PlatformUserRef, Historical: historicalDrafts,
				SyncedAt: in.SyncedAt, AuditDetail: auditDetail,
			},
			Audits: []store.AuditEntry{audit(in, "conversation_zero_overlap_rebaseline",
				fmt.Sprintf("oldTail=%d imported=%d historicalThrough=%d reachedTop=%t anchorMatched=%t",
					tail, len(historicalDrafts), historicalThrough, in.ReachedTop, in.AnchorMatched))},
			HistoricalThroughSeq: historicalThrough,
		}
		return plan, nil
	}

	decision := DecisionAppend
	if len(drafts) == 0 && len(changes) == 0 {
		decision = DecisionNoChange
	}
	plan := &Plan{
		Decision: decision, Apply: baseApply(false, drafts, changes, reclaims),
		EventProjection: append([]store.MessageDraft(nil), drafts...),
		CardTransitions: transitions, Audits: audits,
	}
	if len(reclaims) > 0 {
		seqs := make([]int64, len(reclaims))
		for i := range reclaims {
			seqs[i] = reclaims[i].Seq
		}
		plan.Audits = append(plan.Audits, audit(in, "message_source_key_reclaimed",
			fmt.Sprintf("seqs=%v", seqs)))
	}
	return plan, nil
}

// anchorTailCarriesIdentity 判定账本锚尾(最近 ≤5 行,与 AnchorTail 同窗)
// 是否至少一行携带服务端身份。零身份关联下只有这种锚尾才构成证词矛盾。
func anchorTailCarriesIdentity(ledger []store.Message) bool {
	start := len(ledger) - 5
	if start < 0 {
		start = 0
	}
	for i := start; i < len(ledger); i++ {
		if ledger[i].SourceKey != nil {
			return true
		}
	}
	return false
}

// dedupeByIdentity 按身份去掉跨页聚合的重复行,保留首次出现的页面顺序。
// 能力门槛保证每行有身份;首收编与重建梯不经主循环的 seen 去重,必须在
// 这里补同一保证,否则跨页重复行会被双收编(性质测试实证)。
func dedupeByIdentity(normalized []NormalizedMessage, keys []messageKey) []NormalizedMessage {
	seen := make(map[string]struct{}, len(normalized))
	out := make([]NormalizedMessage, 0, len(normalized))
	for i := range normalized {
		if _, duplicate := seen[keys[i].sourceKey]; duplicate {
			continue
		}
		seen[keys[i].sourceKey] = struct{}{}
		out = append(out, normalized[i])
	}
	return out
}

// collectCardChangeByIdentity 按身份配对后的卡片状态跃迁(战役拍板 4:配对
// 不再依赖位置偏移)。同 key 的 hash/cardType 相等已由 validateSourceKeySemantics
// 保证,这里的守卫只是防御。
func collectCardChangeByIdentity(
	in ReconcileInput,
	old store.Message,
	observed NormalizedMessage,
	changes *[]store.CardStateChange,
	transitions *[]CardTransition,
	audits *[]store.AuditEntry,
) {
	if old.Kind != "card" || observed.Kind != "card" || old.ContentHash != observed.ContentHash {
		return
	}
	from := normalizedCardState(old.CardState)
	to := normalizedCardState(observed.CardState)
	if from == to {
		return
	}
	if !forwardCardTransition(from, to) {
		*audits = append(*audits, audit(in, "card_state_regression_ignored",
			fmt.Sprintf("seq=%d hash=%s from=%s to=%s", old.Seq, old.ContentHash, from, to)))
		return
	}
	*changes = append(*changes, store.CardStateChange{
		Seq: old.Seq, ContentHash: old.ContentHash, FromState: from, CardState: to,
	})
	*transitions = append(*transitions, CardTransition{
		Seq: old.Seq, ContentHash: old.ContentHash, CardType: old.CardType, From: from, To: to,
	})
}

// classificationCorrectionPlan 只承认一个已被真机证明的狭窄形态：
// 旧活动账本尾是被误归类的 system/system，完整页面快照可在账本之前
// 携带若干历史上下文，但只能在快照唯一尾行给出带稳定等值键的
// in/text。内部候选只用于发现歧义并停住，绝不授权修正中间历史行。
// 一旦候选呈现该形态，其他证据不全就必须响亮停住，不得降格进入
// deep/rebaseline。
func classificationCorrectionPlan(
	in ReconcileInput,
	normalized []NormalizedMessage,
	ledgerKeys, snapshotKeys []messageKey,
) (*Plan, bool, error) {
	if len(in.Ledger) == 0 {
		return nil, false, nil
	}
	old := in.Ledger[len(in.Ledger)-1]
	last := len(in.Ledger) - 1
	if old.Direction != "system" || old.Kind != "system" || old.Origin != "external" || old.SourceKey != nil {
		return nil, false, nil
	}
	unsafe := func() (*Plan, bool, error) {
		return nil, true, ErrUnsafeMessageClassificationCorrection
	}

	// 账本只有误分类尾行时没有可用于定位的前缀；此时只扫描实际呈现的
	// correction skeleton，不从空前缀推导任意位置的“缺尾”。
	prefix := ledgerKeys[:last]
	candidates := make([]int, 0, 1)
	missingExpectedTail := false
	if len(prefix) == 0 {
		for index := range normalized {
			if classificationCorrectionSkeleton(old, normalized[index]) {
				candidates = append(candidates, index)
			}
		}
	} else {
		for start := 0; start+len(prefix) <= len(snapshotKeys); start++ {
			if !equalKeys(prefix, snapshotKeys[start:start+len(prefix)]) {
				continue
			}
			observedIndex := start + len(prefix)
			if observedIndex == len(normalized) {
				missingExpectedTail = true
				continue
			}
			if classificationCorrectionSkeleton(old, normalized[observedIndex]) {
				candidates = append(candidates, observedIndex)
				continue
			}
			// 前缀之后只剩唯一快照尾，但它既不是旧 system 尾，也不是
			// correction skeleton：这是真机故障形态的“预期尾被替换”，不得重建基线。
			if observedIndex == len(normalized)-1 &&
				!equalMessageKey(ledgerKeys[last], snapshotKeys[observedIndex]) {
				missingExpectedTail = true
			}
		}
	}

	if missingExpectedTail || len(candidates) > 1 {
		return unsafe()
	}
	if len(candidates) == 0 {
		return nil, false, nil
	}
	observedIndex := candidates[0]
	if observedIndex != len(normalized)-1 {
		return unsafe()
	}
	observed := normalized[observedIndex]
	if in.Adopt || in.RoundID == "" || !in.ReachedTop || observed.SourceKey == "" ||
		old.TsApproxMs == nil || observed.TsApproxMs == nil || *old.TsApproxMs != *observed.TsApproxMs ||
		old.Text == nil || observed.Text == nil ||
		NormalizeText(*old.Text) == "" || NormalizeText(*old.Text) != NormalizeText(*observed.Text) ||
		old.BlobRef != "" || old.CardType != "" || old.CardState != "" ||
		observed.BlobRef != "" || observed.CardType != "" || observed.CardState != "" {
		return unsafe()
	}
	return &Plan{
		Decision: DecisionClassificationCorrection,
		Correction: &store.CorrectMessageClassificationRequest{
			Key: in.Key, RoundID: in.RoundID, ExpectedTailSeq: old.Seq, OldSeq: old.Seq,
			Corrected: observed.draft(), SyncedAt: in.SyncedAt,
		},
	}, true, nil
}

func classificationCorrectionSkeleton(old store.Message, observed NormalizedMessage) bool {
	return observed.Direction == "in" && observed.Kind == "text" && observed.Origin == "external" &&
		old.ContentHash == observed.ContentHash
}

type messageKey struct {
	direction string
	kind      string
	hash      string
	cardType  string
	interview string
	sourceKey string
}

func keysFromLedger(messages []store.Message) []messageKey {
	out := make([]messageKey, len(messages))
	for i := range messages {
		sourceKey := ""
		if messages[i].SourceKey != nil {
			sourceKey = *messages[i].SourceKey
		}
		out[i] = messageKey{
			direction: messages[i].Direction, kind: messages[i].Kind, hash: messages[i].ContentHash,
			cardType: messages[i].CardType,
			interview: interviewSignature(
				messages[i].InterviewStartsAtMs,
				messages[i].InterviewEndsAtMs,
				messages[i].InterviewMethod,
			),
			sourceKey: sourceKey,
		}
	}
	return out
}

func keysFromNormalized(messages []NormalizedMessage) []messageKey {
	out := make([]messageKey, len(messages))
	for i := range messages {
		out[i] = messageKey{
			direction: messages[i].Direction, kind: messages[i].Kind, hash: messages[i].ContentHash,
			cardType: messages[i].CardType,
			interview: interviewSignature(
				messages[i].InterviewStartsAtMs,
				messages[i].InterviewEndsAtMs,
				messages[i].InterviewMethod,
			),
			sourceKey: messages[i].SourceKey,
		}
	}
	return out
}

func interviewSignature(startsAtMs, endsAtMs *int64, method *string) string {
	if startsAtMs == nil && endsAtMs == nil && method == nil {
		return ""
	}
	starts, ends, methodValue := "<nil>", "<nil>", "<nil>"
	if startsAtMs != nil {
		starts = strconv.FormatInt(*startsAtMs, 10)
	}
	if endsAtMs != nil {
		ends = strconv.FormatInt(*endsAtMs, 10)
	}
	if method != nil {
		methodValue = *method
	}
	return starts + "\x1f" + ends + "\x1f" + methodValue
}

// validateSourceKeySemantics enforces the scope-local stable identity claim
// before any overlap, append, or rebaseline decision. The opaque key itself is
// deliberately absent from errors and audits.
func validateSourceKeySemantics(groups ...[]messageKey) error {
	type semantic struct {
		direction string
		kind      string
		hash      string
		cardType  string
	}
	seen := make(map[string]semantic)
	for _, keys := range groups {
		for _, key := range keys {
			if key.sourceKey == "" {
				continue
			}
			// system 行不作语义证词(§4.5,2026-08-11 甲方裁决):其
			// text/contentHash 由手侧兜底拼装,随解析实现演进而变化属
			// 预期,不构成平台事实变化的证据。跳过登记即达成"任一侧为
			// system 的同 key 行对不参与冲突判定";该行仍凭 sourceKey
			// 参与去重与已知身份判定(引擎主体不经本表)。
			if key.kind == "system" {
				continue
			}
			current := semantic{
				direction: key.direction, kind: key.kind, hash: key.hash,
				cardType: key.cardType,
			}
			if previous, ok := seen[key.sourceKey]; ok && previous != current {
				return ErrSourceKeySemanticConflict
			}
			seen[key.sourceKey] = current
		}
	}
	return nil
}

func equalKeys(a, b []messageKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !equalMessageKey(a[i], b[i]) {
			return false
		}
	}
	return true
}

func equalMessageKey(a, b messageKey) bool {
	if a.direction != b.direction || a.kind != b.kind || a.hash != b.hash ||
		a.cardType != b.cardType || a.interview != b.interview {
		return false
	}
	// A stable key is authoritative only when both observations have one.
	// Legacy/null observations remain compatible through direction+contentHash.
	if a.sourceKey != "" && b.sourceKey != "" {
		return a.sourceKey == b.sourceKey
	}
	return true
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
		if message.SourceKey != nil && !validSourceKey(*message.SourceKey) {
			return fmt.Errorf("%w: seq=%d sourceKey 非法", ErrInvalidLedger, message.Seq)
		}
		if err := validateInterviewProjection(SnapshotMessage{
			Kind: message.Kind, CardType: message.CardType,
			InterviewStartsAtMs: message.InterviewStartsAtMs,
			InterviewEndsAtMs:   message.InterviewEndsAtMs,
			InterviewMethod:     message.InterviewMethod,
		}); err != nil {
			return fmt.Errorf("%w: seq=%d 邀面参数非法", ErrInvalidLedger, message.Seq)
		}
		// 被更强证据推翻的消息事实仍保留物理 seq，但不进入活动
		// 账本，因而活动视图允许有洞。序号仍必须严格递增，防止乱序或重号。
		if i > 0 && message.Seq <= previous {
			return fmt.Errorf("%w: seq=%d 未严格递增", ErrInvalidLedger, message.Seq)
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
