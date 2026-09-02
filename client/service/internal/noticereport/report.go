// Package noticereport 把开始工作流时读到的平台通知(个人中心「通知」页签第一页)
// 上报旧后台。
//
// 第十一项获准云端出站(AGENTS.md「平台通知上报」,2026-09-02 甲方裁决)。
// 纪律:观察用途,只上行——响应任何字段不得成为业务裁决、配置或控制指令的
// 来源;失败不重试、不建发件箱、只响亮记日志;成败都不影响开始。
// 载荷是白名单:鉴权对、客户端版本、平台、观察时刻,每条通知仅含平台通知 ID、
// 类型码、标题、正文、平台毫秒时刻、已读标记;不含 accountRef/platformUserRef
// 与任何候选人结构化身份。正文是平台原样文案,可能自含候选人姓名,按既有口径
// 不做内容级脱敏。
package noticereport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"recruithelper/contract/gen/go/protocol"
)

// UploadTimeout 是一次上传 HTTP 调用的上限。
const UploadTimeout = 15 * time.Second

// Target 是上报去处与身份,取自已获准的旧后台配置,不新增配置面。
// machineId 与 licenseToken 是旧后台 verify_client 的鉴权对,缺一即 401。
type Target struct {
	BaseURL      string
	MachineID    string
	LicenseToken string
}

func (t Target) valid() error {
	if strings.TrimSpace(t.BaseURL) == "" {
		return errors.New("旧后台地址未配置")
	}
	if strings.TrimSpace(t.MachineID) == "" || strings.TrimSpace(t.LicenseToken) == "" {
		return errors.New("授权未就绪(缺 machineId 或 licenseToken)")
	}
	return nil
}

// Reporter 执行一次"把手读到的通知第一页 → 上传"。
type Reporter struct {
	ClientVersion string
	// Target 返回 ready=false 表示授权未就绪(未激活),此时静默跳过。
	Target func() (Target, bool)
	// Upload 默认走 HTTP;测试替换它。
	Upload func(ctx context.Context, target Target, payload Payload) error
}

// Payload 是上报正文。字段面是白名单,新增字段须先修 AGENTS.md 条款。
type Payload struct {
	MachineID     string   `json:"machineId"`
	LicenseToken  string   `json:"licenseToken"`
	ClientVersion string   `json:"clientVersion,omitempty"`
	Platform      string   `json:"platform"`
	ObservedAt    int64    `json:"observedAt"`
	Notices       []Notice `json:"notices"`
}

// Notice 由独立结构体显式赋值拼装,不直接序列化手侧类型——白名单靠"逐字段抄"
// 兑现,契约将来加字段不会顺手带出去。
type Notice struct {
	NoticeID     string `json:"noticeId"`
	MessageType  *int64 `json:"messageType"`
	Title        string `json:"title"`
	Content      string `json:"content"`
	NoticeTimeMs *int64 `json:"noticeTimeMs"`
	IsRead       bool   `json:"isRead"`
}

// Report 同步执行一次上报;错误只交给调用方记日志。空列表不上传——服务端按
// 通知 ID 幂等 UPSERT,没有条目就没有可写的东西。
func (r *Reporter) Report(ctx context.Context, platform string, data protocol.AccountReadNoticesData) error {
	if r == nil || r.Target == nil {
		return errors.New("平台通知上报未接线")
	}
	target, ready := r.Target()
	if !ready {
		return nil
	}
	if err := target.valid(); err != nil {
		return err
	}
	if len(data.Notices) == 0 {
		return nil
	}
	payload := Payload{
		ClientVersion: truncateRunes(r.ClientVersion, 32),
		Platform:      strings.TrimSpace(platform),
		ObservedAt:    data.ObservedAt,
		Notices:       make([]Notice, 0, len(data.Notices)),
	}
	for _, notice := range data.Notices {
		id := strings.TrimSpace(notice.NoticeId)
		if id == "" {
			continue
		}
		payload.Notices = append(payload.Notices, Notice{
			NoticeID:     id,
			MessageType:  copyInt64(notice.MessageType),
			Title:        truncateRunes(notice.Title, 256),
			Content:      truncateRunes(notice.Content, 4000),
			NoticeTimeMs: copyInt64(notice.NoticeTimeMs),
			IsRead:       notice.IsRead,
		})
	}
	if len(payload.Notices) == 0 {
		return nil
	}
	if r.Upload != nil {
		return r.Upload(ctx, target, payload)
	}
	return Upload(ctx, target, payload)
}

func copyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func truncateRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// Upload 把一份通知观察 POST 到旧后台。失败只返回错误,不重试。
// 回执只看 HTTP 状态码——只上行,不给回执留可读出的地方。
func Upload(ctx context.Context, target Target, payload Payload) error {
	payload.MachineID = target.MachineID
	payload.LicenseToken = target.LicenseToken
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化平台通知: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, UploadTimeout)
	defer cancel()

	endpoint := strings.TrimRight(strings.TrimSpace(target.BaseURL), "/") +
		"/api/v1/client/platform-notices"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := (&http.Client{Timeout: UploadTimeout}).Do(request)
	if err != nil {
		return fmt.Errorf("上传失败: %w", err)
	}
	defer response.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	if response.StatusCode != http.StatusOK {
		// 错误信息要进普通日志,只带状态码与一小段说明,不整段回显(licenseToken)。
		return fmt.Errorf("上传被拒(HTTP %d): %s", response.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}
