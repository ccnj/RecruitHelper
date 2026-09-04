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

// UploadTimeout 是一次上报的整体上限。超了就当这次没传成 —— 按裁决不自动重试、
// 不建发件箱。
//
// 2026-09-04 从 120s 放宽到 10 分钟。原值配的是"压缩后 15~20MB"那个早已作废的
// 假设:真机实测包已经是 114~175MB,是原假设的 7~10 倍,近 14 天 33 次上传失败
// 全部卡在 00:12 —— 即 00:10 触发点加满这 120 秒,包根本传不完就被自己掐断。
// 凌晨没有业务,一条长连接不占用任何东西;配合档期打散(slot.go)后这台机器独占
// 上行,10 分钟对当前包量是很宽的余量。
//
// 但这不是一个可以一直往上拧的旋钮:cmd_records 按裁决行永不删除,包随运行天数
// 单调增长,总有一天会再次越过任何固定的线。治本要靠给包瘦身(见立案文档第三层),
// 那是另案。
//
// 服务端侧已核对无须同批改动:nginx 未显式设置超时,用的编译默认里
// client_body_timeout 等都是"两次读操作之间"的间隔超时而非总时长,只要数据在
// 流动就不触发;client_max_body_size 已是 600m。
const UploadTimeout = 10 * time.Minute

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
