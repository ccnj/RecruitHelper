package blobstore

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

const routePrefix = "/v1/blobs/"

// Handler 提供 PUT /v1/blobs/{ref}。响应与日志都不携带 blob 内容,
// 只出现内容哈希引用与字节数(哈希不含业务明文)。
type Handler struct {
	store    *Store
	tokens   *TokenRegistry
	maxBytes int64
}

func NewHandler(store *Store, tokens *TokenRegistry, maxBytes int64) *Handler {
	return &Handler{store: store, tokens: tokens, maxBytes: maxBytes}
}

func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc(routePrefix, h.handle)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (h *Handler) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || !h.tokens.Valid(strings.TrimSpace(token)) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
		return
	}
	ref := strings.TrimPrefix(r.URL.Path, routePrefix)
	if _, err := ParseRef(ref); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_ref"})
		return
	}
	if r.ContentLength > h.maxBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "too_large"})
		return
	}
	n, err := h.store.put(ref, r.Body, h.maxBytes)
	switch {
	case err == nil:
		slog.Info("blob 已接收", "ref", ref, "bytes", n)
		writeJSON(w, http.StatusOK, map[string]any{"ref": ref, "byteSize": n})
	case errors.Is(err, errTooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "too_large"})
	case errors.Is(err, errHashMismatch):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "hash_mismatch"})
	default:
		slog.Warn("blob 接收失败", "ref", ref, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store_failed"})
	}
}
