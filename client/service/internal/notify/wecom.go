// Package notify 实现运营通知 webhook(AGENTS.md 2026-07-28 裁决,机制照抄旧项目
// notify.py):发件箱 30s 轮询、微信号+聊天截图+简历截图三资产 15 分钟闸门、
// 约面/微信互加两类通知的去重矩阵、企微 text+image 发送与失败重试。
// 纪律:通知正文与截图字节只去往写死的企微 webhook;日志只出现 id/类型/状态/
// 字节数/缺失资产名,不出现候选人姓名、微信号、正文或 webhook URL。
package notify

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// WecomWebhookURL 为甲方 2026-07-28 指定并裁决写死的运营统一群机器人地址;
// 不设配置面,轮换 key 需改码重建(知情接受,见计划风险节)。
const WecomWebhookURL = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=c7527437-0668-4516-9443-670b968173ba"

const (
	wecomTextLimitBytes = 2048            // 企微群机器人 text 消息 content 上限
	wecomImageMaxBytes  = 2 * 1024 * 1024 // 企微 image 消息原图上限 2MB
	requestTimeout      = 5 * time.Second
)

var (
	jpegMagic = []byte{0xff, 0xd8, 0xff}
	pngMagic  = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
)

type wecomResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

func postWecom(client *http.Client, webhookURL string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var decoded wecomResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return fmt.Errorf("企微响应解码失败: %w", err)
	}
	if decoded.ErrCode != 0 {
		return fmt.Errorf("wecom errcode=%d: %s", decoded.ErrCode, decoded.ErrMsg)
	}
	return nil
}

func sendWecomText(client *http.Client, webhookURL string, content string) error {
	return postWecom(client, webhookURL, map[string]any{
		"msgtype": "text",
		"text":    map[string]string{"content": content},
	})
}

// errSkippedImage 表示图片被主动跳过(格式/上限),属预期降级而非发送失败。
type skippedImageError struct{ reason string }

func (e *skippedImageError) Error() string { return "图片跳过:" + e.reason }

func imageFormatOK(imageBytes []byte) bool {
	return bytes.HasPrefix(imageBytes, jpegMagic) || bytes.HasPrefix(imageBytes, pngMagic)
}

// sendWecomImage 发送一张图;超限/格式不符返回 *skippedImageError,由调用方
// 降级(不阻断已发出的文本主通知)。base64 与 md5 均按原始字节计算。
func sendWecomImage(client *http.Client, webhookURL string, imageBytes []byte) error {
	if len(imageBytes) == 0 {
		return &skippedImageError{reason: "empty_image"}
	}
	if !imageFormatOK(imageBytes) {
		return &skippedImageError{reason: "unsupported_image_format"}
	}
	if len(imageBytes) > wecomImageMaxBytes {
		return &skippedImageError{reason: fmt.Sprintf("image_too_large:%d", len(imageBytes))}
	}
	digest := md5.Sum(imageBytes)
	return postWecom(client, webhookURL, map[string]any{
		"msgtype": "image",
		"image": map[string]string{
			"base64": base64.StdEncoding.EncodeToString(imageBytes),
			"md5":    hex.EncodeToString(digest[:]),
		},
	})
}
