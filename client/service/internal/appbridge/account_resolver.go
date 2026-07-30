// 账号跟随登录(2026-07-30 甲方裁决):产品页"开始"不再要求诊断台预先绑定,
// 而是探测当前 Chrome 登录的平台主体,按 (platform, 指纹) 找回既有账本根或当场
// 建档。多账号切换由此获得支持——每个主体一棵账本树,路由永远跟着当前登录走,
// 旧的"全库恰好一个账号"唯一性规则随之退役。
//
// 误登录账号会留下一行永久 Account(业务事实禁止物理删除),这是知情接受的
// 残留:指纹路由下它永远不会被选中,登回正确账号即恢复,无功能损害。
package appbridge

import (
	"context"
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/productapp"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

// resolveProbeTimeout 与诊断台绑定流一致(m2.go 的 45s):probe 是 readonly
// 原语,预算受协议约束,这里只是兜住调用方没设超时的情况。
const resolveProbeTimeout = 45 * time.Second

// resolverPlatform 是产品面唯一支持的平台。脑核心保持平台无关(platform 只是
// AccountKey 的路由维度),但"开始"按钮属于命令生产者,本产品当前只随智联插件
// 交付;诊断台绑定表单传的也是同一字面量。多平台时这里换成显式选择,不是常量。
const resolverPlatform = "zhilian"

type ResolverHub interface {
	ActiveHandIDs() []string
	HandSession(handID string) (sessionID, bootID string, online bool)
	WithCurrentHandSession(handID, sessionID, bootID string, fn func() error) (bool, error)
}

type AccountProber interface {
	Probe(ctx context.Context, handID string) (protocol.ProbePlatformData, error)
}

// AccountBinder 由 patrol.Manager 满足:绑定必须经它与命令派发线性化,
// 不得绕过直接写 store。
type AccountBinder interface {
	BindAccountObservationIfCurrent(
		key store.AccountKey,
		handID, fingerprint, session, bootID string,
		at time.Time,
		reusePrincipal bool,
		withCurrent func(commit func() error) (bool, error),
	) (bound *store.Account, created bool, current bool, err error)
}

// LoginAccountResolver 实现 productapp.AccountResolver。
type LoginAccountResolver struct {
	Hub    ResolverHub
	Prober AccountProber
	Binder AccountBinder
	Now    func() time.Time
}

func (r LoginAccountResolver) ResolveCurrent(ctx context.Context) (store.AccountKey, error) {
	if r.Hub == nil || r.Prober == nil || r.Binder == nil {
		return store.AccountKey{}, errors.New("账号解析器装配不完整")
	}
	hands := r.Hub.ActiveHandIDs()
	if len(hands) == 0 {
		return store.AccountKey{}, productapp.ErrHandUnavailable
	}
	if len(hands) > 1 {
		return store.AccountKey{}, productapp.ErrHandAmbiguous
	}
	handID := hands[0]
	sessionID, bootID, online := r.Hub.HandSession(handID)
	if !online {
		return store.AccountKey{}, productapp.ErrHandUnavailable
	}
	probeCtx, cancel := context.WithTimeout(ctx, resolveProbeTimeout)
	defer cancel()
	probe, err := r.Prober.Probe(probeCtx, handID)
	if err != nil {
		return store.AccountKey{}, errors.Join(productapp.ErrHandUnavailable, err)
	}
	if probe.LoginState != protocol.LoginStateIn || !probe.ContentScriptOk ||
		probe.PrincipalFingerprint == nil || strings.TrimSpace(*probe.PrincipalFingerprint) == "" {
		return store.AccountKey{}, productapp.ErrLoginRequired
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	// reusePrincipal=true + 新铸 accountRef:同一主体永远找回同一账本根,首次
	// 见到的主体用新 ref 建根。换主体绝不覆盖旧根(store 层 ErrAccountPrincipalMismatch
	// 双保险),这是多账号数据不混根的根本保证。
	key := store.AccountKey{Platform: resolverPlatform, AccountRef: ids.NewAccountRef()}
	bound, _, current, err := r.Binder.BindAccountObservationIfCurrent(
		key, handID, *probe.PrincipalFingerprint, sessionID, bootID, now, true,
		func(commit func() error) (bool, error) {
			return r.Hub.WithCurrentHandSession(handID, sessionID, bootID, commit)
		},
	)
	if err != nil {
		return store.AccountKey{}, err
	}
	if !current || bound == nil {
		// 探测期间手重连(session/boot 换代),指纹归属已不可信;按手不可用
		// 报告,用户重试即可。
		return store.AccountKey{}, productapp.ErrHandUnavailable
	}
	return store.AccountKey{Platform: bound.Platform, AccountRef: bound.AccountRef}, nil
}

var _ productapp.AccountResolver = LoginAccountResolver{}
