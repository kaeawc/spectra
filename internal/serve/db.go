package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/kaeawc/spectra/internal/artifact"
	"github.com/kaeawc/spectra/internal/dbinspect"
	"github.com/kaeawc/spectra/internal/logger"
	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/rpc"
)

// dbParams are the shared inputs for read-only database inspection RPCs.
// An empty DSN falls back to the daemon's PG* env vars via pgx.
type dbParams struct {
	DSN    string `json:"dsn"`
	Schema string `json:"schema"`
}

func decodeDBParams(method string, params json.RawMessage) (dbParams, error) {
	var p dbParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return p, fmt.Errorf("%s: invalid params: %w", method, err)
		}
	}
	return p, nil
}

// registerDBHandlers wires read-only database inspection RPCs. Every op
// opens a short-lived session forced read-only with statement timeouts;
// db.sample additionally requires confirm_sensitive because row data may
// contain customer PII.
func registerDBHandlers(d *rpc.Dispatcher, artifactRecorder artifact.Recorder, artifactPolicy artifact.Policy, log logger.Logger) {
	// db.discover — live sockets to database ports plus connection env vars.
	d.Register("db.discover", func(_ json.RawMessage) (any, error) {
		return dbinspect.Discovery{
			Connections: dbinspect.DiscoverConnections(netstate.CollectConnections(netstate.DefaultRunner)),
			Env:         dbinspect.DiscoverEnv(os.Getenv),
		}, nil
	})

	// db.overview — server version, database size, schema summary.
	d.Register("db.overview", func(params json.RawMessage) (any, error) {
		p, err := decodeDBParams("db.overview", params)
		if err != nil {
			return nil, err
		}
		return dbinspect.CollectOverview(context.Background(), p.DSN, dbinspect.Options{})
	})

	// db.schema — tables with columns and indexes. Optional: {"schema": "..."}.
	d.Register("db.schema", func(params json.RawMessage) (any, error) {
		p, err := decodeDBParams("db.schema", params)
		if err != nil {
			return nil, err
		}
		return dbinspect.CollectSchema(context.Background(), p.DSN, p.Schema, dbinspect.Options{})
	})

	// db.relations — foreign-key relationships. Optional: {"schema": "..."}.
	d.Register("db.relations", func(params json.RawMessage) (any, error) {
		p, err := decodeDBParams("db.relations", params)
		if err != nil {
			return nil, err
		}
		return dbinspect.CollectRelations(context.Background(), p.DSN, p.Schema, dbinspect.Options{})
	})

	// db.stats — per-table scan/row/vacuum health. Optional: {"schema": "..."}.
	d.Register("db.stats", func(params json.RawMessage) (any, error) {
		p, err := decodeDBParams("db.stats", params)
		if err != nil {
			return nil, err
		}
		return dbinspect.CollectStats(context.Background(), p.DSN, p.Schema, dbinspect.Options{})
	})

	// db.sample — bounded row sample from one table.
	// Required: {"table": "<schema.table>", "confirm_sensitive": true}.
	d.Register("db.sample", func(params json.RawMessage) (any, error) {
		var p struct {
			DSN              string `json:"dsn"`
			Table            string `json:"table"`
			Limit            int    `json:"limit"`
			ConfirmSensitive bool   `json:"confirm_sensitive"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Table == "" {
			return nil, fmt.Errorf("db.sample requires {\"table\": \"<schema.table>\"}")
		}
		if !p.ConfirmSensitive {
			return nil, fmt.Errorf("db.sample requires {\"confirm_sensitive\": true} because row data may contain secrets and PII")
		}
		rec := artifact.Record{
			Kind:        artifact.KindDBSample,
			Sensitivity: artifact.SensitivityVeryHigh,
			Source:      "daemon-rpc",
			Command:     "db.sample",
			Metadata:    map[string]string{"table": p.Table},
		}
		if err := authorizeArtifact(log, artifactPolicy, rec, p.ConfirmSensitive); err != nil {
			return nil, err
		}
		report, err := dbinspect.SampleTable(context.Background(), p.DSN, p.Table, p.Limit, dbinspect.Options{})
		if err != nil {
			return nil, err
		}
		return recordArtifact(context.Background(), log, artifactRecorder, rec, map[string]any{"sample": report}), nil
	})
}
