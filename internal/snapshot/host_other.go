//go:build !darwin

package snapshot

import "time"

func collectBootTime() (time.Time, error) {
	return time.Time{}, nil
}

func collectLoadAverages(time.Time) (LoadAverages, error) {
	return LoadAverages{}, nil
}
