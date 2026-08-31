package dbinspect

import (
	"net/url"
	"strings"
)

const redacted = "[redacted]"

// RedactDSN masks the password in a database URL or key/value DSN so the
// string is safe to log or return in reports. Unparseable input is fully
// redacted rather than passed through.
func RedactDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	if strings.Contains(dsn, "://") {
		return redactURLDSN(dsn)
	}
	if strings.Contains(dsn, "=") {
		return redactKeywordDSN(dsn)
	}
	return redacted
}

func redactURLDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return redacted
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), redacted)
		}
	}
	// url.UserPassword escapes the placeholder's brackets; undo that so the
	// output reads as [redacted] rather than %5Bredacted%5D.
	return strings.Replace(u.String(), url.QueryEscape(redacted), redacted, 1)
}

// redactKeywordDSN handles libpq keyword form: "host=x password=y dbname=z".
func redactKeywordDSN(dsn string) string {
	fields := strings.Fields(dsn)
	for i, f := range fields {
		if key, _, ok := strings.Cut(f, "="); ok && strings.EqualFold(key, "password") {
			fields[i] = key + "=" + redacted
		}
	}
	return strings.Join(fields, " ")
}
