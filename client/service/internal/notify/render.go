package notify

import (
	"fmt"
	"strings"
	"time"

	"recruithelper/client/service/internal/store"
)

// 正文渲染照抄旧项目 notify.py:纯文本(msgtype=text,不用 markdown 符号),
// 关键行动信息(面试时间、联系方式)在前;结尾提示是否附截图。
// 画像行(年龄/学历/城市/期望薪资)按计划列为后置项,当前字段面不含。

var weekdayCN = [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

var mainStatusLabels = map[store.CandidateProfileStatus]string{
	store.CandidateProfileSelected:      "已选中",
	store.CandidateProfileGreeted:       "已招呼",
	store.CandidateProfileCommunicating: "沟通中",
	store.CandidateProfileInvited:       "已邀面",
	store.CandidateProfileInterviewed:   "已约面",
	store.CandidateProfileEnded:         "已结束",
	store.CandidateProfileEliminated:    "已淘汰",
}

// V4 微信线状态 → 展示文案(状态名本身不改,只改读法;照抄旧项目语义)。
var wechatStateLabels = map[string]string{
	"notInvited": "未邀微信",
	"invited":    "已邀微信",
	"exchanged":  "已成功交换微信",
}

func formatInterviewTime(startsAtMs *int64) string {
	if startsAtMs == nil || *startsAtMs <= 0 {
		return "未获取到,请在客户端核对"
	}
	at := time.UnixMilli(*startsAtMs).Local()
	return fmt.Sprintf(
		"%02d-%02d(%s) %02d:%02d",
		at.Month(), at.Day(), weekdayCN[at.Weekday()], at.Hour(), at.Minute(),
	)
}

// contactLines 渲染联系方式(2026-08-06 甲方裁决改为两行,2026-08-07 修订):
//
//	微信: <换到的号>            —— 恒出;号在即换到,不再跟"已成功交换微信"废话,
//	                              没换到写"未获取(<微信线状态>)",状态此时才有信息量
//	手机号: <侧栏采到的手机号>  —— 只在电话观察事实存在、且与微信号不同串时出
//	                              (真机见过换的微信号就是手机号,两行同号很傻)
func contactLines(snapshot *store.NotificationRenderSnapshot) []string {
	wechatID := strings.TrimSpace(snapshot.WechatID)
	wechatLine := "微信: "
	if wechatID == "" {
		status := wechatStateLabels[snapshot.WechatState]
		if status == "" {
			status = "待跟进"
		}
		wechatLine += "未获取(" + status + ")"
	} else {
		wechatLine += wechatID
	}
	lines := []string{wechatLine}
	if phone := strings.TrimSpace(snapshot.PhoneNumber); phone != "" && phone != wechatID {
		lines = append(lines, "手机号: "+phone)
	}
	return lines
}

// interviewMethodLabels 是契约封闭枚举 enums.interviewMethod 的展示文案;
// 枚举外的值不猜测,整行省略。
var interviewMethodLabels = map[string]string{
	"wechatVideo": "微信视频",
	"onsite":      "线下面试",
}

func candidateTitle(prefix string, snapshot *store.NotificationRenderSnapshot, customerName string) string {
	name := strings.TrimSpace(snapshot.DisplayName)
	if name == "" {
		name = "候选人"
	}
	title := "【" + prefix + "】" + name
	if customerName != "" {
		title += "(" + customerName + ")"
	}
	return title
}

// profileLine 渲染画像摘要行(AGENTS.md 2026-07-28 补充裁决的封闭四项)。
// 简历常常没采到或字段缺失,所以逐项可缺:有几项写几项,一项都没有就返回空串
// 由调用方整行省略。任何缺失都只是少一段文字,不影响通知本身发出。
func profileLine(snapshot *store.NotificationRenderSnapshot) string {
	parts := []string{}
	for _, value := range []string{
		strings.TrimSpace(snapshot.Age),
		strings.TrimSpace(snapshot.Education),
		strings.TrimSpace(snapshot.City),
	} {
		if value != "" {
			parts = append(parts, value)
		}
	}
	line := ""
	if len(parts) > 0 {
		line = "候选人: " + strings.Join(parts, "/")
	}
	if salary := strings.TrimSpace(snapshot.DesiredSalary); salary != "" {
		if line != "" {
			line += " · "
		}
		line += "期望 " + salary
	}
	return line
}

func screenshotHintLine(snapshot *store.NotificationRenderSnapshot) string {
	// 提示只列实际就绪的截图,避免文案宣称有简历图而追发时其实只有聊天图。
	parts := []string{}
	if snapshot.ChatShot != nil {
		parts = append(parts, "聊天记录")
	}
	if snapshot.ResumeShot != nil {
		parts = append(parts, "简历")
	}
	if len(parts) == 0 {
		return "(本次未附截图)"
	}
	return strings.Join(parts, "、") + "见下图"
}

// renderInterviewAccepted 渲染「面试确认」通知(约面成功)。
func renderInterviewAccepted(snapshot *store.NotificationRenderSnapshot, customerName string) string {
	lines := []string{candidateTitle("面试确认", snapshot, customerName)}
	lines = append(lines, "面试时间: "+formatInterviewTime(snapshot.InterviewStartsAtMs))
	if method, ok := interviewMethodLabels[snapshot.InterviewMethod]; ok {
		lines = append(lines, "方式: "+method)
	}
	lines = append(lines, contactLines(snapshot)...)
	if snapshot.PositionTitle != "" {
		lines = append(lines, "职位: "+snapshot.PositionTitle)
	}
	if profile := profileLine(snapshot); profile != "" {
		lines = append(lines, profile)
	}
	lines = append(lines, screenshotHintLine(snapshot))
	return truncateBytes(strings.Join(lines, "\n"), wecomTextLimitBytes)
}

// renderWechatAdded 渲染换微信成功通知。supplement 为真表示约面通知已经发到
// 运营手上、但当时还没收到号(15 分钟兜底先发,正文写的是"联系方式:未获取"),
// 这条是那次面试确认的补号——标题据此改写,免得运营当成一个新事件重复跟进。
func renderWechatAdded(
	snapshot *store.NotificationRenderSnapshot,
	customerName string,
	supplement bool,
) string {
	prefix := "微信互加"
	if supplement {
		prefix = "面试确认--补微信号"
	}
	lines := []string{candidateTitle(prefix, snapshot, customerName)}
	lines = append(lines, contactLines(snapshot)...)
	statusLine := "当前状态: " + mainStatusLabel(snapshot.MainStatus)
	if snapshot.MainStatus == store.CandidateProfileInterviewed && snapshot.InterviewStartsAtMs != nil {
		statusLine += " · 面试 " + formatInterviewTime(snapshot.InterviewStartsAtMs)
	}
	lines = append(lines, statusLine)
	if snapshot.PositionTitle != "" {
		lines = append(lines, "职位: "+snapshot.PositionTitle)
	}
	if profile := profileLine(snapshot); profile != "" {
		lines = append(lines, profile)
	}
	lines = append(lines, screenshotHintLine(snapshot))
	return truncateBytes(strings.Join(lines, "\n"), wecomTextLimitBytes)
}

func mainStatusLabel(status store.CandidateProfileStatus) string {
	if label, ok := mainStatusLabels[status]; ok {
		return label
	}
	return "沟通中"
}

func truncateBytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit - len("…")
	for cut > 0 && !utf8RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }
