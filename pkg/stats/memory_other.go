//go:build !linux

package stats

import "errors"

func processRSS() (uint64, error) {
	return 0, errors.ErrUnsupported
}
