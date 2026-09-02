// 账号跟随登录(2026-07-30 甲方裁决):产品页"开始"不再要求诊断台预先绑定,
// 而是探测当前 Chrome 登录的平台主体,按 (platform, 指纹) 找回既有账本根或当场
// 建档。多账号切换由此获得支持——每个主体一棵账本树,路由永远跟着当前登录走,
// 旧的"全库恰好一个账号"唯一性规则随之退役。
//
// 平台从账号来(2026-09-02 甲方裁决,BOSS 第一刀批 D 2.1):对手在 hello 里声明的每个
// 平台各探一次,恰好一个已登录就用它;多于一个要求 UI 显式选择;零个按需要登录;
// 手未声明平台表(旧手)回落只探智联,行为与今天逐字相同。不做职位级平台归属,
// 不做提前绑定(BOSS 不感知登录态,没有触发事件)。
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
// 原语,预算受协议约束,这里只是兜住调用方没设超时的情况。每个平台各自计时。
const resolveProbeTimeout = 45 * time.Second

// fallbackPlatforms 是手未声明 platforms(旧手)时的探测名单:本产品此前只随智联
// 插件交付,旧手上只有它。新手声明了什么就探什么,这里不再是唯一支持的平台。
var fallbackPlatforms = []string{"zhilian"}

type ResolverHub interface {
	ActiveHandIDs() []string
	HandSession(handID string) (sessionID, bootID string, online bool)
	WithCurrentHandSession(handID, sessionID, bootID string, fn func() error) (bool, error)
	// HandPlatforms 返回手在 hello 里声明的平台 id;未声明返回 nil(回落 fallbackPlatforms)。
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

type loggedInPrincipal struct {
	platform    string
	fingerprint string
}

// ResolveCurrent 解析"开始"该落在哪个平台的哪个账号上。requestedPlatform 非空时只探
// 它(必须在手声明的平台表内,否则按需要登录拒绝,不得偷换成别的在线平台);为空时
// 对声明的每个平台各探一次。失效方向全部朝少做:零在线→需要登录、不建档;多在线→
// 歧义、不猜;全部探测报错→手不可用;部分报错按未登录处理并留痕。
func (r LoginAccountResolver) ResolveCurrent(ctx context.Context, requestedPlatform string) (store.AccountKey, error) {
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
	platforms := r.Hub.HandPlatforms(handID)
	if len(platforms) == 0 {
		platforms = fallbackPlatforms
	}
	requestedPlatform = strings.TrimSpace(requestedPlatform)
	if requestedPlatform != "" {
		if !containsString(platforms, requestedPlatform) {
			slog.Warn("开始:用户指定的平台不在手声明的平台表内,按需要登录拒绝",
				"errorCode", "startPlatformUndeclared", "platform", requestedPlatform, "declared", platforms)
			return store.AccountKey{}, fmt.Errorf("%w: 插件未声明平台 %s", productapp.ErrLoginRequired, requestedPlatform)
		}
		platforms = []string{requestedPlatform}
	}

	var loggedIn []loggedInPrincipal
	var probeErrs []error
	for _, platform := range platforms {
		probe, err := r.probePlatform(ctx, handID, platform)
		if err != nil {
			// 只记不停:一个平台探不到(比如页面没开)不该拦住另一个已登录的平台;
			// 全部探不到才是手不可用。留痕按「错误收敛必须留痕」。
			slog.Warn("开始:平台探测失败,按未登录处理", "errorCode", "startProbeFailed",
				"platform", platform, "err", err)
			probeErrs = append(probeErrs, fmt.Errorf("%s: %w", platform, err))
			continue
		}
		if probe.LoginState != protocol.LoginStateIn || !probe.ContentScriptOk ||
			probe.PrincipalFingerprint == nil || strings.TrimSpace(*probe.PrincipalFingerprint) == "" {
			continue
		}
		loggedIn = append(loggedIn, loggedInPrincipal{platform: platform, fingerprint: *probe.PrincipalFingerprint})
	}
	if len(loggedIn) == 0 {
		if len(probeErrs) == len(platforms) {
			return store.AccountKey{}, errors.Join(append([]error{productapp.ErrHandUnavailable}, probeErrs...)...)
		}
		return store.AccountKey{}, productapp.ErrLoginRequired
	}
	if len(loggedIn) > 1 {
		candidates := make([]string, 0, len(loggedIn))
		for _, principal := range loggedIn {
			candidates = append(candidates, principal.platform)
		}
		return store.AccountKey{}, &productapp.PlatformAmbiguousError{Platforms: candidates}
	}
	chosen := loggedIn[0]

	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	// reusePrincipal=true + 新铸 accountRef:同一主体永远找回同一账本根,首次
	// 见到的主体用新 ref 建根。换主体绝不覆盖旧根(store 层 ErrAccountPrincipalMismatch
	// 双保险),这是多账号数据不混根的根本保证。
	key := store.AccountKey{Platform: chosen.platform, AccountRef: ids.NewAccountRef()}
	bound, _, current, err := r.Binder.BindAccountObservationIfCurrent(
		key, handID, chosen.fingerprint, sessionID, bootID, now, true,
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

func (r LoginAccountResolver) probePlatform(ctx context.Context, handID, platform string) (protocol.ProbePlatformData, error) {
	probeCtx, cancel := context.WithTimeout(ctx, resolveProbeTimeout)
	defer cancel()
	return r.Prober.Probe(probeCtx, handID, platform)
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
