package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kaeawc/spectra/internal/artifact"
	"github.com/kaeawc/spectra/internal/dbinspect"
	"github.com/kaeawc/spectra/internal/netstate"
)

func runDB(args []string) int {
	if handler, ok := resolveDBSubcommand(args); ok {
		return handler(args[1:])
	}
	fmt.Fprintln(os.Stderr, "usage: spectra db <discover|overview|schema|relations|stats|sample> [flags]")
	fmt.Fprintln(os.Stderr, "  discover   Find database connections and env credentials on this host")
	fmt.Fprintln(os.Stderr, "  overview   Server version, database size, schemas (read-only session)")
	fmt.Fprintln(os.Stderr, "  schema     Tables with columns and indexes")
	fmt.Fprintln(os.Stderr, "  relations  Foreign-key relationships between tables")
	fmt.Fprintln(os.Stderr, "  stats      Per-table scan, row, and vacuum health (estimates only)")
	fmt.Fprintln(os.Stderr, "  sample     Read up to N rows from one table (row data may contain PII)")
	return 2
}

func resolveDBSubcommand(args []string) (func([]string) int, bool) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return nil, false
	}
	handlers := map[string]func([]string) int{
		"discover":  runDBDiscover,
		"overview":  runDBOverview,
		"schema":    runDBSchema,
		"relations": runDBRelations,
		"stats":     runDBStats,
		"sample":    runDBSample,
	}
	handler, ok := handlers[args[0]]
	return handler, ok
}

// resolveDBDSN picks the connection string: the --dsn flag, then
// SPECTRA_DB_DSN, then DATABASE_URL. An empty result is still usable —
// pgx falls back to libpq PG* env vars.
func resolveDBDSN(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv("SPECTRA_DB_DSN"); v != "" {
		return v
	}
	return os.Getenv("DATABASE_URL")
}

