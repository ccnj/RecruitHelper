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
	hands   []string
	online  bool
	current bool
}

func (h fakeResolverHub) ActiveHandIDs() []string { return h.hands }

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
}

func (p fakeProber) Probe(context.Context, string) (protocol.ProbePlatformData, error) {
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
	if _, err := resolver.ResolveCurrent(context.Background()); !errors.Is(err, productapp.ErrHandUnavailable) {
		t.Fatalf("零手在线未报手不可用: %v", err)
	}
	resolver, _ = resolverFixture(t,
		fakeResolverHub{hands: []string{"hand-a", "hand-b"}, online: true, current: true}, prober)
	if _, err := resolver.ResolveCurrent(context.Background()); !errors.Is(err, productapp.ErrHandAmbiguous) {
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
		if _, err := resolver.ResolveCurrent(context.Background()); !errors.Is(err, productapp.ErrLoginRequired) {
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

	first, err := resolver.ResolveCurrent(context.Background())
	if err != nil || first.Platform != "zhilian" || first.AccountRef == "" {
		t.Fatalf("首次解析失败: key=%+v err=%v", first, err)
	}
	again, err := resolver.ResolveCurrent(context.Background())
	if err != nil || again != first {
		t.Fatalf("同主体未找回同一账本根: first=%+v again=%+v err=%v", first, again, err)
	}

	resolver.Prober = fakeProber{data: loggedInProbe("fp-account-b")}
	second, err := resolver.ResolveCurrent(context.Background())
	if err != nil || second == first || second.AccountRef == "" {
		t.Fatalf("换主体未建新根: first=%+v second=%+v err=%v", first, second, err)
	}
	accounts, err := st.Accounts()
	if err != nil || len(accounts) != 2 {
		t.Fatalf("应有两棵账本根: accounts=%d err=%v", len(accounts), err)
	}
	resolver.Prober = fakeProber{data: loggedInProbe("fp-account-a")}
	back, err := resolver.ResolveCurrent(context.Background())
	if err != nil || back != first {
		t.Fatalf("切回旧主体未找回原账本根: back=%+v err=%v", back, err)
	}
}

func TestResolveCurrentFailsWhenHandSessionChangesMidProbe(t *testing.T) {
	hub := fakeResolverHub{hands: []string{"hand-1"}, online: true, current: false}
	resolver, st := resolverFixture(t, hub, fakeProber{data: loggedInProbe("fp-a")})
	if _, err := resolver.ResolveCurrent(context.Background()); !errors.Is(err, productapp.ErrHandUnavailable) {
		t.Fatalf("探测期间换代未报手不可用: %v", err)
	}
	if accounts, err := st.Accounts(); err != nil || len(accounts) != 0 {
		t.Fatalf("换代不该留下账号: accounts=%d err=%v", len(accounts), err)
	}
}
