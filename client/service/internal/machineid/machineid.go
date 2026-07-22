// Package machineid derives the product machine identity from the current
// operating system on every process start. Raw platform UUIDs never leave
// this package; callers receive only a namespaced SHA-256 digest.
package machineid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

const namespace = "recruithelper-machine-v1|"

var (
	ErrSystemUUIDUnavailable = errors.New("系统机器 UUID 不可用")
	ErrSystemUUIDInvalid     = errors.New("系统机器 UUID 格式无效")
	ErrUnsupportedPlatform   = errors.New("当前操作系统不支持机器身份读取")
)

// Current reads the current operating system identity and returns its
// namespaced digest. It deliberately has no persistence fallback.
func Current(ctx context.Context) (string, error) {
	return currentFromReader(ctx, readSystemUUID)
}

type systemUUIDReader func(context.Context) (string, error)

func currentFromReader(ctx context.Context, read systemUUIDReader) (string, error) {
	if ctx == nil || ctx.Err() != nil {
		return "", ErrSystemUUIDUnavailable
	}
	raw, err := read(ctx)
	if err != nil {
		if errors.Is(err, ErrUnsupportedPlatform) {
			return "", ErrUnsupportedPlatform
		}
		return "", ErrSystemUUIDUnavailable
	}
	return derive(raw)
}

func derive(raw string) (string, error) {
	canonical, err := canonicalUUID(raw)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(namespace + canonical))
	return hex.EncodeToString(digest[:]), nil
}

func canonicalUUID(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if len(value) >= 2 && value[0] == '{' && value[len(value)-1] == '}' {
		value = value[1 : len(value)-1]
	}
	if len(value) != 36 {
		return "", ErrSystemUUIDInvalid
	}
	allZero := true
	for index := 0; index < len(value); index++ {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if value[index] != '-' {
				return "", ErrSystemUUIDInvalid
			}
			continue
		}
		char := value[index]
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') ||
			(char >= 'A' && char <= 'F')) {
			return "", ErrSystemUUIDInvalid
		}
		if char != '0' {
			allZero = false
		}
	}
	if allZero {
		return "", ErrSystemUUIDInvalid
	}
	return strings.ToLower(value), nil
}
