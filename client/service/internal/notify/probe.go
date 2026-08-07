// 诊断台运营通知彩排的直发路径(AGENTS.md「运营通知 webhook」2026-08-06 增补的
// 第三类触发)。它与 runner 的正式发送共用同一个 webhook、同一份渲染、同一套
// 图片降级规则,唯独完全不经发件箱:不取件、不写 event_key、不改任何状态、
// 不落截图事实行。发件箱那侧的 event_key 是唯一索引且入队时 OnConflict
// DoNothing——彩排只要写进去一行,日后那个候选人真的换到微信或真的约成面试
// 时,入队会被静默吃掉,线上永远少发一条且不报错。所以这条路径一行都不写库。
package notify

import (
	"errors"
	"strings"

	"recruithelper/client/service/internal/store"
)

// ProbeRequest 是一次彩排发送的全部输入。图像字节由调用方现场截取后直接传入,
// 不经 CandidateScreenshot 事实行——落了库线上通知就会挑到这张彩排图。
type ProbeRequest struct {
	NotifyType  string
	Snapshot    *store.NotificationRenderSnapshot
	ChatImage   []byte
	ResumeImage []byte
}

// ProbeImageOutcome 是单张追发图的结局,供诊断台逐张回显。
type ProbeImageOutcome struct {
	Present  bool   `json:"present"`
	ByteSize int    `json:"byteSize,omitempty"`
	Sent     bool   `json:"sent"`
	Skipped  string `json:"skipped,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ProbeOutcome 回显本次实际发出去的东西。Content 是发送的正文原文:诊断台
// 明文边界(2026-07-31 裁决)允许它回显到同机 /admin/*,但 handler 不得记录。
type ProbeOutcome struct {
	Content string            `json:"content"`
	Chat    ProbeImageOutcome `json:"chat"`
	Resume  ProbeImageOutcome `json:"resume"`
}

// SendProbe 按快照现算正文并直发。正文不带任何测试标记,与线上通知长得完全
// 一样(甲方 2026-08-06 明示,要看到与线上一致的观感)。文本发失败即整体失败
// 返回 error;图片失败只降级,与正式路径同款。
func (r *Runner) SendProbe(req ProbeRequest) (ProbeOutcome, error) {
	outcome := ProbeOutcome{}
	if req.Snapshot == nil {
		return outcome, errors.New("缺少候选人快照")
	}
	if req.NotifyType != store.NotificationTypeInterviewAccepted &&
		req.NotifyType != store.NotificationTypeWechatAdded {
		return outcome, errors.New("通知类型只开放面试确认与微信互加")
	}

	// 浅拷贝后按"本次实际拍到什么"重设截图字段:快照里的 ChatShot/ResumeShot
	// 是库里的历史取证行,与本次现拍无关,直接拿来渲染会让正文宣称有图而其实
	// 没有(或反之)。改副本,不碰调用方的对象。
	snapshot := *req.Snapshot
	snapshot.ChatShot = nil
	snapshot.ResumeShot = nil
	if len(req.ChatImage) > 0 {
		snapshot.ChatShot = &store.CandidateScreenshot{}
	}
	if len(req.ResumeImage) > 0 {
		snapshot.ResumeShot = &store.CandidateScreenshot{}
	}

	customer := ""
	if r.customerName != nil {
		customer = strings.TrimSpace(r.customerName())
	}
	// supplement 恒 false:补号是发件箱状态机的结论,彩排没有发件箱。
	if req.NotifyType == store.NotificationTypeWechatAdded {
		outcome.Content = renderWechatAdded(&snapshot, customer, false)
	} else {
		outcome.Content = renderInterviewAccepted(&snapshot, customer)
	}
	if err := sendWecomText(r.client, r.webhookURL, outcome.Content); err != nil {
		return outcome, err
	}
	outcome.Chat = r.sendProbeImage(req.ChatImage)
	outcome.Resume = r.sendProbeImage(req.ResumeImage)
	return outcome, nil
}

func (r *Runner) sendProbeImage(imageBytes []byte) ProbeImageOutcome {
	if len(imageBytes) == 0 {
		return ProbeImageOutcome{}
	}
	result := ProbeImageOutcome{Present: true, ByteSize: len(imageBytes)}
	err := sendWecomImage(r.client, r.webhookURL, imageBytes)
	if err == nil {
		result.Sent = true
		return result
	}
	var skipped *skippedImageError
	if errors.As(err, &skipped) {
		result.Skipped = skipped.reason
	} else {
		result.Error = err.Error()
	}
	return result
}
