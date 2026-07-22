package adminhttp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/patrol"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

type reloadSender struct {
	hub          *fakeAdminHub
	beforeReload func() error
}

func (s *reloadSender) SendEnvelope(handID string, env protocol.Envelope) error {
	var body protocol.CmdBody
	if env.Kind == protocol.KindCmd && json.Unmarshal(env.Body, &body) == nil && body.Name == protocol.PrimDebugReload {
		if s.beforeReload != nil {
			if err := s.beforeReload(); err != nil {
				return err
			}
		}
		go func() {
			time.Sleep(5 * time.Millisecond)
			s.hub.set("session-new", "boot-new", true)
			s.hub.Registry().OnlineWithBuild(
				handID, "session-new", "boot-new",
				[]string{protocol.PrimDebugReload + "@1"}, nil,
				protocol.ContractHash, true, "0.1.0", time.Now(),
			)
		}()
	}
	return nil
}

func TestReloadHandInvalidatesBoundSourcingFeedBeforeDispatch(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	revision := sourcingAdminRevision(now.Add(-time.Hour))
	if _, _, err := st.SaveJobAIContextRevision(revision); err != nil {
		t.Fatal(err)
	}
	key := store.AccountKey{Platform: "zhilian", AccountRef: "account-reload-feed"}
	if err := st.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(
		key, "hand-reload-feed", "principal-reload-feed",
		"session-old", "boot-old", now.Add(-time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	started, err := st.StartSourcingBatch(store.StartSourcingBatchRequest{
		BatchID: "batch-reload-feed", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revision.RevisionHash, TargetCount: 30, StartedAt: now.Add(-time.Minute),
	})
	if err != nil || started == nil || !started.Created {
		t.Fatalf("建立 active 批次失败: result=%+v err=%v", started, err)
	}
	manager, err := patrol.NewManager(
		st, sourcingAdminRunner{}, sourcingAdminHands{},
		patrol.Config{Clock: sourcingAdminClock{now: now}, Location: time.UTC},
	)
	if err != nil {
		t.Fatal(err)
	}
	hub := newFakeAdminHub()
	hub.set("session-old", "boot-old", true)
	hub.Registry().OnlineWithBuild(
		"hand-reload-feed", "session-old", "boot-old",
		[]string{protocol.PrimDebugReload + "@1"}, nil,
		protocol.ContractHash, true, "0.0.9", now,
	)
	invalidatedBeforeSend := false
	sender := &reloadSender{hub: hub}
	sender.beforeReload = func() error {
		batch, err := st.SourcingBatchByID(started.Batch.BatchID)
		if err != nil {
			return err
		}
		account, err := st.AccountByKey(key)
		if err != nil {
			return err
		}
		if batch == nil || batch.Status != store.SourcingBatchStopped ||
			batch.Reason != store.SourcingFeedChangedReason || account == nil ||
			account.SourcingFeedInvalidatedAt == nil {
			return fmt.Errorf("debug.reload 送入 socket 前推荐流仍有效: batch=%+v account=%+v", batch, account)
		}
		invalidatedBeforeSend = true
		return nil
	}
	dispatcher := dispatch.New(st, sender)
	api := New(st, hub, dispatcher, manager, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)

	req := httptest.NewRequest(http.MethodPost, "/admin/hands/reload",
		bytes.NewBufferString(`{"handId":"hand-reload-feed"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !invalidatedBeforeSend {
		t.Fatalf("重载前未完成推荐流失效: code=%d observed=%t body=%s",
			w.Code, invalidatedBeforeSend, w.Body.String())
	}
}

func (s *reloadSender) HandSession(handID string) (string, string, bool) {
	return s.hub.HandSession(handID)
}

func (s *reloadSender) HandNegotiation(string) ([]string, []string, bool) {
	return []string{protocol.PrimDebugReload + "@1"}, nil, true
}

func (*reloadSender) HandContractMatch(string) (bool, bool) { return true, true }

func (s *reloadSender) CloseHand(string, string, string) bool { return true }
func (s *reloadSender) HandOfflineMs(string) int64            { return 0 }

func TestReloadHandWaitsForNewReadyBootAndMatchingContract(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hub := newFakeAdminHub()
	hub.set("session-old", "boot-old", true)
	hub.Registry().OnlineWithBuild(
		"hand-reload", "session-old", "boot-old",
		[]string{protocol.PrimDebugReload + "@1"}, nil,
		"sha256:old", false, "0.0.9", time.Now(),
	)
	disp := dispatch.New(st, &reloadSender{hub: hub})
	api := New(st, hub, disp, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)

	req := httptest.NewRequest(http.MethodPost, "/admin/hands/reload", bytes.NewBufferString(`{"handId":"hand-reload"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("重载确认 code=%d body=%s", w.Code, w.Body.String())
	}
	var response reloadHandView
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Ready || response.PreviousBootID != "boot-old" || response.BootID != "boot-new" ||
		response.ContractHash != protocol.ContractHash || response.ExtensionVersion != "0.1.0" {
		t.Fatalf("新版本就绪证词不完整: %+v", response)
	}
}

func TestReloadHandRequiresExistingCapability(t *testing.T) {
	hub := newFakeAdminHub()
	hub.set("session-old", "boot-old", true)
	hub.Registry().Online("hand-old", "session-old", "boot-old", nil, nil, time.Now())
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api := New(st, hub, dispatch.New(st, &reloadSender{hub: hub}), nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)
	req := httptest.NewRequest(http.MethodPost, "/admin/hands/reload", bytes.NewBufferString(`{"handId":"hand-old"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusConflict || !bytes.Contains(w.Body.Bytes(), []byte("首次启用")) {
		t.Fatalf("旧手应被引导最后一次人工重载: code=%d body=%s", w.Code, w.Body.String())
	}
}

var _ dispatch.Sender = (*reloadSender)(nil)
