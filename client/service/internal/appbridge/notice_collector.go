// 平台通知上报的读取器(2026-09-02 甲方裁决,第十一项云端出站):产品面"开始"
// 在微信配置开工闸通过后,对已绑定账号同步派发 account.readNotices@1,读平台
// 个人中心「通知」页签第一页,读到即交上报函数(main 装配为异步上传)。
// 它不是闸:任何失败只返回错误给调用方记日志,不影响开始。
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

// noticeReadTimeout 兜住调用方没设超时的情况:原语 deadline 120s,手侧两段条件
// 轮询各最长 20 秒;这里只是上限,不是等满。
const noticeReadTimeout = 90 * time.Second

type NoticeCollector struct {
	Hub    ResolverHub
	Runner *PatrolRunner
	Store  *store.Store
	// Report 拿到手侧结果后被同步调用;装配方决定异步与否。nil 即只读不报。
	Report func(platform string, data protocol.AccountReadNoticesData)
}

func (c NoticeCollector) CollectNotices(ctx context.Context, key store.AccountKey) error {
	if c.Hub == nil || c.Runner == nil || c.Store == nil {
		return errors.New("平台通知读取器装配不完整")
	}
	account, err := c.Store.AccountByKey(key)
	if err != nil {
		return err
	}
	if account == nil || strings.TrimSpace(account.BoundHandID) == "" ||
		account.PrincipalFingerprint == nil ||
		strings.TrimSpace(*account.PrincipalFingerprint) == "" {
		return productapp.ErrHandUnavailable
	}
	handID := account.BoundHandID
	sessionID, bootID, online := c.Hub.HandSession(handID)
	if !online {
		return productapp.ErrHandUnavailable
	}
	args, err := protocol.Encode(protocol.AccountReadNoticesArgs{})
	if err != nil {
		return err
	}
	meta := protocol.Primitives[protocol.PrimAccountReadNotices]
	runCtx, cancel := context.WithTimeout(ctx, noticeReadTimeout)
	defer cancel()
	raw, err := c.Runner.Run(runCtx, patrol.RunRequest{
		HandID: handID, ExpectedSession: sessionID, ExpectedBootID: bootID,
		Platform: account.Platform, AccountRef: account.AccountRef,
		ExpectedPrincipalFingerprint: *account.PrincipalFingerprint,
		Name:                         protocol.PrimAccountReadNotices,
		Version:                      meta.Ver,
		Args:                         args,
	})
	if err != nil {
		return err
	}
	var data protocol.AccountReadNoticesData
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("解析 account.readNotices 结果: %w", err)
	}
	if c.Report != nil {
		c.Report(string(account.Platform), data)
	}
	return nil
}

var _ productapp.NoticeCollector = NoticeCollector{}
