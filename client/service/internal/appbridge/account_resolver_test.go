package appbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/productapp"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

type fakeResolverHub struct {
	hands     []string
	online    bool
	current   bool
	platforms []string // hello 声明的平台表;nil=旧手未声明
}

func (h fakeResolverHub) ActiveHandIDs() []string { return h.hands }

func (h fakeResolverHub) HandPlatforms(string) []string { return h.platforms }

func (h fakeResolverHub) HandSession(string) (string, string, bool) {
	return "sess-resolver", "boot-resolver", h.online
}

func (h fakeResolverHub) WithCurrentHandSession(
	_, _, _ string, fn func() error,
) (bool, error) {
	if !h.current {
		return false, nil
	}
	return true, fn()
}

type fakeProber struct {
	data protocol.ProbePlatformData
	err  error
	// seen 记下脑传下来的平台。漏传不会有任何症状 —— 只会在装了第二个平台的
	// 机器上静默绑不上账号,所以这里要能断言它。
	seen *string
	// byPlatform / errByPlatform:多平台各探一次时按平台给不同答案;probed 记下探过哪些。
	byPlatform    map[string]protocol.ProbePlatformData
	errByPlatform map[string]error
	probed        *[]string
}

func (p fakeProber) Probe(_ context.Context, _ string, platform string) (protocol.ProbePlatformData, error) {
	if p.seen != nil {
		*p.seen = platform
	}
	if p.probed != nil {
		*p.probed = append(*p.probed, platform)
	}
	if err, ok := p.errByPlatform[platform]; ok {
		return protocol.ProbePlatformData{}, err
	}
	if data, ok := p.byPlatform[platform]; ok {
		return data, nil
	}
	return p.data, p.err
}

// storeBinder 是绑定入口的最小 store 直连实现,仅供本测试;生产装配必须走
// patrol.Manager 与命令派发线性化。
type storeBinder struct{ st *store.Store }

func (b storeBinder) BindAccountObservationIfCurrent(
	key store.AccountKey,
	handID, fingerprint, session, bootID string,
	at time.Time,
	reusePrincipal bool,
	withCurrent func(commit func() error) (bool, error),
) (*store.Account, bool, bool, error) {
	var bound *store.Account
	var created bool
	current, err := withCurrent(func() error {
		var bindErr error
		bound, created, bindErr = b.st.BindAccountObservation(
			key, handID, fingerprint, session, bootID, at, reusePrincipal,
		)
		return bindErr
	})
	return bound, created, current, err
}

func loggedInProbe(fingerprint string) protocol.ProbePlatformData {
	return protocol.ProbePlatformData{
		LoginState: protocol.LoginStateIn, ContentScriptOk: true,
		PrincipalFingerprint: &fingerprint,
	}
}

func resolverFixture(t *testing.T, hub fakeResolverHub, prober fakeProber) (LoginAccountResolver, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return LoginAccountResolver{
		Hub: hub, Prober: prober, Binder: storeBinder{st: st},
		Now: func() time.Time { return time.Date(2026, 7, 30, 10, 0, 0, 0, time.Local) },
	}, st
}

func TestResolveCurrentRequiresExactlyOneOnlineHand(t *testing.T) {
	prober := fakeProber{data: loggedInProbe("fp-any")}
	resolver, _ := resolverFixture(t, fakeResolverHub{hands: nil}, prober)
	if _, err := resolver.ResolveCurrent(context.Background(), ""); !errors.Is(err, productapp.ErrHandUnavailable) {
		t.Fatalf("零手在线未报手不可用: %v", err)
	}
	resolver, _ = resolverFixture(t,
		fakeResolverHub{hands: []string{"hand-a", "hand-b"}, online: true, current: true}, prober)
	if _, err := resolver.ResolveCurrent(context.Background(), ""); !errors.Is(err, productapp.ErrHandAmbiguous) {
		t.Fatalf("多手在线未报歧义: %v", err)
	}
}

