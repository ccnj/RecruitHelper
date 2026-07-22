package machineid

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDeriveUsesCanonicalUUIDAndFrozenNamespace(t *testing.T) {
	const expected = "7c3b5871f9c5ab7a866f931759c8b1437eb12a83e7c2f308e655554da167c16e"
	for _, input := range []string{
		"00112233-4455-6677-8899-aabbccddeeff",
		"00112233-4455-6677-8899-AABBCCDDEEFF",
		"  {00112233-4455-6677-8899-AABBCCDDEEFF}\n",
	} {
		got, err := derive(input)
		if err != nil {
			t.Fatalf("derive(%q): %v", input, err)
		}
		if got != expected {
			t.Fatalf("derive(%q) = %q, want %q", input, got, expected)
		}
		if len(got) != sha256HexLength || strings.ToLower(got) != got {
			t.Fatalf("machineId must be 64 lowercase hex characters: %q", got)
		}
	}
}

const sha256HexLength = 64

func TestDeriveRejectsInvalidUUIDWithoutEchoingRawValue(t *testing.T) {
	tests := []string{
		"",
		"00000000-0000-0000-0000-000000000000",
		"001122334455-6677-8899-aabbccddeeff",
		"00112233-4455-6677-8899-aabbccddeefg",
		"{00112233-4455-6677-8899-aabbccddeeff",
		"sensitive-raw-system-uuid",
	}
	for _, input := range tests {
		_, err := derive(input)
		if !errors.Is(err, ErrSystemUUIDInvalid) {
			t.Fatalf("derive(%q) error = %v, want ErrSystemUUIDInvalid", input, err)
		}
		if input != "" && strings.Contains(err.Error(), input) {
			t.Fatalf("error leaked raw UUID %q", input)
		}
	}
}

func TestCurrentSanitizesReaderFailuresAndNeverReturnsRawUUID(t *testing.T) {
	const sensitive = "sensitive-raw-system-uuid"
	got, err := currentFromReader(context.Background(), func(context.Context) (string, error) {
		return "", errors.New("read failed: " + sensitive)
	})
	if got != "" || !errors.Is(err, ErrSystemUUIDUnavailable) {
		t.Fatalf("reader failure = (%q, %v)", got, err)
	}
	if strings.Contains(err.Error(), sensitive) {
		t.Fatal("reader failure leaked raw UUID")
	}

	got, err = currentFromReader(context.Background(), func(context.Context) (string, error) {
		return sensitive, nil
	})
	if got != "" || !errors.Is(err, ErrSystemUUIDInvalid) {
		t.Fatalf("invalid reader value = (%q, %v)", got, err)
	}
	if strings.Contains(err.Error(), sensitive) {
		t.Fatal("validation failure leaked raw UUID")
	}
}

func TestCurrentPreservesUnsupportedClassification(t *testing.T) {
	got, err := currentFromReader(context.Background(), func(context.Context) (string, error) {
		return "", ErrUnsupportedPlatform
	})
	if got != "" || !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("unsupported reader = (%q, %v)", got, err)
	}
}
