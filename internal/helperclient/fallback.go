package helperclient

import "errors"

// IsUnavailable reports whether err indicates the helper is not installed
// or not running. Callers use this to decide whether to degrade gracefully
// or surface an error to the user.
func IsUnavailable(err error) bool {
	return errors.Is(err, ErrHelperUnavailable)
}
