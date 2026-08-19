package chatreport

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

// UploadTimeout 是单批上传的整体上限。消息批最大 500 条聊天正文，量级几百 KB，
// 60 秒给明文 http 加慢网留足余量；超时按失败处理——本轮结束，次日自愈。
const UploadTimeout = 60 * time.Second

// Target 是上报的去处与身份。三样都取自已获准的旧后台配置(jobconfig.Config)，
// 不新增配置面（与 statusreport.Target 同款）。
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

// Upload 把一批载荷 POST 到旧后台，2xx 即成功。失败只返回错误，不重试。
//
// **回执只看状态码。**这条通道只上行：响应里的任何字段都不得成为业务裁决、
// 配置或控制指令的来源，所以这里连结构体都不给它——客户端凭本次 2xx 自行推进
// 水位，不解析回执内容。
func Upload(ctx context.Context, payload *Payload, target Target) error {
	if payload == nil {
		return errors.New("没有可上传的载荷")
	}
	if err := target.valid(); err != nil {
		return err
	}

	body := *payload
	body.MachineID = target.MachineID
	body.LicenseToken = target.LicenseToken
	if strings.TrimSpace(body.AppVersion) == "" {
		body.AppVersion = target.AppVersion
	}
	body.SchemaVersion = SchemaVersion

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("序列化载荷: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, UploadTimeout)
	defer cancel()

	endpoint := strings.TrimRight(strings.TrimSpace(target.BaseURL), "/") + "/api/v1/client/chat-report"
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
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("旧后台回了 %d: %s", response.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}
