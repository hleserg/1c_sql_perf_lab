//go:build !windows

package lab

import "time"

func networkTimer() func() time.Duration {
	start := time.Now()
	return func() time.Duration { return time.Since(start) }
}
