package store

import (
	"sort"
	"strconv"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
)

func sourcingSelectionRevision(at time.Time, minScore int) m5ai.ContextRevision {
	documents := []m5ai.JobConfigDocument{
		{DocType: "候选人筛选", Content: `{"minScore":` + strconv.Itoa(minScore) + `}`},
		{DocType: "多轮沟通", Content: "reply"},
		{DocType: "客户事实库", Content: "facts"},
		{DocType: "意向判断", Content: "intent"},
		{DocType: "打分", Content: "score {resume_json}"},
		{DocType: "招呼语", Content: `{"prompt":"{career_state} {resume_summary_json}"}`},
		{DocType: "职位筛选", Content: `[]`},
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	return m5ai.ContextRevision{
		ContextID: "context-selection", RevisionHash: "revision-selection",
		SourceKind: "localImport", DisplayName: "合成职位",
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt: "reply", IntentPrompt: "intent", CustomerFacts: "facts",
			MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: at,
	}
}

func completeSelectionScore(t *testing.T, s *Store, run SourcingCandidateRun, value int, at time.Time) {
	t.Helper()
	reservation := sourcingScoreReservation(run, "invocation-"+run.RunID, at)
	if _, err := s.ReserveSourcingScore(reservation); err != nil {
		t.Fatal(err)
	}
	zero := 0
	if _, err := s.CompleteSourcingScore(CompleteSourcingScoreRequest{
		Completion: AIInvocationCompletion{
			InvocationID: reservation.InvocationID, Status: AIInvocationOK,
			OutputHash: "output-" + run.RunID, InputTokens: 2, OutputTokens: 1,
			ReasoningTokens: &zero, UsageShape: AIInvocationUsageComplete,
			ReasoningContentEmpty: true, FinishedAt: at.Add(time.Second),
		},
		Score: scorePointer(value),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDecideSourcingCandidateAtomicallyCreatesOneSelectedProfile(t *testing.T) {
	s := openTest(t)
	base := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	revision := sourcingSelectionRevision(base.Add(-time.Hour), 5)
	if _, _, err := s.SaveJobAIContextRevision(revision); err != nil {
		t.Fatal(err)
	}
	key := AccountKey{Platform: "zhilian", AccountRef: "account-selection"}
	createM4Account(t, s, key.Platform, key.AccountRef)
	run := seedSourcingScoreRun(t, s, "run-selection", key, revision.RevisionHash, base)
	completeSelectionScore(t, s, run, 7, base)

	first, err := s.DecideSourcingCandidate(DecideSourcingCandidateRequest{
		RunID: run.RunID, ProfileID: "profile-selection-first", DecidedAt: base.Add(2 * time.Minute),
	})
	if err != nil || first == nil || !first.Created || first.Profile == nil ||
		first.Decision.Outcome != SourcingSelectionSelected || first.Profile.MainStatus != CandidateProfileSelected {
		t.Fatalf("合格候选人未原子建档: result=%+v err=%v", first, err)
	}
	replayed, err := s.DecideSourcingCandidate(DecideSourcingCandidateRequest{
		RunID: run.RunID, ProfileID: "profile-selection-must-not-win", DecidedAt: base.Add(3 * time.Minute),
	})
	if err != nil || replayed == nil || replayed.Created || replayed.Profile == nil ||
		replayed.Profile.ProfileID != first.Profile.ProfileID {
		t.Fatalf("选人决策重放未收编首次档案: result=%+v err=%v", replayed, err)
	}
	var decisions, candidates, profiles int64
	_ = s.db.Model(&SourcingSelectionDecision{}).Count(&decisions).Error
	_ = s.db.Model(&Candidate{}).Count(&candidates).Error
	_ = s.db.Model(&CandidateProfile{}).Count(&profiles).Error
	if decisions != 1 || candidates != 1 || profiles != 1 {
		t.Fatalf("重放发生业务事实增生: decisions=%d candidates=%d profiles=%d", decisions, candidates, profiles)
	}
}

func TestDecideSourcingCandidateRecordsTerminalNonSelectionsWithoutProfile(t *testing.T) {
	tests := []struct {
		name       string
		score      int
		contact    string
		want       SourcingSelectionOutcome
		failScorer bool
	}{
		{name: "below-threshold", score: 4, contact: "unestablished", want: SourcingSelectionScoreBelowThreshold},
		{name: "already-contacted", score: 8, contact: "established", want: SourcingSelectionContactStateRejected},
		{name: "scoring-failed", contact: "unestablished", want: SourcingSelectionScoringFailed, failScorer: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := openTest(t)
			base := time.Date(2026, 7, 22, 13, 0, 0, 0, time.UTC)
			revision := sourcingSelectionRevision(base.Add(-time.Hour), 5)
			if _, _, err := s.SaveJobAIContextRevision(revision); err != nil {
				t.Fatal(err)
			}
			key := AccountKey{Platform: "zhilian", AccountRef: "account-" + tc.name}
			createM4Account(t, s, key.Platform, key.AccountRef)
			run := seedSourcingScoreRun(t, s, "run-"+tc.name, key, revision.RevisionHash, base)
			if err := s.db.Model(&SourcingCandidateRun{}).Where("run_id = ?", run.RunID).
				Update("contact_state", tc.contact).Error; err != nil {
				t.Fatal(err)
			}
			if tc.failScorer {
				reservation := sourcingScoreReservation(run, "invocation-"+run.RunID, base)
				if _, err := s.ReserveSourcingScore(reservation); err != nil {
					t.Fatal(err)
				}
				if _, err := s.CompleteSourcingScore(CompleteSourcingScoreRequest{Completion: AIInvocationCompletion{
					InvocationID: reservation.InvocationID, Status: AIInvocationProviderRejected,
					OutputHash: "failed-output", ErrorClass: "providerRejected", FinishedAt: base.Add(time.Second),
				}}); err != nil {
					t.Fatal(err)
				}
			} else {
				completeSelectionScore(t, s, run, tc.score, base)
			}
			result, err := s.DecideSourcingCandidate(DecideSourcingCandidateRequest{
				RunID: run.RunID, ProfileID: "profile-" + tc.name, DecidedAt: base.Add(2 * time.Minute),
			})
			if err != nil || result == nil || result.Decision.Outcome != tc.want || result.Profile != nil {
				t.Fatalf("非选中终局错误: result=%+v err=%v", result, err)
			}
			var profiles int64
			if err := s.db.Model(&CandidateProfile{}).Count(&profiles).Error; err != nil || profiles != 0 {
				t.Fatalf("非选中候选人不应建档: profiles=%d err=%v", profiles, err)
			}
		})
	}
}

func TestNextSourcingRunWithoutSelectionSkipsDecidedRun(t *testing.T) {
	s := openTest(t)
	base := time.Date(2026, 7, 22, 14, 0, 0, 0, time.UTC)
	revision := sourcingSelectionRevision(base.Add(-time.Hour), 5)
	if _, _, err := s.SaveJobAIContextRevision(revision); err != nil {
		t.Fatal(err)
	}
	key := AccountKey{Platform: "zhilian", AccountRef: "account-selection-next"}
	createM4Account(t, s, key.Platform, key.AccountRef)
	first := seedSourcingScoreRun(t, s, "run-selection-first", key, revision.RevisionHash, base)
	second := seedSourcingScoreRun(t, s, "run-selection-second", key, revision.RevisionHash, base.Add(time.Minute))
	completeSelectionScore(t, s, first, 4, base)
	completeSelectionScore(t, s, second, 7, base.Add(time.Minute))
	next, err := s.NextSourcingRunWithoutSelection(key, revision.RevisionHash)
	if err != nil || next == nil || next.RunID != first.RunID {
		t.Fatalf("未返回最早待裁决评分: next=%+v err=%v", next, err)
	}
	if _, err := s.DecideSourcingCandidate(DecideSourcingCandidateRequest{
		RunID: first.RunID, ProfileID: "profile-low", DecidedAt: base.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	next, err = s.NextSourcingRunWithoutSelection(key, revision.RevisionHash)
	if err != nil || next == nil || next.RunID != second.RunID {
		t.Fatalf("已裁决 run 未被排除: next=%+v err=%v", next, err)
	}
}
