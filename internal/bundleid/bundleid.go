// Package bundleid validates macOS bundle identifiers used at command and SQL
// boundaries.
package bundleid

// Valid returns true if s only contains characters allowed in reverse-DNS
// bundle identifiers. This is intentionally stricter than CFBundleIdentifier:
// callers use it as an allowlist before interpolating a bundle ID into sqlite3
// CLI queries, where bind parameters are not available.
func Valid(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
