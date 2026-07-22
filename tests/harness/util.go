package harness

import (
	"net/url"
	"strings"
)

// quoteIdent double-quotes a SQL identifier (test DB names are controlled, but be safe).
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// replaceDBName swaps the database path in a Postgres URL DSN.
func replaceDBName(dsn, name string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		// Fallback: best-effort suffix replace.
		if i := strings.LastIndex(dsn, "/"); i >= 0 {
			return dsn[:i+1] + name
		}
		return dsn
	}
	u.Path = "/" + name
	return u.String()
}
