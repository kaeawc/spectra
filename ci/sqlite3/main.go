// Command sqlite3 is a minimal stand-in for the macOS sqlite3 CLI, used
// only by the Darling CI job (scripts/darling-ci.sh). Darling does not ship
// sqlite3, so the job cross-compiles this shim for darwin and prepends it
// to PATH inside the Darling prefix. It is never part of macOS builds.
//
// Supported invocation (the form Spectra uses for TCC database reads):
//
//	sqlite3 <db-path> <sql>
//
// Rows print in sqlite3's default list mode: columns joined by "|", one
// row per line, NULL as the empty string.
package main

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"

	_ "modernc.org/sqlite"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "sqlite3 shim: usage: sqlite3 <db-path> <sql>")
		return 1
	}
	if err := query(args[0], args[1], stdout); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

func query(path, sqlText string, stdout io.Writer) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := db.Query(sqlText) // #nosec G202 -- CI shim mirrors sqlite3(1): the SQL is the CLI argument
	if err != nil {
		return err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		fields := make([]string, len(vals))
		for i, v := range vals {
			fields[i] = format(v)
		}
		fmt.Fprintln(stdout, strings.Join(fields, "|"))
	}
	return rows.Err()
}

func format(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(t)
	default:
		return fmt.Sprint(t)
	}
}
