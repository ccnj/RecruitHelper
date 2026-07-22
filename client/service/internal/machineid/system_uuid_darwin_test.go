//go:build darwin

package machineid

import (
	"errors"
	"strings"
	"testing"
)

func TestParseIORegOutputRequiresOnePlatformUUID(t *testing.T) {
	const uuid = "00112233-4455-6677-8899-AABBCCDDEEFF"
	output := []byte("+-o IOPlatformExpertDevice  <class IOPlatformExpertDevice>\n" +
		"  {\n" +
		"    \"IOPlatformUUID\" = \"" + uuid + "\"\n" +
		"  }\n")
	got, err := parseIORegOutput(output)
	if err != nil || got != uuid {
		t.Fatalf("parseIORegOutput = (%q, %v), want UUID", got, err)
	}

	for name, invalid := range map[string][]byte{
		"missing":   []byte("+-o IOPlatformExpertDevice\n"),
		"duplicate": append(append([]byte{}, output...), output...),
		"empty":     []byte("\"IOPlatformUUID\" = \"\"\n"),
		"oversized": []byte(strings.Repeat("x", maxIORegOutputBytes+1)),
	} {
		t.Run(name, func(t *testing.T) {
			value, err := parseIORegOutput(invalid)
			if value != "" || !errors.Is(err, ErrSystemUUIDUnavailable) {
				t.Fatalf("parseIORegOutput = (%q, %v)", value, err)
			}
		})
	}
}