func TestResolveCurrentRequiresRecruiterLogin(t *testing.T) {
	hub := fakeResolverHub{hands: []string{"hand-1"}, online: true, current: true}
	for name, probe := range map[string]protocol.ProbePlatformData{
		"loggedOut":     {LoginState: protocol.LoginStateOut, ContentScriptOk: true},
		"unknown":       {LoginState: protocol.LoginStateUnknown, ContentScriptOk: true},
		"noFingerprint": {LoginState: protocol.LoginStateIn, ContentScriptOk: true},
	} {
		resolver, st := resolverFixture(t, hub, fakeProber{data: probe})
		if _, err := resolver.ResolveCurrent(context.Background(), ""); !errors.Is(err, productapp.ErrLoginRequired) {
			t.Fatalf("%s 未报需要登录: %v", name, err)
		}
		if accounts, err := st.Accounts(); err != nil || len(accounts) != 0 {
			t.Fatalf("%s 不该建档: accounts=%d err=%v", name, len(accounts), err)
		}
	}
}

// 同一主体反复解析永远找回同一账本根;换主体建新根且旧根原样保留——这是
// 多账号切换数据不混根的核心保证。
func TestResolveCurrentReusesRootPerPrincipalAndSplitsOnSwitch(t *testing.T) {
	hub := fakeResolverHub{hands: []string{"hand-1"}, online: true, current: true}
	resolver, st := resolverFixture(t, hub, fakeProber{data: loggedInProbe("fp-account-a")})

	first, err := resolver.ResolveCurrent(context.Background(), "")
	if err != nil || first.Platform != "zhilian" || first.AccountRef == "" {
		t.Fatalf("首次解析失败: key=%+v err=%v", first, err)
	}
	again, err := resolver.ResolveCurrent(context.Background(), "")
	if err != nil || again != first {
		t.Fatalf("同主体未找回同一账本根: first=%+v again=%+v err=%v", first, again, err)
	}

	resolver.Prober = fakeProber{data: loggedInProbe("fp-account-b")}
	second, err := resolver.ResolveCurrent(context.Background(), "")
	if err != nil || second == first || second.AccountRef == "" {
		t.Fatalf("换主体未建新根: first=%+v second=%+v err=%v", first, second, err)
	}
	accounts, err := st.Accounts()
	if err != nil || len(accounts) != 2 {
		t.Fatalf("应有两棵账本根: accounts=%d err=%v", len(accounts), err)
	}
	resolver.Prober = fakeProber{data: loggedInProbe("fp-account-a")}
	back, err := resolver.ResolveCurrent(context.Background(), "")
	if err != nil || back != first {
		t.Fatalf("切回旧主体未找回原账本根: back=%+v err=%v", back, err)
	}
}

func TestResolveCurrentFailsWhenHandSessionChangesMidProbe(t *testing.T) {
	hub := fakeResolverHub{hands: []string{"hand-1"}, online: true, current: false}
	resolver, st := resolverFixture(t, hub, fakeProber{data: loggedInProbe("fp-a")})
	if _, err := resolver.ResolveCurrent(context.Background(), ""); !errors.Is(err, productapp.ErrHandUnavailable) {
		t.Fatalf("探测期间换代未报手不可用: %v", err)
	}
	if accounts, err := st.Accounts(); err != nil || len(accounts) != 0 {
		t.Fatalf("换代不该留下账号: accounts=%d err=%v", len(accounts), err)
	}
}

