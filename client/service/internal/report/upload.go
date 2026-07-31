package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"
)

// UploadTimeout 是一次上报的整体上限。压缩后的包实测 15~20MB,明文 http 直传,
// 120s 足够;超了就当这次没传成,人再点一次 —— 按裁决不自动重试、不建发件箱。
const UploadTimeout = 120 * time.Second

// Target 是上报的去处与身份。三样都取自已获准的旧后台配置(jobconfig.Config),
// 不新增配置面:BaseURL 是旧后台地址,LicenseToken 是 bind 换来的正式令牌,
// 客户身份由服务端从令牌解析,客户端不自报客户名。
type Target struct {
	BaseURL      string
	MachineID    string
	LicenseToken string
	AppVersion   string
}

// Receipt 是服务端回执。**只用来告诉本机"传成功没有"** —— 按裁决,这条通道单向
// 上行,回执里的任何字段都不得成为业务裁决、配置或控制指令的来源。
type Receipt struct {
	OK        bool   `json:"ok"`
	ReportID  int64  `json:"reportId"`
	ReportKey string `json:"reportKey"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
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

// Upload 把包 POST 到旧后台的上报接口。失败只返回错误,不重试。
func Upload(ctx context.Context, pack *Pack, target Target) (*Receipt, error) {
	if pack == nil {
		return nil, errors.New("没有可上传的包")
	}
	if err := target.valid(); err != nil {
		return nil, err
	}

	manifestBytes, err := json.Marshal(pack.Manifest)
	if err != nil {
		return nil, fmt.Errorf("序列化清单: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, UploadTimeout)
	defer cancel()

	// 流式送:包有十几 MB,没必要在内存里再拼一份 multipart 正文。
	pipeReader, pipeWriter := io.Pipe()
	formWriter := multipart.NewWriter(pipeWriter)

	go func() {
		err := writeForm(formWriter, pack, target, manifestBytes)
		if err != nil {
			_ = pipeWriter.CloseWithError(err)
			return
		}
		_ = pipeWriter.CloseWithError(formWriter.Close())
	}()

	endpoint := strings.TrimRight(strings.TrimSpace(target.BaseURL), "/") + "/api/v1/client/reports"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, pipeReader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", formWriter.FormDataContentType())

	response, err := (&http.Client{Timeout: UploadTimeout}).Do(request)
	if err != nil {
		return nil, fmt.Errorf("上传失败: %w", err)
	}
	defer response.Body.Close()

	// 出错时只带回状态码与一小段服务端说明。正文可能包含请求回显,
	// 不整段进错误信息 —— 错误信息会进普通日志,而 licenseToken 在请求里。
	if response.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return nil, fmt.Errorf("上传被拒(HTTP %d): %s", response.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var receipt Receipt
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&receipt); err != nil {
		return nil, fmt.Errorf("回执无法解析: %w", err)
	}
	return &receipt, nil
}

func writeForm(formWriter *multipart.Writer, pack *Pack, target Target, manifestBytes []byte) error {
	fields := []struct{ key, value string }{
		{"machineId", target.MachineID},
		{"licenseToken", target.LicenseToken},
		{"appVersion", target.AppVersion},
		{"manifest", string(manifestBytes)},
	}
	for _, field := range fields {
		if err := formWriter.WriteField(field.key, field.value); err != nil {
			return err
		}
	}

	part, err := formWriter.CreateFormFile("file", "report.tar.gz")
	if err != nil {
		return err
	}
	file, err := os.Open(pack.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(part, file)
	return err
}
