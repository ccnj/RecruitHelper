package logreport

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
)

// UploadTimeout 是一批上报的整体上限。一批最多几十条 KB 级事件,15s 足够;
// 超了就当这批没传成 —— 按裁决不重试,由每日整包上报兜底。
const UploadTimeout = 15 * time.Second

// Target 是上报的去处与身份。三样都取自已获准的旧后台配置,不新增配置面:
// BaseURL 是旧后台地址,LicenseToken 是 bind 换来的正式令牌,客户身份由服务端
// 从令牌解析,客户端不自报客户名。
type Target struct {
	BaseURL      string
	MachineID    string
	LicenseToken string
	AppVersion   string
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

type wireEvent struct {
	OccurredAt  int64          `json:"occurredAt"`
	Source      string         `json:"source"`
	Level       string         `json:"level"`
	EventType   string         `json:"eventType"`
	Message     string         `json:"message"`
	Code        string         `json:"code,omitempty"`
	Fingerprint string         `json:"fingerprint,omitempty"`
	MergedCount int            `json:"mergedCount,omitempty"`
	FirstAt     *int64         `json:"firstAt,omitempty"`
	LastAt      *int64         `json:"lastAt,omitempty"`
	Context     map[string]any `json:"context,omitempty"`
}

type wireBatch struct {
	MachineID    string      `json:"machineId"`
	LicenseToken string      `json:"licenseToken"`
	AppVersion   string      `json:"appVersion,omitempty"`
	Events       []wireEvent `json:"events"`
}

// Upload 把一批事件 POST 到旧后台。失败只返回错误,不重试。
func Upload(ctx context.Context, target Target, items []Item) error {
	if len(items) == 0 {
		return nil
	}
	if err := target.valid(); err != nil {
		return err
	}

	batch := wireBatch{
		MachineID:    target.MachineID,
		LicenseToken: target.LicenseToken,
		AppVersion:   target.AppVersion,
		Events:       make([]wireEvent, 0, len(items)),
	}
	for _, item := range items {
		batch.Events = append(batch.Events, toWire(item))
	}
	body, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("序列化上报载荷: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, UploadTimeout)
	defer cancel()

	endpoint := strings.TrimRight(strings.TrimSpace(target.BaseURL), "/") + "/api/v1/client/log-events"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := (&http.Client{Timeout: UploadTimeout}).Do(request)
	if err != nil {
		return fmt.Errorf("上传失败: %w", err)
	}
	defer response.Body.Close()
	// 响应正文只在出错时取一小段。它可能回显请求内容,而请求里有 licenseToken,
	// 整段进错误信息就等于把令牌写进普通日志。
	if response.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, 256))
		return fmt.Errorf("上传被拒(HTTP %d): %s", response.StatusCode, strings.TrimSpace(string(snippet)))
	}
	// 响应按裁决只用来判断"传成功没有",正文一概不解析、不落地 ——
	// 这条通道只上行,任何字段都不得成为业务裁决、配置或控制指令的来源。
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	return nil
}

func toWire(item Item) wireEvent {
	event := wireEvent{
		OccurredAt:  item.OccurredAt.UnixMilli(),
		Source:      item.Source,
		Level:       item.Level,
		EventType:   item.EventType,
		Message:     item.Message,
		Code:        item.Code,
		Fingerprint: item.Fingerprint,
		MergedCount: item.MergedCount,
		Context:     item.Context,
	}
	if item.FirstAt != nil {
		at := item.FirstAt.UnixMilli()
		event.FirstAt = &at
	}
	if item.LastAt != nil {
		at := item.LastAt.UnixMilli()
		event.LastAt = &at
	}
	return event
}
