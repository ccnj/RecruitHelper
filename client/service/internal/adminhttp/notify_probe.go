// POST /admin/notify/probe —— 运营通知彩排(AGENTS.md「运营通知 webhook」
// 2026-08-06 增补的第三类触发)。人粘一条会话的 sessionId 点一下,脑现场截
// 聊天与简历长图,按该候选人此刻的状态/微信/画像现算正文,直发运营群。
//
// 与线上通知的唯一共用面是 webhook 地址、渲染函数与图片降级规则;账本一行
// 不碰:不入发件箱、不写 event_key、不落 CandidateScreenshot、不铸 effect
// intent。发件箱的 event_key 是唯一索引且入队 OnConflict DoNothing,彩排只要
// 占掉一个 key,日后那条真事件入队就会被静默吃掉,线上永远少发且不报错。
package adminhttp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/notify"
	"recruithelper/client/service/internal/store"
)

// NotifyProbeDeps 是彩排所需的两件外部零件:读图与发送。发送方就是常驻的
// notify.Runner——彩排复用它的 webhook 地址、http client 与客户名闭包,
// 不另建第二个出站配置面。
type NotifyProbeDeps struct {
	Blobs  interface{ ReadFile(ref string) ([]byte, error) }
	Sender interface {
		SendProbe(notify.ProbeRequest) (notify.ProbeOutcome, error)
	}
}

func (a *API) SetNotifyProbeDeps(deps NotifyProbeDeps) *API {
	a.notifyProbe = deps
	return a
}

type notifyProbeBody struct {
	Platform        string `json:"platform"`
	AccountRef      string `json:"accountRef"`
	ConversationRef string `json:"conversationRef"`
	// NotifyType 缺省按候选人主状态推断:已约面发面试确认,否则发微信互加。
	NotifyType string `json:"notifyType"`
}

// 两张长图各有 60s 手侧预算,加派发排队与企微上行余量。
const notifyProbeTimeout = 240 * time.Second

func (a *API) notifyProbeSend(w http.ResponseWriter, r *http.Request) {
	if a.notifyProbe.Sender == nil || a.notifyProbe.Blobs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "本进程未装配运营通知发送器"})
		return
	}
	var body notifyProbeBody
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法请求体: " + err.Error()})
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体只能包含一个 JSON 对象"})
		return
	}
	body.Platform = strings.TrimSpace(body.Platform)
	body.AccountRef = strings.TrimSpace(body.AccountRef)
	body.ConversationRef = strings.TrimSpace(body.ConversationRef)
	if body.Platform == "" || body.AccountRef == "" || body.ConversationRef == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的账号/会话标识"})
		return
	}

	// 档案是硬前提:简历截图要 platformUserRef,正文要状态/微信/画像,两者都
	// 只能由档案给出。查不到就直接拒绝,不发一条只有截图的半截通知。
	profile, err := a.st.CandidateProfileByConversation(store.ConversationKey{
		Platform:        body.Platform,
		AccountRef:      body.AccountRef,
		ConversationRef: body.ConversationRef,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if profile == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "库里没有这条会话对应的候选人档案,彩排只能对已收编的候选人做",
		})
		return
	}
	notifyType, err := resolveProbeNotifyType(body.NotifyType, profile.MainStatus)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), notifyProbeTimeout)
	defer cancel()

	// 两张图各自尽力而为:任何一张失败都只是"缺图",与线上 15 分钟兜底同款
	// 降级,正文照发。失败原因回显给操作者判断,不写日志正文。
	chatImage, chatNote := a.probeCapture(ctx, dispatch.ProbeCaptureRequest{
		Platform:        body.Platform,
		AccountRef:      body.AccountRef,
		ConversationRef: body.ConversationRef,
	})
	resumeImage, resumeNote := a.probeCapture(ctx, dispatch.ProbeCaptureRequest{
		Platform:        body.Platform,
		AccountRef:      body.AccountRef,
		ConversationRef: body.ConversationRef,
		PlatformUserRef: profile.PlatformUserRef,
		Resume:          true,
	})

	snapshot, err := a.st.NotificationRenderSnapshotForProfile(profile.ProfileID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if snapshot == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "候选人档案已不可读"})
		return
	}
	outcome, err := a.notifyProbe.Sender.SendProbe(notify.ProbeRequest{
		NotifyType:  notifyType,
		Snapshot:    snapshot,
		ChatImage:   chatImage,
		ResumeImage: resumeImage,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "通知发送失败: " + err.Error()})
		return
	}
	slog.Info(
		"运营通知彩排已发送",
		"profileId", profile.ProfileID,
		"type", notifyType,
		"chatBytes", len(chatImage),
		"resumeBytes", len(resumeImage),
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"notifyType": notifyType,
		"profileId":  profile.ProfileID,
		"content":    outcome.Content,
		"chat":       outcome.Chat,
		"resume":     outcome.Resume,
		"chatNote":   chatNote,
		"resumeNote": resumeNote,
	})
}

// probeCapture 拍一张并读回字节;失败返回空字节与一句给人看的原因。
func (a *API) probeCapture(ctx context.Context, req dispatch.ProbeCaptureRequest) ([]byte, string) {
	data, state, err := a.disp.ProbeCaptureScreenshot(ctx, req)
	if err != nil {
		return nil, captureFailureNote(state, err)
	}
	image, err := a.notifyProbe.Blobs.ReadFile(data.ImageBlobRef)
	if err != nil {
		return nil, "图已拍到但读取失败: " + err.Error()
	}
	if data.Truncated {
		return image, "已按帧预算截断(长图过长)"
	}
	return image, ""
}

func captureFailureNote(state *store.LogicalDispatchState, err error) string {
	note := err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		note = "未在等待窗口内终局"
	}
	if state != nil && state.Leaf.ErrorCode != "" {
		note = string(state.Leaf.ErrorCode) + ": " + note
	}
	return note
}

// resolveProbeNotifyType 缺省按主状态推断:已约面的人发面试确认,其余发微信
// 互加——这也是两类真实通知在业务上的分工。显式指定时只接受这两个值。
func resolveProbeNotifyType(requested string, status store.CandidateProfileStatus) (string, error) {
	switch strings.TrimSpace(requested) {
	case "":
		if status == store.CandidateProfileInterviewed {
			return store.NotificationTypeInterviewAccepted, nil
		}
		return store.NotificationTypeWechatAdded, nil
	case store.NotificationTypeInterviewAccepted:
		return store.NotificationTypeInterviewAccepted, nil
	case store.NotificationTypeWechatAdded:
		return store.NotificationTypeWechatAdded, nil
	default:
		return "", errors.New("notifyType 只开放 " +
			store.NotificationTypeInterviewAccepted + " 与 " + store.NotificationTypeWechatAdded)
	}
}
