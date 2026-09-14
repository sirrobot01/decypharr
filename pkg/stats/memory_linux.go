//go:build linux

package stats

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// processRSS reads the kernel's resident page estimate without scanning mappings.
func processRSS() (uint64, error) {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0, fmt.Errorf("missing resident page count in /proc/self/statm")
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, err
	}
	return pages * uint64(os.Getpagesize()), nil
}
