// Package blobstore 实现协议规格 §13 blob/1 的上行子集:内容寻址存储、
// PUT /v1/blobs/{ref} 端点与会话作用域 bearer token。blob 通道零协议语义,
// 不推进任何状态;脑侧消费走进程内文件读取,不提供 HTTP GET。
package blobstore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const RefPrefix = "sha256:"

var ErrInvalidRef = errors.New("blob 引用格式非法")

// ParseRef 校验 "sha256:<64位小写hex>" 并返回 hex 部分。
func ParseRef(ref string) (string, error) {
	if !strings.HasPrefix(ref, RefPrefix) {
		return "", ErrInvalidRef
	}
	hexPart := ref[len(RefPrefix):]
	if len(hexPart) != sha256.Size*2 {
		return "", ErrInvalidRef
	}
	for _, r := range hexPart {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", ErrInvalidRef
		}
	}
	return hexPart, nil
}

// Store 是目录内的内容寻址 blob 存储;文件名即内容 sha256,天然幂等。
type Store struct {
	dir string
}

func New(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("blob 存储目录不能为空")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 blob 存储目录失败: %w", err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(hexPart string) string {
	return filepath.Join(s.dir, hexPart[:2], hexPart)
}

// Path 返回 ref 对应的磁盘路径(不保证存在)。
func (s *Store) Path(ref string) (string, error) {
	hexPart, err := ParseRef(ref)
	if err != nil {
		return "", err
	}
	return s.path(hexPart), nil
}

func (s *Store) Has(ref string) bool {
	p, err := s.Path(ref)
	if err != nil {
		return false
	}
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// ReadFile 按 ref 读取完整内容(进程内消费路径)。
func (s *Store) ReadFile(ref string) ([]byte, error) {
	p, err := s.Path(ref)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// put 流式写入并逐字节复核 sha256;超限或哈希不符不落正式文件。
// n 上限由调用方(handler)用 LimitReader 保证,这里再核对声明引用。
func (s *Store) put(ref string, r io.Reader, maxBytes int64) (int64, error) {
	hexPart, err := ParseRef(ref)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Join(s.dir, hexPart[:2]), 0o755); err != nil {
		return 0, fmt.Errorf("创建 blob 分片目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, ".upload-*")
	if err != nil {
		return 0, fmt.Errorf("创建 blob 临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName) // rename 成功后为 no-op
	}()
	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(r, maxBytes+1))
	if err != nil {
		return 0, fmt.Errorf("写入 blob 失败: %w", err)
	}
	if n == 0 {
		return 0, errors.New("blob 内容为空")
	}
	if n > maxBytes {
		return 0, errTooLarge
	}
	if hex.EncodeToString(hasher.Sum(nil)) != hexPart {
		return 0, errHashMismatch
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("收口 blob 临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, s.path(hexPart)); err != nil {
		return 0, fmt.Errorf("落位 blob 失败: %w", err)
	}
	return n, nil
}

var (
	errTooLarge     = errors.New("blob 超过单件上限")
	errHashMismatch = errors.New("blob 内容与声明引用的 sha256 不符")
)

// TokenRegistry 管理会话作用域 bearer:同一手每次 welcome 轮换,旧值即刻作废。
// 它不是配对或身份凭据(协议规格 §4 注 4);进程退出即全部消失。
type TokenRegistry struct {
	mu     sync.Mutex
	byHand map[string]string
	valid  map[string]struct{}
}

func NewTokenRegistry() *TokenRegistry {
	return &TokenRegistry{byHand: map[string]string{}, valid: map[string]struct{}{}}
}

// Rotate 为该手签发新 token 并作废其旧 token。
func (t *TokenRegistry) Rotate(handID string) string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand 失败等于本机熵源坏死;宁可发不出 token(手视为未协商 blob)。
		return ""
	}
	token := "bt-" + hex.EncodeToString(raw)
	t.mu.Lock()
	defer t.mu.Unlock()
	if old, ok := t.byHand[handID]; ok {
		delete(t.valid, old)
	}
	t.byHand[handID] = token
	t.valid[token] = struct{}{}
	return token
}

func (t *TokenRegistry) Valid(token string) bool {
	if token == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.valid[token]
	return ok
}
