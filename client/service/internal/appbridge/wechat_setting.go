// 微信配置开工闸的读取器(2026-08-18 甲方裁决):产品面"开始"在账号解析
// 通过后,对已绑定账号同步派发 account.readWechatSetting@1,读平台个人中心
// 的微信号配置是否已填。result 只有布尔;招聘方自己的微信号不进任何一层。
package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"recruithelper/client/service/internal/patrol"
	"recruithelper/client/service/internal/productapp"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// wechatReadTimeout 兜住调用方没设超时的情况:原语 deadline 60s,再给账号
// 串行域排队留余量。
const wechatReadTimeout = 90 * time.Second

type WechatSettingReader struct {
	Hub    ResolverHub
	Runner *PatrolRunner
	Store  *store.Store
}

func (r WechatSettingReader) ReadWechatConfigured(
	ctx context.Context,
	key store.AccountKey,
) (bool, error) {
	if r.Hub == nil || r.Runner == nil || r.Store == nil {
		return false, errors.New("微信配置读取器装配不完整")
	}
	account, err := r.Store.AccountByKey(key)
	if err != nil {
		return false, err
	}
	if account == nil || strings.TrimSpace(account.BoundHandID) == "" ||
		account.PrincipalFingerprint == nil ||
		strings.TrimSpace(*account.PrincipalFingerprint) == "" {
		return false, productapp.ErrHandUnavailable
	}
	handID := account.BoundHandID
	sessionID, bootID, online := r.Hub.HandSession(handID)
	if !online {
		return false, productapp.ErrHandUnavailable
	}
	args, err := protocol.Encode(protocol.AccountReadWechatSettingArgs{})
	if err != nil {
		return false, err
	}
	meta := protocol.Primitives[protocol.PrimAccountReadWechatSetting]
	runCtx, cancel := context.WithTimeout(ctx, wechatReadTimeout)
	defer cancel()
	raw, err := r.Runner.Run(runCtx, patrol.RunRequest{
		HandID: handID, ExpectedSession: sessionID, ExpectedBootID: bootID,
		Platform: account.Platform, AccountRef: account.AccountRef,
		ExpectedPrincipalFingerprint: *account.PrincipalFingerprint,
		Name:                         protocol.PrimAccountReadWechatSetting,
		Version:                      meta.Ver,
		Args:                         args,
	})
	if err != nil {
		return false, err
	}
	var data protocol.AccountReadWechatSettingData
	if err := json.Unmarshal(raw, &data); err != nil {
		return false, fmt.Errorf("解析 account.readWechatSetting 结果: %w", err)
	}
	return data.Configured, nil
}

var _ productapp.WechatSettingReader = WechatSettingReader{}
