//go:build !darwin || !cgo

package sysinfo

// ReadRusage is unsupported off darwin/cgo. Callers should treat the error
// as platform-not-supported and skip energy collection.
func ReadRusage(_ int) (ProcRusage, error) {
	return ProcRusage{}, ErrRusageUnsupported
}
