// 账号跟随登录(2026-07-30 甲方裁决):产品页"开始"不再要求诊断台预先绑定,
// 而是探测当前 Chrome 登录的平台主体,按 (platform, 指纹) 找回既有账本根或当场
// 建档。多账号切换由此获得支持——每个主体一棵账本树,路由永远跟着当前登录走,
// 旧的"全库恰好一个账号"唯一性规则随之退役。
//
// 平台从客户记录来(2026-09-02 甲方裁决,模型 1「一条客户记录一个平台」):调用方把后台
// 客户快照下发的平台当指定平台传进来,本解析器只探它;快照缺席按智联(部署默认值,不是
// 业务判断)。手在 hello 里声明了平台表而指定平台不在表内时按需要登录拒绝,不偷换成别的
// 在线平台。批 D 曾做过「各探一次、多在线由 UI 选」,已按模型 1 底稿 1.3 甲方案砍掉。
//
// 误登录账号会留下一行永久 Account(业务事实禁止物理删除),这是知情接受的
// 残留:指纹路由下它永远不会被选中,登回正确账号即恢复,无功能损害。
package appbridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

// DefaultPlatform 是客户快照缺席平台字段、或手未声明平台表时的默认值:本产品此前只随
// 智联交付,缺席即旧世界。它是部署默认值,与 productapp.DefaultPlatform 同一个事实。
const DefaultPlatform = productapp.DefaultPlatform

type ResolverHub interface {
	ActiveHandIDs() []string
	HandSession(handID string) (sessionID, bootID string, online bool)
	WithCurrentHandSession(handID, sessionID, bootID string, fn func() error) (bool, error)
	// HandPlatforms 返回手在 hello 里声明的平台 id;未声明返回 nil(按只有智联处理)。
	HandPlatforms(handID string) []string
}

type AccountProber interface {
	// platform 让手侧确定探哪个适配器。不带它的话,手只能靠"恰好注册了一个
	// 平台"去猜,而装上第二个平台之后那条路必然拒绝(见 plugin registry.ts)。
	Probe(ctx context.Context, handID, platform string) (protocol.ProbePlatformData, error)
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

// ResolveCurrent 解析"开始"该落在哪个平台的哪个账号上。platform 为空按 DefaultPlatform。
// 失效方向全部朝少做:指定平台手未声明→需要登录、不建档、不偷换;未登录→需要登录、不建档;
// 探测报错→手不可用。
func (r LoginAccountResolver) ResolveCurrent(ctx context.Context, platform string) (store.AccountKey, error) {
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
	platform = strings.TrimSpace(platform)
	if platform == "" {
		platform = DefaultPlatform
	}
	declared := r.Hub.HandPlatforms(handID)
	if len(declared) == 0 {
		declared = []string{DefaultPlatform}
	}
	if !containsString(declared, platform) {
		slog.Warn("开始:客户记录的平台不在手声明的平台表内,按需要登录拒绝",
			"errorCode", "startPlatformUndeclared", "platform", platform, "declared", declared)
		return store.AccountKey{}, &productapp.LoginRequiredError{Platform: platform}
	}

	probeCtx, cancel := context.WithTimeout(ctx, resolveProbeTimeout)
	defer cancel()
	probe, err := r.Prober.Probe(probeCtx, handID, platform)
	if err != nil {
		return store.AccountKey{}, errors.Join(productapp.ErrHandUnavailable, fmt.Errorf("%s: %w", platform, err))
	}
	if probe.LoginState != protocol.LoginStateIn || !probe.ContentScriptOk ||
		probe.PrincipalFingerprint == nil || strings.TrimSpace(*probe.PrincipalFingerprint) == "" {
		return store.AccountKey{}, &productapp.LoginRequiredError{Platform: platform}
	}

	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	// reusePrincipal=true + 新铸 accountRef:同一主体永远找回同一账本根,首次
	// 见到的主体用新 ref 建根。换主体绝不覆盖旧根(store 层 ErrAccountPrincipalMismatch
	// 双保险),这是多账号数据不混根的根本保证。
	key := store.AccountKey{Platform: platform, AccountRef: ids.NewAccountRef()}
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

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

var _ productapp.AccountResolver = LoginAccountResolver{}
