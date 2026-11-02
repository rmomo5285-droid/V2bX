//go:build !windows && sing && xray && hysteria2

package limiter_test

import (
	"os"
	"strconv"
	"strings"
)

// Linux /proc reports resident pages. Unsupported platforms report zero.
func stressRSS() uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}
