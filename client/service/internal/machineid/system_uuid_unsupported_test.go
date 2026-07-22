//go:build !windows && !darwin

package machineid

import (
	"context"
	"errors"
	"testing"
)

func TestCurrentFailsOnUnsupportedPlatform(t *testing.T) {
	value, err := Current(context.Background())
	if value != "" || !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("Current = (%q, %v), want ErrUnsupportedPlatform", value, err)
	}
}
