//go:build windows

package machineid

import (
	"context"

	"golang.org/x/sys/windows/registry"
)

func readSystemUUID(ctx context.Context) (string, error) {
	if ctx.Err() != nil {
		return "", ErrSystemUUIDUnavailable
	}
	key, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Cryptography`,
		registry.QUERY_VALUE,
	)
	if err != nil {
		return "", ErrSystemUUIDUnavailable
	}
	defer key.Close()

	value, _, err := key.GetStringValue("MachineGuid")
	if err != nil {
		return "", ErrSystemUUIDUnavailable
	}
	return value, nil
}
