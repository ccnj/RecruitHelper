//go:build !windows

package handinput

import "time"

var monoBase = time.Now()

func nowNanos() int64 { return int64(time.Since(monoBase)) }

func clockSourceName() string { return "time.Now(runtime 单调时钟)" }
