package statusreport

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

// UploadTimeout 是一次上报的整体上限。载荷不到 2KB,15 秒足够;超了就当这次没传成,
// 下一轮(5 分钟后)自然补上 —— 按裁决不重试、不建发件箱。
const UploadTimeout = 15 * time.Second

// Target 是上报的去处与身份。三样都取自已获准的旧后台配置(jobconfig.Config),
// 不新增配置面。客户身份由服务端从 licenseToken 解析,客户端不自报客户名。
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

// Upload 把一份快照 POST 到旧后台。失败只返回错误,不重试。
//
// **回执只解析 ok。**这条通道只上行:响应里的任何字段都不得成为业务裁决、配置或
// 控制指令的来源,所以这里连结构体都不给它 —— 没有可以被读出来的地方,后来的人
// 就没法顺手"从回执里取个配置"。
func Upload(ctx context.Context, payload *Payload, target Target) error {
	if payload == nil {
		return errors.New("没有可上传的快照")
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

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("序列化快照: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, UploadTimeout)
	defer cancel()

	endpoint := strings.TrimRight(strings.TrimSpace(target.BaseURL), "/") + "/api/v1/client/status"
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
	// 正文一定要读掉再关,否则连接不能复用 —— 这是每 5 分钟一次的常驻调用。
	snippet, _ := io.ReadAll(io.LimitReader(response.Body, 512))

	if response.StatusCode != http.StatusOK {
		// 只带回状态码与一小段服务端说明。整段正文可能回显请求内容,而请求里有
		// licenseToken,错误信息是要进普通日志的。
		return fmt.Errorf("上传被拒(HTTP %d): %s", response.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}
