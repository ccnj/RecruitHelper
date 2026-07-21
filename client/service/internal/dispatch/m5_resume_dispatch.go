package dispatch

import (
	"encoding/json"
	"errors"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

type ResumeCaptureDispatchRequest struct {
	ProfileID                    string
	HandID                       string
	ExpectedSession              string
	ExpectedBootID               string
	Platform                     string
	AccountRef                   string
	ExpectedPrincipalFingerprint string
}

type ResumeCaptureDispatchReceipt struct {
	LogicalDispatchID string
	Created           bool
}

// DispatchResumeCapture 是 candidate.readResume 唯一脑侧构造入口。profile 已
// inFlight 时只附着持久 logical root，不重新协商、不写 socket，也不另造命令。
func (d *Dispatcher) DispatchResumeCapture(req ResumeCaptureDispatchRequest) (*ResumeCaptureDispatchReceipt, error) {
	if req.ProfileID == "" || req.HandID == "" || req.Platform == "" || req.AccountRef == "" ||
		req.ExpectedPrincipalFingerprint == "" {
		return nil, errors.New("简历补采派发缺少 profile/hand/account/fingerprint")
	}
	target, err := d.st.ActiveM5TrialForAccount(store.AccountKey{Platform: req.Platform, AccountRef: req.AccountRef})
	if err != nil {
		return nil, err
	}
	if target == nil || target.Profile.ProfileID != req.ProfileID {
		return nil, store.ErrM5TrialNotActive
	}
	if target.Profile.ResumeCaptureState == store.ResumeCaptureInFlight {
		if target.Profile.ResumeCaptureLogicalDispatchID == nil || *target.Profile.ResumeCaptureLogicalDispatchID == "" {
			return nil, store.ErrCandidateProfileState
		}
		return &ResumeCaptureDispatchReceipt{LogicalDispatchID: *target.Profile.ResumeCaptureLogicalDispatchID}, nil
	}
	if target.Profile.ResumeCaptureState != store.ResumeCaptureUnattempted || target.Profile.ConversationRef == nil {
		return nil, store.ErrResumeCaptureNotAllowed
	}
	args, err := json.Marshal(protocol.CandidateReadResumeArgs{
		ConversationRef: *target.Profile.ConversationRef,
		PlatformUserRef: target.Profile.PlatformUserRef,
	})
	if err != nil {
		return nil, err
	}
	detailed, err := d.dispatchDetailed(DispatchRequest{
		HandID: req.HandID, ExpectedSession: req.ExpectedSession, ExpectedBootID: req.ExpectedBootID,
		Name: protocol.PrimCandidateReadResume, Args: args,
		Context: &protocol.CmdContext{
			Platform: req.Platform, AccountRef: req.AccountRef,
			ExpectedPrincipalFingerprint: req.ExpectedPrincipalFingerprint,
		},
	}, dispatchOptions{resumeCaptureProfileID: req.ProfileID})
	if err != nil {
		return nil, err
	}
	return &ResumeCaptureDispatchReceipt{LogicalDispatchID: detailed.MsgID, Created: detailed.Created}, nil
}