func runDBDiscover(args []string) int {
	fs := flag.NewFlagSet("spectra db discover", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	discovery := dbinspect.Discovery{
		Connections: dbinspect.DiscoverConnections(netstate.CollectConnections(netstate.DefaultRunner)),
		Env:         dbinspect.DiscoverEnv(os.Getenv),
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(discovery)
		return 0
	}
	printDBDiscovery(discovery)
	return 0
}

func runDBOverview(args []string) int {
	fs := flag.NewFlagSet("spectra db overview", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dsn := fs.String("dsn", "", "Connection string (default: SPECTRA_DB_DSN, DATABASE_URL, then PG* env vars)")
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	overview, err := dbinspect.CollectOverview(context.Background(), resolveDBDSN(*dsn), dbinspect.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(overview)
		return 0
	}
	printDBOverview(*overview)
	return 0
}

func runDBSchema(args []string) int {
	fs := flag.NewFlagSet("spectra db schema", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dsn := fs.String("dsn", "", "Connection string (default: SPECTRA_DB_DSN, DATABASE_URL, then PG* env vars)")
	schema := fs.String("schema", "", "Limit to one schema (default: all user schemas)")
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	report, err := dbinspect.CollectSchema(context.Background(), resolveDBDSN(*dsn), *schema, dbinspect.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return 0
	}
	printDBSchema(*report)
	return 0
}

func runDBRelations(args []string) int {
	fs := flag.NewFlagSet("spectra db relations", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dsn := fs.String("dsn", "", "Connection string (default: SPECTRA_DB_DSN, DATABASE_URL, then PG* env vars)")
	schema := fs.String("schema", "", "Limit to one schema (default: all user schemas)")
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	report, err := dbinspect.CollectRelations(context.Background(), resolveDBDSN(*dsn), *schema, dbinspect.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return 0
	}
	printDBRelations(*report)
	return 0
}

func runDBStats(args []string) int {
	fs := flag.NewFlagSet("spectra db stats", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dsn := fs.String("dsn", "", "Connection string (default: SPECTRA_DB_DSN, DATABASE_URL, then PG* env vars)")
	schema := fs.String("schema", "", "Limit to one schema (default: all user schemas)")
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	report, err := dbinspect.CollectStats(context.Background(), resolveDBDSN(*dsn), *schema, dbinspect.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return 0
	}
	printDBStats(*report)
	return 0
}

func runDBSample(args []string) int {
	fs := flag.NewFlagSet("spectra db sample", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dsn := fs.String("dsn", "", "Connection string (default: SPECTRA_DB_DSN, DATABASE_URL, then PG* env vars)")
	limit := fs.Int("limit", 10, "Maximum rows to read (capped at 500)")
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra db sample [--dsn <dsn>] [--limit 10] [--json] <table>")
		return 2
	}

	report, err := dbinspect.SampleTable(context.Background(), resolveDBDSN(*dsn), fs.Arg(0), *limit, dbinspect.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	recordArtifactCLI(artifact.Record{
		Kind:        artifact.KindDBSample,
		Sensitivity: artifact.SensitivityVeryHigh,
		Source:      "cli",
		Command:     "spectra db sample",
		Metadata: map[string]string{
			"table": report.Schema + "." + report.Table,
		},
	})
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return 0
	}
	printDBSample(*report)
	return 0
}

func printDBDiscovery(d dbinspect.Discovery) {
	fmt.Println("=== Database discovery ===")
	if len(d.Connections) == 0 && len(d.Env) == 0 {
		fmt.Println("no database connections or connection env vars found")
		return
	}
	if len(d.Connections) > 0 {
		fmt.Printf("live connections (%d):\n", len(d.Connections))
		fmt.Printf("  %-10s  %-7s  %-20s  %-26s  %s\n", "ENGINE", "PID", "COMMAND", "REMOTE", "STATE")
		fmt.Println("  " + strings.Repeat("-", 76))
		for _, c := range d.Connections {
			fmt.Printf("  %-10s  %-7d  %-20s  %-26s  %s\n",
				c.Engine, c.PID, truncate(c.Command, 20), truncate(c.RemoteAddr, 26), c.State)
		}
	}
	if len(d.Env) > 0 {
		fmt.Printf("\nconnection env vars (%d):\n", len(d.Env))
		for _, h := range d.Env {
			engine := ""
			if h.Engine != "" {
				engine = fmt.Sprintf(" (%s)", h.Engine)
			}
			fmt.Printf("  %-16s %s%s\n", h.Name, h.Value, engine)
		}
	}
}

func printDBOverview(o dbinspect.Overview) {
	fmt.Printf("Engine          %s\n", o.Engine)
	fmt.Printf("Server version  %s\n", strOrDash(o.ServerVersion))
	fmt.Printf("Database        %s\n", strOrDash(o.Database))
	fmt.Printf("User            %s\n", strOrDash(o.User))
	fmt.Printf("Read-only       %t\n", o.ReadOnlySession)
	fmt.Printf("Size            %s\n", humanSize(o.SizeBytes))
	fmt.Printf("Connections     %d / %d\n", o.Connections, o.MaxConnections)
	if len(o.Schemas) > 0 {
		fmt.Println("\nSchemas:")
		for _, s := range o.Schemas {
			fmt.Printf("  %-30s  %d tables\n", s.Name, s.TableCount)
		}
	}
}

func printDBSchema(r dbinspect.SchemaReport) {
	fmt.Printf("Tables: %d\n", len(r.Tables))
	for _, t := range r.Tables {
		fmt.Printf("\n%s.%s (%s, ~%d rows, %s)\n", t.Schema, t.Name, t.Kind, t.EstimatedRows, humanSize(t.TotalBytes))
		for _, c := range t.Columns {
			printDBColumn(c)
		}
		for _, idx := range t.Indexes {
			flags := ""
			if idx.Primary {
				flags = " [primary]"
			} else if idx.Unique {
				flags = " [unique]"
			}
			fmt.Printf("  index %s%s\n", idx.Name, flags)
		}
	}
}

func printDBColumn(c dbinspect.Column) {
	var notes []string
	if c.PrimaryKey {
		notes = append(notes, "pk")
	}
	if !c.Nullable {
		notes = append(notes, "not null")
	}
	if c.Default != "" {
		notes = append(notes, "default "+truncate(c.Default, 40))
	}
	suffix := ""
	if len(notes) > 0 {
		suffix = "  (" + strings.Join(notes, ", ") + ")"
	}
	fmt.Printf("  %-30s %s%s\n", c.Name, c.Type, suffix)
}

func printDBRelations(r dbinspect.RelationsReport) {
	fmt.Printf("Foreign keys: %d\n", len(r.ForeignKeys))
	for _, fk := range r.ForeignKeys {
		fmt.Printf("  %s.%s(%s) -> %s.%s(%s)",
			fk.FromSchema, fk.FromTable, strings.Join(fk.FromColumns, ","),
			fk.ToSchema, fk.ToTable, strings.Join(fk.ToColumns, ","))
		if fk.OnDelete != "" && fk.OnDelete != "no action" {
			fmt.Printf(" on delete %s", fk.OnDelete)
		}
		fmt.Printf("  [%s]\n", fk.Name)
	}
}

func printDBStats(r dbinspect.StatsReport) {
	fmt.Printf("%-36s %10s %10s %10s %10s %10s  %s\n",
		"TABLE", "SIZE", "LIVE", "DEAD", "SEQSCAN", "IDXSCAN", "LAST VACUUM")
	fmt.Println(strings.Repeat("-", 110))
	for _, t := range r.Tables {
		fmt.Printf("%-36s %10s %10d %10d %10d %10d  %s\n",
			truncate(t.Schema+"."+t.Name, 36), humanSize(t.TotalBytes),
			t.LiveRows, t.DeadRows, t.SeqScans, t.IdxScans, strOrDash(t.LastVacuum))
	}
}

func printDBSample(r dbinspect.SampleReport) {
	fmt.Printf("Sample of %s.%s (limit %d, %d rows)\n", r.Schema, r.Table, r.Limit, len(r.Rows))
	fmt.Println(strings.Join(r.Columns, " | "))
	fmt.Println(strings.Repeat("-", 80))
	for _, row := range r.Rows {
		cells := make([]string, len(row))
		for i, v := range row {
			if v == nil {
				cells[i] = "NULL"
				continue
			}
			cells[i] = truncate(fmt.Sprintf("%v", v), 60)
		}
		fmt.Println(strings.Join(cells, " | "))
	}
}
