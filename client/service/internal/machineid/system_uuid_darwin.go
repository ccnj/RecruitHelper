//go:build darwin

package machineid

import (
	"bytes"
	"context"
	"os/exec"
	"regexp"
)

const maxIORegOutputBytes = 64 * 1024

var ioPlatformUUIDLine = regexp.MustCompile(
	`(?m)^[[:space:]]*"IOPlatformUUID"[[:space:]]*=[[:space:]]*"([^"]+)"[[:space:]]*$`,
)

func readSystemUUID(ctx context.Context) (string, error) {
	output, err := exec.CommandContext(
		ctx,
		"/usr/sbin/ioreg",
		"-rd1",
		"-c", "IOPlatformExpertDevice",
		"-k", "IOPlatformUUID",
	).Output()
	if err != nil {
		return "", ErrSystemUUIDUnavailable
	}
	return parseIORegOutput(output)
}

func parseIORegOutput(output []byte) (string, error) {
	if len(output) == 0 || len(output) > maxIORegOutputBytes {
		return "", ErrSystemUUIDUnavailable
	}
	matches := ioPlatformUUIDLine.FindAllSubmatch(output, -1)
	if len(matches) != 1 || len(matches[0]) != 2 {
		return "", ErrSystemUUIDUnavailable
	}
	value := bytes.TrimSpace(matches[0][1])
	if len(value) == 0 {
		return "", ErrSystemUUIDUnavailable
	}
	return string(value), nil
}
