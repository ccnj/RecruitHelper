//go:build !windows && !darwin

package machineid

import "context"

func readSystemUUID(context.Context) (string, error) {
	return "", ErrUnsupportedPlatform
}
