package store

import (
	"strings"
	"testing"
)

// 本文件是 bnd-v1 输入边界指纹(协议 §7.4,2026-08-27 停机点第二步)的
// 性质测试:同世界同键、重算恒键、尾条等价、裁决代次区分、存量无身份行
// seq 兜底、来聊根变体区分。指纹是 idemKey 派生链的根,这些性质是
// "账本闸拒绝后重算不出第二个键"安全论证的机器化形态。

func fingerprintTestMessage(seq int64, direction, kind, hash string, sourceKey string) Message {
	m := Message{Seq: seq, Direction: direction, Kind: kind, ContentHash: hash}
	if sourceKey != "" {
		key := sourceKey
		m.SourceKey = &key
	}
	return m
}

func TestBoundaryFingerprintDeterministicAcrossRecomputation(t *testing.T) {
	anchor := fingerprintTestMessage(1, "out", "text", "greeting-hash", "")
	inbound := []Message{
		fingerprintTestMessage(2, "in", "text", "hash-a", "sk-a"),
		fingerprintTestMessage(3, "in", "text", "hash-b", "sk-b"),
	}
	digest1, turnID1, err1 := DialogueTurnIdentity("profile-fp", anchor, inbound, 0)
	digest2, turnID2, err2 := DialogueTurnIdentity("profile-fp", anchor, inbound, 0)
	if err1 != nil || err2 != nil || digest1 != digest2 || turnID1 != turnID2 {
		t.Fatalf("同世界重算必须恒同键: %q/%q vs %q/%q err=%v/%v",
			digest1, turnID1, digest2, turnID2, err1, err2)
	}
	if !strings.HasPrefix(turnID1, "bnd-v1-") || len(digest1) != 64 {
		t.Fatalf("版本域与 digest 形态不符: turnID=%q digest=%q", turnID1, digest1)
	}
}

func TestBoundaryFingerprintAnchorsOnlyTailIdentity(t *testing.T) {
	// 同尾条 = 同边界 = 同键(多条新输入并一响应);中段成员内容、条数、
	// 出站锚内容都不入键——文案可变位置不变(决策 2:重发=重新规划)。
	anchorA := fingerprintTestMessage(1, "out", "text", "greeting-hash", "")
	anchorB := fingerprintTestMessage(1, "out", "text", "different-greeting-hash", "")
	tail := fingerprintTestMessage(5, "in", "text", "tail-hash", "sk-tail")
	short := []Message{tail}
	long := []Message{
		fingerprintTestMessage(2, "in", "text", "hash-a", "sk-a"),
		fingerprintTestMessage(3, "in", "card", "hash-b", "sk-b"),
		tail,
	}
	digestShort, _, err1 := DialogueTurnIdentity("profile-fp", anchorA, short, 0)
	digestLong, _, err2 := DialogueTurnIdentity("profile-fp", anchorB, long, 0)
	if err1 != nil || err2 != nil || digestShort != digestLong {
		t.Fatalf("同尾条必须同键: %q vs %q err=%v/%v", digestShort, digestLong, err1, err2)
	}
	otherTail := []Message{fingerprintTestMessage(6, "in", "text", "tail-hash", "sk-other")}
	digestOther, _, err3 := DialogueTurnIdentity("profile-fp", anchorA, otherTail, 0)
	if err3 != nil || digestOther == digestShort {
		t.Fatalf("异尾条必须异键: %q vs %q err=%v", digestOther, digestShort, err3)
	}
}

func TestBoundaryFingerprintVerdictGenerationSeparatesReplans(t *testing.T) {
	anchor := fingerprintTestMessage(1, "out", "text", "greeting-hash", "")
	inbound := []Message{fingerprintTestMessage(2, "in", "text", "hash-a", "sk-a")}
	gen0, _, err0 := DialogueTurnIdentity("profile-fp", anchor, inbound, 0)
	gen1, _, err1 := DialogueTurnIdentity("profile-fp", anchor, inbound, 1)
	if err0 != nil || err1 != nil || gen0 == gen1 {
		t.Fatalf("裁决代次必须区分重规划键: %q vs %q err=%v/%v", gen0, gen1, err0, err1)
	}
	if _, _, err := DialogueTurnIdentity("profile-fp", anchor, inbound, -1); err == nil {
		t.Fatal("负代次必须拒绝")
	}
}

func TestBoundaryFingerprintFallsBackToSeqForLegacyRows(t *testing.T) {
	// 2026-08-09 前收编的存量行可能无 sourceKey(立案 C3):以账本 seq
	// 确定性兜底,不转人工;有身份行与兜底行键必须互异。
	anchor := fingerprintTestMessage(1, "out", "text", "greeting-hash", "")
	legacy := []Message{fingerprintTestMessage(2, "in", "text", "hash-a", "")}
	digestLegacy1, _, err1 := DialogueTurnIdentity("profile-fp", anchor, legacy, 0)
	digestLegacy2, _, err2 := DialogueTurnIdentity("profile-fp", anchor, legacy, 0)
	if err1 != nil || err2 != nil || digestLegacy1 != digestLegacy2 {
		t.Fatalf("seq 兜底必须同样确定: %q vs %q err=%v/%v", digestLegacy1, digestLegacy2, err1, err2)
	}
	keyed := []Message{fingerprintTestMessage(2, "in", "text", "hash-a", "sk-a")}
	digestKeyed, _, err3 := DialogueTurnIdentity("profile-fp", anchor, keyed, 0)
	if err3 != nil || digestKeyed == digestLegacy1 {
		t.Fatalf("身份记号与 seq 兜底不得撞键: %q vs %q err=%v", digestKeyed, digestLegacy1, err3)
	}
}

func TestBoundaryFingerprintInboundRootVariantIsDistinct(t *testing.T) {
	rootRef, err := InboundConversationV4RootRef(
		"zhilian", "acct", "conv", strings.Repeat("ab", 32),
	)
	if err != nil {
		t.Fatal(err)
	}
	inbound := []Message{fingerprintTestMessage(2, "in", "text", "hash-a", "sk-a")}
	rootDigest, rootTurnID, err1 := DialogueTurnIdentityFromInboundRoot("profile-fp", rootRef, inbound, 0)
	anchor := fingerprintTestMessage(1, "out", "text", "greeting-hash", "")
	anchoredDigest, _, err2 := DialogueTurnIdentity("profile-fp", anchor, inbound, 0)
	if err1 != nil || err2 != nil || rootDigest == anchoredDigest {
		t.Fatalf("来聊根变体必须与出站锚变体异键: %q vs %q err=%v/%v",
			rootDigest, anchoredDigest, err1, err2)
	}
	if !strings.HasPrefix(rootTurnID, "bnd-v1-") {
		t.Fatalf("来聊根变体版本域不符: %q", rootTurnID)
	}
}
