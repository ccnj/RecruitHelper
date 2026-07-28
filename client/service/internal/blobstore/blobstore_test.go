package blobstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func refOf(content []byte) string {
	sum := sha256.Sum256(content)
	return RefPrefix + hex.EncodeToString(sum[:])
}

func newTestHandler(t *testing.T, maxBytes int64) (*Handler, *Store, *TokenRegistry) {
	t.Helper()
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tokens := NewTokenRegistry()
	return NewHandler(store, tokens, maxBytes), store, tokens
}

func doPut(h *Handler, ref, token string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, routePrefix+ref, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.handle(rec, req)
	return rec
}

func TestPutRoundTripAndIdempotent(t *testing.T) {
	h, store, tokens := newTestHandler(t, 1<<20)
	token := tokens.Rotate("hand-a")
	content := []byte("jpeg-bytes-立此存照")
	ref := refOf(content)

	if rec := doPut(h, ref, token, content); rec.Code != http.StatusOK {
		t.Fatalf("首次 PUT 状态=%d body=%s", rec.Code, rec.Body.String())
	}
	got, err := store.ReadFile(ref)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("回读不一致: err=%v got=%q", err, got)
	}
	// 同内容重复 PUT 幂等成功。
	if rec := doPut(h, ref, token, content); rec.Code != http.StatusOK {
		t.Fatalf("重复 PUT 状态=%d", rec.Code)
	}
	if !store.Has(ref) {
		t.Fatal("Has 应为 true")
	}
}

func TestPutRejectsBadToken(t *testing.T) {
	h, _, tokens := newTestHandler(t, 1<<20)
	_ = tokens.Rotate("hand-a")
	content := []byte("x")
	if rec := doPut(h, refOf(content), "bt-wrong", content); rec.Code != http.StatusUnauthorized {
		t.Fatalf("坏 token 状态=%d", rec.Code)
	}
	if rec := doPut(h, refOf(content), "", content); rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺 token 状态=%d", rec.Code)
	}
}

func TestRotateInvalidatesOldToken(t *testing.T) {
	h, _, tokens := newTestHandler(t, 1<<20)
	old := tokens.Rotate("hand-a")
	fresh := tokens.Rotate("hand-a")
	content := []byte("y")
	if rec := doPut(h, refOf(content), old, content); rec.Code != http.StatusUnauthorized {
		t.Fatalf("旧 token 应作废,状态=%d", rec.Code)
	}
	if rec := doPut(h, refOf(content), fresh, content); rec.Code != http.StatusOK {
		t.Fatalf("新 token 应有效,状态=%d", rec.Code)
	}
}

func TestPutRejectsHashMismatch(t *testing.T) {
	h, store, tokens := newTestHandler(t, 1<<20)
	token := tokens.Rotate("hand-a")
	declared := refOf([]byte("declared"))
	rec := doPut(h, declared, token, []byte("actual-other"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "hash_mismatch") {
		t.Fatalf("哈希不符状态=%d body=%s", rec.Code, rec.Body.String())
	}
	if store.Has(declared) {
		t.Fatal("哈希不符不得落正式文件")
	}
}

func TestPutRejectsOversizeAndEmptyAndBadRef(t *testing.T) {
	h, _, tokens := newTestHandler(t, 8)
	token := tokens.Rotate("hand-a")
	big := []byte("123456789") // 9 > 8
	if rec := doPut(h, refOf(big), token, big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限状态=%d", rec.Code)
	}
	if rec := doPut(h, refOf(nil), token, nil); rec.Code != http.StatusInternalServerError && rec.Code != http.StatusBadRequest {
		t.Fatalf("空内容应失败,状态=%d", rec.Code)
	}
	if rec := doPut(h, "sha256:zz", token, []byte("a")); rec.Code != http.StatusBadRequest {
		t.Fatalf("坏引用状态=%d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, routePrefix+refOf(big), nil)
	rec := httptest.NewRecorder()
	h.handle(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 状态=%d", rec.Code)
	}
}

func TestParseRef(t *testing.T) {
	if _, err := ParseRef(refOf([]byte("a"))); err != nil {
		t.Fatalf("合法引用被拒: %v", err)
	}
	for _, bad := range []string{"", "sha256:", "sha256:ABC", "md5:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("g", 64)} {
		if _, err := ParseRef(bad); err == nil {
			t.Fatalf("非法引用未被拒: %q", bad)
		}
	}
}
