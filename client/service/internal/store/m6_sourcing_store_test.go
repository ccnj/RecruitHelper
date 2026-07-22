package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"recruithelper/contract/gen/go/protocol"
)

func seedSourcingCaptureProof(
	t *testing.T,
	s *Store,
	data protocol.CandidateReadSourcingResumeData,
	excluded []string,
) (AccountKey, string, string) {
	t.Helper()
	const (
		revisionHash = "revision-sourcing-capture"
		logicalID    = "logical-sourcing-capture"
	)
	if _, _, err := s.SaveJobAIContextRevision(contextRevisionFixture(
		"context-sourcing-capture", revisionHash, time.Now().Add(-time.Hour),
	)); err != nil {
		t.Fatal(err)
	}
	key := AccountKey{Platform: "zhilian", AccountRef: "account-sourcing-capture"}
	if err := s.CreateAccount(&Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := s.BindAccountPrincipal(key, "hand-sourcing", "principal-sourcing", "session-sourcing", "boot-sourcing", time.Now()); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	if err := s.MutateAccount(key, func(account *Account) error {
		account.SourcingEnabled = true
		account.SourcingContextRevisionHash = revisionHash
		account.SourcingStartedAt = &startedAt
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	argsRaw, _ := protocol.Encode(protocol.CandidateReadSourcingResumeArgs{ExcludePlatformUserRefs: excluded})
	contextRaw, _ := protocol.Encode(protocol.CmdContext{
		Platform: key.Platform, AccountRef: key.AccountRef,
		ExpectedPrincipalFingerprint: "principal-sourcing",
	})
	dataRaw, _ := protocol.Encode(data)
	resultRaw, _ := protocol.Encode(protocol.ResultBody{
		Ref: logicalID, Status: protocol.ResultStatusOk, Data: dataRaw, ExecMs: 1,
	})
	terminalAt := time.Now()
	if err := s.CreateCmd(&CmdRecord{
		MsgID: logicalID, LogicalDispatchID: logicalID,
		Name: protocol.PrimCandidateReadSourcingResume, Class: string(protocol.ClassIntrusive),
		Domain: key.Platform + ":" + key.AccountRef, Platform: key.Platform, AccountRef: key.AccountRef,
		ExpectedPrincipalFingerprint: "principal-sourcing", ContextJSON: string(contextRaw), Args: string(argsRaw),
		HandID: "hand-sourcing", Session: "session-sourcing", BootIDAtDispatch: "boot-sourcing",
		Status: CmdOk, ResultBody: string(resultRaw), TerminalAt: &terminalAt,
	}); err != nil {
		t.Fatal(err)
	}
	return key, revisionHash, logicalID
}

func TestCompleteSourcingCandidateRunPersistsProofBoundFactAndSafeStatus(t *testing.T) {
	s := openTest(t)
	displayName, positionTitle := "候选人敏感姓名", "敏感职位标题"
	data := protocol.CandidateReadSourcingResumeData{
		PlatformUserRef: "opaque-sensitive-user-ref", DisplayName: &displayName,
		PositionRef: "opaque-sensitive-position-ref", PositionTitle: &positionTitle,
		ContactState: protocol.CandidateContactStateUnestablished, ObservedAt: time.Now().UnixMilli(),
		Basic:        []protocol.CandidateResumeLabelValue{{Label: "姓名", Value: "简历敏感正文"}},
		Expectations: []protocol.CandidateResumeLabelValue{}, SelfEvaluation: "自评敏感正文",
		Education: "教育敏感正文", WorkExperiences: "经历敏感正文",
	}
	key, revisionHash, logicalID := seedSourcingCaptureProof(t, s, data, []string{"another-user"})
	run, err := s.CompleteSourcingCandidateRun(CompleteSourcingCandidateRunRequest{
		RunID: "run-sourcing-one", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, LogicalDispatchID: logicalID, Data: data,
	})
	if err != nil || run.RunID != "run-sourcing-one" || run.ContentHash == "" || !strings.Contains(run.ResumeJSON, "简历敏感正文") {
		t.Fatalf("采集事实收编失败: run=%+v err=%v", run, err)
	}
	replayed, err := s.CompleteSourcingCandidateRun(CompleteSourcingCandidateRunRequest{
		RunID: "ignored-replay-run", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, LogicalDispatchID: logicalID, Data: data,
	})
	if err != nil || replayed.RunID != run.RunID {
		t.Fatalf("同 logical 重放未幂等复用: replayed=%+v err=%v", replayed, err)
	}

	refs, err := s.SourcingExcludedPlatformUserRefs(key, revisionHash, 32)
	if err != nil || len(refs) != 1 || refs[0] != data.PlatformUserRef {
		t.Fatalf("脑内排除列表错误: refs=%+v err=%v", refs, err)
	}
	startedAt := time.Now()
	reservation, err := s.ReserveSourcingScore(ReserveSourcingScoreRequest{
		InvocationID: "score-safe-status", RunID: run.RunID,
		ContextRevisionHash: run.ContextRevisionHash, RunContentHash: run.ContentHash,
		Provider: "fixture-provider", Model: "fixture-model", InputHash: "fixture-input-hash",
		StartedAt: startedAt,
	})
	if err != nil || reservation == nil || !reservation.Created {
		t.Fatalf("评分预留失败: reservation=%+v err=%v", reservation, err)
	}
	zero := 0
	if _, err := s.CompleteSourcingScore(CompleteSourcingScoreRequest{
		Completion: AIInvocationCompletion{
			InvocationID: reservation.Invocation.InvocationID, Status: AIInvocationOK,
			OutputHash: "fixture-output-hash", InputTokens: 4, OutputTokens: 2,
			ReasoningTokens: &zero, UsageShape: AIInvocationUsageComplete,
			ReasoningContentEmpty: true, EstimatedCostMicros: 1, FinishedAt: startedAt.Add(time.Second),
		},
		Score: scorePointer(8),
	}); err != nil {
		t.Fatal(err)
	}
	status, err := s.AccountSourcingStatus(key)
	if err != nil || status == nil || status.CaptureCount != 1 || status.Latest == nil ||
		status.Latest.ContentHash != run.ContentHash || status.Latest.Score == nil ||
		status.Latest.Score.Score == nil || *status.Latest.Score.Score != 8 {
		t.Fatalf("安全状态投影错误: status=%+v err=%v", status, err)
	}
	statusRaw, _ := json.Marshal(status)
	for _, forbidden := range []string{
		data.PlatformUserRef, data.PositionRef, displayName, positionTitle,
		"简历敏感正文", "自评敏感正文", "教育敏感正文", "经历敏感正文",
	} {
		if strings.Contains(string(statusRaw), forbidden) {
			t.Fatalf("管理状态投影泄漏候选人 PII %q: %s", forbidden, statusRaw)
		}
	}
}

func TestCompleteSourcingCandidateRunRejectsCallerDataDifferentFromPersistedResult(t *testing.T) {
	s := openTest(t)
	data := protocol.CandidateReadSourcingResumeData{
		PlatformUserRef: "user-proof", PositionRef: "position-proof",
		ContactState: protocol.CandidateContactStateUnestablished, ObservedAt: time.Now().UnixMilli(),
		Basic: []protocol.CandidateResumeLabelValue{}, Expectations: []protocol.CandidateResumeLabelValue{},
		SelfEvaluation: "", Education: "", WorkExperiences: "",
	}
	key, revisionHash, logicalID := seedSourcingCaptureProof(t, s, data, nil)
	data.Education = "caller-forged"
	_, err := s.CompleteSourcingCandidateRun(CompleteSourcingCandidateRunRequest{
		RunID: "run-forged", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, LogicalDispatchID: logicalID, Data: data,
	})
	if err != ErrSourcingConflict {
		t.Fatalf("调用方 data 与持久 result 不同必须拒绝: %v", err)
	}
}