// 平台从客户记录来(2026-09-02 甲方裁决,模型 1):只探指定平台;空按默认智联;手声明了
// 平台表而指定平台不在表内、或指定平台未登录,一律需要登录、不建档、不偷换。
func TestResolveCurrentProbesOnlyTheRequestedPlatform(t *testing.T) {
	hub := fakeResolverHub{hands: []string{"hand-1"}, online: true, current: true, platforms: []string{"zhilian", "boss"}}
	var probed []string
	prober := fakeProber{
		probed: &probed,
		byPlatform: map[string]protocol.ProbePlatformData{
			"zhilian": loggedInProbe("fp-zl"),
			"boss":    loggedInProbe("fp-boss"),
		},
	}
	resolver, st := resolverFixture(t, hub, prober)
	key, err := resolver.ResolveCurrent(context.Background(), "boss")
	if err != nil || key.Platform != "boss" || key.AccountRef == "" {
		t.Fatalf("指定 boss 应只探 boss 并建根: key=%+v err=%v", key, err)
	}
	if len(probed) != 1 || probed[0] != "boss" {
		t.Fatalf("两平台都在线也只探指定的那个: %v", probed)
	}
	accounts, err := st.Accounts()
	if err != nil || len(accounts) != 1 || accounts[0].Platform != "boss" {
		t.Fatalf("应只建 boss 一棵根: %+v err=%v", accounts, err)
	}
	// 空平台按默认智联。
	probed = probed[:0]
	if key, err := resolver.ResolveCurrent(context.Background(), " "); err != nil || key.Platform != DefaultPlatform {
		t.Fatalf("空平台应按默认智联: key=%+v err=%v", key, err)
	}
	if len(probed) != 1 || probed[0] != DefaultPlatform {
		t.Fatalf("默认智联只探智联: %v", probed)
	}
}

func TestResolveCurrentRejectsUndeclaredOrLoggedOutRequestedPlatform(t *testing.T) {
	hub := fakeResolverHub{hands: []string{"hand-1"}, online: true, current: true, platforms: []string{"zhilian", "boss"}}
	prober := fakeProber{byPlatform: map[string]protocol.ProbePlatformData{
		"zhilian": loggedInProbe("fp-zl"),
		"boss":    {PageKind: protocol.PageKindNone, LoginState: protocol.LoginStateUnknown},
	}}
	resolver, st := resolverFixture(t, hub, prober)
	// 指定了手没声明的平台:不偷换成在线的智联。
	err := errors.Unwrap(nil)
	_, err = resolver.ResolveCurrent(context.Background(), "lagou")
	var typed *productapp.LoginRequiredError
	if !errors.Is(err, productapp.ErrLoginRequired) || !errors.As(err, &typed) || typed.Platform != "lagou" {
		t.Fatalf("未声明平台应按需要登录拒绝并带平台: %v", err)
	}
	// 指定了已声明但未登录的平台:同样需要登录,不偷换。
	_, err = resolver.ResolveCurrent(context.Background(), "boss")
	if !errors.Is(err, productapp.ErrLoginRequired) || !errors.As(err, &typed) || typed.Platform != "boss" {
		t.Fatalf("指定平台未登录应报需要登录并带平台: %v", err)
	}
	if accounts, _ := st.Accounts(); len(accounts) != 0 {
		t.Fatalf("拒绝不得建档: %+v", accounts)
	}
}

func TestResolveCurrentLegacyHandOnlyKnowsZhilian(t *testing.T) {
	hub := fakeResolverHub{hands: []string{"hand-1"}, online: true, current: true} // 未声明 platforms
	var probed []string
	prober := fakeProber{data: loggedInProbe("fp-zl"), probed: &probed}
	resolver, _ := resolverFixture(t, hub, prober)
	key, err := resolver.ResolveCurrent(context.Background(), "")
	if err != nil || key.Platform != "zhilian" {
		t.Fatalf("旧手默认智联: key=%+v err=%v", key, err)
	}
	if len(probed) != 1 || probed[0] != "zhilian" {
		t.Fatalf("旧手只探智联一次: %v", probed)
	}
	// 旧手配 boss 客户记录:手没声明 boss,按需要登录拒绝(插件未升级)。
	if _, err := resolver.ResolveCurrent(context.Background(), "boss"); !errors.Is(err, productapp.ErrLoginRequired) {
		t.Fatalf("旧手对 boss 客户应报需要登录: %v", err)
	}
}
